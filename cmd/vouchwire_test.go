package cmd_test

import (
	"encoding/base32"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// pipedOut is [piped] and [stdoutOf] at once: stdin fed, stdout captured --
// which is the shape of every command here, since a secret goes in on the one
// and comes out on the other.
func pipedOut(t *testing.T, in string, k *xli.Command, args ...string) (string, error) {
	t.Helper()
	x := require.New(t)

	ir, iw, err := os.Pipe()
	x.NoError(err)
	_, err = iw.WriteString(in)
	x.NoError(err)
	x.NoError(iw.Close())

	or, ow, err := os.Pipe()
	x.NoError(err)

	wasIn, wasOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = ir, ow

	runErr := k.Run(t.Context(), args)

	os.Stdin, os.Stdout = wasIn, wasOut
	x.NoError(ow.Close())

	b, err := io.ReadAll(or)
	x.NoError(err)

	return strings.TrimSpace(string(b)), runErr
}

// pipedErr is [pipedOut] that also hands back stderr, for a command whose
// point is the sentence it prints there.
func pipedErr(t *testing.T, in string, k *xli.Command, args ...string) (string, string, error) {
	t.Helper()
	x := require.New(t)

	ir, iw, err := os.Pipe()
	x.NoError(err)
	_, err = iw.WriteString(in)
	x.NoError(err)
	x.NoError(iw.Close())

	or, ow, err := os.Pipe()
	x.NoError(err)
	er, ew, err := os.Pipe()
	x.NoError(err)

	wasIn, wasOut, wasErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = ir, ow, ew

	runErr := k.Run(t.Context(), args)

	os.Stdin, os.Stdout, os.Stderr = wasIn, wasOut, wasErr
	x.NoError(ow.Close())
	x.NoError(ew.Close())

	out, err := io.ReadAll(or)
	x.NoError(err)
	errOut, err := io.ReadAll(er)
	x.NoError(err)

	return strings.TrimSpace(string(out)), string(errOut), runErr
}

// sets plants a password on the serving stack, in process -- the operator's
// act the wire commands then prove things against.
func sets(t *testing.T, b *cliServed, who []byte, secret string) {
	t.Helper()

	_, err := vouch.New(b.Server.Ungated, b.Server.Ungated).Set(t.Context(), app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: who}.Build(),
		Secret: []byte(secret),
	}.Build())
	require.NoError(t, err)
}

// actingAs calls HolderService/Get bearing her key with a delegation beside
// it, which is the only correct way to spend one.
func actingAs(t *testing.T, b *cliServed, delegation string, of []byte) error {
	t.Helper()
	x := require.New(t)

	conn, err := grpc.NewClient(b.Hers.Client.Addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })

	ctx := metadata.AppendToOutgoingContext(t.Context(),
		"authorization", "Bearer "+b.Hers.Client.Auth.Credential,
		keys.HeaderActing, delegation,
	)

	_, err = app.NewHolderServiceClient(conn).Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: of}.Build(),
	}.Build())

	return err
}

// TestTheTerminalIsACallerThatSignsPeopleIn is the wire half of `roster
// vouch`, end to end: verify, delegate, link, redeem, revoke and accept, each
// from a shell, as the caller the credential in `client.auth` says.
func TestTheTerminalIsACallerThatSignsPeopleIn(t *testing.T) {
	const (
		holderGet = "/roster.HolderService/Get"
		verify    = "/roster.VouchService/Verify"
		delegate  = "/roster.VouchService/Delegate"
		link      = "/roster.VouchService/Link"
		redeem    = "/roster.VouchService/Redeem"
		revoke    = "/roster.VouchService/Revoke"
		accept    = "/roster.VouchService/Accept"
	)

	b := cliUp(t, holderGet, verify, delegate, link, redeem, revoke, accept)
	const pw = "correct horse battery staple"
	sets(t, b, b.Alice.GetId(), pw)

	t.Run("verify answers ok, and prints no secret anywhere", func(t *testing.T) {
		x := require.New(t)

		out, err := pipedOut(t, pw+"\n", cmd.Cmd(&b.Hers),
			"vouch", "verify", "--secret-stdin", "@newco/alice")
		x.NoError(err)
		x.Empty(out, "verify has nothing for stdout; ok is an exit code")
	})

	t.Run("a wrong secret and a stranger are one identical no", func(t *testing.T) {
		x := require.New(t)

		_, err1 := pipedOut(t, "not the password\n", cmd.Cmd(&b.Hers),
			"vouch", "verify", "--secret-stdin", "@newco/alice")
		x.Error(err1)

		_, err2 := pipedOut(t, pw+"\n", cmd.Cmd(&b.Hers),
			"vouch", "verify", "--secret-stdin", "@newco/ghost")
		x.Error(err2)

		x.Equal(err1.Error(), err2.Error(),
			"the uniform no did not survive the terminal")
	})

	t.Run("delegate mints, and the token acts as her beside her key", func(t *testing.T) {
		x := require.New(t)

		rd, err := pipedOut(t, pw+"\n", cmd.Cmd(&b.Hers),
			"vouch", "delegate", "--secret-stdin", "--allow", holderGet, "@newco/alice")
		x.NoError(err)
		x.True(strings.HasPrefix(rd, "rd_"), "%q", rd)

		x.NoError(actingAs(t, b, rd, b.Alice.GetId()))

		t.Run("and revoke ends it, now", func(t *testing.T) {
			x := require.New(t)

			_, err := pipedOut(t, rd+"\n", cmd.Cmd(&b.Hers),
				"vouch", "revoke", "--token-stdin")
			x.NoError(err)

			err = actingAs(t, b, rd, b.Alice.GetId())
			x.Error(err, "a revoked delegation went on working")
		})
	})

	t.Run("a link answers the same for a name that is nobody's", func(t *testing.T) {
		x := require.New(t)

		hers := stdoutOf(t, cmd.Cmd(&b.Hers), "vouch", "link", "@newco/alice")
		dud := stdoutOf(t, cmd.Cmd(&b.Hers), "vouch", "link", "@newco/ghost")

		x.NotEmpty(hers)
		x.NotEmpty(dud)
		x.Equal(hers[:3], dud[:3], "the shape differs, which answers *is this name here*")

		t.Run("redeem spends hers, once", func(t *testing.T) {
			x := require.New(t)

			rd, err := pipedOut(t, hers+"\n", cmd.Cmd(&b.Hers),
				"vouch", "redeem", "--token-stdin", "--allow", holderGet)
			x.NoError(err)
			x.True(strings.HasPrefix(rd, "rd_"), "%q", rd)
			x.NoError(actingAs(t, b, rd, b.Alice.GetId()))

			_, again := pipedOut(t, hers+"\n", cmd.Cmd(&b.Hers),
				"vouch", "redeem", "--token-stdin", "--allow", holderGet)
			x.Error(again, "a link is single use")

			_, ghost := pipedOut(t, dud+"\n", cmd.Cmd(&b.Hers),
				"vouch", "redeem", "--token-stdin", "--allow", holderGet)
			x.Error(ghost)
			x.Equal(again.Error(), ghost.Error(),
				"a spent link and a dud must be one indistinguishable no")
		})
	})

	t.Run("accept mints on the caller's word, and its errors are real", func(t *testing.T) {
		x := require.New(t)

		mustIdentity(t, t.Context(), b.Server, mustId(t, b.Alice.GetId()), "entra", "entra-subject-9")
		tn, _ := pdid.From(b.Tenant.GetId())

		rd := stdoutOf(t, cmd.Cmd(&b.Hers), "vouch", "accept",
			"--tenant", tn.String(), "--provider", "entra", "--subject", "entra-subject-9",
			"--allow", holderGet)
		x.True(strings.HasPrefix(rd, "rd_"), "%q", rd)
		x.NoError(actingAs(t, b, rd, b.Alice.GetId()))

		err := cmd.Cmd(&b.Hers).Run(t.Context(), []string{"vouch", "accept",
			"--tenant", tn.String(), "--provider", "entra", "--subject", "nobody-ever",
			"--allow", holderGet})
		x.Equal(codes.NotFound, status.Code(err),
			"Accept is the one sign-in call whose errors mean something")
	})

	t.Run("and locally every one of these is a refusal that names the split", func(t *testing.T) {
		x := require.New(t)

		err := cmd.Cmd(&b.Local).Run(t.Context(), []string{"vouch", "verify",
			"--secret-stdin", "@newco/alice"})
		x.Error(err)
		x.ErrorContains(err, "client.addr")
		x.ErrorContains(err, "reset|set|unlock")
	})
}

// TestASecondFactorEndToEndAtAShell is enrol, confirm, and the two-step
// sign-in, entirely from a terminal: the flow `vouch.proto` writes as
// Delegate -> Delegate-with-continuation, with the codes read off the seed the
// enrolment printed.
func TestASecondFactorEndToEndAtAShell(t *testing.T) {
	const (
		holderGet = "/roster.HolderService/Get"
		verify    = "/roster.VouchService/Verify"
		delegate  = "/roster.VouchService/Delegate"
		enrol     = "/roster.VouchService/Enrol"
	)

	x := require.New(t)
	b := cliUp(t, holderGet, verify, delegate, enrol)
	const pw = "correct horse battery staple"
	sets(t, b, b.Alice.GetId(), pw)

	uri := stdoutOf(t, cmd.Cmd(&b.Hers), "vouch", "enrol", "--name", "phone", "@newco/alice")
	x.True(strings.HasPrefix(uri, "otpauth://"), "%q", uri)

	u, err := url.Parse(uri)
	x.NoError(err)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(u.Query().Get("secret"))
	x.NoError(err)

	// Confirmed with the previous step's code, so the current one stays
	// unspent for the sign-in below -- a code verifies once, ever.
	step := time.Now().Unix() / 30
	out, err := pipedOut(t, vouch.CodeAt(seed, step-1)+"\n", cmd.Cmd(&b.Hers),
		"vouch", "verify", "--kind", "totp", "--name", "phone", "--secret-stdin", "@newco/alice")
	// A continuation, not a finished sign-in -- the password is still open --
	// so the exit is non-zero, which is what keeps a token capture from
	// mistaking a `vc_` for an `rd_`. The continuation is on stdout regardless.
	x.Error(err)
	x.True(strings.HasPrefix(out, "vc_"), "confirming over the wire answers a continuation: %q", out)

	// The password alone is not a sign-in any more.
	cont, err := pipedOut(t, pw+"\n", cmd.Cmd(&b.Hers),
		"vouch", "delegate", "--secret-stdin", "--allow", holderGet, "@newco/alice")
	x.Error(err, "a half-done sign-in exits non-zero so a script does not capture the wrong token")
	x.True(strings.HasPrefix(cont, "vc_"), "a second factor is enrolled, so this is half way: %q", cont)

	// And the continuation plus one code is. The name travels too: an unset
	// name means the unnamed row, never "any" -- D36's rule, felt at a shell.
	rd, err := pipedOut(t, vouch.CodeAt(seed, step)+"\n", cmd.Cmd(&b.Hers),
		"vouch", "delegate", "--secret-stdin", "--continuation", cont,
		"--kind", "totp", "--name", "phone", "--allow", holderGet)
	x.NoError(err)
	x.True(strings.HasPrefix(rd, "rd_"), "%q", rd)
	x.NoError(actingAs(t, b, rd, b.Alice.GetId()))
}

// TestContinueProvesAndDelegateMints holds the CLI to the split the service
// draws: `Continue` ends a sign-in proven, with nothing to show for it, and
// the command says so instead of leaving an empty success on the screen.
func TestContinueProvesAndDelegateMints(t *testing.T) {
	const (
		holderGet = "/roster.HolderService/Get"
		verify    = "/roster.VouchService/Verify"
		delegate  = "/roster.VouchService/Delegate"
		enrol     = "/roster.VouchService/Enrol"
		cont      = "/roster.VouchService/Continue"
	)

	x := require.New(t)
	b := cliUp(t, holderGet, verify, delegate, enrol, cont)
	const pw = "correct horse battery staple"
	sets(t, b, b.Alice.GetId(), pw)

	uri := stdoutOf(t, cmd.Cmd(&b.Hers), "vouch", "enrol", "@newco/alice")
	u, err := url.Parse(uri)
	x.NoError(err)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(u.Query().Get("secret"))
	x.NoError(err)

	step := time.Now().Unix() / 30
	_, err = pipedOut(t, vouch.CodeAt(seed, step-1)+"\n", cmd.Cmd(&b.Hers),
		"vouch", "verify", "--kind", "totp", "--secret-stdin", "@newco/alice")
	x.Error(err, "confirming leaves the password open, which is a continuation and a non-zero exit")

	half, err := pipedOut(t, pw+"\n", cmd.Cmd(&b.Hers),
		"vouch", "delegate", "--secret-stdin", "--allow", holderGet, "@newco/alice")
	x.Error(err)
	x.True(strings.HasPrefix(half, "vc_"), "%q", half)

	out, err := pipedOut(t, vouch.CodeAt(seed, step)+"\n", cmd.Cmd(&b.Hers),
		"vouch", "continue", "--continuation", half, "--kind", "totp", "--secret-stdin")
	x.NoError(err)
	x.Empty(out, "Continue never mints, so there is nothing for stdout")
}

// TestDelegateRefusesTwoWaysOfNamingSomebody keeps the CLI to the server's own
// rule: a continuation carries a sign-in already begun, so naming somebody
// beside it is refused rather than resolved by precedence -- otherwise a call
// could name one person on the line and sign in another.
func TestDelegateRefusesTwoWaysOfNamingSomebody(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, "/roster.VouchService/Delegate")

	err := cmd.Cmd(&b.Hers).Run(t.Context(), []string{
		"vouch", "delegate", "--secret-stdin", "--continuation", "vc_whatever",
		"--allow", "/roster.HolderService/Get", "@newco/alice"})
	x.Error(err)
	x.NotContains(err.Error(), "rpc error",
		"the refusal is the CLI's, before the wire, not the server's after it")
	x.ErrorContains(err, "carry on")
}

// TestTheEnrolHintNamesWhom runs the exact `verify` line enrol suggests, and
// asserts it resolves somebody -- the finding was that the hint quoted a
// command with no WHO, which names nobody.
func TestTheEnrolHintNamesWhom(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, "/roster.VouchService/Enrol", "/roster.VouchService/Verify")
	sets(t, b, b.Alice.GetId(), "correct horse battery staple")

	_, hint, err := pipedErr(t, "", cmd.Cmd(&b.Hers),
		"vouch", "enrol", "--name", "phone", "@newco/alice")
	x.NoError(err)

	// The line the hint printed, taken apart into arguments and stripped of
	// the leading `roster`, is run verbatim -- the strongest form of "it works
	// if typed".
	var line string
	for _, l := range strings.Split(hint, "\n") {
		if strings.Contains(l, "vouch verify") {
			line = strings.TrimSpace(l)
		}
	}
	x.NotEmpty(line, "enrol printed no verify hint:\n%s", hint)

	fields := strings.Fields(strings.TrimPrefix(line, "roster "))
	// Rebuild `--name "phone"` which Fields split on the space inside quotes.
	args := []string{}
	for i := 0; i < len(fields); i++ {
		if fields[i] == `--name` && i+1 < len(fields) {
			args = append(args, "--name", strings.Trim(fields[i+1], `"`))
			i++
			continue
		}
		args = append(args, fields[i])
	}

	// A wrong code, on purpose: what is asserted is that it reaches the secret
	// comparison at all -- i.e. WHO resolved -- not that the code is right. A
	// missing WHO would have failed in the CLI, before the wire, naming nobody.
	err = cmd.Cmd(&b.Hers).Run(t.Context(), argsWithStdin(t, args, "000000"))
	x.Error(err)
	x.NotContains(err.Error(), "names nobody")
	x.NotContains(err.Error(), "WHO:")
}

// argsWithStdin is a shim: the verify the hint names reads its secret from
// stdin, so this test feeds one by swapping os.Stdin around the run. Returning
// the args unchanged keeps the call site readable; the stdin swap is the work.
func argsWithStdin(t *testing.T, args []string, in string) []string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	_, err = w.WriteString(in + "\n")
	x.NoError(err)
	x.NoError(w.Close())

	was := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = was })

	return args
}
