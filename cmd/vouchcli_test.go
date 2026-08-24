package cmd_test

import (
	"os"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
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

	h, err := s.Ungated.Holder().Add(t.Context(), app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  holder,
	}.Build())
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
// and the console was the right one.
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
	secret := minted(t, cmd.NewCmdVouch(&c), "reset", "@newco/admin")
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
			cmd.NewCmdVouch(&c), "set", "--password-stdin", "@newco/admin"))

		s3, err := cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s3.Close() })

		v = vouch.New(s3.Ungated, s3.Ungated)
		x.True(asked("correct horse battery staple"))
		x.False(asked(secret), "the password it replaced still works")
	})

	t.Run("and never as an argument", func(t *testing.T) {
		x := require.New(t)

		err := cmd.NewCmdVouch(&c).Run(ctx, []string{"set", "@newco/admin"})
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
	c.Vouch.Breached = leakedCorpus(t, "hunter2")

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	customer(t, s, "newco", "admin")
	x.NoError(s.Close())

	err = piped(t, "hunter2", cmd.NewCmdVouch(&c), "set", "--password-stdin", "@newco/admin")
	x.Error(err, "a shell stored a password this deployment refuses everywhere else")

	// `FailedPrecondition` rather than `InvalidArgument`, as everywhere else:
	// there is nothing wrong with the request, the world changed under the
	// value in it.
	x.Equal(codes.FailedPrecondition, status.Code(err))

	t.Run("and the check is a check rather than a refusal of everything", func(t *testing.T) {
		x := require.New(t)

		x.NoError(piped(t, "correct horse battery staple",
			cmd.NewCmdVouch(&c), "set", "--password-stdin", "@newco/admin"))
	})

	t.Run("and a generated one goes through the same door", func(t *testing.T) {
		x := require.New(t)

		v := minted(t, cmd.NewCmdVouch(&c), "reset", "@newco/admin")
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
	err = cmd.NewCmdVouch(&c).Run(ctx, []string{"unlock", "@newco/admin"})
	x.Error(err, "there was nothing to unlock and it said otherwise")
	x.Equal(codes.NotFound, status.Code(err))

	_ = minted(t, cmd.NewCmdVouch(&c), "reset", "@newco/admin")

	x.NoError(cmd.NewCmdVouch(&c).Run(ctx, []string{"unlock", "@newco/admin"}))

	t.Run("and nobody is not somebody", func(t *testing.T) {
		x := require.New(t)

		err := cmd.NewCmdVouch(&c).Run(ctx, []string{"unlock", "@newco/nobody"})
		x.Error(err)
		x.ErrorContains(err, "nobody")
	})

	t.Run("and an alias in no tenant at all", func(t *testing.T) {
		x := require.New(t)

		err := cmd.NewCmdVouch(&c).Run(ctx, []string{"reset", "@nowhere/admin"})
		x.Error(err)
	})
}
