package cmd_test

import (
	"context"
	"slices"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/forget"
)

// roster's verbs, held for one app (`Binding.app`, #567).
//
// An app's administrator holds roster's verbs over that app's roles, the
// bindings of them, its groups and their memberships, and nothing else of the
// tenant's: a role of roster's bound for the app. It fails closed twice: at
// the gate, such a grant counts for those verbs and no other; and in the
// layer and the wall, each of them reaches that app's rows alone.

// forApp binds a role of roster's to somebody for the app `of`, at the box.
func (b *built) forApp(t *testing.T, ctx context.Context, who, role, of pdid.Id) {
	t.Helper()

	_, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		App:    app.HolderRef_builder{Id: of.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
}

// administers is the tenant's `app-administrator`, naming what reaches an
// app's rows, made at the box.
func (b *built) administers(t *testing.T, ctx context.Context) pdid.Id {
	t.Helper()

	return b.role(t, ctx, core.AppAdministrator, core.ForAnApp...)
}

// appsWorld is two apps of Contoso's and rows of each, and of the tenant's.
type appsWorld struct {
	foo, bar   pdid.Id
	admin      pdid.Id // the role of roster's bound for an app
	everything pdid.Id // Foo's everything, a role of Foo's

	// Bar's, and the tenant's own.
	barRole, barGroup, barBinding, barPlace pdid.Id
	reader, employees, readerBinding        pdid.Id
}

func (b *built) appsWorld(t *testing.T, ctx context.Context) appsWorld {
	t.Helper()
	x := require.New(t)

	w := appsWorld{
		foo: b.holder(t, ctx, b.Contoso, "foo"),
		bar: b.holder(t, ctx, b.Contoso, "bar"),
	}
	w.admin = b.administers(t, ctx)
	w.everything = b.roleAtApp(t, ctx, w.foo, "foo-everything", "/foo.*/*")

	someone := b.holder(t, ctx, b.Contoso, "someone-else")
	w.barRole = b.roleAtApp(t, ctx, w.bar, "bar-staff", "/bar.*/*")
	w.barGroup = b.appGroup(t, ctx, w.bar, "bar-users")
	v, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: w.barRole.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: someone.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	w.barBinding = mustId(t, v.GetId())
	w.barPlace = b.putsIn(t, ctx, someone, w.barGroup)

	w.reader = b.role(t, ctx, "reader", getHolder)
	w.employees = b.appGroup(t, ctx, pdid.Nil, "employees")
	v, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: w.reader.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: someone.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	w.readerBinding = mustId(t, v.GetId())

	return w
}

// fooAdmin is somebody bound `app-administrator` for Foo and Foo's
// everything, and nothing else.
func (b *built) fooAdmin(t *testing.T, ctx context.Context, w appsWorld, alias string) pdid.Id {
	t.Helper()

	who := b.holder(t, ctx, b.Contoso, alias)
	b.forApp(t, ctx, who, w.admin, w.foo)
	b.binds(t, who, w.everything, nil)

	return who
}

// rostersClients is roster's four services an app's administrator calls.
type rostersClients struct {
	roles    app.RoleServiceClient
	bindings app.BindingServiceClient
	groups   app.GroupServiceClient
	members  app.GroupMembershipServiceClient
}

func clientsOf(conn grpc.ClientConnInterface) rostersClients {
	return rostersClients{
		roles:    app.NewRoleServiceClient(conn),
		bindings: app.NewBindingServiceClient(conn),
		groups:   app.NewGroupServiceClient(conn),
		members:  app.NewGroupMembershipServiceClient(conn),
	}
}

func idsOf[T interface{ GetId() []byte }](vs []T) [][]byte {
	out := make([][]byte, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.GetId())
	}

	return out
}

func bytesIn(vs [][]byte, v []byte) bool {
	return slices.ContainsFunc(vs, func(b []byte) bool { return string(b) == string(v) })
}

// TestAnAppsAdministratorHoldsRostersVerbsForThatAppAlone.
//
// Nobody but an administrator of the whole tenant could write a binding, and
// what they wrote reached every role, group and person of it. An app's
// administrator is bound `app-administrator` for Foo, and Foo's everything:
// they make a role and a group of Foo's, bind the one to the other and to a
// person, put somebody in and take them out, and list Foo's roles, bindings,
// groups and members and nothing of Bar's or the tenant's -- each of which
// answered every row of the tenant to somebody holding the verb. The same acts
// at Bar, and on the tenant's own roles and groups, are refused,
// `PermissionDenied`, naming the app; a row that is not Foo's is not there to
// a read. And they call no other method of roster's by it: the gate counts it
// for those verbs alone, even once the role is widened past them.
func TestAnAppsAdministratorHoldsRostersVerbsForThatAppAlone(t *testing.T) {
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	alice := b.fooAdmin(t, ctx, w, "alice")
	erin := b.holder(t, ctx, b.Contoso, "erin")

	conn := served(t, b.Server)
	c := clientsOf(conn)
	as := asOverTheWire(ctx, alice)
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	foo := app.HolderRef_builder{Id: w.foo.Bytes()}.Build()
	bar := app.HolderRef_builder{Id: w.bar.Bytes()}.Build()
	// erin is named by her alias: what naming somebody takes (#567, decided 5).
	byAlias := app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: z.Ptr("erin"), Tenant: tenant}.Build()}.Build()

	var viewer, users, toGroup, toErin, place []byte
	t.Run("at Foo, they make, bind, put in and take out", func(t *testing.T) {
		x := require.New(t)

		r, err := c.roles.Add(as, app.RoleAddRequest_builder{
			Tenant: tenant, Alias: "foo-viewer", Methods: []string{"/foo.AssetService/Get"}, App: foo,
		}.Build())
		x.NoError(err, "a role of Foo's")
		viewer = r.GetId()

		g, err := c.groups.Add(as, app.GroupAddRequest_builder{Tenant: tenant, Alias: "foo-users", App: foo}.Build())
		x.NoError(err, "a group of Foo's")
		users = g.GetId()

		v, err := c.bindings.Add(as, app.BindingAddRequest_builder{
			Role: app.RoleRef_builder{Id: viewer}.Build(), Group: app.GroupRef_builder{Id: users}.Build(),
		}.Build())
		x.NoError(err, "a role of Foo's bound to a group of Foo's")
		toGroup = v.GetId()

		v, err = c.bindings.Add(as, app.BindingAddRequest_builder{Role: app.RoleRef_builder{Id: viewer}.Build(), Holder: byAlias}.Build())
		x.NoError(err, "a role of Foo's bound to a person")
		toErin = v.GetId()

		m, err := c.members.Add(as, app.GroupMembershipAddRequest_builder{
			Group: app.GroupRef_builder{Id: users}.Build(), Holder: byAlias,
		}.Build())
		x.NoError(err, "somebody put in a group of Foo's")
		place = m.GetId()

		_, err = c.roles.Widen(as, app.RoleWidenRequest_builder{
			Ref: app.RoleRef_builder{Id: viewer}.Build(), Methods: []string{"/foo.AssetService/List"},
		}.Build())
		x.NoError(err)
		_, err = c.roles.Narrow(as, app.RoleNarrowRequest_builder{
			Ref: app.RoleRef_builder{Id: viewer}.Build(), Methods: []string{"/foo.AssetService/List"},
		}.Build())
		x.NoError(err)
	})

	t.Run("and read Foo's and nothing else", func(t *testing.T) {
		x := require.New(t)

		rs, err := c.roles.List(as, app.RoleListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		x.ElementsMatch([][]byte{w.everything.Bytes(), viewer}, idsOf(rs.GetItems()), "the roles listed are not Foo's")

		bs, err := c.bindings.List(as, app.BindingListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		got := idsOf(bs.GetItems())
		x.Contains(got, toGroup)
		x.Contains(got, toErin)
		x.False(bytesIn(got, w.barBinding.Bytes()), "a binding of Bar's was listed")
		x.False(bytesIn(got, w.readerBinding.Bytes()), "a binding of the tenant's was listed")
		for _, v := range bs.GetItems() {
			r, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
				Ref: app.RoleRef_builder{Id: v.GetRole().GetId()}.Build(), Select: app.RoleSelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
			}.Build())
			x.NoError(err)
			x.True(string(r.GetApp().GetId()) == string(w.foo.Bytes()) || string(v.GetApp().GetId()) == string(w.foo.Bytes()),
				"a binding listed is neither of a role of Foo's nor for Foo")
		}

		gs, err := c.groups.List(as, app.GroupListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		x.Equal([][]byte{users}, idsOf(gs.GetItems()), "the groups listed are not Foo's")

		ms, err := c.members.List(as, app.GroupMembershipListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		x.Contains(idsOf(ms.GetItems()), place)
		for _, m := range ms.GetItems() {
			x.Equal(users, m.GetGroup().GetId(), "a place in a group that is not Foo's was listed")
		}

		_, err = c.roles.Get(as, app.RoleGetRequest_builder{Ref: app.RoleRef_builder{Id: w.barRole.Bytes()}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a role of Bar's was read")
		_, err = c.roles.Get(as, app.RoleGetRequest_builder{Ref: app.RoleRef_builder{Id: w.reader.Bytes()}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a role of the tenant's was read")
		_, err = c.groups.Get(as, app.GroupGetRequest_builder{Ref: app.GroupRef_builder{Id: w.barGroup.Bytes()}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a group of Bar's was read")
		_, err = c.bindings.Get(as, app.BindingGetRequest_builder{Ref: app.BindingRef_builder{Id: w.readerBinding.Bytes()}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a binding of the tenant's was read")
		_, err = c.members.Get(as, app.GroupMembershipGetRequest_builder{Ref: app.GroupMembershipRef_builder{Id: w.barPlace.Bytes()}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a place in a group of Bar's was read")
	})

	t.Run("and are refused each at Bar and on the tenant's own", func(t *testing.T) {
		x := require.New(t)

		refused := func(what string, err error) {
			t.Helper()
			x.Equal(codes.PermissionDenied, status.Code(err), "%s: %v", what, err)
		}

		_, err := c.roles.Add(as, app.RoleAddRequest_builder{Tenant: tenant, Alias: "bar-viewer", Methods: []string{"/bar.AssetService/Get"}, App: bar}.Build())
		refused("a role of Bar's made", err)
		_, err = c.roles.Add(as, app.RoleAddRequest_builder{Tenant: tenant, Alias: "tenant-reader", Methods: []string{getHolder}}.Build())
		refused("a role of roster's made", err)
		x.Equal("app: you hold /roster.RoleService/Add for @contoso/foo, and this role is not @contoso/foo's", status.Convert(err).Message())

		_, err = c.groups.Add(as, app.GroupAddRequest_builder{Tenant: tenant, Alias: "bar-more", App: bar}.Build())
		refused("a group of Bar's made", err)
		_, err = c.groups.Add(as, app.GroupAddRequest_builder{Tenant: tenant, Alias: "tenant-more"}.Build())
		refused("a group of the tenant's made", err)

		for _, r := range []pdid.Id{w.barRole, w.reader} {
			_, err = c.bindings.Add(as, app.BindingAddRequest_builder{Role: app.RoleRef_builder{Id: r.Bytes()}.Build(), Holder: byAlias}.Build())
			refused("a role not Foo's bound", err)
			_, err = c.roles.Widen(as, app.RoleWidenRequest_builder{Ref: app.RoleRef_builder{Id: r.Bytes()}.Build(), Methods: []string{"/roster.HolderService/List"}}.Build())
			refused("a role not Foo's widened", err)
			_, err = c.roles.Narrow(as, app.RoleNarrowRequest_builder{Ref: app.RoleRef_builder{Id: r.Bytes()}.Build(), Methods: []string{getHolder}}.Build())
			refused("a role not Foo's narrowed", err)
			_, err = c.roles.Erase(as, app.RoleRef_builder{Id: r.Bytes()}.Build())
			refused("a role not Foo's erased", err)
		}
		for _, v := range []pdid.Id{w.barBinding, w.readerBinding} {
			_, err = c.bindings.Erase(as, app.BindingRef_builder{Id: v.Bytes()}.Build())
			refused("a binding not Foo's erased", err)
		}
		for _, g := range []pdid.Id{w.barGroup, w.employees} {
			_, err = c.members.Add(as, app.GroupMembershipAddRequest_builder{Group: app.GroupRef_builder{Id: g.Bytes()}.Build(), Holder: byAlias}.Build())
			refused("somebody put in a group not Foo's", err)
			_, err = c.groups.Erase(as, app.GroupRef_builder{Id: g.Bytes()}.Build())
			refused("a group not Foo's erased", err)
		}
		_, err = c.members.Erase(as, app.GroupMembershipRef_builder{Id: w.barPlace.Bytes()}.Build())
		refused("somebody taken out of a group of Bar's", err)
		x.Equal("/roster.GroupMembershipService/Erase: you hold it for @contoso/foo, and @contoso/bar-users is not @contoso/foo's", status.Convert(err).Message())
	})

	t.Run("and take Foo's away again", func(t *testing.T) {
		x := require.New(t)

		_, err := c.members.Erase(as, app.GroupMembershipRef_builder{Id: place}.Build())
		x.NoError(err, "somebody taken out of a group of Foo's")
		_, err = c.bindings.Erase(as, app.BindingRef_builder{Id: toErin}.Build())
		x.NoError(err, "a binding of Foo's role erased")
	})

	t.Run("and call nothing else of roster's by it, even widened", func(t *testing.T) {
		x := require.New(t)

		// What the role names when it is bound is held to the list; what it
		// names after is not counted. And with nothing of their own to call
		// either, so that what answers is the gate.
		b.withoutSelfService(t, ctx)
		_, err := b.Ungated.Role().Widen(ctx, app.RoleWidenRequest_builder{
			Ref: app.RoleRef_builder{Id: w.admin.Bytes()}.Build(), Methods: []string{core.EveryMethod},
		}.Build())
		x.NoError(err)

		_, err = app.NewHolderServiceClient(conn).Erase(as, app.HolderRef_builder{Id: erin.Bytes()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "HolderService/Erase by a grant for an app")
		x.Contains(status.Convert(err).Message(), "no role of yours allows that", "refused past the gate")
		_, err = app.NewApiKeyServiceClient(conn).Issue(as, app.ApiKeyIssueRequest_builder{
			Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Methods: []string{getHolder},
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "ApiKeyService/Issue by a grant for an app")
		x.Contains(status.Convert(err).Message(), "no role of yours allows that", "refused past the gate")
		_, err = app.NewGroupExposureServiceClient(conn).Add(as, app.GroupExposureAddRequest_builder{
			Group: app.GroupRef_builder{Id: users}.Build(), Client: "dtrack",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "GroupExposureService/Add by a grant for an app")
		x.Contains(status.Convert(err).Message(), "no role of yours allows that", "refused past the gate")
	})

	t.Run("and Reaches at Foo is answered them, at Bar not", func(t *testing.T) {
		x := require.New(t)

		// `HolderService/Reaches` is in what they hold for Foo, and is
		// answered at Foo with nothing else bound to them.
		holders := app.NewHolderServiceClient(conn)
		v, err := holders.Reaches(as, app.HolderReachesRequest_builder{
			Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(), App: foo,
		}.Build())
		x.NoError(err)
		// Foo's everything, and what Foo's group they made and are in is
		// bound.
		x.ElementsMatch([]string{"/foo.*/*", "/foo.AssetService/Get"}, v.GetMethods())

		_, err = holders.Reaches(as, app.HolderReachesRequest_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(), App: bar}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "Reaches at Bar")
		_, err = holders.Reaches(as, app.HolderReachesRequest_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Roster: z.Ptr(true)}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "Reaches at roster")
		x.Contains(status.Convert(err).Message(), "roster: what somebody may call at roster is answered to whoever holds")
		_, err = holders.Reaches(as, app.HolderReachesRequest_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "Reaches about themselves as an app, which is another")
	})
}

// TestAnAppsAdministratorMakesASecondAndNoWider.
//
// What somebody holds for one app they hand out for that app and no wider. So
// Foo's first administrator binds a second, who can then do the same -- where
// before only an administrator of the whole tenant wrote any binding -- and is
// refused binding the same role across the tenant, or for Bar,
// `PermissionDenied`. And taking it away is the mirror: they take away what
// they could have given.
func TestAnAppsAdministratorMakesASecondAndNoWider(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	alice := b.fooAdmin(t, ctx, w, "alice")
	bob := b.holder(t, ctx, b.Contoso, "bob")
	carol := b.holder(t, ctx, b.Contoso, "carol")

	c := clientsOf(served(t, b.Server))
	as := asOverTheWire(ctx, alice)
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	admin := app.RoleRef_builder{Id: w.admin.Bytes()}.Build()
	foo := app.HolderRef_builder{Id: w.foo.Bytes()}.Build()

	second, err := c.bindings.Add(as, app.BindingAddRequest_builder{
		Role: admin, Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(), App: foo,
	}.Build())
	x.NoError(err, "Foo's administrator made a second")
	_, err = c.bindings.Add(as, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: w.everything.Bytes()}.Build(), Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(),
	}.Build())
	x.NoError(err, "and gave them Foo's everything")

	_, err = c.roles.Add(asOverTheWire(ctx, bob), app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "foo-second", Methods: []string{"/foo.AssetService/Get"}, App: foo,
	}.Build())
	x.NoError(err, "the second makes a role of Foo's")

	_, err = c.bindings.Add(as, app.BindingAddRequest_builder{Role: admin, Holder: app.HolderRef_builder{Id: carol.Bytes()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "Foo's administrator made somebody an administrator across the tenant")
	_, err = c.bindings.Add(as, app.BindingAddRequest_builder{
		Role: admin, Holder: app.HolderRef_builder{Id: carol.Bytes()}.Build(), App: app.HolderRef_builder{Id: w.bar.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "Foo's administrator made somebody Bar's")

	_, err = c.bindings.Erase(as, app.BindingRef_builder{Id: second.GetId()}.Build())
	x.NoError(err, "and took the second's away")
}

// TestARoleForAnAppNamesOnlyWhatReachesAnAppsRows.
//
// A binding for an app is of a role of roster's that names those verbs and no
// other, as a manager's role names a manager's: refused otherwise,
// `InvalidArgument`, even at the box. It says an app or a site and not both,
// since the two narrow the same way; a binding of an app's own role says no
// app, since the role says it; and the app is a holder of the tenant's. One
// for Foo and one for Bar are two grants, and the same one twice is one.
func TestARoleForAnAppNamesOnlyWhatReachesAnAppsRows(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	erin := b.holder(t, ctx, b.Contoso, "erin")

	bind := func(role pdid.Id, of *app.HolderRef, site *pdid.Id) error {
		req := app.BindingAddRequest_builder{
			Role: app.RoleRef_builder{Id: role.Bytes()}.Build(), Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(), App: of,
		}
		if site != nil {
			req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
		}
		_, err := b.Ungated.Binding().Add(ctx, req.Build())

		return err
	}
	foo := app.HolderRef_builder{Id: w.foo.Bytes()}.Build()

	err := bind(b.role(t, ctx, "wider", app.RoleService_Add_FullMethodName, getHolder), foo, nil)
	x.Equal(codes.InvalidArgument, status.Code(err), "a role naming more than an app's rows, bound for an app")
	x.Contains(status.Convert(err).Message(), getHolder)

	err = bind(b.role(t, ctx, "patterned", "/roster.RoleService/*"), foo, nil)
	x.Equal(codes.InvalidArgument, status.Code(err), "a pattern reaching past an app's rows, bound for an app")

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	err = bind(w.admin, foo, &seoul)
	x.Equal(codes.InvalidArgument, status.Code(err), "a binding for an app and in a site")

	err = bind(w.everything, foo, nil)
	x.Equal(codes.InvalidArgument, status.Code(err), "a binding of an app's own role, for an app")

	err = bind(w.admin, app.HolderRef_builder{Id: b.holder(t, ctx, b.Fabrikam, "foo").Bytes()}.Build(), nil)
	x.Equal(codes.InvalidArgument, status.Code(err), "a binding for an app of another tenant's")

	x.NoError(bind(w.admin, foo, nil))
	x.NoError(bind(w.admin, app.HolderRef_builder{Id: w.bar.Bytes()}.Build(), nil), "for another app, another grant")
	x.NoError(bind(w.admin, nil, nil), "across the tenant, another grant")
	err = bind(w.admin, foo, nil)
	x.Equal(codes.AlreadyExists, status.Code(err), "the same grant twice")

	// One naming fewer is as good.
	x.NoError(bind(b.role(t, ctx, "foo-reader", app.RoleService_Get_FullMethodName, app.RoleService_List_FullMethodName), foo, nil))
}

// TestAnAppsAdministratorIsWiderThanAHelpdesk.
//
// *Nobody writes a way into an account wider than their own* reads what is
// held of roster's, and a verb held for one app is roster's: a helpdesk that
// may reset passwords and holds no such verb is refused Foo's administrator's,
// `PermissionDenied`, and an administrator of the tenant, who holds the verbs
// across it, is not.
func TestAnAppsAdministratorIsWiderThanAHelpdesk(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	alice := b.fooAdmin(t, ctx, w, "alice")

	issue := func(as context.Context) error {
		_, err := b.operated().Issue(as, app.CredentialIssueRequest_builder{
			Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		}.Build())

		return err
	}

	helpdesk := b.holder(t, ctx, b.Contoso, "helpdesk")
	err := issue(b.mayCall(t, ctx, helpdesk, "helpdesk", app.CredentialService_Issue_FullMethodName, getHolder))
	x.Equal(codes.PermissionDenied, status.Code(err), "a helpdesk wrote a way into an app's administrator's account")
	x.Contains(status.Convert(err).Message(), "/roster.")

	admin := b.holder(t, ctx, b.Contoso, "admin")
	x.NoError(issue(b.asAtApp(ctx, admin)), "the tenant's administrator was refused")
}

// TestARoleHeldForAnAppAdministersNothing.
//
// A tenant is administered by whoever holds `/roster.*/*` across it, and no
// write leaves it with nobody who does. A role of roster's bound for an app
// reaches that app's rows and nothing else, whatever it names by now: widened
// to everything and bound for Foo, it was counted as the tenant's
// administration, and its last administrator unbound themselves. Right is
// `FailedPrecondition`, as for anybody whose binding is the last.
func TestARoleHeldForAnAppAdministersNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	b.fooAdmin(t, ctx, w, "alice")

	_, err := b.Ungated.Role().Widen(ctx, app.RoleWidenRequest_builder{
		Ref: app.RoleRef_builder{Id: w.admin.Bytes()}.Build(), Methods: []string{core.EveryMethod},
	}.Build())
	x.NoError(err)

	admin := b.holder(t, ctx, b.Contoso, "admin")
	_, err = b.Walled.Binding().Erase(b.asAtApp(ctx, admin), b.bindingOf(t, ctx, admin))
	x.Equal(codes.FailedPrecondition, status.Code(err),
		"a binding for an app was counted as administering the tenant, and its last administrator unbound themselves")
}

// TestWhatIsHeldForAnAppIsSaidApart.
//
// What somebody is told they hold is what the gate would let through, a verb
// held for one app among it -- and it did not say that the verb reaches one
// app's rows alone: a page drew it as held across the tenant. `apps` says
// which apps, beside `sites` and in the same shape, and `everywhere`, which is
// what is held across the whole tenant, holds none of it.
func TestWhatIsHeldForAnAppIsSaidApart(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.forApp(t, ctx, alice, w.admin, w.foo)

	conn := served(t, b.Server)
	me, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, alice), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Subset(me.GetMethods(), core.ForAnApp, "what the gate lets through")
	x.Equal([][]byte{w.foo.Bytes()}, me.GetApps())
	x.False(me.GetEverySite())

	admin := b.holder(t, ctx, b.Contoso, "admin")
	v, err := app.NewHolderServiceClient(conn).Reaches(asOverTheWire(ctx, admin), app.HolderReachesRequest_builder{
		Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Roster: z.Ptr(true),
	}.Build())
	x.NoError(err)
	x.Subset(v.GetMethods(), core.ForAnApp)
	x.Empty(v.GetEverywhere(), "what is held for an app was said to be held across the tenant")
	x.Equal([][]byte{w.foo.Bytes()}, v.GetApps())
	x.False(v.GetEverySite())

	// At an app there is none.
	v, err = app.NewHolderServiceClient(conn).Reaches(asOverTheWire(ctx, admin), app.HolderReachesRequest_builder{
		Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(), App: app.HolderRef_builder{Id: w.foo.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Empty(v.GetApps())
}

// TestWhatEndsAnAppsHolderEndsTheBindingsForIt.
//
// Erasing an app's holder erased its roles and its groups, and left the
// bindings of roster's for it: `app-administrator` for an app that has ended,
// still listed and counted. They are erased in the same act, and a restore of
// the holder brings back none of the three; a binding for another app stays.
func TestWhatEndsAnAppsHolderEndsTheBindingsForIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.forApp(t, ctx, alice, w.admin, w.foo)
	b.forApp(t, ctx, alice, w.admin, w.bar)

	ofAlice := func() map[string]bool {
		vs, err := b.Ent.Binding.Query().All(ctx)
		x.NoError(err)
		out := map[string]bool{}
		for _, v := range vs {
			if v.HolderId == alice.Uuid() && v.DateErased == nil {
				out[v.AppId.String()] = true
			}
		}

		return out
	}
	x.True(ofAlice()[w.foo.Uuid().String()])

	admin := b.holder(t, ctx, b.Contoso, "admin")
	_, err := b.Walled.Holder().Erase(b.asAtApp(ctx, admin), app.HolderRef_builder{Id: w.foo.Bytes()}.Build())
	x.NoError(err)
	x.False(ofAlice()[w.foo.Uuid().String()], "a binding for the erased app is still there")
	x.True(ofAlice()[w.bar.Uuid().String()], "a binding for another app went with it")

	x.NoError(forget.Restore(ctx, b.Ent, w.foo, audit.Contents{}, nil))
	x.False(ofAlice()[w.foo.Uuid().String()], "a restore of the holder brought the binding for it back")
}
