package cmd_test

import (
	"context"
	"slices"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cmd"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/forget"
)

// A grant is at one app (`Role.app`, #567).
//
// A permission was a method's name held across a tenant, and every app was
// answered the whole list: a role naming a method two apps both serve was a
// grant at both, and whoever could hand the name out handed it out wherever
// it was read. These are the readers that each had to learn the app: roster's
// own gate, the hold rule, the rule that protects an account, and where an
// app's role may be bound.

// roleAtApp is a role of Contoso's naming the methods of the app `of` -- the
// holder that app is answered as -- made at the box.
func (b *built) roleAtApp(t *testing.T, ctx context.Context, of pdid.Id, alias string, methods ...string) pdid.Id {
	t.Helper()

	v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
		App:     app.HolderRef_builder{Id: of.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// asAtApp is a request from somebody, in Contoso, with no binding made for it.
func (b *built) asAtApp(ctx context.Context, who pdid.Id) context.Context {
	return frame.Into(ctx, frame.New(who, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))
}

// TestARoleOfAnAppGivesNothingAtRoster.
//
// roster's gate read every role somebody held, whichever app it named: a
// role of Loft's naming `/roster.HolderService/Erase`, bound to somebody, let
// them erase anybody in the tenant -- answered `{erased: true}`; right is
// `PermissionDenied`. A role of an app's is read by that app, and roster's
// gate reads roster's.
func TestARoleOfAnAppGivesNothingAtRoster(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	erin := b.holder(t, ctx, b.Contoso, "erin")
	victim := b.holder(t, ctx, b.Contoso, "victim")
	b.binds(t, erin, b.roleAtApp(t, ctx, loft, "loft-eraser", eraseHold, getHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, erin)

	_, err := app.NewHolderServiceClient(conn).Erase(wire, app.HolderRef_builder{Id: victim.Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a role of an app's erased somebody at roster")

	// Nor does it open a site, or a read: the wall reads roster's too.
	_, err = app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: victim.Bytes()}.Build(),
	}.Build())
	x.Error(err, "a role of an app's read somebody at roster")

	// And what the page draws is what the gate allows.
	me, err := app.NewMeServiceClient(conn).Get(wire, app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.NotContains(me.GetMethods(), eraseHold, "MeService.Get said a role of an app's is held at roster")
	x.False(me.GetEverySite())
}

// TestAMethodBothAppsServeIsHeldAtTheOneTheRoleNames.
//
// Two apps' holders ask `Reaches` about one person, bound a role of the first
// naming a method both serve. It was answered to both, and to roster's own
// gate: one grant, at three places. The first is answered it, the second
// nothing, and roster lets the person call nothing for it.
func TestAMethodBothAppsServeIsHeldAtTheOneTheRoleNames(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const both = "/roster.HolderService/List"

	loft := b.holder(t, ctx, b.Contoso, "loft")
	levyan := b.holder(t, ctx, b.Contoso, "levyan")
	asks := b.role(t, ctx, "asks", "/roster.HolderService/Reaches")
	b.binds(t, loft, asks, nil)
	b.binds(t, levyan, asks, nil)

	erin := b.holder(t, ctx, b.Contoso, "erin")
	b.binds(t, erin, b.roleAtApp(t, ctx, loft, "loft-staff", both), nil)

	conn := served(t, b.Server)
	holders := app.NewHolderServiceClient(conn)
	her := app.HolderReachesRequest_builder{Ref: app.HolderRef_builder{Id: erin.Bytes()}.Build()}.Build()

	at, err := holders.Reaches(asOverTheWire(ctx, loft), her)
	x.NoError(err)
	x.Contains(at.GetMethods(), both, "the app the role names was not answered it")
	x.Contains(at.GetEverywhere(), both)

	at, err = holders.Reaches(asOverTheWire(ctx, levyan), her)
	x.NoError(err)
	x.NotContains(at.GetMethods(), both, "another app was answered a role of the first's")
	x.NotContains(at.GetEverywhere(), both)

	_, err = holders.List(asOverTheWire(ctx, erin), app.HolderListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "roster's gate let a role of an app's through")
}

// TestWhoHoldsEverythingAtOneAppHandsOutThereAndNowhereElse.
//
// The hold rule compared a method's name with every name the caller held,
// whichever app's: somebody who held all of Loft's methods could write a role
// of levyan's naming the very same names, or one of roster's, and both were
// taken; right is `PermissionDenied` for each. At the app they hold it, they
// write and bind as before.
func TestWhoHoldsEverythingAtOneAppHandsOutThereAndNowhereElse(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const booking = "/shared.BookingService/Cancel"

	loft := b.holder(t, ctx, b.Contoso, "loft")
	levyan := b.holder(t, ctx, b.Contoso, "levyan")
	owner := b.holder(t, ctx, b.Contoso, "owner")
	b.binds(t, owner, b.roleAtApp(t, ctx, loft, "loft-everything", "/shared.*/*"), nil)
	as := b.mayCall(t, ctx, owner, "role-writer",
		"/roster.RoleService/Add", "/roster.RoleService/Get", "/roster.BindingService/Add", "/roster.HolderService/Get")

	roles := b.Walled.Role()
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	v, err := roles.Add(as, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "loft-cancel", Methods: []string{booking},
		App: app.HolderRef_builder{Id: loft.Bytes()}.Build(),
	}.Build())
	x.NoError(err, "a role of the app they hold everything at")
	_, err = b.Walled.Binding().Add(as, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: v.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "frank").Bytes()}.Build(),
	}.Build())
	x.NoError(err, "a binding of a role of the app they hold everything at")

	_, err = roles.Add(as, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "levyan-cancel", Methods: []string{booking},
		App: app.HolderRef_builder{Id: levyan.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a role of another app naming the same methods")
	x.Contains(status.Convert(err).Message(), "at @contoso/levyan")

	_, err = roles.Add(as, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "roster-cancel", Methods: []string{"/shared.*/*"},
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "a role of roster's naming the methods held at an app")
}

// TestAllOfRosterHandsOutNothingAtAnApp.
//
// A tenant's administrator holds `/roster.*/*`, and the hold rule read it
// against an app's methods as it read it against roster's -- refusing, but in
// words that said nothing of where. Holding all of roster is holding nothing
// at an app, and the refusal names the app.
func TestAllOfRosterHandsOutNothingAtAnApp(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	admin := b.holder(t, ctx, b.Contoso, "admin")
	b.mayAnything(admin, b.Contoso)

	_, err := b.Walled.Role().Add(b.asAtApp(ctx, admin), app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "loft-cancel",
		Methods: []string{"/loft.BookingService/Cancel"},
		App:     app.HolderRef_builder{Id: loft.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Equal(`methods: you do not hold "/loft.BookingService/Cancel" at @contoso/loft, so you may not grant it`,
		status.Convert(err).Message())
}

// TestAWayInIsAskedOfRostersRolesAlone.
//
// *Nobody writes a way into an account wider than their own* is about the
// account, whose ways in are roster's, and it is asked of roster's roles alone:
// a role of an app's makes nobody wider and nobody narrower. Counted app by
// app, everybody who held any role of an app's was wider than a tenant's
// administrator who holds nothing there, and nobody could reset the password
// of somebody who merely uses an app -- refused; right is the reset. What is
// roster's still counts wherever it is held: a caller who holds less at roster
// is refused as before, whatever either holds at an app, and an app's own
// holder is wider than a caller who does not hold its role at roster.
func TestAWayInIsAskedOfRostersRolesAlone(t *testing.T) {
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	b.binds(t, loft, b.role(t, ctx, "loft-app", "/roster.HolderService/Reaches"), nil)

	staff := b.holder(t, ctx, b.Contoso, "staff")
	b.binds(t, staff, b.roleAtApp(t, ctx, loft, "loft-staff", "/loft.*/*", "/roster.HolderService/List"), nil)

	issue := func(as context.Context, who pdid.Id) error {
		_, err := b.operated().Issue(as, app.CredentialIssueRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())

		return err
	}

	t.Run("a caller who holds no less at roster writes it, whatever the person holds at an app", func(t *testing.T) {
		x := require.New(t)

		ops := b.holder(t, ctx, b.Contoso, "ops")
		x.NoError(issue(b.mayCall(t, ctx, ops, "operator", "/roster.*/*"), staff),
			"somebody who uses an app was wider than a caller holding all of roster")
	})

	t.Run("and one who holds less at roster is refused, whatever either holds at an app", func(t *testing.T) {
		x := require.New(t)

		boss := b.holder(t, ctx, b.Contoso, "boss")
		b.binds(t, boss, b.role(t, ctx, "eraser", eraseHold), nil)
		b.binds(t, boss, b.roleAtApp(t, ctx, loft, "loft-boss", "/loft.BookingService/Cancel"), nil)

		helpdesk := b.holder(t, ctx, b.Contoso, "helpdesk")
		as := b.mayCall(t, ctx, helpdesk, "helpdesk", app.CredentialService_Issue_FullMethodName, getHolder)
		b.binds(t, helpdesk, b.roleAtApp(t, ctx, loft, "loft-helpdesk", "/loft.*/*", eraseHold), nil)

		err := issue(as, boss)
		x.Equal(codes.PermissionDenied, status.Code(err))
		x.Equal("holder: they hold "+eraseHold+" and you do not, so you may not write their credential", status.Convert(err).Message())

		// The app's own holder holds its role at roster, and is wider than
		// a caller who does not.
		err = issue(as, loft)
		x.Equal(codes.PermissionDenied, status.Code(err))
		x.Contains(status.Convert(err).Message(), "/roster.HolderService/Reaches")
	})
}

// TestTheRulesThatProtectATenantReadRostersRoles.
//
// A tenant is administered by whoever holds `/roster.*/*` at roster. A role
// of an app's naming it administers nothing here, so it neither counts as the
// tenant's last administration nor is taken for one: erasing its binding asks
// nothing about who is left, and the tenant's own administrator's binding is
// still the last one.
func TestTheRulesThatProtectATenantReadRostersRoles(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	admin := b.holder(t, ctx, b.Contoso, "admin")
	b.binds(t, admin, b.roleAtApp(t, ctx, loft, "loft-everything", "/roster.*/*", "/loft.*/*"), nil)

	// The tenant's administrator, whose binding of roster's `/roster.*/*` is
	// the tenant's only administration whatever an app's role names.
	bs, err := b.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
		Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: admin.Bytes()}.Build()}.Build()},
	}.Build())
	x.NoError(err)

	var theirs []byte
	for _, v := range bs.GetItems() {
		r, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Id: v.GetRole().GetId()}.Build(),
			Select: app.RoleSelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		if len(r.GetApp().GetId()) == 0 {
			theirs = v.GetId()
		}
	}
	x.NotNil(theirs, "the tenant's administrator has no binding of roster's")

	_, err = b.Walled.Binding().Erase(b.asAtApp(ctx, admin), app.BindingRef_builder{Id: theirs}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err),
		"a role of an app's was counted as administering the tenant, and its last administrator unbound themselves")

	m, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{
			Alias: z.Ptr("loft-everything"), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.True(slices.Contains(m.GetMethods(), "/roster.*/*"))
}

// TestAnAppsRoleIsNoManagersRole.
//
// A place in a group that carries a role makes its holder a manager of it, by
// roster's verbs on that group. A role of an app's is nothing at roster, and
// was taken as one: a manager of nobody, who read as one -- answered with the
// place; right is `InvalidArgument`, in the words a manager's role naming
// anything else is refused in.
func TestAnAppsRoleIsNoManagersRole(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	role := b.roleAtApp(t, ctx, loft, "loft-manager", app.GroupMembershipService_Add_FullMethodName)
	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "ops",
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
		Group:  app.GroupRef_builder{Id: g.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "erin").Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a role of an app's was held as a manager's")
	x.Contains(status.Convert(err).Message(), "a role held in a group is one of roster's")
	x.Contains(status.Convert(err).Message(), "@contoso/loft")
}

// TestAnAppsRoleTakesNoNameRosterKeeps.
//
// A role's alias is one within its tenant, whichever app's it is, and roster
// finds roles of its own by a handful of them. A role of an app's called
// `group-manager` stood where the tenant's managers' role is looked for, and
// the tenant could make no group after it: `AlreadyExists`, for a role nobody
// had asked to make. Each of those names is refused a role of an app's,
// `InvalidArgument`, saying it is roster's own; and a group is then made as
// before.
func TestAnAppsRoleTakesNoNameRosterKeeps(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	for _, alias := range core.Kept {
		_, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:   alias,
			Methods: []string{"/loft.*/*"},
			App:     app.HolderRef_builder{Id: loft.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%s: a role of an app's took a name roster keeps", alias)
	}
	_, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: core.Manages,
		Methods: []string{"/loft.*/*"}, App: app.HolderRef_builder{Id: loft.Bytes()}.Build(),
	}.Build())
	x.Equal(`alias: "group-manager" is roster's own: roster finds a role of its own by it, and this is a role of @contoso/loft's`,
		status.Convert(err).Message())

	maker := b.holder(t, ctx, b.Contoso, "maker")
	as := b.mayCall(t, ctx, maker, "group-maker", app.GroupService_Add_FullMethodName,
		app.GroupMembershipService_Add_FullMethodName, app.GroupMembershipService_Erase_FullMethodName,
		app.GroupService_Update_FullMethodName, app.GroupService_Erase_FullMethodName)
	_, err = b.Walled.Group().Add(as, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "first",
	}.Build())
	x.NoError(err, "the tenant made its first group")
}

// TestWhatEndsAnAppsHolderEndsItsRoles.
//
// A role of an app's names the methods of the holder the app is answered as.
// Erasing that holder left its roles, bound, naming the methods of somebody
// who is gone -- and a holder erased is an alias given back, which another may
// take. They are erased with it, in the same act, and what they handed out is
// gone as an erased role's is; a restore of the holder brings the holder back
// and not them.
func TestWhatEndsAnAppsHolderEndsItsRoles(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	role := b.roleAtApp(t, ctx, loft, "loft-staff", "/loft.*/*")
	other := b.roleAtApp(t, ctx, b.holder(t, ctx, b.Contoso, "levyan"), "levyan-staff", "/levyan.*/*")
	erin := b.holder(t, ctx, b.Contoso, "erin")
	b.binds(t, erin, role, nil)

	admin := b.holder(t, ctx, b.Contoso, "admin")
	v, err := b.Walled.Holder().Erase(b.asAtApp(ctx, admin), app.HolderRef_builder{Id: loft.Bytes()}.Build())
	x.NoError(err)
	x.True(v.GetErased())

	erased := func(r pdid.Id) bool {
		v, err := b.Ent.Role.Get(ctx, r.Uuid())
		x.NoError(err)

		return v.DateErased != nil
	}
	x.True(erased(role), "a role of the erased app's is still there")
	x.False(erased(other), "a role of another app's went with it")

	gs, err := cmd.Holding(b.Ent)(ctx, erin)
	x.NoError(err)
	for _, g := range gs {
		x.NotEqual(loft, g.App, "what the erased role handed out is still held")
	}

	x.NoError(forget.Restore(ctx, b.Ent, loft, audit.Contents{}, nil))
	x.True(erased(role), "a restore of the holder brought its roles back")
}

// TestNoRoadThatDestroysAHolderLeavesARoleOfRosters.
//
// A role's `app` is `ON DELETE SET NULL`, and a role whose app is NULL is
// roster's: a holder's row deleted under its roles would make each a role of
// roster's naming the app's methods. No road deletes a holder's row -- an
// erase is a stamp, a forget blanks the row, and a tenant destroyed blanks
// its people and deletes its roles -- and this holds the last of them: a
// tenant destroyed keeps no role of its own, of an app's or of roster's.
func TestNoRoadThatDestroysAHolderLeavesARoleOfRosters(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "loft")
	b.binds(t, b.holder(t, ctx, b.Contoso, "erin"), b.roleAtApp(t, ctx, loft, "loft-staff", "/loft.*/*"), nil)
	people, err := b.Ent.Holder.Query().Where(entholder.TenantId(b.Contoso.Uuid())).Count(ctx)
	x.NoError(err)

	closes(t, ctx, b.Ungated, b.Contoso)
	_, err = forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)

	n, err := b.Ent.Role.Query().Where(entrole.TenantId(b.Contoso.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "a destroyed tenant kept a role")
	left, err := b.Ent.Holder.Query().Where(entholder.TenantId(b.Contoso.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(people, left, "a holder's row was deleted, which a role's app is not kept against")
}

// granteeAt is [built.grantee] holding `rosters` at roster and `methods` at
// the app `of`, each through a role of its own: a role names one app's methods.
func (b *built) granteeAt(t *testing.T, ctx context.Context, alias string, of pdid.Id, rosters []string, methods ...string) pdid.Id {
	t.Helper()

	who := b.grantee(t, ctx, alias, rosters...)
	b.binds(t, who, b.roleAtApp(t, ctx, of, alias+"-at-app", methods...), nil)

	return who
}

// heldAtApp is somebody of the tenant `in`, bound a role of the app `of`
// naming `methods`, at the box: what a test served by a process of its own
// gives somebody at an app.
func heldAtApp(t *testing.T, s *cmd.Server, in, of, who []byte, alias string, methods ...string) []byte {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: in}.Build(),
		Alias:   alias,
		Methods: methods,
		App:     app.HolderRef_builder{Id: of}.Build(),
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who}.Build(),
	}.Build())
	x.NoError(err)

	return r.GetId()
}

// appIn is a holder of the tenant `in` an app is answered as, made at the box.
func appIn(t *testing.T, s *cmd.Server, in []byte, alias string) []byte {
	t.Helper()

	v, err := s.Ungated.Holder().Add(t.Context(), app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return v.GetId()
}
