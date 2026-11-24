package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// A group says whose it is (`Group.app`, #567).
//
// An app's users are the people in its groups and the people bound its roles,
// so that taking one of them out is ending a membership or a binding: there is
// no exclusion and no denial. That holds only if an app's role is bound to a
// person or to a group of that app, and a group of an app's takes that app's
// roles and no other.

// appGroup is a group of Contoso's, of the app `of` -- the tenant's own where
// it is [pdid.Nil] -- made at the box.
func (b *built) appGroup(t *testing.T, ctx context.Context, of pdid.Id, alias string) pdid.Id {
	t.Helper()

	req := app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  alias,
	}
	if of != pdid.Nil {
		req.App = app.HolderRef_builder{Id: of.Bytes()}.Build()
	}
	v, err := b.Ungated.Group().Add(ctx, req.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// bindsGroup binds a role to a group across the tenant, at the box, and
// answers with the refusal if there is one.
func (b *built) bindsGroup(ctx context.Context, role, group pdid.Id) error {
	_, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:  app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Group: app.GroupRef_builder{Id: group.Bytes()}.Build(),
	}.Build())

	return err
}

// putsIn is somebody put in a group at the box, and the place they have there.
func (b *built) putsIn(t *testing.T, ctx context.Context, who, group pdid.Id) pdid.Id {
	t.Helper()

	v, err := b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
		Group:  app.GroupRef_builder{Id: group.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// TestARoleOfAnAppIsBoundToAPersonOrToAGroupOfThatApp.
//
// A group did not say whose it was, so a role of an app's was bound to no
// group at all -- the stand-in the first piece of #567 left -- and the app's
// users could not be a list the app keeps. A group says whose it is now: a
// role of Loft's bound to a group of Loft's reaches its members, and Loft is
// answered so; bound to the tenant's own group, or to another app's, it is
// refused naming both, `PermissionDenied` -- a person in a group somebody
// else keeps held the app's role for as long as they were in it, and the app
// could not take them out. And a group of Loft's takes Loft's roles and no
// other: a role of roster's bound to one is refused in the same shape. A role
// of roster's bound to the tenant's own group is as it was, and a role of an
// app's bound to a person too; held in a team it is refused
// (`TestAnAppsRoleIsNotHeldInATeam`).
func TestARoleOfAnAppIsBoundToAPersonOrToAGroupOfThatApp(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const booking = "/loft.BookingService/Cancel"

	loft := b.holder(t, ctx, b.Contoso, "loft")
	levyan := b.holder(t, ctx, b.Contoso, "levyan")
	role := b.roleAtApp(t, ctx, loft, "loft-staff", booking)

	users := b.appGroup(t, ctx, loft, "loft-users")
	employees := b.appGroup(t, ctx, pdid.Nil, "employees")
	theirs := b.appGroup(t, ctx, levyan, "levyan-users")

	x.NoError(b.bindsGroup(ctx, role, users), "a role of an app's bound to a group of that app's")
	erin := b.holder(t, ctx, b.Contoso, "erin")
	b.putsIn(t, ctx, erin, users)

	// And Reaches at the app says so, to the app asking about itself.
	b.binds(t, loft, b.role(t, ctx, "loft-app", "/roster.HolderService/Reaches"), nil)
	at, err := app.NewHolderServiceClient(served(t, b.Server)).Reaches(asOverTheWire(ctx, loft), app.HolderReachesRequest_builder{
		Ref: app.HolderRef_builder{Id: erin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal([]string{booking}, at.GetMethods(), "a member of the app's group was not answered its role")
	x.Equal([]string{booking}, at.GetEverywhere())

	err = b.bindsGroup(ctx, role, employees)
	x.Equal(codes.PermissionDenied, status.Code(err), "a role of an app's was bound to the tenant's own group")
	x.Equal("group: a role of @contoso/loft's is bound to a person or to a group of @contoso/loft's, and @contoso/employees is the tenant's own",
		status.Convert(err).Message())

	err = b.bindsGroup(ctx, role, theirs)
	x.Equal(codes.PermissionDenied, status.Code(err), "a role of an app's was bound to another app's group")
	x.Equal("group: a role of @contoso/loft's is bound to a person or to a group of @contoso/loft's, and @contoso/levyan-users is @contoso/levyan's",
		status.Convert(err).Message())

	reader := b.role(t, ctx, "reader", getHolder)
	err = b.bindsGroup(ctx, reader, users)
	x.Equal(codes.PermissionDenied, status.Code(err), "a role of roster's was bound to an app's group")
	x.Equal("group: @contoso/loft-users is a group of @contoso/loft's, which is bound a role of @contoso/loft's and no other, and @contoso/reader is roster's",
		status.Convert(err).Message())

	// And the rest is as it was: a role of roster's to the tenant's own group,
	// and a role of an app's to a person.
	x.NoError(b.bindsGroup(ctx, reader, employees))
	b.binds(t, b.holder(t, ctx, b.Contoso, "frank"), role, nil)
}

// TestSomebodyTakenOutOfAnAppsGroupHoldsNothingOfIt.
//
// Taking somebody out of an app is ending their membership of its group, and
// nothing else is asked for: there is no exclusion to write. At the next
// `Reaches` the app is answered nothing of the role the group was bound.
func TestSomebodyTakenOutOfAnAppsGroupHoldsNothingOfIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const booking = "/loft.BookingService/Cancel"

	loft := b.holder(t, ctx, b.Contoso, "loft")
	b.binds(t, loft, b.role(t, ctx, "loft-app", "/roster.HolderService/Reaches"), nil)
	users := b.appGroup(t, ctx, loft, "loft-users")
	x.NoError(b.bindsGroup(ctx, b.roleAtApp(t, ctx, loft, "loft-staff", booking), users))

	erin := b.holder(t, ctx, b.Contoso, "erin")
	place := b.putsIn(t, ctx, erin, users)

	holders := app.NewHolderServiceClient(served(t, b.Server))
	reaches := func() []string {
		v, err := holders.Reaches(asOverTheWire(ctx, loft), app.HolderReachesRequest_builder{
			Ref: app.HolderRef_builder{Id: erin.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		return v.GetMethods()
	}
	x.Equal([]string{booking}, reaches())

	v, err := b.Ungated.GroupMembership().Erase(ctx, app.GroupMembershipRef_builder{Id: place.Bytes()}.Build())
	x.NoError(err)
	x.True(v.GetErased())
	x.Empty(reaches(), "somebody taken out of the app's group still held its role")
}

// TestAGroupSaysWhoseItIs.
//
// `Group.app` is a holder of the group's own tenant and not erased, and that is
// all that is asked of it, as of a role's: one of another tenant's is refused
// as every edge across tenants is, `InvalidArgument`, and one erased is nobody.
// Whoever makes a group manages it, whichever app's it is.
func TestAGroupSaysWhoseItIs(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	outside := b.holder(t, ctx, b.Fabrikam, "loft")
	_, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "loft-users",
		App:    app.HolderRef_builder{Id: outside.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a group of an app of another tenant's")

	gone := b.holder(t, ctx, b.Contoso, "gone")
	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone.Bytes()}.Build())
	x.NoError(err)
	_, err = b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "gone-users",
		App:    app.HolderRef_builder{Id: gone.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a group of an app whose holder was erased")

	// Made by somebody of the tenant, it is theirs to manage.
	loft := b.holder(t, ctx, b.Contoso, "loft")
	admin := b.holder(t, ctx, b.Contoso, "admin")
	g, err := b.Walled.Group().Add(b.asAtApp(ctx, admin), app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "loft-users",
		App:    app.HolderRef_builder{Id: loft.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(loft.Bytes(), g.GetApp().GetId())

	ms, err := b.Ungated.GroupMembership().List(ctx, app.GroupMembershipListRequest_builder{
		Filters: []*app.GroupMembershipFilter{app.GroupMembershipFilter_builder{Group: app.GroupRef_builder{Id: g.GetId()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(ms.GetItems(), 1)
	x.Equal(admin.Bytes(), ms.GetItems()[0].GetHolder().GetId())
	x.NotEmpty(ms.GetItems()[0].GetRole().GetId(), "the maker of an app's group does not manage it")
}

// TestWhatEndsAnAppsHolderEndsItsGroups.
//
// A group of an app's is the app's list of its users. Erasing the app's holder
// left its groups, bound its roles' bindings and with people in them, as the
// users of nobody -- and a holder erased is an alias given back, which another
// may take. They are erased with it, in the act that erases its roles, and a
// restore of the holder brings back neither.
func TestWhatEndsAnAppsHolderEndsItsGroups(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	role := b.roleAtApp(t, ctx, loft, "loft-staff", "/loft.*/*")
	users := b.appGroup(t, ctx, loft, "loft-users")
	x.NoError(b.bindsGroup(ctx, role, users))
	b.putsIn(t, ctx, b.holder(t, ctx, b.Contoso, "erin"), users)

	levyan := b.holder(t, ctx, b.Contoso, "levyan")
	other := b.appGroup(t, ctx, levyan, "levyan-users")
	own := b.appGroup(t, ctx, pdid.Nil, "employees")

	admin := b.holder(t, ctx, b.Contoso, "admin")
	v, err := b.Walled.Holder().Erase(b.asAtApp(ctx, admin), app.HolderRef_builder{Id: loft.Bytes()}.Build())
	x.NoError(err)
	x.True(v.GetErased())

	erased := func(g pdid.Id) bool {
		v, err := b.Ent.Group.Get(ctx, g.Uuid())
		x.NoError(err)

		return v.DateErased != nil
	}
	rolesErased := func(r pdid.Id) bool {
		v, err := b.Ent.Role.Get(ctx, r.Uuid())
		x.NoError(err)

		return v.DateErased != nil
	}
	x.True(erased(users), "a group of the erased app's is still there")
	x.True(rolesErased(role), "and its role")
	x.False(erased(other), "a group of another app's went with it")
	x.False(erased(own), "a group of the tenant's went with it")

	x.NoError(forget.Restore(ctx, b.Ent, loft, audit.Contents{}, nil))
	x.True(erased(users), "a restore of the holder brought its groups back")
	x.True(rolesErased(role), "a restore of the holder brought its roles back")
}
