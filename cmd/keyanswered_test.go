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
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
)

// `TokenService/Introspect` answers a key to the app it is for (#567).
//
// Any app that could ask was answered whose a key is and what it names, so a
// key minted for one app was that key at every app. A key for an app is
// answered to that app alone, and a key of roster's, which is spent by
// presenting it to roster, to no app.

// spender is an app of Contoso's that keys are minted for, and a call it makes
// as itself: `b.Service`'s key narrowed to the holder Contoso nominated for it,
// which is bound `Introspect` there.
type spender struct {
	id pdid.Id
	as context.Context
}

// spenderOf makes the app `alias` in Contoso, answered as `borrower`'s key
// narrowed there, and lets it ask `Introspect`.
func spenderOf(t *testing.T, ctx context.Context, b *keyedBuilt, alias string, borrower pdid.Id, token string) spender {
	t.Helper()

	who := addHolder(t, ctx, b.Server, b.Contoso, alias)
	nominates(t, b.Server, b.Contoso, borrower, who)
	permits(t, ctx, b, b.Contoso, who, alias+"-introspects", introspect)

	return spender{id: who, as: arrivedAt(ctx, token, front.AtTenant("contoso"))}
}

// spenderFor is [spenderOf] for `b.Service`, the app a test has a key of.
func spenderFor(t *testing.T, ctx context.Context, b *keyedBuilt) spender {
	t.Helper()

	return spenderOf(t, ctx, b, "foo", b.Service, b.Token)
}

// mintSpent is [mintFor] for an app: an `rt_` on `who` for `of`.
func mintSpent(t *testing.T, ctx context.Context, b *keyedBuilt, who, of pdid.Id, alias string, methods []string, expires time.Time) string {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	req := app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		App:     app.HolderRef_builder{Id: of.Bytes()}.Build(),
		Alias:   alias,
		Secret:  sum,
		Methods: methods,
	}
	if !expires.IsZero() {
		req.DateExpired = timestamppb.New(expires)
	}
	_, err = b.Ungated.ApiKey().Add(ctx, req.Build())
	x.NoError(err)

	return token
}

// TestAKeyIsAnsweredToTheAppItIsFor.
//
// `Introspect` answered whose a key is, its tenant, its methods and its dates
// to whoever could call it. Foo is answered them about a key for Foo; Bar, and
// an administrator of the tenant, are answered what a token that is none is
// answered, `NotFound: no such token`, and so is Foo about a key for Bar. A
// key of roster's is spent by presenting it to roster, and is answered to no
// app. A key for Foo ended is none to Foo at the next ask.
func TestAKeyIsAnsweredToTheAppItIsFor(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, introspect)
	ctx := t.Context()

	const y = "/foo.Permission/Y"
	foo := spenderFor(t, ctx, b)
	barKey, barToken := serviceKey(t, b.Server, "bar", introspect)
	bar := spenderOf(t, ctx, b, "bar", barKey, barToken)

	admin := addHolder(t, ctx, b.Server, b.Contoso, "tenant-admin")
	permits(t, ctx, b, b.Contoso, admin, "everything-and-introspect", "/roster.*/*", introspect)
	adminToken := mintFor(t, ctx, b, admin, "admin", []string{introspect}, time.Time{})

	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	forFoo := mintSpent(t, ctx, b, b.Who, foo.id, "for-foo", []string{y}, expires)
	forBar := mintSpent(t, ctx, b, b.Who, bar.id, "for-bar", []string{y}, time.Time{})
	rosters := mintFor(t, ctx, b, b.Who, "rosters", []string{"/roster.HolderService/Get"}, time.Time{})

	c := pdpb.NewTokenServiceClient(b.Conn)
	ask := func(as context.Context, token string) (*pdpb.TokenIntrospectResponse, error) {
		return c.Introspect(as, pdpb.TokenIntrospectRequest_builder{Token: token}.Build())
	}
	none := func(what string, err error) {
		t.Helper()
		x.Equal(codes.NotFound, status.Code(err), "%s: %v", what, err)
		x.Equal("no such token", status.Convert(err).Message(), what)
	}

	_, err := ask(bar.as, forFoo)
	none("Bar, about a key for Foo", err)
	_, err = ask(bearing(ctx, adminToken), forFoo)
	none("the tenant's administrator, about a key for Foo", err)
	_, err = ask(bearing(ctx, b.Token), forFoo)
	none("Foo's key across every tenant, which is not the holder Foo is", err)
	_, err = ask(foo.as, forBar)
	none("Foo, about a key for Bar", err)

	res, err := ask(foo.as, forFoo)
	x.NoError(err, "Foo was not answered about a key for Foo")
	x.Equal(b.Who.Bytes(), res.GetId(), "whose it is")
	x.Equal(b.Contoso.Bytes(), res.GetTenantId())
	x.Equal(expires.Unix(), res.GetDateExpired().GetSeconds())
	id, err := auth.IdentityFrom(res)
	x.NoError(err)
	x.True(id.Grant.Allows(y), "its methods")

	_, err = ask(bar.as, forBar)
	x.NoError(err, "Bar was not answered about a key for Bar")

	for who, as := range map[string]context.Context{"Foo": foo.as, "Bar": bar.as, "the administrator": bearing(ctx, adminToken)} {
		_, err = ask(as, rosters)
		none(who+", about a key of roster's", err)
	}

	// Ended, it is none to Foo at the next ask.
	_, err = b.Ungated.ApiKey().Erase(ctx, app.ApiKeyRef_builder{Secret: keys.Sum(forFoo)}.Build())
	x.NoError(err)
	_, err = ask(foo.as, forFoo)
	none("Foo, about a key for Foo that was ended", err)
}
