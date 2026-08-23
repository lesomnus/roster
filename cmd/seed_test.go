package cmd_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// seedbed is one deployment that `roster init` can be pointed at more than
// once.
//
// [inited] cannot be used for any of this: it makes its databases and then
// forgets where they were, which is right for a test about what one `init`
// leaves and useless for a test about the second one. What matters here is that
// both runs see the same rows, so the configuration is what is handed back.
func seedbed(t *testing.T) cmd.Config {
	t.Helper()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	return cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}
}

// initRun is one `roster init`, run the way a shell or an entrypoint runs it.
//
// A fresh command each time rather than one reused, because that is what a
// second `docker run` is: a process that parsed its flags from nothing.
func initRun(t *testing.T, c cmd.Config, args ...string) (string, error) {
	t.Helper()

	out := &bytes.Buffer{}
	k := cmd.NewCmdInit(&c)
	k.Writer = out

	err := k.Run(t.Context(), args)

	return out.String(), err
}

// TestInitRefusesToRunTwice is an operational contract with a shell script
// depending on it and, until now, nothing pinning it.
//
// `docker/entrypoint.sh` seeds once and decides "once" by a marker file it
// writes **after** `init` succeeds -- and it says, in as many words, that
// tolerating a failed `init` would be tolerating the one case the failure
// exists to report: somebody pointed this at the wrong deployment. That whole
// design rests on the second run being an error. If `init` ever became a no-op
// on an existing database -- which is the shape somebody reaches for the first
// time a container restarts and the logs look ugly -- the script would keep
// working, the marker would keep being written, and nothing anywhere would say
// that a deployment had been seeded twice.
//
// So this asserts the refusal, and then asserts the thing that makes the
// refusal safe rather than merely loud: that a second run leaves the first
// deployment exactly as it was. An `init` that errored *after* writing is worse
// than one that succeeded, because the operator is told to look and finds
// nothing wrong.
func TestInitRefusesToRunTwice(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	secret := passwordFrom(t, out)
	x.NotEmpty(secret)

	_, err = initRun(t, c)
	x.Error(err, "a second init against a seeded deployment reported success")

	// Named by the alias that collided, because the operator reading this is
	// deciding whether they are looking at the wrong database or at a restart.
	x.ErrorContains(err, "contoso")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// One of everything the first run wrote, which is the claim that the
	// refusal came before any write and not after some of them.
	t.Run("and writes nothing on the way out", func(t *testing.T) {
		x := require.New(t)

		n, err := s.Ent.Tenant.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "a second tenant was written by the run that failed")

		n, err = s.Ent.Holder.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n)

		n, err = s.Ent.Role.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n)

		n, err = s.Ent.Binding.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n)
	})

	// The one a botched second run would be most expensive to have touched.
	// The password is shown once and written down by a person; a run that
	// rotated it would lock the operator out of the deployment they were trying
	// to stand up, with the new one printed in a command that reported failure.
	t.Run("and the operator can still sign in", func(t *testing.T) {
		x := require.New(t)

		n, err := s.Control.Ent.Credential.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n)

		x.NotNil(signIn(t, s, "ops", secret),
			"the second init changed the password the first one printed")
	})
}

// TestASecondInitStopsBeforeTheOperator is the same command interrupted where
// it actually can be, which is not where the test above stops it.
//
// [cmd.Seed] is a sequence of RPCs and not a transaction: a tenant, a holder, a
// role, a binding, and then the control plane's operator in a second database
// that no transaction could have spanned anyway. The run above never gets past
// the first call, so it says nothing about what a partial one leaves.
//
// A second `init` naming a **different** tenant does get past it. The data
// plane's four rows are written, and then the control plane refuses: the
// operator's tenant already holds a role called "everything", and an alias is
// unique. That refusal lands in `seedOperator` before the passphrase is
// generated and before it is set -- which is the property worth pinning,
// because the alternative is an operator who wrote down the first password
// finding it no longer works after a command that failed.
//
// What is left behind is a customer nobody asked for, and the point of the
// error is that somebody is told to go and look at it. `docker/entrypoint.sh`
// does not write its marker, so it retries -- and the retry fails on the tenant
// instead, which is the loud version of not converging.
func TestASecondInitStopsBeforeTheOperator(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	secret := passwordFrom(t, out)

	_, err = initRun(t, c, "--tenant", "fabrikam")
	x.Error(err, "a second init reseeded the control plane of a live deployment")
	x.ErrorContains(err, "operator", "it failed somewhere other than the operator")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// The half that did happen, said out loud so that a later change which
	// makes `Seed` roll this back fails here and is looked at, rather than
	// quietly making the comment above wrong.
	t.Run("the data plane keeps what the failed run wrote", func(t *testing.T) {
		x := require.New(t)

		n, err := s.Ent.Tenant.Query().Count(ctx)
		x.NoError(err)
		x.Equal(2, n, "contoso from the first run, fabrikam from the one that failed")
	})

	// The half that must not have.
	t.Run("and the operator's password is untouched", func(t *testing.T) {
		x := require.New(t)

		n, err := s.Control.Ent.Credential.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "a second credential was written for the one operator")

		x.NotNil(signIn(t, s, "ops", secret),
			"the failed run rotated the password its own output did not print")
	})
}

// TestAConsoleIssuesAPasswordForAnOperator is `IssueService.IssuePassword`
// travelled end to end, which nothing did.
//
// It is registered on the control plane's port by both `cmd/serve.go` and
// `wasm/main.go` and has no caller in this repository, which is a reasonable
// thing to be suspicious of. It is not dead: it is the **only** way a second
// operator ever gets a password. `roster init` makes the first one and refuses
// to run again -- the two tests above are that refusal -- `roster key add`
// mints keys and not passwords, `console.SetPassword` is exported for the
// sandbox and takes a secret somebody chose, and `VouchService.Set` behind the
// wall is how a person changes their own. Somebody has to be able to hand a way
// in to a colleague, and to whoever lost theirs, without a shell on the box.
//
// So it stays, and what it promises is the part worth pinning: the string it
// answers with is a string that signs in. That is one call between a generator
// and a hasher and it is easy to get subtly wrong in a way no unit test of
// either half would see -- the wrong holder, the wrong plane, a password
// trimmed on the way out.
func TestAConsoleIssuesAPasswordForAnOperator(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)

	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	conn := servedControl(t, s)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	issue := app.NewIssueServiceClient(conn)

	// Naming somebody who is not there creates them, which is the flow: an
	// operator is added by being given a way in, the same decision `roster key
	// add --service` already made about a caller.
	v, err := issue.IssuePassword(as, app.IssuePasswordRequest_builder{Alias: "second"}.Build())
	x.NoError(err)
	x.NotEmpty(v.GetPassword())
	x.GreaterOrEqual(len(v.GetPassword()), 32)

	t.Run("and it is a password that signs in", func(t *testing.T) {
		x := require.New(t)
		x.NotNil(signIn(t, s, "second", v.GetPassword()),
			"the console handed out a password the console door does not take")
	})

	// The reason it exists rather than a nicety. An operator who has lost their
	// password cannot be helped by `init`, and this is what a colleague calls.
	t.Run("and issuing again replaces what was there", func(t *testing.T) {
		x := require.New(t)

		w, err := issue.IssuePassword(as, app.IssuePasswordRequest_builder{Alias: "second"}.Build())
		x.NoError(err)
		x.NotEqual(v.GetPassword(), w.GetPassword())

		x.NotNil(signIn(t, s, "second", w.GetPassword()))
		x.Nil(signIn(t, s, "second", v.GetPassword()),
			"the password it replaced still opens the door")
	})

	// What is stored is a verifier, like everywhere else. This service exists
	// **because** `(payday.field).secret` means the column never comes back out
	// -- so an implementation that kept the plaintext to be able to answer
	// twice would have quietly undone the rule it was written to respect.
	t.Run("and the plaintext is not kept", func(t *testing.T) {
		x := require.New(t)

		// Issued **here**, and asked about immediately.
		//
		// Written against the password from the top of this test, it asserted
		// nothing: the subtest above has already replaced that operator's
		// password, so the plaintext it looked for was one no row was holding
		// any more and the loop was guaranteed to pass. What has to be absent
		// is the secret that was just handed out.
		u, err := issue.IssuePassword(as, app.IssuePasswordRequest_builder{Alias: "kept"}.Build())
		x.NoError(err)
		x.NotEmpty(u.GetPassword())

		vs, err := s.Control.Ent.Credential.Query().All(ctx)
		x.NoError(err)
		x.NotEmpty(vs)

		for _, cred := range vs {
			x.NotContains(string(cred.Secret), u.GetPassword())
		}

		// And it is a verifier rather than nothing at all: a column that came
		// back empty would satisfy the loop above and mean the password was
		// never stored.
		x.NotNil(signIn(t, s, "kept", u.GetPassword()))
	})

	t.Run("and nobody without a session asks for one", func(t *testing.T) {
		x := require.New(t)

		_, err := issue.IssuePassword(ctx, app.IssuePasswordRequest_builder{Alias: "third"}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// Refused rather than defaulted to the caller, which would be an RPC whose
	// misuse is silently a self-service password reset.
	t.Run("and it will not guess whose", func(t *testing.T) {
		x := require.New(t)

		_, err := issue.IssuePassword(as, app.IssuePasswordRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.InvalidArgument, status.Code(err))
	})
}
