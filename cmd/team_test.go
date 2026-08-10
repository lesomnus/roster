package cmd_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

	v, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
		Site:  app.SiteRef_builder{Id: in.Bytes()}.Build(),
		Alias: alias,
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

	seoul := b.site(t, ctx, b.Acme, "seoul")
	frankfurt := b.site(t, ctx, b.Acme, "frankfurt")

	b.team(t, ctx, seoul, "operators")
	b.team(t, ctx, frankfurt, "operators")

	ctx = b.as(ctx, b.AcmeUser, b.Acme)

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

// TestARowInNoSetIsInvisibleToANarrowedRead is fail-closed, pinned.
//
// The site edge is nullable, because a schema gains field 3 after it already
// has rows and requiring it would mean no app could ever add one. A team that
// named no site is then in no set — and a read narrowed to a set does not
// include it.
//
// Surprising, and the right way round: the alternative is a row that appears in
// every narrowed read because it belongs to none of them.
func TestARowInNoSetIsInvisibleToANarrowedRead(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")

	_, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{Alias: "homeless"}.Build())
	x.NoError(err)

	ctx = b.as(ctx, b.AcmeUser, b.Acme)

	vs, err := b.grouped(t, seoul).Team().List(ctx, app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(vs.GetItems())
}

// TestTheTenantWallStillApplies, because the second axis narrows further and
// never widens. A caller handed another tenant's site does not thereby see it.
func TestTheTenantWallStillApplies(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	theirs := b.site(t, ctx, b.Hooli, "theirs")
	b.team(t, ctx, theirs, "operators")

	// Asking as Acme, while naming Hooli's site as the set.
	ctx = b.as(ctx, b.AcmeUser, b.Acme)

	vs, err := b.grouped(t, theirs).Team().List(ctx, app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(vs.GetItems(), "naming another tenant's site widened the wall")
}

// TestATeamReachesItsTenantThroughItsSite is `tenanted: {via: "site.tenant"}`,
// which is the same multi-hop path Identity takes through its Holder.
func TestATeamReachesItsTenantThroughItsSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	team := b.team(t, ctx, seoul, "operators")

	hooliUser := b.holder(t, ctx, b.Hooli, "theirs")
	_, err := b.Walled.Team().Get(
		b.as(ctx, hooliUser, b.Hooli),
		app.TeamGetRequest_builder{Ref: app.TeamRef_builder{Id: team.Bytes()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestATeamAliasIsUniqueWithinItsSite, so `operators` exists in every site and
// names a different group in each.
func TestATeamAliasIsUniqueWithinItsSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	frankfurt := b.site(t, ctx, b.Acme, "frankfurt")

	b.team(t, ctx, seoul, "operators")
	b.team(t, ctx, frankfurt, "operators")

	_, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
		Site:  app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		Alias: "operators",
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err))
}
