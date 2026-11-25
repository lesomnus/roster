package cmd_test

import (
	"encoding/base64"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
	"github.com/lesomnus/roster/server/vouch/vouchtest"
)

// TestAnOperatorHoldsASecondFactor: whoever runs a deployment enrols an
// authenticator app from a shell on the box, proves it, and is asked for its
// code at the admin console from then on.
//
// # What it was
//
// They could not have one. The control plane was built with the deployment's
// lockout and its password rules and **not** its keyring -- *the control plane
// holds no second factors*, said the line that left it out -- so
// `CredentialService/Enrol` there answered that the deployment held no key to
// wrap a seed with, whatever `vouch.keys` said. A customer's people could ask
// for a second proof and the one account that is every customer's root could
// not (#275; the owner's decision, 2026-11-15).
//
// From a shell, because that is where who runs a deployment is managed
// (`docs/operating.md`): the admin console's work is customers.
func TestAnOperatorHoldsASecondFactor(t *testing.T) {
	x := require.New(t)

	s, c, out := adminDeployment(t, func(c *cmd.Config) {
		c.Vouch.Keys = []string{"one:" + base64.StdEncoding.EncodeToString(freshKey(t))}
	})
	password := passwordFrom(t, out)
	c.Client.As = "admin" // who `init` made, named at the box (#284)

	g, err := s.GrpcAdmin(t.Context(), c)
	x.NoError(err)
	auth := app.NewAuthServiceClient(pdtest.Serve(t, g))

	signIn := func(req *app.AuthSignInRequest) (*app.AuthSignInResponse, string, error) {
		var h metadata.MD
		res, err := auth.SignIn(t.Context(), req, grpc.Header(&h))

		return res, cookieOf(h), err
	}

	// Enrolled at the box, and printed once as what an authenticator app scans.
	uri := stdoutOf(t, cli.NewCmdControl(&c), "vouch", "enrol", "@admin")
	u, err := url.Parse(uri)
	x.NoError(err, "what `enrol` printed is not a URI: %q", uri)
	x.Equal("otpauth", u.Scheme)
	seed := seedOf(t, u.Query().Get("secret"))

	t.Run("it does not count until it is proved", func(t *testing.T) {
		x := require.New(t)

		// A seed nobody has proved is a QR somebody may have mis-scanned, and
		// asking for its code would be locking them out with it.
		res, cookie, err := signIn(app.AuthSignInRequest_builder{Alias: "admin", Secret: []byte(password)}.Build())
		x.NoError(err)
		x.NotEmpty(cookie)
		x.Empty(res.GetContinuation())
	})

	t.Run("a wrong code proves nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := pipedOut(t, "000000", cli.NewCmdControl(&c), "vouch", "prove", "--code-stdin", "@admin")
		x.Error(err)
	})

	// Proved with the code of the step before, which the drift takes and which
	// leaves this step's for the sign-in below.
	said, err := pipedOut(t, vouchtest.CodeBefore(seed), cli.NewCmdControl(&c), "vouch", "prove", "--code-stdin", "@admin")
	x.NoError(err, said)

	t.Run("and then the password alone is not a session", func(t *testing.T) {
		x := require.New(t)

		res, cookie, err := signIn(app.AuthSignInRequest_builder{Alias: "admin", Secret: []byte(password)}.Build())
		x.NoError(err)
		x.Empty(cookie, "an operator with a second factor was handed a session for the first")
		x.Len(res.GetAvailable(), 1)
		x.Equal(vouch.KindTotp, res.GetAvailable()[0].GetKind())

		_, cookie, err = signIn(app.AuthSignInRequest_builder{
			Continuation: res.GetContinuation(), Kind: vouch.KindTotp, Secret: []byte("000000"),
		}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.Empty(cookie)

		res, _, err = signIn(app.AuthSignInRequest_builder{Alias: "admin", Secret: []byte(password)}.Build())
		x.NoError(err)

		_, cookie, err = signIn(app.AuthSignInRequest_builder{
			Continuation: res.GetContinuation(), Kind: vouch.KindTotp,
			Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
		}.Build())
		x.NoError(err)
		x.NotEmpty(cookie)
	})
}
