package cmd_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// lastAdmin is a tenant of its own whose administrator -- the holder
// `Tenant.Add` made, through the role it was made with -- is the last person
// administering it, and the ways to their administration a test may take away.
type lastAdmin struct {
	b      *built
	tenant pdid.Id
	admin  pdid.Id
	as     context.Context

	everything *app.RoleRef
	binding    *app.BindingRef
}

func newLastAdmin(t *testing.T, b *built, ctx context.Context, alias string) lastAdmin {
	t.Helper()
	tn := b.tenant(t, ctx, alias)
	admin := b.holder(t, ctx, tn, core.Administers)

	return lastAdmin{
		b: b, tenant: tn, admin: admin, as: framed(ctx, admin, tn),
		everything: b.everything(t, ctx, tn),
		binding:    b.bindingOf(t, ctx, admin),
	}
}

// second makes somebody else administer the tenant too, through a role of
// their own across it.
func (l lastAdmin) second(t *testing.T, ctx context.Context) pdid.Id {
	t.Helper()
	who := l.b.holder(t, ctx, l.tenant, "second")
	r, err := l.b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: l.tenant.Bytes()}.Build(), Alias: "second", Methods: []string{core.EveryMethod},
	}.Build())
	require.NoError(t, err)
	l.b.binds(t, who, mustId(t, r.GetId()), nil)

	return who
}

// administered is whether the tenant still has somebody administering it.
func (l lastAdmin) administered(t *testing.T, ctx context.Context) bool {
	t.Helper()
	ok, err := cmd.Administered(l.b.Ent)(ctx, nil, l.tenant, core.EveryMethod)
	require.NoError(t, err)

	return ok
}

// TestEveryWriteThatEndsTheLastAdministrationIsRefused: whichever way the
// last person administering a tenant would lose it -- their binding erased,
// the role erased or patched, the group it reaches them through erased or
// their place in it, they suspended or erased -- the write is refused and
// nothing is written; with somebody else administering it, each goes through.
func TestEveryWriteThatEndsTheLastAdministrationIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		// through puts the administration where the write takes it from, and
		// answers the write.
		write func(t *testing.T, ctx context.Context, l lastAdmin) error
	}{
		{"their binding erased", func(t *testing.T, ctx context.Context, l lastAdmin) error {
			_, err := l.b.Walled.Binding().Erase(l.as, l.binding)
			return err
		}},
		{"the role erased", func(t *testing.T, ctx context.Context, l lastAdmin) error {
			_, err := l.b.Walled.Role().Erase(l.as, l.everything)
			return err
		}},
		{"the role patched", func(t *testing.T, ctx context.Context, l lastAdmin) error {
			v, err := l.b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{Ref: l.everything}.Build())
			require.NoError(t, err)
			_, err = l.b.Walled.Role().Patch(l.as, app.RolePatchRequest_builder{
				Ref: l.everything, Methods: []string{"/roster.HolderService/*"}, DateUpdated: v.GetDateUpdated(),
			}.Build())
			return err
		}},
		{"they suspended", func(t *testing.T, ctx context.Context, l lastAdmin) error {
			_, err := l.b.Walled.Holder().Disable(l.as, app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: l.admin.Bytes()}.Build(),
			}.Build())
			return err
		}},
		{"they erased", func(t *testing.T, ctx context.Context, l lastAdmin) error {
			_, err := l.b.Walled.Holder().Erase(l.as, app.HolderRef_builder{Id: l.admin.Bytes()}.Build())
			return err
		}},
		{"their place in the group it reaches them through", func(t *testing.T, ctx context.Context, l lastAdmin) error {
			m := l.throughAGroup(t, ctx)
			_, err := l.b.Walled.GroupMembership().Erase(l.as, m)
			return err
		}},
		{"the group it reaches them through", func(t *testing.T, ctx context.Context, l lastAdmin) error {
			l.throughAGroup(t, ctx)
			_, err := l.b.Walled.Group().Erase(l.as, l.admins(t, ctx))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t)

			l := newLastAdmin(t, b, ctx, "solo")
			err := tc.write(t, ctx, l)
			x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
			x.ErrorContains(err, "nobody administering the tenant")
			x.True(l.administered(t, ctx), "refused, and something of it was written")

			l = newLastAdmin(t, b, ctx, "pair")
			l.second(t, ctx)
			x.NoError(tc.write(t, ctx, l), "somebody else administers it")
			x.True(l.administered(t, ctx))
		})
	}
}

// throughAGroup moves the tenant's administration from the administrator's
// binding to a group they are in, and answers their membership of it.
func (l lastAdmin) throughAGroup(t *testing.T, ctx context.Context) *app.GroupMembershipRef {
	t.Helper()
	x := require.New(t)
	g, err := l.b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: l.tenant.Bytes()}.Build(), Alias: "admins",
	}.Build())
	x.NoError(err)
	group := app.GroupRef_builder{Id: g.GetId()}.Build()
	_, err = l.b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{Role: l.everything, Group: group}.Build())
	x.NoError(err)
	m, err := l.b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
		Group: group, Holder: app.HolderRef_builder{Id: l.admin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = l.b.Ungated.Binding().Erase(ctx, l.binding)
	x.NoError(err)
	x.True(l.administered(t, ctx), "through the group")

	return app.GroupMembershipRef_builder{Id: m.GetId()}.Build()
}

func (l lastAdmin) admins(t *testing.T, ctx context.Context) *app.GroupRef {
	t.Helper()
	vs, err := l.b.Ungated.Group().List(ctx, app.GroupListRequest_builder{
		Filters: []*app.GroupFilter{app.GroupFilter_builder{Tenant: app.TenantRef_builder{Id: l.tenant.Bytes()}.Build()}.Build()},
	}.Build())
	require.NoError(t, err)
	require.Len(t, vs.GetItems(), 1)

	return app.GroupRef_builder{Id: vs.GetItems()[0].GetId()}.Build()
}

// TestTwoWritesCannotBothTakeTheLast: two administrators of a tenant, each
// taking the other's administration away at once -- unbinding each other in
// half the tenants, suspending each other in the rest. Each sees the other's
// still there; the tenant's lock makes the second count what the first left,
// and refuses it. Without it both went through on PostgreSQL, either way.
func TestTwoWritesCannotBothTakeTheLast(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const tenants = 8
	ls := make([]lastAdmin, tenants)
	seconds := make([]pdid.Id, tenants)
	for i := range ls {
		ls[i] = newLastAdmin(t, b, ctx, "race"+string(rune('a'+i)))
		seconds[i] = ls[i].second(t, ctx)
	}

	var wg sync.WaitGroup
	errs := make([][2]error, tenants)
	for i, l := range ls {
		one, two := l.as, framed(ctx, seconds[i], l.tenant)
		theirs := b.bindingOf(t, ctx, seconds[i])
		wg.Add(2)
		if i%2 == 0 {
			go func() {
				defer wg.Done()
				_, errs[i][0] = b.Walled.Binding().Erase(one, theirs)
			}()
			go func() {
				defer wg.Done()
				_, errs[i][1] = b.Walled.Binding().Erase(two, l.binding)
			}()

			continue
		}
		go func() {
			defer wg.Done()
			_, errs[i][0] = b.Walled.Holder().Disable(one, app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: seconds[i].Bytes()}.Build(),
			}.Build())
		}()
		go func() {
			defer wg.Done()
			_, errs[i][1] = b.Walled.Holder().Disable(two, app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: l.admin.Bytes()}.Build(),
			}.Build())
		}()
	}
	wg.Wait()

	for i, l := range ls {
		x.True(l.administered(t, ctx), "tenant %d left with nobody administering it: %v", i, errs[i])
		x.True(errs[i][0] == nil || errs[i][1] == nil, "neither went through in tenant %d: %v", i, errs[i])
	}
}

// TestNobodyTakesAwayWhatARowGaveThem: a binding, a role, a group and a place
// in one, and a role held in a team each gave somebody methods, and taking one
// away is held to what granting it was -- nobody takes away what they could
// not have given. Here a steward holds the writes and a narrow part of
// what is handed out, and may take that part away and nothing wider: which is
// what keeps an administrator out of reach of somebody who could otherwise
// strip them below themselves and reset their password.
func TestNobodyTakesAwayWhatARowGaveThem(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	steward := b.grantee(t, ctx, "steward",
		app.BindingService_Erase_FullMethodName,
		app.RoleService_Erase_FullMethodName,
		app.GroupService_Erase_FullMethodName,
		app.GroupMembershipService_Erase_FullMethodName,
		app.TeamMembershipService_Erase_FullMethodName,
		"/loft.*/*",
	)
	as := framed(ctx, steward, b.Contoso)
	victim := b.holder(t, ctx, b.Contoso, "victim")

	wide := b.role(t, ctx, "wide", "/roster.HolderService/Erase")
	narrow := b.role(t, ctx, "narrow", "/loft.AssetService/Get")

	// A binding.
	b.binds(t, victim, wide, nil)
	b.binds(t, victim, narrow, nil)
	of := func(role pdid.Id) *app.BindingRef {
		vs, err := b.Ungated.Binding().List(ctx, app.BindingListRequest_builder{Filters: []*app.BindingFilter{app.BindingFilter_builder{
			Holder: app.HolderRef_builder{Id: victim.Bytes()}.Build(), Role: roleRef(role),
		}.Build()}}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)

		return app.BindingRef_builder{Id: vs.GetItems()[0].GetId()}.Build()
	}
	_, err := b.Walled.Binding().Erase(as, of(wide))
	x.Equal(codes.PermissionDenied, status.Code(err), "a binding of what its taker does not hold, erased: %v", err)
	_, err = b.Walled.Binding().Erase(as, of(narrow))
	x.NoError(err, "what they hold")

	// A role.
	_, err = b.Walled.Role().Erase(as, roleRef(wide))
	x.Equal(codes.PermissionDenied, status.Code(err), "a role wider than its taker, erased: %v", err)
	_, err = b.Walled.Role().Erase(as, roleRef(narrow))
	x.NoError(err)

	// A group, and a place in one.
	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "ops",
	}.Build())
	x.NoError(err)
	group := app.GroupRef_builder{Id: g.GetId()}.Build()
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{Role: roleRef(wide), Group: group}.Build())
	x.NoError(err)
	m, err := b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
		Group: group, Holder: app.HolderRef_builder{Id: victim.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = b.Walled.GroupMembership().Erase(as, app.GroupMembershipRef_builder{Id: m.GetId()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "somebody taken out of a group bound wider than the taker: %v", err)
	_, err = b.Walled.Group().Erase(as, group)
	x.Equal(codes.PermissionDenied, status.Code(err), "a group bound wider than its taker, erased: %v", err)

	// A role held in a team, at the team's site.
	seoul := b.site(t, ctx, b.Contoso, "seoul")
	team := b.team(t, ctx, seoul, "desk")
	tm, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Team: app.TeamRef_builder{Id: team.Bytes()}.Build(), Holder: app.HolderRef_builder{Id: victim.Bytes()}.Build(), Role: roleRef(wide),
	}.Build())
	x.NoError(err)
	_, err = b.Walled.TeamMembership().Erase(as, app.TeamMembershipRef_builder{Id: tm.GetId()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a team's role wider than its taker, taken away: %v", err)
}

// TestASuspensionIsNotATakingAway: suspending somebody takes nothing a row
// granted -- their bindings stay, and so does everything `mayReach` sees of
// them -- and somebody on call must be able to suspend a compromised account
// wider than they are. So it is held to the tenant keeping an administrator
// and to nothing about what the suspender holds (D26).
func TestASuspensionIsNotATakingAway(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	oncall := b.grantee(t, ctx, "oncall", app.HolderService_Disable_FullMethodName)
	wide := b.grantee(t, ctx, "wide", "/roster.HolderService/*", "/roster.RoleService/*")

	_, err := b.Walled.Holder().Disable(framed(ctx, oncall, b.Contoso), app.HolderDisableRequest_builder{
		Ref: app.HolderRef_builder{Id: wide.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// TestADirectoryMaySuspendTheLastAdministrator: a directory's suspension is
// its word about who works there, and refused, the person it removed would go
// on signing in -- so it is not held to the tenant keeping an administrator,
// and a tenant whose directory removes its last one is an operator's to put
// right.
func TestADirectoryMaySuspendTheLastAdministrator(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.directory(t, ctx)

	h, err := b.provision(as, "erin", erinOid, "erin@contoso.example")
	x.NoError(err)
	erin := mustId(t, h.GetId())
	everything := b.everything(t, ctx, b.Contoso)
	b.binds(t, erin, mustId(t, everything.GetId()), nil)
	_, err = b.Ungated.Binding().Erase(ctx, b.bindingOf(t, ctx, b.holder(t, ctx, b.Contoso, core.Administers)))
	x.NoError(err)

	_, err = b.Walled.Holder().Deactivate(as, app.HolderDeactivateRequest_builder{
		Ref: app.HolderRef_builder{Id: erin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	ok, err := cmd.Administered(b.Ent)(ctx, nil, b.Contoso, core.EveryMethod)
	x.NoError(err)
	x.False(ok, "and the tenant is the operator's to put right")
}

// TestAnErasedRoleGrantsNothing: a role erased takes its methods from
// everybody it was bound to, held in a team or as a group's manager -- what
// erasing one in a console says it does. It used not to: the role's edge was
// loaded whatever its erasure said, and its bindings went on granting it.
func TestAnErasedRoleGrantsNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	who := b.holder(t, ctx, b.Contoso, "bound")
	r := b.role(t, ctx, "reader", "/roster.HolderService/Get")
	b.binds(t, who, r, nil)
	held := cmd.Everything(b.Ent)

	ms, _, _, err := held(ctx, who)
	x.NoError(err)
	x.Equal([]string{"/roster.HolderService/Get"}, ms)

	_, err = b.Ungated.Role().Erase(ctx, roleRef(r))
	x.NoError(err)
	ms, sites, every, err := held(ctx, who)
	x.NoError(err)
	x.Empty(ms, "an erased role went on granting")
	x.Empty(sites)
	x.False(every)

	gs, err := cmd.Granted(b.Ent)(ctx, who)
	x.NoError(err)
	x.Empty(gs, "and on being passed on")
}
