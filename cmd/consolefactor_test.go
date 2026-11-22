package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// cookieOf is the cookie a sign-in answered with, or none.
func cookieOf(h metadata.MD) string {
	cookie := ""
	for _, v := range h.Get("set-cookie") {
		if i := strings.IndexByte(v, ';'); i >= 0 {
			v = v[:i]
		}
		if !strings.HasSuffix(v, "=") {
			cookie = v
		}
	}

	return cookie
}

// TestRostersOwnSignInAsksTheSecondFactor: somebody who has a second factor is
// asked for it at `AuthService/SignIn`, as at every other door.
//
// # What it was
//
// `Vouch.Verify` answers `ok` only when nothing is left to prove, and withholds
// it behind a continuation minted for whoever asked. `SignIn` is public: it has
// no caller, so there was nobody to mint for, and a verifier in that position
// answered `ok` to the first factor -- a branch whose comment said nothing in a
// deployment reached it. `server/console` minted a session on that `ok`.
//
// So kim, who administers contoso and has an authenticator app, was asked for
// its code by the Login App, by the account app, and refused outright by an
// LDAP bind; and whoever had kim's password typed it into roster's own page
// and was kim. `AuthSignInResponse` was empty, so the method had no way to say
// a second answer was wanted (#275).
func TestRostersOwnSignInAsksTheSecondFactor(t *testing.T) {
	const password = "correct horse battery staple"

	b, ctx := build(t, func(c *cmd.Config) {
		c.SignIn.Enabled = true
		c.Vouch.Lockout = cmd.LockoutConfig{Failures: 3, For: time.Hour}
	})

	for name, tenant := range map[string][]byte{"contoso.example": b.Contoso.Bytes(), "fabrikam.example": b.Fabrikam.Bytes()} {
		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant}.Build(),
			Name:   name,
		}.Build())
		require.NoError(t, err)
	}

	// kim: a password and a confirmed authenticator app.
	kim := b.holder(t, ctx, b.Contoso, "kim")
	seed := enrolled(t, ctx, b.Ungated.Credential(), b.keyedLocal(t), kim)
	code := func() []byte { return []byte(vouch.CodeAt(seed, time.Now().Unix()/30)) }

	conn := pdtest.Serve(t, b.grpc(t))
	auth := app.NewAuthServiceClient(conn)

	at := func(name string) context.Context {
		return metadata.NewOutgoingContext(ctx, arrivingAt(t, name))
	}
	signIn := func(ctx context.Context, req *app.AuthSignInRequest) (*app.AuthSignInResponse, string, error) {
		var h metadata.MD
		res, err := auth.SignIn(ctx, req, grpc.Header(&h))

		return res, cookieOf(h), err
	}
	begin := func(t *testing.T) string {
		t.Helper()

		res, cookie, err := signIn(at("contoso.example"), app.AuthSignInRequest_builder{Alias: "kim", Secret: []byte(password)}.Build())
		require.NoError(t, err)
		require.Empty(t, cookie)
		require.NotEmpty(t, res.GetContinuation())

		return res.GetContinuation()
	}

	t.Run("the password alone is not a session", func(t *testing.T) {
		x := require.New(t)

		res, cookie, err := signIn(at("contoso.example"), app.AuthSignInRequest_builder{Alias: "kim", Secret: []byte(password)}.Build())
		x.NoError(err)
		x.Empty(cookie, "somebody with a second factor was handed a session for the first")

		// What the page draws the second form from.
		x.True(strings.HasPrefix(res.GetContinuation(), vouch.PrefixContinuation))
		x.Len(res.GetAvailable(), 1)
		x.Equal(vouch.KindTotp, res.GetAvailable()[0].GetKind())
	})

	t.Run("and the code finishes it, in the session a password ends in", func(t *testing.T) {
		x := require.New(t)

		res, cookie, err := signIn(at("contoso.example"), app.AuthSignInRequest_builder{
			Continuation: begin(t), Kind: vouch.KindTotp, Secret: code(),
		}.Build())
		x.NoError(err)
		x.NotEmpty(cookie)
		x.Empty(res.GetContinuation())
		x.Empty(res.GetAvailable())

		me, err := app.NewMeServiceClient(conn).Get(
			metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", cookie)), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("kim", me.GetAlias())
	})

	t.Run("an attempt is spent by its first answer", func(t *testing.T) {
		x := require.New(t)
		attempt := begin(t)

		_, cookie, err := signIn(at("contoso.example"), app.AuthSignInRequest_builder{
			Continuation: attempt, Kind: vouch.KindTotp, Secret: []byte("000000"),
		}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.Empty(cookie)

		// The right code, a moment too late: the form is the first one again.
		_, cookie, err = signIn(at("contoso.example"), app.AuthSignInRequest_builder{
			Continuation: attempt, Kind: vouch.KindTotp, Secret: code(),
		}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.Empty(cookie)
	})

	t.Run("and it is spent where it was begun", func(t *testing.T) {
		x := require.New(t)

		// At another tenant's name, which is another door.
		_, cookie, err := signIn(at("fabrikam.example"), app.AuthSignInRequest_builder{
			Continuation: begin(t), Kind: vouch.KindTotp, Secret: code(),
		}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.Empty(cookie)
	})

	t.Run("one way of asking at a time", func(t *testing.T) {
		x := require.New(t)

		for name, req := range map[string]*app.AuthSignInRequest{
			// One `secret` is proved at each step, so a password and a code
			// cannot be sent at once: an attempt is told by its continuation,
			// and an alias or a factor beside the wrong one is refused.
			"an attempt and an alias": app.AuthSignInRequest_builder{Continuation: "vc_x", Kind: vouch.KindTotp, Secret: code(), Alias: "kim"}.Build(),
			"an attempt and a link":   app.AuthSignInRequest_builder{Continuation: "vc_x", Kind: vouch.KindTotp, Secret: code(), Link: "rl_x"}.Build(),
			"an attempt and no proof": app.AuthSignInRequest_builder{Continuation: "vc_x"}.Build(),
			"a factor and no attempt": app.AuthSignInRequest_builder{Alias: "kim", Secret: []byte(password), Kind: vouch.KindTotp}.Build(),
		} {
			_, _, err := signIn(at("contoso.example"), req)
			x.Equal(codes.InvalidArgument, status.Code(err), "%s: %v", name, err)
		}
	})

	t.Run("somebody with a password and nothing else signs in as they did", func(t *testing.T) {
		x := require.New(t)
		x.NoError(setFor(ctx, b.Server, b.ContosoUser.Bytes(), password))

		res, cookie, err := signIn(at("contoso.example"), app.AuthSignInRequest_builder{Alias: "someone", Secret: []byte(password)}.Build())
		x.NoError(err)
		x.NotEmpty(cookie)
		x.Empty(res.GetContinuation())
		x.Empty(res.GetAvailable())
	})

	t.Run("wrong codes are counted where wrong passwords are", func(t *testing.T) {
		x := require.New(t)

		// Somebody of their own, so that the count is this test's.
		lee := b.holder(t, ctx, b.Contoso, "lee")
		enrolled(t, ctx, b.Ungated.Credential(), b.keyedLocal(t), lee)

		for range 3 {
			res, _, err := signIn(at("contoso.example"), app.AuthSignInRequest_builder{Alias: "lee", Secret: []byte(password)}.Build())
			x.NoError(err)

			_, _, err = signIn(at("contoso.example"), app.AuthSignInRequest_builder{
				Continuation: res.GetContinuation(), Kind: vouch.KindTotp, Secret: []byte("000000"),
			}.Build())
			x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		}

		// Three wrong codes, each behind a right password, and it is the
		// password that is closed: nothing begins.
		_, cookie, err := signIn(at("contoso.example"), app.AuthSignInRequest_builder{Alias: "lee", Secret: []byte(password)}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.Empty(cookie)
	})
}

// TestAVerifierWithNobodyToMintForSaysWhatIsLeft: asked by nobody -- no frame,
// so nobody a continuation could be minted for -- `Vouch.Verify` still answers
// `ok` only when nothing is left to prove.
//
// `vouch.proto` says `ok` is never *this factor was proved*: every caller reads
// it as signed in. The branch for a caller with no frame answered `ok` to a
// first factor all the same, on the argument that nothing in a deployment
// reached it, and `AuthService/SignIn` did. It answers what was proved and what
// is left, and no handle, since there is nobody to hold one: a caller gating
// on `ok` fails closed, which is the property the field is for.
func TestAVerifierWithNobodyToMintForSaysWhatIsLeft(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyedLocal(t)
	seed := enrolled(t, ctx, b.Ungated.Credential(), v, b.ContosoUser)
	who := app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build()

	res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{Who: who, Secret: []byte("correct horse battery staple")}.Build())
	x.NoError(err)
	x.False(res.GetOk(), "one factor of two answered as the whole sign-in")
	x.Equal([]string{vouch.KindPassword}, res.GetSatisfied())
	x.Len(res.GetAvailable(), 1)
	x.Empty(res.GetContinuation(), "an attempt was minted for nobody")

	// The other one alone is no more a sign-in than it is for a caller.
	res, err = v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who: who, Kind: vouch.KindTotp, Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.False(res.GetOk())
	x.Equal([]string{vouch.KindTotp}, res.GetSatisfied())

	// And somebody with one factor is answered as they were.
	other := b.holder(t, ctx, b.Contoso, "one-factor")
	x.NoError(setFor(ctx, b.Server, other.Bytes(), "correct horse battery staple"))

	res, err = v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who: app.VouchWho_builder{Id: other.Bytes()}.Build(), Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	x.True(res.GetOk())
}
