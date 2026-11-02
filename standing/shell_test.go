package standing_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/standing"
)

// What roster's operator does at a shell on the server's own box, beside a
// roster that is serving (#214).
//
// The command is a process of its own. Here that is a `cmd.Build` of its own in
// the test's process -- its own server, its own pool, its own broker -- which is
// the arrangement that matters: nothing it could publish inside itself would
// reach a subscriber of the serving roster's, `memory` most plainly, and the
// process is gone the moment the command answers. What reaches the app is what
// the command leaves in the outbox, in the transaction of each write, for the
// serving roster's drain to publish.

// serving runs what `roster serve` runs besides answering calls -- the outbox's
// drain among it -- until the test ends.
func (e *external) serving(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- spin.Run(ctx, slices.Values(e.s.Spin)) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
}

// shell is `roster ARGS` at the server's own box: this deployment's
// configuration, and its database read directly.
func (e *external) shell(t *testing.T, args ...string) {
	t.Helper()

	// As the operator `init` made: a command at the box names who is at the
	// shell, for the access record (#221).
	c := e.c
	c.Client.As = "admin"

	k := &xli.Command{Name: "roster", Commands: append(xli.Commands{
		cli.NewCmdForget(&c),
		cli.NewCmdRestore(&c),
	}, cli.NewCmdEntities(&c)...)}
	require.NoError(t, k.Run(t.Context(), args), strings.Join(args, " "))
}

// said is a write made somewhere else, which is waited for rather than made
// again: a shell's word is said once.
func said() error { return nil }

// TestWhatAShellSaysArrivesAsItIsSaid: roster's operator suspending somebody,
// letting them back in and signing them out everywhere, at a shell on the
// server's own box, is heard by an app following the stream as the same
// calls over the wire are -- and not at the next time the app asks.
//
// The entity tree, which reaches the deployment's own server on a pipe.
func TestWhatAShellSaysArrivesAsItIsSaid(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	e.serving(t)
	got := follow(t, accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}))
	op, as := e.asOperator(t)
	ref := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	// Over the wire first, for the stream to be known to be open: a write
	// before it is heard by nobody, however it was made.
	v := heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: ref}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })

	e.shell(t, "holder", "disable", "@acme/kim")
	v = heard(t, got, e.kim, said, func(v standing.Standing) bool { return !v.Disabled.IsZero() })
	x.False(v.Good(time.Now()), "suspended, at the shell")

	e.shell(t, "holder", "enable", "@acme/kim")
	heard(t, got, e.kim, said, func(v standing.Standing) bool { return v.Disabled.IsZero() })

	before := v.Invalidated
	e.shell(t, "holder", "invalidate", "@acme/kim")
	v = heard(t, got, e.kim, said, func(v standing.Standing) bool { return v.Invalidated.After(before) })
	x.Equal(e.acme, v.Tenant)
}

// TestADirectoryRestoredAtTheShellIsHeard: a command that calls the
// deployment's server itself rather than through the pipe -- `roster restore
// @tenant`, here -- is heard as well, on each of the directory's people.
func TestADirectoryRestoredAtTheShellIsHeard(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	e.serving(t)
	got := follow(t, accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}))
	op, as := e.asOperator(t)
	tenants := rstr.NewTenantServiceClient(e.dial(t))
	ref := rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build()

	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })

	// Read once before it closes, so that the stream saw it open; see
	// `TestADirectoryClosedShutsOutEveryoneInIt`.
	was, err := tenants.Get(as, rstr.TenantGetRequest_builder{Ref: ref, Select: rstr.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build()}.Build())
	x.NoError(err)
	_, err = tenants.Update(as, rstr.TenantUpdateRequest_builder{Ref: ref, DateUpdated: was.GetDateUpdated(), Desc: proto.String("seen")}.Build())
	x.NoError(err)
	_, err = tenants.Close(as, rstr.TenantCloseRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	heard(t, got, e.kim, said, func(v standing.Standing) bool { return !v.Closed.IsZero() })

	e.shell(t, "restore", "@acme")
	v := heard(t, got, e.kim, said, func(v standing.Standing) bool { return v.Closed.IsZero() })
	x.Equal(e.acme, v.Tenant)
}

// TestARestoreIsHeardAsItIsMade: `roster restore` brings back somebody erased,
// and an app that heard the erasure hears them back when they are -- not at the
// next thing that happens to them, which is how it was heard until the restore
// put itself on the stream (#214).
//
// It writes past roster's servers, as `forget` does, so it is in the outbox
// because it puts itself there, in the transaction that undoes the erasure.
func TestARestoreIsHeardAsItIsMade(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	e.serving(t)
	got := follow(t, accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}))
	op, as := e.asOperator(t)
	ref := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	// Invalidated first, for the stream to be known to be open: an erasure is
	// made once, and one made before the stream is open is heard by nobody.
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: ref}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	_, err := op.Erase(as, ref)
	x.NoError(err)
	heard(t, got, e.kim, said, func(v standing.Standing) bool { return !v.Erased.IsZero() })

	e.shell(t, "restore", "@acme/kim")
	v := heard(t, got, e.kim, said, func(v standing.Standing) bool { return v.Erased.IsZero() })
	x.False(v.Invalidated.IsZero(), "what roster says of them now, which is everything but the erasure")
}

// TestAForgetIsHeardAsTheErasureItIs: somebody nobody had erased, forgotten on
// request at the shell -- erased and destroyed in one act -- is heard as an
// erasure, as `HolderService.Erase` over the wire is.
//
// `roster forget` writes past roster's servers, so what it writes reaches the
// stream only because it puts the person in the outbox itself, in the
// transaction that destroys them.
func TestAForgetIsHeardAsTheErasureItIs(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	e.serving(t)
	got := follow(t, accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}))
	op, as := e.asOperator(t)

	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })

	e.shell(t, "forget", "@acme/kim")
	v := heard(t, got, e.kim, said, func(v standing.Standing) bool { return !v.Erased.IsZero() })
	x.Equal(e.acme, v.Tenant)
}
