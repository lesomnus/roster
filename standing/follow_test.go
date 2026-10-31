package standing_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/standing"
)

// heard writes until the stream says what was written: there is nothing to say
// it has subscribed, and a write made before it has is heard by nobody.
func heard(t *testing.T, got <-chan standing.Standing, who pdid.Id, write func() error, ok func(standing.Standing) bool) standing.Standing {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		require.NoError(t, write())
		select {
		case v := <-got:
			if v.Holder == who && ok(v) {
				return v
			}
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("nothing about them came down the stream")
		}
	}
}

// follow runs [standing.Accounts.Follow] until the test ends, and is what it
// handed on.
func follow(t *testing.T, a *standing.Accounts) <-chan standing.Standing {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	got := make(chan standing.Standing, 64)
	go func() {
		defer close(done)
		_ = a.Follow(ctx, func(_ context.Context, v standing.Standing) error {
			select {
			case got <- v:
			default:
			}
			return nil
		})
	}()

	return got
}

// TestWhatRosterSaysArrivesAsItIsSaid: what roster says of an account comes
// down its stream as it is said -- signed out everywhere, suspended, erased --
// to an app with a roster of its own, and what this process knew of the
// account is dropped with it: a session held to roster is refused at once,
// not once HeldTtl is over.
func TestWhatRosterSaysArrivesAsItIsSaid(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	// A clock that does not move, so that nothing known runs out on its own.
	c := &clock{now: time.Now()}
	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme"), Now: c.Now})
	got := follow(t, a)
	op, as := e.asOperator(t)
	ref := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	v := heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: ref}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	x.Equal(e.acme, v.Tenant)
	x.True(v.Disabled.IsZero())
	x.True(v.Erased.IsZero())

	// Believed, and then suspended: heard, and the next call asks.
	after := time.Now().Add(time.Second)
	x.NoError(a.Held(ctx, e.kim, after))
	v = heard(t, got, e.kim, func() error {
		_, err := op.Disable(as, rstr.HolderDisableRequest_builder{Ref: ref}.Build())
		if status.Code(err) == codes.FailedPrecondition {
			// Suspended already: once it has been heard, once is enough.
			return nil
		}
		return err
	}, func(v standing.Standing) bool { return !v.Disabled.IsZero() })
	x.False(v.Good(time.Now()))
	x.ErrorIs(a.Held(ctx, e.kim, after), standing.ErrRefused, "what the stream said, before HeldTtl was over")

	v = heard(t, got, e.kim, func() error {
		_, err := op.Erase(as, ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}, func(v standing.Standing) bool { return !v.Erased.IsZero() })
	x.Equal(e.acme, v.Tenant)
}

// TestAnErasureIsHeardOfSomebodyNothingElseHappenedTo: most people's erasure
// is the first thing roster's stream hears of them since the app connected --
// nothing was written about them before -- and it is heard all the same, with
// when roster erased them (roster#104). Somebody erased in a tenant the app
// does not serve is not.
func TestAnErasureIsHeardOfSomebodyNothingElseHappenedTo(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)

	// Made before the stream opens, through roster's server in this process,
	// which publishes nothing: the stream has never read them.
	quiet := addPerson(t, e.s, e.acme, "quiet")
	elsewhere := addPerson(t, e.s, e.beta, "elsewhere")

	got := follow(t, accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}))
	op, as := e.asOperator(t)

	// kim first, for the stream to be known to be open: a write before it is
	// heard by nobody, and an erasure is not made twice.
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })

	// The one the app may not hear of first, so that the other arriving makes
	// its absence a fact rather than a race.
	_, err := op.Erase(as, rstr.HolderRef_builder{Id: elsewhere.Bytes()}.Build())
	x.NoError(err)
	_, err = op.Erase(as, rstr.HolderRef_builder{Id: quiet.Bytes()}.Build())
	x.NoError(err)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case v := <-got:
			x.NotEqual(elsewhere, v.Holder, "an erasure in a tenant the app does not serve")
			if v.Holder != quiet {
				continue
			}
			x.False(v.Erased.IsZero(), "heard, and as an erasure")
			x.Equal(e.acme, v.Tenant)
			return
		case <-deadline:
			t.Fatal("the erasure of somebody nothing else happened to was not heard")
		}
	}
}

// TestADirectoryClosedShutsOutEveryoneInIt: roster closing the directory an
// app's accounts are in is heard as an event about each of them, with when --
// and is no session for any of them -- until roster restores it, which is
// heard too (roster#105).
func TestADirectoryClosedShutsOutEveryoneInIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})
	got := follow(t, a)
	op, as := e.asOperator(t)
	tenants := rstr.NewTenantServiceClient(e.dial(t))
	ref := rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build()
	kim := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()
	invalidate := func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: kim}.Build())
		return err
	}

	// kim first, for the stream to be known to be open.
	heard(t, got, e.kim, invalidate, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	began := time.Now()
	x.NoError(a.Held(ctx, e.kim, began))

	// The stream reads a tenant when it moves, and a tenant it had seen
	// before closing is a closing it names for everybody in it.
	was, err := tenants.Get(as, rstr.TenantGetRequest_builder{Ref: ref, Select: rstr.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build()}.Build())
	x.NoError(err)
	_, err = tenants.Update(as, rstr.TenantUpdateRequest_builder{Ref: ref, DateUpdated: was.GetDateUpdated(), Desc: proto.String("seen")}.Build())
	x.NoError(err)
	closed := false
	v := heard(t, got, e.kim, func() error {
		if closed {
			return nil
		}
		_, err := tenants.Close(as, rstr.TenantCloseRequest_builder{Ref: ref}.Build())
		closed = err == nil
		return err
	}, func(v standing.Standing) bool { return !v.Closed.IsZero() })
	x.Equal(e.acme, v.Tenant)
	x.True(v.Disabled.IsZero(), "a directory closing is not its people suspended")
	x.False(v.Good(time.Now()))
	x.Error(a.Held(ctx, e.kim, began), "a session in a closed directory")

	_, err = tenants.Restore(as, rstr.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	v = heard(t, got, e.kim, invalidate, func(v standing.Standing) bool { return v.Closed.IsZero() })
	x.True(v.Disabled.IsZero())
	x.NoError(a.Held(ctx, e.kim, time.Now()), "a session again, once it is restored")
}

// TestAClosedDirectoryIsTheFourthFactToAKeyOutsideIt: asked as a holder in a
// closed directory, roster answers nothing at all, which is a roster that will
// not answer; asked by a deployment key as itself, from outside every tenant,
// the closing is read with the other three, as the stream reads it.
func TestAClosedDirectoryIsTheFourthFactToAKeyOutsideIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	_, as := e.asOperator(t)
	_, err := rstr.NewTenantServiceClient(e.dial(t)).Close(as, rstr.TenantCloseRequest_builder{
		Ref: rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	inside := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})
	_, err = inside.StandingOf(ctx, e.kim)
	x.Error(err)
	x.NotErrorIs(err, standing.ErrNoPerson, "a closed directory is not its people gone")
	x.ErrorIs(inside.Held(ctx, e.kim, time.Now()), standing.ErrUnreachable)

	reader := stdoutOf(t, cli.NewCmdControl(&e.c), "key", "add", "--allow", rstr.HolderService_Get_FullMethodName, "reader")
	outside := accounts(t, standing.Config{Conn: e.dial(t), As: bearer(reader)})
	st, err := outside.StandingOf(ctx, e.kim)
	x.NoError(err)
	x.False(st.Closed.IsZero(), "the directory's closing")
	x.Equal(e.acme, st.Tenant)
	x.ErrorIs(outside.Held(ctx, e.kim, time.Now()), standing.ErrRefused)
}

// TestAStreamRosterRefusesIsSaidAndThenHeld: a role installed before the app
// held roster's stream is refused it. That is said once, as an error, with what
// to do -- and doing it is noticed without a restart.
func TestAStreamRosterRefusesIsSaidAndThenHeld(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	e.role(t, slices.DeleteFunc(slices.Clone(methods), func(m string) bool { return m == rstr.SyncService_Watch_FullMethodName }))

	l := &logs{}
	a := accounts(t, standing.Config{
		Conn: e.dial(t), As: at(e.key, "acme"),
		Log:     slog.New(slog.NewTextHandler(l, nil)),
		Refused: 300 * time.Millisecond,
	})
	got := follow(t, a)

	x.Eventually(func() bool { return strings.Contains(l.String(), "roster role patch") }, 10*time.Second, 20*time.Millisecond)
	x.Contains(l.String(), "level=ERROR")
	x.NotContains(l.String(), "level=WARN", "a refusal is not a stream that dropped")

	e.role(t, methods)
	op, as := e.asOperator(t)
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
}

// TestARosterThatPublishesNothingIsSaidOnce: the roster an app runs in its own
// process has no broker, so it has no stream -- and says so, which is said
// once as an error and asked about again only after Refused, rather than as a
// stream that keeps dropping.
func TestARosterThatPublishesNothingIsSaidOnce(t *testing.T) {
	x := require.New(t)
	em := newEmbedded(t)

	l := &logs{}
	follow(t, accounts(t, standing.Config{Conn: em.dial(t), As: em.as, Log: slog.New(slog.NewTextHandler(l, nil))}))

	x.Eventually(func() bool { return strings.Contains(l.String(), "watch.broker") }, 10*time.Second, 20*time.Millisecond)
	x.Contains(l.String(), "level=ERROR")
	time.Sleep(200 * time.Millisecond)
	x.Equal(1, strings.Count(l.String(), "level=ERROR"), "said once, and not asked again within Refused")
	x.NotContains(l.String(), "level=WARN")
}

// dropping is a connection whose streams the test can end, as a network does.
type dropping struct {
	mu     sync.Mutex
	opened int
	drop   []context.CancelFunc
}

func (d *dropping) option() grpc.DialOption {
	return grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx, cancel := context.WithCancel(ctx)
		d.mu.Lock()
		d.opened++
		d.drop = append(d.drop, cancel)
		d.mu.Unlock()
		return streamer(ctx, desc, cc, method, opts...)
	})
}

func (d *dropping) opens() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opened
}

func (d *dropping) cut() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range d.drop {
		f()
	}
}

// TestAStreamOpenedAgainForgetsWhatWasKnown: a stream that ended missed
// things, so when it is open again nothing known before is believed -- here, a
// suspension written where no stream hears it, while the stream was down.
func TestAStreamOpenedAgainForgetsWhatWasKnown(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	d := &dropping{}
	c := &clock{now: time.Now()}
	a := accounts(t, standing.Config{Conn: e.dial(t, d.option()), As: at(e.key, "acme"), Now: c.Now})
	got := follow(t, a)
	op, as := e.asOperator(t)

	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	after := time.Now().Add(time.Second)
	x.NoError(a.Held(ctx, e.kim, after))

	// Through roster's own server, which publishes nothing: no stream says it.
	_, err := e.s.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
	x.NoError(err)
	x.NoError(a.Held(ctx, e.kim, after), "believed, as nothing said otherwise and the clock has not moved")

	d.cut()
	x.Eventually(func() bool { return d.opens() > 1 }, 10*time.Second, 20*time.Millisecond, "opened again")
	x.Eventually(func() bool { return errors.Is(a.Held(ctx, e.kim, after), standing.ErrRefused) }, 10*time.Second, 20*time.Millisecond,
		"what was known before the stream was open again")
}

// TestTheFollowerIsALoopEveryProcessRuns: the follower is a named loop, and
// marked to run in every process it runs in one that holds no lease -- which
// is each process but one, under `spin.lease`.
func TestTheFollowerIsALoopEveryProcessRuns(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})

	got := make(chan standing.Standing, 64)
	loop := a.Follower("test.roster-stream", func(_ context.Context, v standing.Standing) error {
		select {
		case got <- v:
		default:
		}
		return nil
	})
	n, ok := loop.(spin.Named)
	x.True(ok)
	x.Equal("test.roster-stream", n.SpinName())

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- spin.Run(ctx, slices.Values([]spin.Spinner{spin.InEveryProcess(loop)}), spin.WithLease(spin.Nowhere()))
	}()
	t.Cleanup(func() {
		stop()
		x.NoError(<-done, "the loop stopped as asked")
	})

	op, as := e.asOperator(t)
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
}
