package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// groups is a tenant's groups and the writes a test makes on them, as somebody.
type groups struct {
	b     *built
	admin context.Context
}

func (g groups) add(t *testing.T, alias string) *app.GroupRef {
	t.Helper()
	v, err := g.b.Walled.Group().Add(g.admin, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: g.b.Contoso.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return app.GroupRef_builder{Id: v.GetId()}.Build()
}

// join puts somebody into a group, as `as`, naming a role or none.
func (g groups) join(as context.Context, who pdid.Id, group *app.GroupRef, role *app.RoleRef) (*app.GroupMembership, error) {
	return g.b.Walled.GroupMembership().Add(as, app.GroupMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Group:  group,
		Role:   role,
	}.Build())
}

func (g groups) leave(as context.Context, v *app.GroupMembership) error {
	_, err := g.b.Walled.GroupMembership().Erase(as, app.GroupMembershipRef_builder{Id: v.GetId()}.Build())

	return err
}

func roleRef(k pdid.Id) *app.RoleRef { return app.RoleRef_builder{Id: k.Bytes()}.Build() }

// staff binds people a role that lets them see the tenant's groups and do
// nothing to them: how a manager sees the group they manage, which a role held
// in it does not give -- the wall narrows by where a binding reaches, and a
// group of no site is the whole tenant's.
func (g groups) staff(t *testing.T, ctx context.Context, who ...pdid.Id) {
	t.Helper()
	role := g.b.role(t, ctx, "staff", app.GroupService_Get_FullMethodName)
	for _, k := range who {
		_, err := g.b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   roleRef(role),
			Holder: app.HolderRef_builder{Id: k.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)
	}
}

// TestAGroupsManagersDecideItsMembers is #120's example. The tenant's
// administrator makes groups A and B and appoints a to manage A and b to manage
// B. a may put b into A; a may not put herself into B; b, now a plain member
// of A and still B's manager, may not put c into A -- being in a group is not
// managing it, and managing one group is not managing another.
func TestAGroupsManagersDecideItsMembers(t *testing.T) {
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}

	A, B := g.add(t, "a-team"), g.add(t, "b-team")
	manager := roleRef(b.role(t, ctx, "group-manager",
		app.GroupMembershipService_Add_FullMethodName,
		app.GroupMembershipService_Erase_FullMethodName,
	))
	a, bee, c := b.holder(t, ctx, b.Contoso, "a"), b.holder(t, ctx, b.Contoso, "b"), b.holder(t, ctx, b.Contoso, "c")
	g.staff(t, ctx, a, bee, c)

	_, err := g.join(g.admin, a, A, manager)
	require.NoError(t, err)
	_, err = g.join(g.admin, bee, B, manager)
	require.NoError(t, err)

	// Neither holds anything through a binding but the sight of the groups:
	// what they may do to one is what they hold in it.
	asA, asB := b.asNobody(ctx, a, b.Contoso), b.asNobody(ctx, bee, b.Contoso)

	var bInA, cInB *app.GroupMembership
	t.Run("a manager puts somebody into their group", func(t *testing.T) {
		bInA, err = g.join(asA, bee, A, nil)
		require.NoError(t, err)
	})

	t.Run("and not into another, themselves included", func(t *testing.T) {
		_, err := g.join(asA, a, B, nil)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and a member is not a manager", func(t *testing.T) {
		_, err := g.join(asB, c, A, nil)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("while the group they manage is still theirs", func(t *testing.T) {
		cInB, err = g.join(asB, c, B, nil)
		require.NoError(t, err)
	})

	t.Run("a manager does not make another", func(t *testing.T) {
		_, err := g.join(asA, c, A, manager)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and takes people out of their own group alone", func(t *testing.T) {
		x := require.New(t)
		x.Equal(codes.PermissionDenied, status.Code(g.leave(asA, cInB)))
		x.NoError(g.leave(asA, bInA))
	})
}

// TestARoleHeldInAGroupReachesNoFurther: the gate holds a role held in a group
// everywhere, and only the group's own membership writes are narrowed back to
// it -- so that is all such a role may name, and it belongs to no site.
func TestARoleHeldInAGroupReachesNoFurther(t *testing.T) {
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}
	A := g.add(t, "a-team")
	a := b.holder(t, ctx, b.Contoso, "a")

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	sited, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		Alias:   "seoul-manager",
		Methods: []string{app.GroupMembershipService_Add_FullMethodName},
	}.Build())
	require.NoError(t, err)

	for _, tc := range []struct {
		desc string
		role *app.RoleRef
	}{
		{"one naming anything else", roleRef(b.role(t, ctx, "reads-people",
			app.GroupMembershipService_Add_FullMethodName, app.HolderService_Get_FullMethodName))},
		{"a pattern, which names the reads too", roleRef(b.role(t, ctx, "every-membership", "/roster.GroupMembershipService/*"))},
		{"one of a site", app.RoleRef_builder{Id: sited.GetId()}.Build()},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := g.join(g.admin, a, A, tc.role)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		})
	}
}

// TestAGroupBoundToARoleStillAsksWhoeverJoinsSomebody: managers change nothing
// about `mayJoin`. A group bound to one of roster's roles asks whoever puts
// somebody in to hold it -- somebody who edits memberships across the tenant
// without holding it is refused, as before. Its manager is a member and holds
// it, and an appointment is held to it too.
func TestAGroupBoundToARoleStillAsksWhoeverJoinsSomebody(t *testing.T) {
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}

	ops := g.add(t, "ops")
	operator := b.role(t, ctx, "operator", app.HolderService_Disable_FullMethodName)
	_, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:  roleRef(operator),
		Group: ops,
	}.Build())
	require.NoError(t, err)
	manager := roleRef(b.role(t, ctx, "group-manager",
		app.GroupMembershipService_Add_FullMethodName,
		app.GroupMembershipService_Erase_FullMethodName,
	))

	a, c, help := b.holder(t, ctx, b.Contoso, "a"), b.holder(t, ctx, b.Contoso, "c"), b.holder(t, ctx, b.Contoso, "help")
	desk := b.role(t, ctx, "helpdesk", app.GroupMembershipService_Add_FullMethodName)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   roleRef(desk),
		Holder: app.HolderRef_builder{Id: help.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
	asHelp := b.asNobody(ctx, help, b.Contoso)

	t.Run("not by somebody who does not hold what it hands out", func(t *testing.T) {
		_, err := g.join(asHelp, c, ops, nil)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("nor appointed by them, though they hold the method", func(t *testing.T) {
		_, err := g.join(asHelp, a, ops, roleRef(b.role(t, ctx, "adds-only", app.GroupMembershipService_Add_FullMethodName)))
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and by its manager, who holds it by being in it", func(t *testing.T) {
		x := require.New(t)
		_, err := g.join(g.admin, a, ops, manager)
		x.NoError(err)

		_, err = g.join(b.asNobody(ctx, a, b.Contoso), c, ops, nil)
		x.NoError(err)
	})
}

// TestAGroupsManagerIsNoWayInForSomebodyHoldingLess: what a manager holds in
// their group counts as theirs where `mayReach` asks -- the gate holds it
// everywhere -- so somebody who may write people's ways in, and does not hold
// what the manager does, may not write one onto the manager.
func TestAGroupsManagerIsNoWayInForSomebodyHoldingLess(t *testing.T) {
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}

	A := g.add(t, "a-team")
	manager := roleRef(b.role(t, ctx, "group-manager",
		app.GroupMembershipService_Add_FullMethodName,
		app.GroupMembershipService_Erase_FullMethodName,
	))
	a, help := b.holder(t, ctx, b.Contoso, "a"), b.holder(t, ctx, b.Contoso, "help")
	g.staff(t, ctx, a)
	_, err := g.join(g.admin, a, A, manager)
	require.NoError(t, err)

	linker := b.role(t, ctx, "linker", app.IdentityService_Add_FullMethodName, app.GroupService_Get_FullMethodName)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   roleRef(linker),
		Holder: app.HolderRef_builder{Id: help.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	_, err = b.Walled.Identity().Add(b.asNobody(ctx, help, b.Contoso), app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: a.Bytes()}.Build(),
		Provider: "github",
		Subject:  "12345678",
	}.Build())
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
}

// TestAManagerReachesTheirGroupsWritesAndNotEverywhere: what the gate lets a
// manager call is the union `Reaches` answers with, so their group's
// membership writes are in `methods` -- and not in `everywhere`, which is what
// an app with no wall of its own grants on, and which a role held in one group
// is not.
func TestAManagerReachesTheirGroupsWritesAndNotEverywhere(t *testing.T) {
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}

	A := g.add(t, "a-team")
	manager := roleRef(b.role(t, ctx, "group-manager",
		app.GroupMembershipService_Add_FullMethodName,
		app.GroupMembershipService_Erase_FullMethodName,
	))
	a := b.holder(t, ctx, b.Contoso, "a")
	_, err := g.join(g.admin, a, A, manager)
	require.NoError(t, err)

	v, err := b.Walled.Holder().Reaches(g.admin, app.HolderReachesRequest_builder{
		Ref: app.HolderRef_builder{Id: a.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
	require.Contains(t, v.GetMethods(), app.GroupMembershipService_Add_FullMethodName)
	require.Contains(t, v.GetMethods(), app.GroupMembershipService_Erase_FullMethodName)
	require.NotContains(t, v.GetEverywhere(), app.GroupMembershipService_Add_FullMethodName)
}
