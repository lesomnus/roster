package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// A row that names two tenants, refused.
//
// This was written and accepted, and whichever path the wall happened to take
// decided who could see it. One tenant read a row naming the other's, which is
// the one thing the wall exists to make impossible. It was found by writing it:
// nothing refused, nothing logged.
//
// The wall cannot answer it -- it is a predicate on reads, and by the time a
// read is narrowed the row exists. So it is refused at the write, in
// `server/core`, where the judgements no schema can state already live.

// TestAMembershipCannotCrossTenants is the one that was open.
func TestAMembershipCannotCrossTenants(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	theirs := b.site(t, ctx, b.Hooli, "theirs")

	_, err := b.Ungated.SiteMembership().Add(ctx, app.SiteMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: theirs.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err),
		"an acme person was made a member of a hooli site")

	// And the same row within one tenant is fine, so what refused is the
	// disagreement rather than the shape of the call.
	ours := b.site(t, ctx, b.Acme, "ours")
	_, err = b.Ungated.SiteMembership().Add(ctx, app.SiteMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: ours.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// TestATeamCannotBeInAnotherTenantsSite, which is the same rule where the two
// paths are a tenant edge and the optional axis.
func TestATeamCannotBeInAnotherTenantsSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	theirs := b.site(t, ctx, b.Hooli, "theirs")

	_, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: theirs.Bytes()}.Build(),
		Alias:  "trespassers",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestATeamNeedsNoSite, which is what an optional namespace means.
//
// The site edge was made required for a while, because a team without one
// reached no tenant and was invisible to everybody. The cause was the wall
// going through the optional axis; with it on the tenant edge the row is
// ordinary again -- seen by a read of the whole tenant, and not by one narrowed
// to a site, which is the right way round.
func TestATeamNeedsNoSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:  "everybody",
	}.Build())
	x.NoError(err)

	// Their own tenant sees it.
	vs, err := b.Walled.Team().List(b.as(ctx, b.AcmeUser, b.Acme),
		app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1)

	// Another tenant does not.
	hooliUser := b.holder(t, ctx, b.Hooli, "theirs")
	vs, err = b.Walled.Team().List(b.as(ctx, hooliUser, b.Hooli),
		app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(vs.GetItems())

	// And a read narrowed to a site does not, because it is in no site.
	seoul := b.site(t, ctx, b.Acme, "seoul")
	vs, err = b.grouped(t, seoul).Team().List(b.as(ctx, b.AcmeUser, b.Acme),
		app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(vs.GetItems())
}

// TestABindingGrantsToOneSubject. A schema can say two nullable edges and
// cannot say "exactly one of them".
func TestABindingGrantsToOneSubject(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	role := b.role(t, ctx, "operator", "/roster.HolderService/Get")
	group, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:  "admins",
	}.Build())
	x.NoError(err)

	// Both.
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Group:  app.GroupRef_builder{Id: group.GetId()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))

	// Neither.
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: role.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))

	// One.
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}
