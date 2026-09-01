package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/pd"
)

// grouped is the app with **both** axes on: the tenant wall, and the sites a
// caller may see.
//
// `cmd.Build` installs only the first, because which sites somebody may see is
// a membership this app owns and payday takes the answer where it is rather
// than holding it. This is that answer, handed in.
func (b *built) grouped(t *testing.T, sites ...pdid.Id) app.Server {
	t.Helper()

	of := frame.Sets(func(ctx context.Context) ([]uuid.UUID, bool, error) {
		vs := make([]uuid.UUID, 0, len(sites))
		for _, v := range sites {
			vs = append(vs, v.Uuid())
		}

		return vs, false, nil
	})

	// `pd.NewSink` and not `bare.NewServer`: List and Watch are generated into
	// the pd layer, so a bare server answers Unimplemented for them.
	//
	// And `Scopes` because payday refuses two scopes given separately -- it will
	// not guess whether they narrow together or replace one another.
	s, err := pd.NewSink(b.Ent,
		bare.WithMinter(pd.Minter()),
		bare.WithScope(bare.Scopes{pd.Wall(), pd.Grouped(of)}))
	require.NoError(t, err)

	return s
}

func (b *built) team(t *testing.T, ctx context.Context, in pdid.Id, alias string) pdid.Id {
	t.Helper()

	// The tenant comes from the site, because a helper that took both would let
	// every test that uses it disagree with itself. The rule that they must
	// agree is `server/core`'s, and `agree_test.go` is where it is checked.
	w, err := b.Ungated.Site().Get(ctx, app.SiteGetRequest_builder{
		Ref:    app.SiteRef_builder{Id: in.Bytes()}.Build(),
		Select: app.SiteSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	require.NoError(t, err)

	v, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.GetTenant().GetId()}.Build(),
		Site:   app.SiteRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// TestTheSecondAxisNarrowsWithinATenant is what field 3 is for, and the thing
// custody never exercises.
//
// Both teams are in one tenant, so the wall lets both through. A caller who may
// see only Seoul sees only Seoul's.
func TestTheSecondAxisNarrowsWithinATenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	frankfurt := b.site(t, ctx, b.Contoso, "frankfurt")

	b.team(t, ctx, seoul, "operators")
	b.team(t, ctx, frankfurt, "operators")

	ctx = b.as(ctx, b.ContosoUser, b.Contoso)

	// The wall alone: one tenant, both teams.
	all, err := b.Walled.Team().List(ctx, app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(all.GetItems(), 2, "the tenant wall should not have narrowed by site")

	// And with the second axis: one.
	some, err := b.grouped(t, seoul).Team().List(ctx, app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(some.GetItems(), 1)
	x.Equal("operators", some.GetItems()[0].GetAlias())
}

// TestATeamMustNameASite, which it did not have to until it was tried.
//
// The edge was nullable, and a team with no site reached no tenant -- so it was
// written, was invisible to everybody including the tenant that made it, and
// nothing anywhere said so. It was found by asking what `Site` is for and
// answering "a namespace", because a namespaced thing with no namespace is not
// a thing.
//
// Refused at the write now, which is the only place the answer is useful.
func TestATeamMustNameASite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{Alias: "homeless"}.Build())
	x.Error(err, "a team with no site was written, and nobody can see it")
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestTheTenantWallStillApplies, because the second axis narrows further and
// never widens. A caller handed another tenant's site does not thereby see it.
func TestTheTenantWallStillApplies(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	theirs := b.site(t, ctx, b.Fabrikam, "theirs")
	b.team(t, ctx, theirs, "operators")

	// Asking as Contoso, while naming Fabrikam's site as the set.
	ctx = b.as(ctx, b.ContosoUser, b.Contoso)

	vs, err := b.grouped(t, theirs).Team().List(ctx, app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(vs.GetItems(), "naming another tenant's site widened the wall")
}

// TestATeamReachesItsTenantThroughItsSite is `tenanted: {via: "site.tenant"}`,
// which is the same multi-hop path Identity takes through its Holder.
func TestATeamReachesItsTenantThroughItsSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	team := b.team(t, ctx, seoul, "operators")

	fabrikamUser := b.holder(t, ctx, b.Fabrikam, "theirs")
	_, err := b.Walled.Team().Get(
		b.as(ctx, fabrikamUser, b.Fabrikam),
		app.TeamGetRequest_builder{Ref: app.TeamRef_builder{Id: team.Bytes()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestATeamAliasIsUniqueWithinItsSite, so `operators` exists in every site and
// names a different group in each.
func TestATeamAliasIsUniqueWithinItsSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	frankfurt := b.site(t, ctx, b.Contoso, "frankfurt")

	b.team(t, ctx, seoul, "operators")
	b.team(t, ctx, frankfurt, "operators")

	_, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		Alias:  "operators",
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err))
}
