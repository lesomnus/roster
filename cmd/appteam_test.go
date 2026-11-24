package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnAppsRoleIsNotHeldInATeam.
//
// A place on a team took a role of an app's, and such a place was in nobody's
// list at the app: an app's administrator could neither see it nor take it
// away without taking the person off the team. A place on a team carries a
// role of roster's and no other; one of an app's is refused, at the box as at
// a port, `PermissionDenied`, naming the app in the words a role of an app's
// bound to another's group is -- put on the team with it, or patched to it
// after. A role of roster's is held there as it was.
func TestAnAppsRoleIsNotHeldInATeam(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	role := b.roleAtApp(t, ctx, loft, "loft-staff", "/loft.BookingService/Cancel")
	desk := b.team(t, ctx, b.site(t, ctx, b.Contoso, "seoul"), "front-desk")
	grace := b.holder(t, ctx, b.Contoso, "grace")

	const said = "role: a role of @contoso/loft's is bound to a person or to a group of @contoso/loft's, and a place on a team carries a role of roster's and no other"

	_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: grace.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: desk.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a role of an app's was held in a team")
	x.Equal(said, status.Convert(err).Message())

	v, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: grace.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: desk.Bytes()}.Build(),
	}.Build())
	x.NoError(err, "a place on a team with no role")
	_, err = b.Ungated.TeamMembership().Patch(ctx, app.TeamMembershipPatchRequest_builder{
		Ref:  app.TeamMembershipRef_builder{Id: v.GetId()}.Build(),
		Role: app.RoleRef_builder{Id: role.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a place on a team was patched to a role of an app's")
	x.Equal(said, status.Convert(err).Message())

	// A role of roster's is held there as it was.
	b.inTeam(t, ctx, b.holder(t, ctx, b.Contoso, "frank"), desk, b.role(t, ctx, "reader", getHolder))
}
