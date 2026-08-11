package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

func (b *built) joins(t *testing.T, ctx context.Context, who, site pdid.Id) {
	t.Helper()

	_, err := b.Ungated.SiteMembership().Add(ctx, app.SiteMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: site.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
}

// TestOnePersonIsInSeveralSites, which is why the Holder carries no site edge
// and this is a row instead. Somebody works at one factory and audits another.
func TestOnePersonIsInSeveralSites(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	frankfurt := b.site(t, ctx, b.Acme, "frankfurt")

	b.joins(t, ctx, b.AcmeUser, seoul)
	b.joins(t, ctx, b.AcmeUser, frankfurt)

	vs, err := b.Walled.SiteMembership().List(
		b.as(ctx, b.AcmeUser, b.Acme), app.SiteMembershipListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2)
}

// TestJoiningTwiceIsRefused. A membership is a fact and not an event; twice is
// whatever wrote it being wrong.
func TestJoiningTwiceIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	b.joins(t, ctx, b.AcmeUser, seoul)

	_, err := b.Ungated.SiteMembership().Add(ctx, app.SiteMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err))
}

// TestMembershipsAreThemselvesOnTheSecondAxis, which is what makes "who is
// here" answerable without also answering "who is anywhere else".
func TestMembershipsAreThemselvesOnTheSecondAxis(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	frankfurt := b.site(t, ctx, b.Acme, "frankfurt")

	b.joins(t, ctx, b.AcmeUser, seoul)
	b.joins(t, ctx, b.AcmeUser, frankfurt)

	ctx = b.as(ctx, b.AcmeUser, b.Acme)

	vs, err := b.grouped(t, seoul).SiteMembership().List(ctx, app.SiteMembershipListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1)
}

// TestATeamMembershipReachesItsTenantThreeHopsAway is the deepest `via` in this
// schema: a membership names a team, a team names a site, a site names a
// tenant.
//
// It is generated as one predicate --
// `HasTeamWith(HasSiteWith(TenantIDIn(…)))` -- rather than as three reads, and
// this asserts the wall it produces actually holds.
func TestATeamMembershipReachesItsTenantThreeHopsAway(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	team := b.team(t, ctx, seoul, "operators")

	v, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: b.role(t, ctx, "operator").Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// Their own tenant sees it.
	got, err := b.Walled.TeamMembership().Get(
		b.as(ctx, b.AcmeUser, b.Acme),
		app.TeamMembershipGetRequest_builder{Ref: v.Ref()}.Build())
	x.NoError(err)
	x.NotNil(got.GetRole())

	// Another tenant does not, three hops away.
	hooliUser := b.holder(t, ctx, b.Hooli, "theirs")
	_, err = b.Walled.TeamMembership().Get(
		b.as(ctx, hooliUser, b.Hooli),
		app.TeamMembershipGetRequest_builder{Ref: v.Ref()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestARoleIsPerTeamAndThereforePerSite, which is the whole reason roles are
// derived from team membership rather than held on the person.
func TestARoleIsPerTeamAndThereforePerSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	frankfurt := b.site(t, ctx, b.Acme, "frankfurt")

	for _, v := range []struct {
		site pdid.Id
		role string
	}{{seoul, "operator"}, {frankfurt, "reader"}} {
		team := b.team(t, ctx, v.site, "staff")
		_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
			Role:   app.RoleRef_builder{Id: b.role(t, ctx, v.role).Bytes()}.Build(),
		}.Build())
		x.NoError(err)
	}

	vs, err := b.Walled.TeamMembership().List(
		b.as(ctx, b.AcmeUser, b.Acme), app.TeamMembershipListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2, "one person, two roles, because they are two teams")
}
