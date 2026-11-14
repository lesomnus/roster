package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
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

// manages is the tenant's manager's role: the one roster made when somebody of
// the tenant first made a group (`core.Manages`), which is what an
// administrator appoints another manager with.
func (g groups) manages(t *testing.T, ctx context.Context) *app.RoleRef {
	t.Helper()
	v, err := g.b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{
			Alias:  proto.String(core.Manages),
			Tenant: app.TenantRef_builder{Id: g.b.Contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	require.NoError(t, err, "a group made by somebody of the tenant leaves the manager's role behind")

	return app.RoleRef_builder{Id: v.GetId()}.Build()
}

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
	manager := g.manages(t, ctx)
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
// everywhere, and only the writes that change that group are narrowed back to
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
	manager := g.manages(t, ctx)

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
	manager := g.manages(t, ctx)
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
	manager := g.manages(t, ctx)
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

// TestWhoeverMakesAGroupManagesIt: a group made by somebody of the tenant comes
// with their place in it, carrying the manager's role, in the same write -- so
// somebody who may make a group and holds no membership write across the
// tenant can put people into the one they made, take them out, and say what it
// is called, and can do none of that to a group somebody else made. A group
// made with nobody asking -- the deployment's own work -- has no manager, as
// every group had none before.
func TestWhoeverMakesAGroupManagesIt(t *testing.T) {
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	m, c := b.holder(t, ctx, b.Contoso, "maker"), b.holder(t, ctx, b.Contoso, "c")
	g.staff(t, ctx, m, c)
	asM, asC := b.asNobody(ctx, m, b.Contoso), b.asNobody(ctx, c, b.Contoso)

	made, err := b.Walled.Group().Add(asM, app.GroupAddRequest_builder{Tenant: at, Alias: "mine", Name: "Mine"}.Build())
	require.NoError(t, err)
	mine := app.GroupRef_builder{Id: made.GetId()}.Build()
	theirs := g.add(t, "theirs")

	membersOf := func(group *app.GroupRef) []*app.GroupMembership {
		t.Helper()
		vs, err := b.Ungated.GroupMembership().List(ctx, app.GroupMembershipListRequest_builder{
			Filters: []*app.GroupMembershipFilter{app.GroupMembershipFilter_builder{Group: group}.Build()},
		}.Build())
		require.NoError(t, err)

		out := make([]*app.GroupMembership, 0, len(vs.GetItems()))
		for _, v := range vs.GetItems() {
			v, err := b.Ungated.GroupMembership().Get(ctx, app.GroupMembershipGetRequest_builder{
				Ref: app.GroupMembershipRef_builder{Id: v.GetId()}.Build(),
				Select: app.GroupMembershipSelect_builder{
					Holder: app.HolderSelect_builder{}.Build(),
					Role:   app.RoleSelect_builder{Alias: proto.Bool(true), Methods: proto.Bool(true)}.Build(),
				}.Build(),
			}.Build())
			require.NoError(t, err)
			out = append(out, v)
		}

		return out
	}
	// Under the version read, as every write of a row that was read is.
	rename := func(as context.Context, group *app.GroupRef, name string) error {
		was, err := b.Ungated.Group().Get(ctx, app.GroupGetRequest_builder{
			Ref: group, Select: app.GroupSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		require.NoError(t, err)
		_, err = b.Walled.Group().Update(as, app.GroupUpdateRequest_builder{
			Ref: group, Name: proto.String(name), DateUpdated: was.GetDateUpdated(),
		}.Build())

		return err
	}

	t.Run("the maker is in it, as its manager", func(t *testing.T) {
		x := require.New(t)
		vs := membersOf(mine)
		x.Len(vs, 1)
		x.Equal(m.Bytes(), vs[0].GetHolder().GetId())
		x.Equal(core.Manages, vs[0].GetRole().GetAlias())
		x.ElementsMatch([]string{
			app.GroupMembershipService_Add_FullMethodName,
			app.GroupMembershipService_Erase_FullMethodName,
			app.GroupService_Update_FullMethodName,
			app.GroupService_Erase_FullMethodName,
		}, vs[0].GetRole().GetMethods())
	})

	var cIn *app.GroupMembership
	t.Run("and decides who else is, there and nowhere else", func(t *testing.T) {
		x := require.New(t)
		cIn, err = g.join(asM, c, mine, nil)
		x.NoError(err)

		_, err = g.join(asM, c, theirs, nil)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and says what it is called, and not what another is", func(t *testing.T) {
		x := require.New(t)
		x.NoError(rename(asM, mine, "Mine, renamed"))
		v, err := b.Ungated.Group().Get(ctx, app.GroupGetRequest_builder{Ref: mine}.Build())
		x.NoError(err)
		x.Equal("Mine, renamed", v.GetName())
		x.Equal("mine", v.GetAlias(), "what a slug names it by, and a token carries, is not what it is called")

		x.Equal(codes.PermissionDenied, status.Code(rename(asM, theirs, "Theirs, renamed")))
	})

	t.Run("a member who is not its manager does neither", func(t *testing.T) {
		x := require.New(t)
		x.Equal(codes.PermissionDenied, status.Code(rename(asC, mine, "C's")))
		_, err := g.join(asC, b.holder(t, ctx, b.Contoso, "d"), mine, nil)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("an administrator says what any group is called", func(t *testing.T) {
		require.NoError(t, rename(g.admin, mine, "Mine, by the administrator"))
	})

	t.Run("and the maker takes out who they put in", func(t *testing.T) {
		require.NoError(t, g.leave(asM, cIn))
	})

	t.Run("a group nobody was asking for has no manager", func(t *testing.T) {
		x := require.New(t)
		v, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{Tenant: at, Alias: "declared"}.Build())
		x.NoError(err)
		x.Empty(membersOf(app.GroupRef_builder{Id: v.GetId()}.Build()))
	})
}

// TestWhoeverManagesAGroupMayEraseIt: the other end of whoever makes a group
// managing it. Its manager erases it -- theirs, and no other: the gate lets
// every group's manager ask, since a role held in a group may name the method,
// and `mayChangeGroup` is what holds each to their own. A member who does not
// manage it does not, an administrator erases any as before, and a group
// erased leaves its alias for the next one.
func TestWhoeverManagesAGroupMayEraseIt(t *testing.T) {
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	m, c, o := b.holder(t, ctx, b.Contoso, "maker"), b.holder(t, ctx, b.Contoso, "c"), b.holder(t, ctx, b.Contoso, "other")
	g.staff(t, ctx, m, c, o)
	asM, asC, asO := b.asNobody(ctx, m, b.Contoso), b.asNobody(ctx, c, b.Contoso), b.asNobody(ctx, o, b.Contoso)

	make := func(as context.Context, alias string) *app.GroupRef {
		t.Helper()
		v, err := b.Walled.Group().Add(as, app.GroupAddRequest_builder{Tenant: at, Alias: alias}.Build())
		require.NoError(t, err)

		return app.GroupRef_builder{Id: v.GetId()}.Build()
	}
	erase := func(as context.Context, group *app.GroupRef) (bool, error) {
		v, err := b.Walled.Group().Erase(as, group)

		return v.GetErased(), err
	}
	there := func(group *app.GroupRef) bool {
		t.Helper()
		_, err := b.Ungated.Group().Get(ctx, app.GroupGetRequest_builder{Ref: group}.Build())
		if status.Code(err) == codes.NotFound {
			return false
		}
		require.NoError(t, err)

		return true
	}

	mine, others, theirs := make(asM, "mine"), make(asO, "others"), g.add(t, "theirs")
	_, err := g.join(asM, c, mine, nil)
	require.NoError(t, err)

	t.Run("not another manager's, nor one an administrator made", func(t *testing.T) {
		x := require.New(t)
		for _, group := range []*app.GroupRef{others, theirs} {
			_, err := erase(asM, group)
			x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
			x.True(there(group))
		}
	})

	t.Run("not by a member who does not manage it", func(t *testing.T) {
		x := require.New(t)
		_, err := erase(asC, mine)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.True(there(mine))
	})

	t.Run("their own, which is then gone and its alias free", func(t *testing.T) {
		x := require.New(t)
		erased, err := erase(asM, mine)
		x.NoError(err)
		x.True(erased)
		x.False(there(mine))

		again := make(asM, "mine")
		x.NotEqual(mine.GetId(), again.GetId(), "a group of that alias made now is another group")

		// And what is gone is nobody's to erase twice: nothing is, and
		// nothing is refused.
		erased, err = erase(asM, mine)
		x.NoError(err)
		x.False(erased)
	})

	t.Run("an administrator erases any, as before", func(t *testing.T) {
		x := require.New(t)
		erased, err := erase(g.admin, others)
		x.NoError(err)
		x.True(erased)
		x.False(there(others))
	})
}

// TestATenantsOwnManagersRoleIsTheOneAMakerHolds: the role a maker holds is
// the tenant's `group-manager`, made when it is not there. One that is already
// there is the tenant's word for what a manager does and is used as it stands
// -- so a tenant whose managers do not rename groups keeps them so -- and one
// that names anything a role held in a group may not is refused where the
// group is made, with nothing left behind: the group and its maker's place
// are one write.
func TestATenantsOwnManagersRoleIsTheOneAMakerHolds(t *testing.T) {
	at := func(b *built) *app.TenantRef { return app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build() }

	t.Run("used as it stands", func(t *testing.T) {
		b, ctx := build(t)
		x := require.New(t)
		own := b.role(t, ctx, core.Manages, app.GroupMembershipService_Add_FullMethodName)

		m := b.holder(t, ctx, b.Contoso, "maker")
		groups{b: b}.staff(t, ctx, m)
		asM := b.asNobody(ctx, m, b.Contoso)

		made, err := b.Walled.Group().Add(asM, app.GroupAddRequest_builder{Tenant: at(b), Alias: "mine"}.Build())
		x.NoError(err)
		mine := app.GroupRef_builder{Id: made.GetId()}.Build()

		vs, err := b.Ungated.GroupMembership().List(ctx, app.GroupMembershipListRequest_builder{
			Filters: []*app.GroupMembershipFilter{app.GroupMembershipFilter_builder{Group: mine}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		v, err := b.Ungated.GroupMembership().Get(ctx, app.GroupMembershipGetRequest_builder{
			Ref:    app.GroupMembershipRef_builder{Id: vs.GetItems()[0].GetId()}.Build(),
			Select: app.GroupMembershipSelect_builder{Role: app.RoleSelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(own.Bytes(), v.GetRole().GetId())

		_, err = b.Walled.Group().Update(asM, app.GroupUpdateRequest_builder{
			Ref: mine, Name: proto.String("renamed"), DateUpdated: made.GetDateUpdated(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "this tenant's managers do not rename: %v", err)

		_, err = b.Walled.Group().Erase(asM, mine)
		x.Equal(codes.PermissionDenied, status.Code(err), "nor erase: %v", err)
		_, err = b.Ungated.Group().Get(ctx, app.GroupGetRequest_builder{Ref: mine}.Build())
		x.NoError(err)
	})

	// Somebody who sees one site does not see a role of the whole tenant. The
	// role they manage by is found all the same, so the second group they make
	// is not refused for a role that is already there.
	t.Run("found whoever is making the group", func(t *testing.T) {
		b, ctx := build(t)
		x := require.New(t)

		seoul := b.site(t, ctx, b.Contoso, "seoul")
		m := b.holder(t, ctx, b.Contoso, "maker")
		b.binds(t, m, b.role(t, ctx, "site-admin", app.GroupService_Add_FullMethodName, app.GroupService_Get_FullMethodName), &seoul)
		asM := b.asNobody(ctx, m, b.Contoso)

		var roles [][]byte
		for _, alias := range []string{"first", "second"} {
			made, err := b.Walled.Group().Add(asM, app.GroupAddRequest_builder{
				Tenant: at(b), Site: app.SiteRef_builder{Id: seoul.Bytes()}.Build(), Alias: alias,
			}.Build())
			x.NoError(err, alias)

			vs, err := b.Ungated.GroupMembership().List(ctx, app.GroupMembershipListRequest_builder{
				Filters: []*app.GroupMembershipFilter{app.GroupMembershipFilter_builder{
					Group: app.GroupRef_builder{Id: made.GetId()}.Build(),
				}.Build()},
			}.Build())
			x.NoError(err)
			x.Len(vs.GetItems(), 1, alias)
			v, err := b.Ungated.GroupMembership().Get(ctx, app.GroupMembershipGetRequest_builder{
				Ref:    app.GroupMembershipRef_builder{Id: vs.GetItems()[0].GetId()}.Build(),
				Select: app.GroupMembershipSelect_builder{Role: app.RoleSelect_builder{}.Build()}.Build(),
			}.Build())
			x.NoError(err)
			roles = append(roles, v.GetRole().GetId())
		}
		x.Equal(roles[0], roles[1], "one role a tenant")
	})

	t.Run("refused when it is not what a manager may hold, and nothing is left", func(t *testing.T) {
		b, ctx := build(t)
		x := require.New(t)
		b.role(t, ctx, core.Manages, app.HolderService_Get_FullMethodName)

		m := b.holder(t, ctx, b.Contoso, "maker")
		_, err := b.Walled.Group().Add(b.asNobody(ctx, m, b.Contoso), app.GroupAddRequest_builder{Tenant: at(b), Alias: "mine"}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

		_, err = b.Ungated.Group().Get(ctx, app.GroupGetRequest_builder{
			Ref: app.GroupRef_builder{Slug: app.GroupRefBySlug_builder{Alias: proto.String("mine"), Tenant: at(b)}.Build()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "the group and its maker's place are one write: %v", err)
	})
}
