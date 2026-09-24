package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// arrivingAt is a call carrying the name a browser came in on, the way a
// transcoded request carries it.
func arrivingAt(t *testing.T, host string) metadata.MD {
	t.Helper()

	return metadata.Pairs("x-forwarded-host", host)
}

// TestARosterUserSignsInOnTheirOwnTenantsName is the door #30 is about.
//
// `AuthService` was registered once, on the control listener, so the only
// people who could obtain a credential **from roster** were the control
// plane's: operators and the deployment's own callers. A holder in a tenant
// could not sign in at all, which is why tenant administration had nowhere to
// live but the port that waives the rules (#26).
//
// Which tenant comes from the name the request arrived at, because on a plane
// with many there is nowhere else it could come from.
func TestARosterUserSignsInOnTheirOwnTenantsName(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })

	// A name contoso answers at, and a password for somebody in it.
	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "contoso.example",
	}.Build())
	x.NoError(err)

	res, err := b.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
		Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	secret := res.GetSecret()
	x.NotEmpty(secret)

	conn := pdtest.Serve(t, b.grpc(t))
	auth := app.NewAuthServiceClient(conn)

	at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))
	_, err = auth.SignIn(at, app.AuthSignInRequest_builder{
		Alias: "someone", Password: secret,
	}.Build())
	x.NoError(err, "the tenant's own person could not sign in at the tenant's own name")

	t.Run("and a wrong password is one answer however it was wrong", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.SignIn(at, app.AuthSignInRequest_builder{
			Alias: "someone", Password: secret + "x",
		}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// The failure the resolution exists to refuse: a sign-in that carried on
	// with no tenant would look somebody up in whichever one it reached.
	t.Run("and a name nothing claims is refused, naming it", func(t *testing.T) {
		x := require.New(t)

		nowhere := metadata.NewOutgoingContext(ctx, arrivingAt(t, "nobody.example"))
		_, err := auth.SignIn(nowhere, app.AuthSignInRequest_builder{
			Alias: "someone", Password: secret,
		}.Build())
		x.Error(err)
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.ErrorContains(err, "nobody.example")
	})

	t.Run("and a request that says no name at all is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.SignIn(ctx, app.AuthSignInRequest_builder{
			Alias: "someone", Password: secret,
		}.Build())
		x.Error(err)
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	// Somebody in another tenant with the same alias is a different person, and
	// the name is what tells them apart.
	t.Run("and the name decides which tenant's alias it is", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
			Name:   "fabrikam.example",
		}.Build())
		x.NoError(err)

		elsewhere := metadata.NewOutgoingContext(ctx, arrivingAt(t, "fabrikam.example"))
		_, err = auth.SignIn(elsewhere, app.AuthSignInRequest_builder{
			Alias: "someone", Password: secret,
		}.Build())
		x.Error(err, "contoso's password signed somebody in at fabrikam's name")
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestTheDataPlaneServesNoSignInUnlessItSaysSo is the default, and it is off.
//
// Not *served and refusing*: a method that answers no is a method somebody can
// count answers from. Off, it is not on the wire, and `Unimplemented` is what a
// caller gets.
func TestTheDataPlaneServesNoSignInUnlessItSaysSo(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t)

	conn := pdtest.Serve(t, b.grpc(t))
	_, err := app.NewAuthServiceClient(conn).SignIn(
		metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example")),
		app.AuthSignInRequest_builder{Alias: "someone", Password: "whatever"}.Build())

	x.Error(err)
	x.Equal(codes.Unimplemented, status.Code(err))
}

// TestTheCookieARosterUserGetsIsACaller is the half that makes the door worth
// having: a session that names somebody the wall can narrow.
//
// A control plane session cannot be resolved here -- two databases, no query
// between them -- and that is what `auth.proto` was reading when it said the
// control listener is where the people who sign in live. A session minted over
// **these** rows names a holder of this plane, and every read it makes is
// narrowed to the tenant that holder is in.
func TestTheCookieARosterUserGetsIsACaller(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })

	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "contoso.example",
	}.Build())
	x.NoError(err)

	res, err := b.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
		Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, b.grpc(t))

	var h metadata.MD
	_, err = app.NewAuthServiceClient(conn).SignIn(
		metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example")),
		app.AuthSignInRequest_builder{Alias: "someone", Password: res.GetSecret()}.Build(),
		grpc.Header(&h))
	x.NoError(err)

	cookie := ""
	for _, v := range h.Get("set-cookie") {
		if i := strings.IndexByte(v, ';'); i >= 0 {
			cookie = v[:i]
		} else {
			cookie = v
		}
	}
	x.NotEmpty(cookie, "a sign-in that minted no cookie is a sign-in nobody can use")

	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", cookie))

	// Who the caller is, answered from the session and from nothing the caller
	// sent: `MeService` takes no subject.
	me, err := app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
	x.NoError(err, "the cookie it minted names nobody")
	x.Equal(b.ContosoUser.Bytes(), me.GetId())
	x.Equal(b.Contoso.Bytes(), me.GetTenant(), "the session named a holder of another tenant")
	x.Equal("someone", me.GetAlias())
}
