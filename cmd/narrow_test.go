package cmd_test

import (
	"context"
	"sync"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

const narrow = "/roster.RoleService/Narrow"

// methodsOf reads a role's methods back, as the deployment does.
func (b *built) methodsOf(t *testing.T, ctx context.Context, role *app.RoleRef) []string {
	t.Helper()
	v, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{Ref: role}.Build())
	require.NoError(t, err)

	return v.GetMethods()
}

// TestANarrowTakesOutWhatIsNamedAndAddsNothing is the verb a tenant's
// administrator takes a method out of a role with (`role_svc.ext.proto`): an
// entry is taken out when what is named covers it, a pattern taking every
// entry under it; a role may be narrowed to nothing; nothing to take out is no
// write; and a method still reached through an entry left in the role is
// refused rather than answered as done. Over the wire, where `Patch` is closed
// and this is not.
func TestANarrowTakesOutWhatIsNamedAndAddsNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := asOverTheWire(ctx, b.grantee(t, ctx, "steward", narrow, "/loft.*/*"))
	roles := app.NewRoleServiceClient(served(t, b.Server))

	ref := app.RoleRef_builder{Id: b.role(t, ctx, "staff",
		"/loft.AssetService/Get", "/loft.AssetService/List", "/loft.Permission/RunWork").Bytes()}.Build()
	v, err := roles.Narrow(as, app.RoleNarrowRequest_builder{Ref: ref, Methods: []string{"/loft.AssetService/*"}}.Build())
	x.NoError(err)
	x.Equal([]string{"/loft.Permission/RunWork"}, v.GetMethods(), "every entry the pattern covers, and nothing else")

	v, err = roles.Narrow(as, app.RoleNarrowRequest_builder{Ref: ref, Methods: []string{"/loft.Permission/RunWork"}}.Build())
	x.NoError(err)
	x.Empty(v.GetMethods(), "a role narrowed to nothing")
	x.Empty(b.methodsOf(t, ctx, ref))

	again, err := roles.Narrow(as, app.RoleNarrowRequest_builder{Ref: ref, Methods: []string{"/loft.Permission/RunWork"}}.Build())
	x.NoError(err)
	x.Equal(v.GetDateUpdated().AsTime(), again.GetDateUpdated().AsTime(), "nothing to take out, and something was written")

	_, err = roles.Narrow(as, app.RoleNarrowRequest_builder{Ref: ref}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a narrowing that names nothing")

	// What was named still reached through what is left: refused, whole.
	wide := app.RoleRef_builder{Id: b.role(t, ctx, "wide", "/loft.*/*", "/loft.AssetService/Get").Bytes()}.Build()
	_, err = roles.Narrow(as, app.RoleNarrowRequest_builder{Ref: wide, Methods: []string{"/loft.AssetService/Get"}}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.ErrorContains(err, "/loft.*/*")
	x.Equal([]string{"/loft.*/*", "/loft.AssetService/Get"}, b.methodsOf(t, ctx, wide), "and something of it was taken out")
}

// TestNobodyTakesAwayWhatTheyCouldNotHaveGiven: a narrowing hands nobody
// anything, so what it leaves in a role is not weighed -- but what it takes out
// is, as a grant of it would have been. Somebody holding less than a role may
// take out what they hold and nothing more, which keeps whoever the role
// reaches holding what the caller does not -- and so out of the caller's reach
// for a password reset.
func TestNobodyTakesAwayWhatTheyCouldNotHaveGiven(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := framed(ctx, b.grantee(t, ctx, "steward", narrow, "/loft.AssetService/*"), b.Contoso)

	ref := app.RoleRef_builder{Id: b.role(t, ctx, "officer", "/loft.AssetService/Get", "/roster.HolderService/Erase").Bytes()}.Build()
	_, err := b.Walled.Role().Narrow(as, app.RoleNarrowRequest_builder{Ref: ref, Methods: []string{"/roster.HolderService/Erase"}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a method its taker does not hold, taken away")
	x.ErrorContains(err, "take it away")

	v, err := b.Walled.Role().Narrow(as, app.RoleNarrowRequest_builder{Ref: ref, Methods: []string{"/loft.AssetService/Get"}}.Build())
	x.NoError(err, "what they hold, out of a role that holds more besides")
	x.Equal([]string{"/roster.HolderService/Erase"}, v.GetMethods())

	// Bound at a site, a role is taken from at that site's width: what the
	// taker holds only in another site is not theirs to take there.
	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	local, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "desk", Methods: []string{narrow},
	}.Build())
	x.NoError(err)
	clerk := b.holder(t, ctx, b.Contoso, "clerk")
	b.binds(t, clerk, mustId(t, local.GetId()), nil)
	there, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "busan-assets", Methods: []string{"/loft.AssetService/*"},
	}.Build())
	x.NoError(err)
	b.binds(t, clerk, mustId(t, there.GetId()), &busan)
	inSeoul, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Site: app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		Alias: "seoul-desk", Methods: []string{"/loft.AssetService/Get"},
	}.Build())
	x.NoError(err)
	_, err = b.Walled.Role().Narrow(framed(ctx, clerk, b.Contoso), app.RoleNarrowRequest_builder{
		Ref: app.RoleRef_builder{Id: inSeoul.GetId()}.Build(), Methods: []string{"/loft.AssetService/Get"},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a site's role narrowed by somebody who holds it only in another site")
}

// TestADeclaredRoleIsNobodysToNarrow: as it is nobody's to widen -- even a
// narrowing that would take nothing out -- and the deployment's own work is
// not held to it.
func TestADeclaredRoleIsNobodysToNarrow(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := framed(ctx, b.grantee(t, ctx, "steward", narrow, "/loft.*/*"), b.Contoso)

	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "front-door",
		Labels:  map[string]string{cmd.Declared: "roster.yaml"},
		Methods: []string{"/loft.AssetService/Get", "/loft.AssetService/List"},
	}.Build())
	x.NoError(err)
	ref := app.RoleRef_builder{Id: r.GetId()}.Build()

	for _, m := range []string{"/loft.AssetService/List", "/loft.Permission/RunWork"} {
		_, err = b.Walled.Role().Narrow(as, app.RoleNarrowRequest_builder{Ref: ref, Methods: []string{m}}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%s, out of a declared role", m)
	}

	v, err := b.Ungated.Role().Narrow(ctx, app.RoleNarrowRequest_builder{Ref: ref, Methods: []string{"/loft.AssetService/List"}}.Build())
	x.NoError(err, "the deployment's own work")
	x.Equal([]string{"/loft.AssetService/Get"}, v.GetMethods())
}

// everything is the role a tenant was made with, which `Tenant.Add` binds its
// first holder: the administrator, holding the tenant's whole administration.
func (b *built) everything(t *testing.T, ctx context.Context, tenant pdid.Id) *app.RoleRef {
	t.Helper()
	v, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{
			Alias: z.Ptr(core.Everyverb), Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	require.NoError(t, err)
	require.Equal(t, []string{core.EveryMethod}, v.GetMethods())

	return app.RoleRef_builder{Id: v.GetId()}.Build()
}

// TestATenantKeepsSomebodyAdministeringIt: a narrowing that takes `/roster.*/*`
// from the last person holding it across the tenant is refused, because nobody
// in the tenant could put it back -- and it is that and no more. Somebody else
// holding it, directly or through a group, lets it through; somebody suspended,
// or a group nobody is in, does not count; a tenant nobody administered is not
// refused for it; and the deployment's own work is the deployment's.
func TestATenantKeepsSomebodyAdministeringIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	admin := b.holder(t, ctx, b.Contoso, core.Administers)
	as := framed(ctx, admin, b.Contoso)
	everything := b.everything(t, ctx, b.Contoso)
	take := app.RoleNarrowRequest_builder{Ref: everything, Methods: []string{core.EveryMethod}}.Build()

	_, err := b.Walled.Role().Narrow(as, take)
	x.Equal(codes.FailedPrecondition, status.Code(err), "the last administrator's administration, taken away")
	x.ErrorContains(err, "nobody administering the tenant")
	x.Equal([]string{core.EveryMethod}, b.methodsOf(t, ctx, everything), "and it was taken out")

	// Somebody else holds it, and is suspended: nobody who could use it.
	deputy := b.holder(t, ctx, b.Contoso, "deputy")
	b.binds(t, deputy, b.role(t, ctx, "deputy", core.EveryMethod), nil)
	_, err = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: app.HolderRef_builder{Id: deputy.Bytes()}.Build()}.Build())
	x.NoError(err)
	_, err = b.Walled.Role().Narrow(as, take)
	x.Equal(codes.FailedPrecondition, status.Code(err), "a suspended administrator counted")

	// And back: then the administrator may narrow away their own.
	_, err = b.Ungated.Holder().Enable(ctx, app.HolderEnableRequest_builder{Ref: app.HolderRef_builder{Id: deputy.Bytes()}.Build()}.Build())
	x.NoError(err)
	v, err := b.Walled.Role().Narrow(as, take)
	x.NoError(err, "somebody else administers it")
	x.Empty(v.GetMethods())

	// A tenant nobody administers across the whole of it is not refused for
	// it: somebody holding it in one site narrows that site's role.
	_, err = b.Ungated.Binding().Erase(ctx, b.bindingOf(t, ctx, deputy))
	x.NoError(err)
	seoul := b.site(t, ctx, b.Contoso, "seoul")
	local, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Site: app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		Alias: "seoul-admin", Methods: []string{narrow, core.EveryMethod},
	}.Build())
	x.NoError(err)
	keeper := b.holder(t, ctx, b.Contoso, "keeper")
	b.binds(t, keeper, mustId(t, local.GetId()), &seoul)
	_, err = b.Walled.Role().Narrow(framed(ctx, keeper, b.Contoso), app.RoleNarrowRequest_builder{
		Ref: app.RoleRef_builder{Id: local.GetId()}.Build(), Methods: []string{core.EveryMethod},
	}.Build())
	x.NoError(err, "refused for a tenant nobody administered across the whole of it")

	// And the deployment's own work is not held to it: fabrikam's
	// administrator is its last, and an operator at the box may still.
	_, err = b.Ungated.Role().Narrow(ctx, app.RoleNarrowRequest_builder{
		Ref: b.everything(t, ctx, b.Fabrikam), Methods: []string{core.EveryMethod},
	}.Build())
	x.NoError(err, "the deployment's own work")
}

// bindingOf is the one binding somebody holds, directly.
func (b *built) bindingOf(t *testing.T, ctx context.Context, who pdid.Id) *app.BindingRef {
	t.Helper()
	vs, err := b.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
		Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build()},
	}.Build())
	require.NoError(t, err)
	require.Len(t, vs.GetItems(), 1)

	return app.BindingRef_builder{Id: vs.GetItems()[0].GetId()}.Build()
}

// TestAnAdministratorThroughAGroupCounts: the administration a tenant keeps may
// be held through a group, as `cmd/policy.go` counts a group's bindings for
// everybody in it -- while the group has somebody in it, and is there.
func TestAnAdministratorThroughAGroupCounts(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := framed(ctx, b.holder(t, ctx, b.Contoso, core.Administers), b.Contoso)
	take := app.RoleNarrowRequest_builder{Ref: b.everything(t, ctx, b.Contoso), Methods: []string{core.EveryMethod}}.Build()

	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "admins",
	}.Build())
	x.NoError(err)
	group := app.GroupRef_builder{Id: g.GetId()}.Build()
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:  app.RoleRef_builder{Id: b.role(t, ctx, "admins", core.EveryMethod).Bytes()}.Build(),
		Group: group,
	}.Build())
	x.NoError(err)

	_, err = b.Walled.Role().Narrow(as, take)
	x.Equal(codes.FailedPrecondition, status.Code(err), "a group nobody is in, counted")

	_, err = b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
		Group: group, Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "ops").Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = b.Walled.Role().Narrow(as, take)
	x.NoError(err, "somebody administers it through a group")
}

// TestTwoNarrowingsCannotBothTakeTheLast: two administrators, each narrowing
// the role that makes the other one of the last two, at once. Each sees the
// other's still there; counted outside one transaction, both go through and
// the tenant is left with nobody. Counted after a write on the tenant's row
// they both have to make, the second counts what the first left -- and is
// refused.
func TestTwoNarrowingsCannotBothTakeTheLast(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const tenants = 8
	type pair struct {
		tenant     pdid.Id
		one, two   pdid.Id
		ones, twos *app.RoleRef
	}
	ps := make([]pair, tenants)
	for i := range ps {
		tn := b.tenant(t, ctx, "pair"+string(rune('a'+i)))
		one := b.holder(t, ctx, tn, core.Administers)
		two := b.holder(t, ctx, tn, "second")
		r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.Bytes()}.Build(), Alias: "second", Methods: []string{core.EveryMethod},
		}.Build())
		x.NoError(err)
		b.binds(t, two, mustId(t, r.GetId()), nil)
		ps[i] = pair{tenant: tn, one: one, two: two, ones: b.everything(t, ctx, tn), twos: app.RoleRef_builder{Id: r.GetId()}.Build()}
	}

	var wg sync.WaitGroup
	errs := make([][2]error, tenants)
	for i, p := range ps {
		for j, at := range []struct {
			who  pdid.Id
			role *app.RoleRef
		}{{p.one, p.ones}, {p.two, p.twos}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i][j] = b.Walled.Role().Narrow(framed(ctx, at.who, p.tenant), app.RoleNarrowRequest_builder{
					Ref: at.role, Methods: []string{core.EveryMethod},
				}.Build())
			}()
		}
	}
	wg.Wait()

	administered := cmd.Administered(b.Ent)
	for i, p := range ps {
		ok, err := administered(ctx, nil, p.tenant, core.EveryMethod)
		x.NoError(err)
		x.True(ok, "tenant %d left with nobody administering it: %v", i, errs[i])
		x.True(errs[i][0] == nil || errs[i][1] == nil, "neither went through in tenant %d: %v", i, errs[i])
	}
}

// TestARoleIsNarrowedFromATerminal: `roster role narrow`, from a shell over the
// wire as anybody holding the method and what it takes out.
func TestARoleIsNarrowedFromATerminal(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, narrow, "/loft.*/*")
	r, err := b.Server.Ungated.Role().Add(t.Context(), app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Tenant.GetId()}.Build(),
		Alias:   "support",
		Methods: []string{"/loft.AssetService/Get", "/loft.AssetService/List"},
	}.Build())
	x.NoError(err)

	_, err = cliRun(t, &b.Hers, "role", "narrow", "@newco/support", `{"methods":["/loft.AssetService/List"]}`)
	x.NoError(err)
	v, err := b.Server.Ungated.Role().Get(t.Context(), app.RoleGetRequest_builder{Ref: app.RoleRef_builder{Id: r.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal([]string{"/loft.AssetService/Get"}, v.GetMethods())
}
