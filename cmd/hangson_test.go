package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestWhatHungOnSomethingErasedIsStillListed: an erase destroys nothing
// (`docs/baseline.md`), and that is true of what hangs on the row as well as
// of the row. It writes the one row it names. So a place on a team that was
// erased, a place in a group that was and a binding to it, and somebody's
// place in a site after they were erased are each still answered by a list,
// naming something `Get` answers `NotFound` for.
//
// It is the stated position and not a defect, and it was said nowhere a
// reader of a list would find it (#279): `docs/usage/customers.md` says it
// now, with what a reader does about it. This is what holds the manual to the
// lists. What such a row grants is nothing (`TestAnErasedRoleGrantsNothing`,
// `TestATeamErasedHandsOutNothing`), and it is erased by whoever may erase
// where it is (`TestWhatGrantsNothingAnyMoreIsTakenAway`).
func TestWhatHungOnSomethingErasedIsStillListed(t *testing.T) {
	x := require.New(t)
	k := takingAt(t)
	b, ctx := k.b, k.ctx

	park := b.holder(t, ctx, b.Contoso, "park")
	whose := app.HolderRef_builder{Id: park.Bytes()}.Build()
	reader := k.role(t, "reader", nil, getHolder)

	// A place on a team, a place in a group and a binding to the group, and
	// a place in a site.
	team := b.team(t, ctx, k.seoul, "day-shift")
	k.placed(t, park, team, reader)
	group, _ := k.group(t, "auditors", nil, park, nil)
	k.bound(t, reader, group, nil)
	b.joins(t, ctx, park, k.seoul)

	_, err := k.disband(k.admin, team)
	x.NoError(err)
	k.eraseGroup(t, group)

	places, err := app.NewTeamMembershipServiceClient(k.conn).List(k.admin, app.TeamMembershipListRequest_builder{
		Filters: []*app.TeamMembershipFilter{app.TeamMembershipFilter_builder{Holder: whose}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(places.GetItems(), 1, "a place on a team that was erased")
	_, err = app.NewTeamServiceClient(k.conn).Get(k.admin, app.TeamGetRequest_builder{
		Ref: app.TeamRef_builder{Id: places.GetItems()[0].GetTeam().GetId()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)

	in, err := app.NewGroupMembershipServiceClient(k.conn).List(k.admin, app.GroupMembershipListRequest_builder{
		Filters: []*app.GroupMembershipFilter{app.GroupMembershipFilter_builder{Holder: whose}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(in.GetItems(), 1, "a place in a group that was erased")

	bound, err := app.NewBindingServiceClient(k.conn).List(k.admin, app.BindingListRequest_builder{
		Filters: []*app.BindingFilter{app.BindingFilter_builder{Role: roleRef(reader)}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(bound.GetItems(), 1, "a binding to a group that was erased")
	_, err = app.NewGroupServiceClient(k.conn).Get(k.admin, app.GroupGetRequest_builder{
		Ref: app.GroupRef_builder{Id: bound.GetItems()[0].GetGroup().GetId()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)

	// And somebody erased is still recorded as of the site they were of.
	_, err = app.NewHolderServiceClient(k.conn).Erase(k.admin, whose)
	x.NoError(err)
	of, err := app.NewSiteMembershipServiceClient(k.conn).List(k.admin, app.SiteMembershipListRequest_builder{
		Filters: []*app.SiteMembershipFilter{app.SiteMembershipFilter_builder{
			Site: app.SiteRef_builder{Id: k.seoul.Bytes()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(of.GetItems(), 1, "a place in a site, of somebody who was erased")
	_, err = app.NewHolderServiceClient(k.conn).Get(k.admin, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: of.GetItems()[0].GetHolder().GetId()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)
}
