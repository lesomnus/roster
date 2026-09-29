package cmd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
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

// TestThePageIsToldWhetherThereIsAFormToDraw is `#62`'s first half.
//
// `TenantConfig.password` is a fact roster enforces, and `Vouch.Verify` refuses a
// password for a tenant that has it off **with the same answer as a wrong one**
// -- `server/vouch/vouch.go` argues that and ends with *the app that draws the
// form was told by `/flow` and has no form to draw*. The Login App is told, the
// account app is told (`GET /providers` carries `password`), and the user console
// was not: it drew the form unconditionally, so a tenant whose people all arrive
// through a directory got a form every answer to which was refused, and the word
// "no".
//
// So the page asks first, and this is the asking. Both directions, because a
// method that answered `false` always would pass a test for the tenant this is
// about and break every other one.
func TestThePageIsToldWhetherThereIsAFormToDraw(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })

	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "contoso.example",
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, b.grpc(t))
	auth := app.NewAuthServiceClient(conn)
	at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))

	// Unset is yes, which is what a tenant written before the field existed
	// relies on -- `vouch.Offers` is the one sentence both sides ask.
	got, err := auth.Offers(at, app.AuthOffersRequest_builder{}.Build())
	x.NoError(err)
	x.True(got.GetPassword(), "a tenant that has never been configured was said to have no password")

	off(t, b, b.Contoso)

	got, err = auth.Offers(at, app.AuthOffersRequest_builder{}.Build())
	x.NoError(err)
	x.False(got.GetPassword(), "the page would have drawn a form every answer to which is refused")

	// And the subject is the name, not a field: there is nothing in the request
	// to point at another operator, so this cannot be used to read how one is
	// configured. A name nothing claims is refused rather than answered about
	// whichever tenant the call happened to reach.
	_, err = auth.Offers(metadata.NewOutgoingContext(ctx, arrivingAt(t, "nobody.example")),
		app.AuthOffersRequest_builder{}.Build())
	x.Error(err, "a name no tenant claims was answered about somebody")
}

// TestASessionTheServerHasForgottenIsNobodyAtTheDoor is #70.
//
// A session that idled out leaves its cookie in the browser, and every call
// the user console then made was refused before the handler -- the two that
// exist to be callable without a credential included. So the page could not
// learn how to sign in, drew a password form as its fallback, and that form
// could not work either; the only way out was deleting the cookie by hand.
//
// The rule is payday's (`auth.Public`): a credential that is no good is nobody
// on a method that asks nothing of the caller, and every answer to it clears
// the cookie. What is pinned here is that roster's two doors are those methods,
// that `Me` still is not, and that the sign-in the page then makes wins over
// the cookie it was made with.
func TestASessionTheServerHasForgottenIsNobodyAtTheDoor(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })
	answersAt(t, b.Server, b.Contoso, "contoso.example")

	res, err := b.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
		Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	secret := res.GetSecret()

	conn := pdtest.Serve(t, b.grpc(t))
	auth := app.NewAuthServiceClient(conn)
	at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))

	// A session, and then the row gone from under it: signed out here, idled
	// out in the wild, and one answer for both.
	var h metadata.MD
	_, err = auth.SignIn(at, app.AuthSignInRequest_builder{Alias: "someone", Password: secret}.Build(), grpc.Header(&h))
	x.NoError(err)
	dead := cookieIn(t, h)
	carrying := metadata.NewOutgoingContext(ctx,
		metadata.Join(arrivingAt(t, "contoso.example"), metadata.Pairs("cookie", dead)))

	_, err = auth.SignOut(carrying, app.AuthSignOutRequest_builder{}.Build())
	x.NoError(err)

	// Where the method asks who is calling: refused, as before.
	_, err = app.NewMeServiceClient(conn).Get(carrying, app.MeGetRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err), "a dead cookie was served")

	// Where it does not: the page learns what to draw ...
	got, err := auth.Offers(carrying, app.AuthOffersRequest_builder{}.Build())
	x.NoError(err, "a dead cookie shut the door it exists to open")
	x.True(got.GetPassword())

	// ... draws it, and the form works with the dead cookie still in the
	// browser. What comes back is the cookie that clears the dead one and then
	// the live one, in that order, so a browser applying them ends signed in.
	h = metadata.MD{}
	_, err = auth.SignIn(carrying, app.AuthSignInRequest_builder{Alias: "someone", Password: secret}.Build(), grpc.Header(&h))
	x.NoError(err, "the form the page drew could not work")
	x.Len(h.Get("set-cookie"), 2, "the dead cookie was not dropped, or the live one not minted: %v", h.Get("set-cookie"))
	live := cookieIn(t, h)
	x.NotEqual(dead, live)

	_, err = app.NewMeServiceClient(conn).Get(
		metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", live)),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err, "the session the page got names nobody")
}

// answersAt is a name a tenant answers at, which is a `Host` row.
func answersAt(t *testing.T, s *cmd.Server, in pdid.Id, name string) {
	t.Helper()

	_, err := s.Ungated.Host().Add(t.Context(), app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Name:   name,
	}.Build())
	require.NoError(t, err)
}

// decides writes a tenant's settings, through the verb the consoles call and
// under the version it read -- `off` in `waysin_test.go` one setting wider.
func decides(t *testing.T, s *cmd.Server, in pdid.Id, cfg *app.TenantConfig) error {
	t.Helper()
	ref := app.TenantRef_builder{Id: in.Bytes()}.Build()

	got, err := s.Ungated.Tenant().Get(t.Context(), app.TenantGetRequest_builder{
		Ref:    ref,
		Select: app.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	require.NoError(t, err)

	_, err = s.Ungated.Tenant().Update(t.Context(), app.TenantUpdateRequest_builder{
		Ref:         ref,
		DateUpdated: got.GetDateUpdated(),
		Config:      cfg,
	}.Build())

	return err
}

// arrivesThrough is a directory a tenant's people arrive through: a
// `Connection` row, which is configuration roster validates none of.
func arrivesThrough(t *testing.T, s *cmd.Server, in pdid.Id, name string) {
	t.Helper()

	_, err := s.Ungated.Connection().Add(t.Context(), app.ConnectionAddRequest_builder{
		Tenant:    app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Name:      name,
		Issuer:    "https://" + name + ".invalid/v2.0",
		ClientId:  "a-client-id",
		Scopes:    []string{"openid", "email"},
		SecretRef: "env:UNUSED",
	}.Build())
	require.NoError(t, err)
}

// claimBy is what a front door hands `Accept` after checking somebody.
func claimBy(tenant pdid.Id, provider, subject string) *app.VouchClaim {
	return app.VouchClaim_builder{Tenant: tenant.Bytes(), Provider: provider, Subject: subject}.Build()
}

// cookieIn is the session a sign-in answered with, as a browser would send it
// back.
func cookieIn(t *testing.T, h metadata.MD) string {
	t.Helper()

	cookie := ""
	for _, v := range h.Get("set-cookie") {
		if i := strings.IndexByte(v, ';'); i >= 0 {
			cookie = v[:i]
		} else {
			cookie = v
		}
	}
	require.NotEmpty(t, cookie, "a sign-in that minted no cookie is a sign-in nobody can use")

	return cookie
}

// TestAFrontDoorHandsSomebodyIntoTheirOwnConsole is `#62`'s second half, and
// the road a tenant whose people all arrive through a directory takes into its
// own console.
//
// The user console cannot do the sign-in itself: doing the OIDC exchange is being
// the relying party, and `connection.proto` decided roster is not (D19). So
// the front door stays the relying party -- it checks the person against the
// tenant's own directory and calls `Vouch.Accept` exactly as before -- and
// what is new is one hop. It asks for a **link** for the user console's name,
// hands it to the browser, and the user console spends it for a session of
// roster's own. roster checks nothing but its own link, which is not D19's
// question at all.
//
// Three rules tell that link from a recovery link, and each has a subtest: it
// is bound to the name it is spent at rather than to the caller that minted
// it; a tenant with no passwords may have one; and it ends in a session rather
// than a delegation. Two refusals bound the grant, and each has one too: the
// tenant has to have named a front door, and the name has to be the tenant's.
func TestAFrontDoorHandsSomebodyIntoTheirOwnConsole(t *testing.T) {
	x := require.New(t)
	b := keyedFor(t, func(c *cmd.Config) { c.SignIn.Enabled = true }, accept, redeem, listPeople)
	ctx := t.Context()

	// contoso answers at a name, has said where its people sign in, and somebody
	// arrives there through entra.
	answersAt(t, b.Server, b.Contoso, "contoso.example")
	x.NoError(decides(t, b.Server, b.Contoso, app.TenantConfig_builder{FrontDoor: "https://account.contoso.example"}.Build()))
	arrivesThrough(t, b.Server, b.Contoso, "entra")
	mustIdentity(t, ctx, b.Server, b.Who, "entra", "entra-subject-1")
	mayList(t, ctx, b, b.Who, listPeople)

	c := app.NewVouchServiceClient(b.Conn)
	auth := app.NewAuthServiceClient(b.Conn)
	as := bearing(ctx, b.Token)
	at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))

	// The page asks what the form is, and is told where to send somebody and
	// for which providers -- the two fields `GET /providers` answers, on the
	// wire this time.
	got, err := auth.Offers(at, app.AuthOffersRequest_builder{}.Build())
	x.NoError(err)
	x.Equal("https://account.contoso.example", got.GetFrontDoor())
	x.Len(got.GetProviders(), 1)
	x.Equal("entra", got.GetProviders()[0].GetName())
	x.Equal("https://entra.invalid/v2.0", got.GetProviders()[0].GetIssuer())

	// The front door checked somebody, and asks for a link beside its own
	// delegation.
	res, err := c.Accept(as, app.VouchAcceptRequest_builder{
		Claim:   claimBy(b.Contoso, "entra", "entra-subject-1"),
		Methods: []string{listPeople},
		At:      "contoso.example",
	}.Build())
	x.NoError(err)
	x.True(res.GetVerified().GetOk())
	x.True(strings.HasPrefix(res.GetToken(), keys.PrefixDelegation), "the front door's own credential, unchanged")
	x.True(strings.HasPrefix(res.GetLink(), vouch.PrefixLink), "no link for the user console: %q", res.GetLink())
	x.WithinDuration(time.Now().Add(vouch.LinkAtFor), res.GetLinkExpires().AsTime(), time.Minute)

	// The browser carries it to the name, and the user console spends it for the
	// same session a password ends in: a cookie, and `Me.Get` answering who.
	var h metadata.MD
	_, err = auth.SignIn(at, app.AuthSignInRequest_builder{Link: res.GetLink()}.Build(), grpc.Header(&h))
	x.NoError(err, "the link was refused at the name it was minted for")

	me, err := app.NewMeServiceClient(b.Conn).Get(
		metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", cookieIn(t, h))),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err, "the cookie it minted names nobody")
	x.Equal(b.Who.Bytes(), me.GetId(), "the session names somebody else")

	// The same claim, minted again: one link per hop, because the one above is
	// spent.
	mint := func(t *testing.T, at string) string {
		t.Helper()

		res, err := c.Accept(as, app.VouchAcceptRequest_builder{
			Claim:   claimBy(b.Contoso, "entra", "entra-subject-1"),
			Methods: []string{listPeople},
			At:      at,
		}.Build())
		require.NoError(t, err)
		require.NotEmpty(t, res.GetLink())

		return res.GetLink()
	}

	t.Run("and once only", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.SignIn(at, app.AuthSignInRequest_builder{Link: res.GetLink()}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "a link was spent twice")
	})

	t.Run("and at the name it was minted for, not merely the tenant", func(t *testing.T) {
		x := require.New(t)

		// A second name of contoso's own, which is a second door.
		answersAt(t, b.Server, b.Contoso, "other.contoso.example")
		link := mint(t, "contoso.example")

		elsewhere := metadata.NewOutgoingContext(ctx, arrivingAt(t, "other.contoso.example"))
		_, err := auth.SignIn(elsewhere, app.AuthSignInRequest_builder{Link: link}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "a link for one name opened another")

		// And a refusal spends nothing: the same link at its own name still works.
		_, err = auth.SignIn(at, app.AuthSignInRequest_builder{Link: link}.Build())
		x.NoError(err, "a refused spend spent the link")
	})

	t.Run("and not by the door recovery links are spent at", func(t *testing.T) {
		x := require.New(t)

		// The caller that minted it, holding `Redeem`, is refused too: the
		// discriminator is read on both doors.
		link := mint(t, "contoso.example")
		again, err := c.Redeem(as, app.VouchRedeemRequest_builder{
			Token:   link,
			Methods: []string{listPeople},
		}.Build())
		x.NoError(err)
		x.False(again.GetVerified().GetOk(), "a link for the user console was redeemed for a delegation")
		x.Empty(again.GetToken())
	})

	t.Run("and not both ways at once", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.SignIn(at, app.AuthSignInRequest_builder{
			Link: mint(t, "contoso.example"), Alias: "someone", Password: "whatever",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	// The two refusals that bound the grant.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	answersAt(t, b.Server, fabrikam, "fabrikam.example")
	somebody := addHolder(t, ctx, b.Server, fabrikam, "somebody")
	mustIdentity(t, ctx, b.Server, somebody, "entra", "entra-subject-2")

	t.Run("and not until the tenant has named a front door", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Accept(as, app.VouchAcceptRequest_builder{
			Claim:   claimBy(fabrikam, "entra", "entra-subject-2"),
			Methods: []string{listPeople},
			At:      "fabrikam.example",
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "somebody was handed into a console their tenant never opened")
	})

	t.Run("and not for a name that is not the tenant's", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Accept(as, app.VouchAcceptRequest_builder{
			Claim:   claimBy(b.Contoso, "entra", "entra-subject-1"),
			Methods: []string{listPeople},
			At:      "fabrikam.example",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "contoso's person was handed into fabrikam's console")

		_, err = c.Accept(as, app.VouchAcceptRequest_builder{
			Claim:   claimBy(b.Contoso, "entra", "entra-subject-1"),
			Methods: []string{listPeople},
			At:      "nobody.example",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	t.Run("and a tenant with no passwords is exactly who this is for", func(t *testing.T) {
		x := require.New(t)

		x.NoError(decides(t, b.Server, b.Contoso, app.TenantConfig_builder{
			Password: proto.Bool(false), FrontDoor: "https://account.contoso.example",
		}.Build()))

		got, err := auth.Offers(at, app.AuthOffersRequest_builder{}.Build())
		x.NoError(err)
		x.False(got.GetPassword())
		x.Len(got.GetProviders(), 1, "the page has nothing to draw but the buttons, and was told there are none")

		var h metadata.MD
		_, err = auth.SignIn(at, app.AuthSignInRequest_builder{Link: mint(t, "contoso.example")}.Build(), grpc.Header(&h))
		x.NoError(err, "the one road into this tenant's console was shut")
		cookieIn(t, h)
	})

	t.Run("and a front door is an origin and nothing more", func(t *testing.T) {
		x := require.New(t)

		for _, bad := range []string{
			"https://account.contoso.example/login",
			"account.contoso.example",
			"https://account.contoso.example?x=1",
			"ftp://account.contoso.example",
		} {
			err := decides(t, b.Server, b.Contoso, app.TenantConfig_builder{FrontDoor: bad}.Build())
			x.Equal(codes.InvalidArgument, status.Code(err), "%q was written", bad)
		}

		// Trimmed to what it is, so the page and the mint read one value.
		x.NoError(decides(t, b.Server, b.Contoso, app.TenantConfig_builder{FrontDoor: " https://Account.Contoso.Example/ "}.Build()))
		got, err := auth.Offers(at, app.AuthOffersRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("https://account.contoso.example", got.GetFrontDoor())
	})
}
