package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

const introspect = pdpb.TokenService_Introspect_FullMethodName

// mintFor puts a key on a **data plane** holder and answers with the token.
//
// Through `Ungated` because nothing mints one over the wire yet: the console and
// the rules that would let a customer do it are not written. What this stands in
// for is a row, and a row is all `Introspect` reads.
func mintFor(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, alias string, methods []string, expires time.Time) string {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	req := app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   alias,
		Secret:  sum,
		Methods: methods,
	}
	if !expires.IsZero() {
		req.DateExpires = timestamppb.New(expires)
	}

	_, err = b.Ungated.ApiKey().Add(ctx, req.Build())
	x.NoError(err)

	return token
}

// TestATokenSaysWhoItIs is `payday.TokenService` over roster's keys, asked the
// way a product app asks it.
//
// The answer names the **holder** and not the key, which is the one thing this
// had to get right: an app in front resolves what it is told against its own
// rows, and handed a key identifier it finds nobody -- or, with custody's
// on-demand resolver, creates a person-shaped row for a row that is not a
// person.
func TestATokenSaysWhoItIs(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, introspect)
	ctx := t.Context()

	const get = "/roster.HolderService/Get"
	token := mintFor(t, ctx, b, b.Who, "reader", []string{get}, time.Time{})

	c := pdpb.NewTokenServiceClient(b.Conn)
	res, err := c.Introspect(bearing(ctx, b.Token),
		pdpb.TokenIntrospectRequest_builder{Token: token}.Build())
	x.NoError(err)

	x.Equal(b.Who.Bytes(), res.GetId(), "the holder, not the key")
	x.Equal(b.Contoso.Bytes(), res.GetTenantId())

	// And what payday makes of it, through the same decoder an app uses.
	id, err := auth.IdentityFrom(res)
	x.NoError(err)
	x.Equal(b.Who.String(), id.Id)
	x.False(id.Grant.IsWhole())
	x.True(id.Grant.Allows(get))
	x.False(id.Grant.Allows("/roster.HolderService/Erase"))
}

// TestATokenNarrowedToNothingStaysThatWay is the shape an empty list cannot
// express on its own.
//
// A key with no methods allows nothing -- `frame.Grant`'s zero for actions --
// and the wire form of that is three empty lists, which is also what "allows
// everything" looks like. Only the flags beside them differ, and this is the
// test that says roster wrote the flags.
func TestATokenNarrowedToNothingStaysThatWay(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, introspect)
	ctx := t.Context()

	token := mintFor(t, ctx, b, b.Who, "narrow", nil, time.Time{})

	res, err := pdpb.NewTokenServiceClient(b.Conn).Introspect(bearing(ctx, b.Token),
		pdpb.TokenIntrospectRequest_builder{Token: token}.Build())
	x.NoError(err)

	id, err := auth.IdentityFrom(res)
	x.NoError(err)
	x.False(id.Grant.Allows("/roster.HolderService/Get"))
	x.False(id.Grant.IsWhole())
}

// TestIntrospectSaysNoMoreThanNo is every refusal about the token being the
// same refusal.
//
// Expired, revoked and never-existed told apart are an oracle for "this string
// was a real token once", which is worth more to somebody guessing than to
// anybody else.
func TestIntrospectSaysNoMoreThanNo(t *testing.T) {
	b := keyFor(t, introspect)
	ctx := t.Context()

	live := mintFor(t, ctx, b, b.Who, "live", []string{"/roster.HolderService/Get"}, time.Time{})
	stale := mintFor(t, ctx, b, b.Who, "stale", []string{"/roster.HolderService/Get"},
		time.Now().Add(-time.Minute))

	c := pdpb.NewTokenServiceClient(b.Conn)

	// A **real** deployment key, not a fabricated string: `b.Token` is a live
	// row in the control plane, and it is the caller's own credential besides.
	// The data plane's TokenService is built on the data plane's rows, so a
	// control-plane key is not refused here -- it is invisible, which
	// `operating.md` promises in as many words. A fabricated `rk_` proves the
	// prefix is not special; only a minted one proves the population is.
	for _, tc := range []struct{ desc, token string }{
		{"never existed", "rk_" + "nothingatallwhatsoever"},
		{"not one of ours", "not-even-prefixed"},
		{"expired", stale},
		{"a deployment key, and it is real", b.Token},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			_, err := c.Introspect(bearing(ctx, b.Token),
				pdpb.TokenIntrospectRequest_builder{Token: tc.token}.Build())
			x.Error(err)
			x.Equal(codes.NotFound, status.Code(err),
				"every refusal about the token is the same refusal")
		})
	}

	// And the one that is not a refusal, so that the three above are not all
	// passing because nothing works.
	t.Run("a live one is answered", func(t *testing.T) {
		x := require.New(t)

		res, err := c.Introspect(bearing(ctx, b.Token),
			pdpb.TokenIntrospectRequest_builder{Token: live}.Build())
		x.NoError(err)
		x.Equal(b.Who.Bytes(), res.GetId())
	})
}

// TestIntrospectIsNotPublic is that asking is itself a call somebody has to be
// allowed to make.
//
// It is the whole of the trust decision this service rests on: the bearer's
// token is the *subject*, and the caller is a product app holding its own
// credential. Served open, anybody who could reach the port could test tokens
// against the store as fast as they could send them.
func TestIntrospectIsNotPublic(t *testing.T) {
	ctx := t.Context()

	t.Run("with no credential at all", func(t *testing.T) {
		x := require.New(t)
		b := keyFor(t, introspect)

		token := mintFor(t, ctx, b, b.Who, "narrow", nil, time.Time{})

		_, err := pdpb.NewTokenServiceClient(b.Conn).Introspect(ctx,
			pdpb.TokenIntrospectRequest_builder{Token: token}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// A key that was not made for this. The grant is a list of methods on the
	// row, read by the interceptor before the handler runs.
	t.Run("with a key that may not ask", func(t *testing.T) {
		x := require.New(t)
		b := keyFor(t, "/roster.VouchService/Verify")

		token := mintFor(t, ctx, b, b.Who, "narrow", nil, time.Time{})

		_, err := pdpb.NewTokenServiceClient(b.Conn).Introspect(bearing(ctx, b.Token),
			pdpb.TokenIntrospectRequest_builder{Token: token}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}
