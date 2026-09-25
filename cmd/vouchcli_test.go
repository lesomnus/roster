package cmd_test

import (
	"github.com/lesomnus/roster/cli"
	"os"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// piped runs a command with something on stdin, the way a shell pipes into it.
func piped(t *testing.T, in string, k *xli.Command, args ...string) error {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)

	_, err = w.WriteString(in)
	x.NoError(err)
	x.NoError(w.Close())

	was := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = was }()

	return k.Run(t.Context(), args)
}

// customer is a tenant and one person in it, written the way a shell writes
// them: locally, through `Ungated`, with no rules at all.
func customer(t *testing.T, s *cmd.Server, tenant, holder string) pdid.Id {
	t.Helper()
	x := require.New(t)

	tn, err := s.Ungated.Tenant().Add(t.Context(), app.TenantAddRequest_builder{Alias: tenant}.Build())
	x.NoError(err)

	// Made if they are not there: a tenant arrives with the holder that
	// administers it (`server/core/tenant.go`), so asking for `admin` is
	// asking for the row that is already in it.
	at := app.HolderRef_builder{
		Slug: app.HolderRefBySlug_builder{
			Alias:  z.Ptr(holder),
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		}.Build(),
	}.Build()

	h, err := s.Ungated.Holder().Get(t.Context(), app.HolderGetRequest_builder{Ref: at}.Build())
	if err != nil {
		h, err = s.Ungated.Holder().Add(t.Context(), app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Alias:  holder,
		}.Build())
	}
	x.NoError(err)

	k, err := pdid.From(h.GetId())
	x.NoError(err)

	return k
}

// TestTheCliWritesAWayInForAPerson is the other half of D57.
//
// `roster key add --tenant` gives a **machine** a way in. This is the one for a
// person, and it was refused on the grounds that generating a password is an
// act with somebody on the other end of it -- so a terminal was the wrong place
// and the admin console was the right one.
//
// That is not a difference. An operator at a console is a person at a screen
// reading a secret out; both reach the same `VouchService`, over the same rows,
// and `admin.addr` is one of them making an RPC exactly as this is. What the
// reason described was which of the two had been written.
func TestTheCliWritesAWayInForAPerson(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	who := customer(t, s, "newco", "admin")
	x.NoError(s.Close())

	// Generated here and answered with once, which is `IssueService`'s argument
	// about a key unchanged: a secret the caller chose is one the caller knows.
	secret := stdoutOf(t, cli.NewCmdVouch(&c), "reset", "@newco/admin")
	x.NotEmpty(secret)

	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s2.Close() })

	v := vouch.New(s2.Ungated, s2.Ungated)
	asked := func(secret string) bool {
		t.Helper()

		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())
		require.NoError(t, err)

		return res.GetOk()
	}

	x.True(asked(secret), "the password the command printed does not sign in")
	x.False(asked(secret + "x"))

	t.Run("and one somebody chose, on a pipe", func(t *testing.T) {
		x := require.New(t)
		x.NoError(s2.Close())

		x.NoError(piped(t, "correct horse battery staple\n",
			cli.NewCmdVouch(&c), "set", "--password-stdin", "@newco/admin"))

		s3, err := cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s3.Close() })

		v = vouch.New(s3.Ungated, s3.Ungated)
		x.True(asked("correct horse battery staple"))
		x.False(asked(secret), "the password it replaced still works")
	})

	t.Run("and never as an argument", func(t *testing.T) {
		x := require.New(t)

		err := cli.NewCmdVouch(&c).Run(ctx, []string{"set", "@newco/admin"})
		x.Error(err)
		x.ErrorContains(err, "--password-stdin",
			"a password could be given somewhere it would be in the shell history")
	})
}

// TestTheCliIsNotADoorPastTheCorpus is the failure `cmd/admin.go` records about
// the port beside it, asked of the command.
//
// A deployment that names `vouch.breached` has said it will not hold a password
// somebody has already lost. That is a fact about the secret and not about the
// door it came through -- and leaving `WithBreached` off one door makes that
// door the only way such a password gets in, while every other one refuses it
// and nothing says the two disagree. It happened once, on the admin port.
func TestTheCliIsNotADoorPastTheCorpus(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	c.Vouch.Breached = leakedCorpus(t, "hunter2hunter2")

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	customer(t, s, "newco", "admin")
	x.NoError(s.Close())

	err = piped(t, "hunter2hunter2", cli.NewCmdVouch(&c), "set", "--password-stdin", "@newco/admin")
	x.Error(err, "a shell stored a password this deployment refuses everywhere else")

	// `FailedPrecondition` rather than `InvalidArgument`, as everywhere else:
	// there is nothing wrong with the request, the world changed under the
	// value in it.
	x.Equal(codes.FailedPrecondition, status.Code(err))

	t.Run("and the check is a check rather than a refusal of everything", func(t *testing.T) {
		x := require.New(t)

		x.NoError(piped(t, "correct horse battery staple",
			cli.NewCmdVouch(&c), "set", "--password-stdin", "@newco/admin"))
	})

	t.Run("and a generated one goes through the same door", func(t *testing.T) {
		x := require.New(t)

		v := stdoutOf(t, cli.NewCmdVouch(&c), "reset", "@newco/admin")
		x.NotEmpty(v, "thirty-two random bytes were in a corpus of one")
	})
}

// TestUnlockSaysWhetherItDidAnything, which is the difference between having
// fixed something and having looked in the wrong place.
func TestUnlockSaysWhetherItDidAnything(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	customer(t, s, "newco", "admin")
	x.NoError(s.Close())

	// A credential first, because a lockout is a fact about one: unlocking
	// somebody who has no password is `NotFound` rather than a no-op, which is
	// the right answer and worth knowing before an operator meets it at three
	// in the morning.
	err = cli.NewCmdVouch(&c).Run(ctx, []string{"unlock", "@newco/admin"})
	x.Error(err, "there was nothing to unlock and it said otherwise")
	x.Equal(codes.NotFound, status.Code(err))

	_ = stdoutOf(t, cli.NewCmdVouch(&c), "reset", "@newco/admin")

	x.NoError(cli.NewCmdVouch(&c).Run(ctx, []string{"unlock", "@newco/admin"}))

	t.Run("and nobody is not somebody", func(t *testing.T) {
		x := require.New(t)

		err := cli.NewCmdVouch(&c).Run(ctx, []string{"unlock", "@newco/nobody"})
		x.Error(err)
		x.ErrorContains(err, "nobody")
	})

	t.Run("and an alias in no tenant at all", func(t *testing.T) {
		x := require.New(t)

		err := cli.NewCmdVouch(&c).Run(ctx, []string{"reset", "@nowhere/admin"})
		x.Error(err)
	})
}

// TestTheCliLetsASoleOperatorBackIn is #16.
//
// `roster init` makes the first operator and prints their password once. A
// deployment with one operator that loses it had no way back: every door that
// writes a password needs a credential, and `vouch` -- the one that needs none
// -- looked the person up on the data plane, where the operator is not.
//
// `reset` and not `set`, and the difference is the second half of the test: a
// recovery that leaves the old sessions alive is not one.
func TestTheCliLetsASoleOperatorBackIn(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	lost := passwordFrom(t, out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// Somebody holding the session the lost password opened -- which is the
	// case a recovery is for.
	held := signIn(t, s, "admin", lost)
	x.NotNil(held)

	conn := servedControl(t, s)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", held.Name+"="+held.Value))

	_, err = app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
	x.NoError(err)

	t.Run("and on the data plane the operator is nobody", func(t *testing.T) {
		x := require.New(t)

		err := cli.NewCmdVouch(&c).Run(ctx, []string{"reset", "@admin"})
		x.Error(err, "the data plane answered about somebody who lives on the control plane")
		x.ErrorContains(err, "no holder is called")
	})

	secret := stdoutOf(t, cli.NewCmdControl(&c), "vouch", "reset", "@admin")
	x.NotEmpty(secret)

	x.NotNil(signIn(t, s, "admin", secret), "the password the command printed does not open the console")
	x.Nil(signIn(t, s, "admin", lost), "the lost password still opens the console")

	t.Run("and the session the lost one opened is over", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("and a typo is refused rather than made an operator", func(t *testing.T) {
		x := require.New(t)

		err := cli.NewCmdControl(&c).Run(ctx, []string{"vouch", "reset", "@admni"})
		x.Error(err)

		n, err := s.Control.Ent.Holder.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "a recovery wrote a holder")
	})

	t.Run("and set and unlock cross with it", func(t *testing.T) {
		x := require.New(t)

		x.NoError(piped(t, "correct horse battery staple",
			cli.NewCmdControl(&c), "vouch", "set", "--password-stdin", "@admin"))
		x.NotNil(signIn(t, s, "admin", "correct horse battery staple"))

		x.NoError(cli.NewCmdControl(&c).Run(ctx, []string{"vouch", "unlock", "@admin"}))
	})
}

// TestTheControlPlaneHoldsTheCorpusToo is #18.
//
// The control plane was built with `vouch.lockout` and `vouch.password` and not
// `vouch.breached`, beside a comment saying an operator's password is held to
// the same numbers as anybody's. So the account that runs the deployment was
// the one whose password nothing checked against the corpus.
func TestTheControlPlaneHoldsTheCorpusToo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	c.Vouch.Breached = leakedCorpus(t, "hunter2hunter2")

	// On the same database the rest of this runs on, which is #20: a refused
	// `init` is one transaction and leaves nothing for the next to trip on.
	err := piped(t, "hunter2hunter2", cli.NewCmdInit(&c), "--password-stdin")
	x.Error(err, "the first operator was given a password this deployment refuses everywhere else")
	x.Equal(codes.FailedPrecondition, status.Code(err))

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	err = piped(t, "hunter2hunter2", cli.NewCmdControl(&c), "vouch", "set", "--password-stdin", "@admin")
	x.Error(err, "an operator's password went past the corpus")
	x.Equal(codes.FailedPrecondition, status.Code(err))

	t.Run("and the check is a check rather than a refusal of everything", func(t *testing.T) {
		x := require.New(t)

		x.NoError(piped(t, "correct horse battery staple",
			cli.NewCmdControl(&c), "vouch", "set", "--password-stdin", "@admin"))

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		x.NotNil(signIn(t, s, "admin", "correct horse battery staple"))
	})
}
