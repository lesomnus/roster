package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// installedAs is a call of `method` made straight at a stack as somebody of
// `in`, as a port would have framed it and said which method it is
// (`cmd.Calling`), which is what the wall narrows by.
func installedAs(ctx context.Context, who, in pdid.Id, method string) context.Context {
	return cmd.Calling(frame.Into(ctx, frame.New(who, in, frame.Whole()).WithScope(frame.Only(in))), method)
}

// TestInstallingAnAppMakesItsAdministrators.
//
// `roster app install --administer` bound the people it named a role of the
// app's, which hands out the app's methods at the app -- and nothing of
// roster's: binding one of those roles, or putting somebody in the app's
// group, is roster's `BindingService/Add` and `GroupMembershipService/Add`,
// which they did not hold, and the app's holder could not read its own roles
// unless `--allow` named the reads, and then every role of the tenant. It
// binds `app-administrator` for the app beside `NAME-everything`, to the app's
// holder and to each person named, and makes that role the first time it is
// needed. With nothing else bound to them they make the app's roles and
// groups, bind them, put somebody in -- named by alias -- and take them out,
// read the app's rows and no other, ask `Reaches` at the app and not at
// roster, and make a second administrator; the
// tenant's own are refused them. The app reads its own as itself. A second run
// changes nothing.
func TestInstallingAnAppMakesItsAdministrators(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	c.Client.As = "admin" // who `init` made, named at the box (#284)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()
	for _, alias := range []string{"alice", "bob", "erin"} {
		_, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: alias}.Build())
		x.NoError(err)
	}
	reader, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Alias: "reader", Methods: []string{getHolder}}.Build())
	x.NoError(err)
	employees, err := s.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{Tenant: at, Alias: "employees"}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	install := func() error {
		return cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme",
			"--allow", "/roster.HolderService/Reaches", "--serves", "/loft.*/*", "--administer", "alice", "loft"})
	}
	x.NoError(install())

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	tenant := mustId(t, acme.GetId())
	named := func(alias string) pdid.Id {
		v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String(alias), Tenant: at}.Build()}.Build(),
			Select: app.HolderSelect_builder{}.Build(),
		}.Build())
		x.NoError(err)

		return mustId(t, v.GetId())
	}
	loft, alice, bob := named("loft"), named("alice"), named("bob")
	ofLoft := app.HolderRef_builder{Id: loft.Bytes()}.Build()
	erin := app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String("erin"), Tenant: at}.Build()}.Build()
	as := func(who pdid.Id, method string) context.Context { return installedAs(ctx, who, tenant, method) }

	t.Run("the role is made, naming what reaches an app's rows", func(t *testing.T) {
		x := require.New(t)

		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(core.AppAdministrator), Tenant: at}.Build()}.Build(),
			Select: app.RoleSelect_builder{Methods: proto.Bool(true), App: app.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(core.ForAnApp, r.GetMethods())
		x.Empty(r.GetApp().GetId(), "the role is roster's")
	})

	var viewer, users, place []byte
	t.Run("and whoever --administer named administers the app with nothing else", func(t *testing.T) {
		x := require.New(t)

		r, err := s.Walled.Role().Add(as(alice, app.RoleService_Add_FullMethodName), app.RoleAddRequest_builder{
			Tenant: at, Alias: "loft-viewer", Methods: []string{"/loft.AssetService/Get"}, App: ofLoft,
		}.Build())
		x.NoError(err, "a role of the app's")
		viewer = r.GetId()

		g, err := s.Walled.Group().Add(as(alice, app.GroupService_Add_FullMethodName), app.GroupAddRequest_builder{
			Tenant: at, Alias: "loft-users", App: ofLoft,
		}.Build())
		x.NoError(err, "a group of the app's")
		users = g.GetId()

		bind := as(alice, app.BindingService_Add_FullMethodName)
		_, err = s.Walled.Binding().Add(bind, app.BindingAddRequest_builder{
			Role: app.RoleRef_builder{Id: viewer}.Build(), Group: app.GroupRef_builder{Id: users}.Build(),
		}.Build())
		x.NoError(err, "the one bound to the other")
		_, err = s.Walled.Binding().Add(bind, app.BindingAddRequest_builder{Role: app.RoleRef_builder{Id: viewer}.Build(), Holder: erin}.Build())
		x.NoError(err, "and to a person, by alias")

		m, err := s.Walled.GroupMembership().Add(as(alice, app.GroupMembershipService_Add_FullMethodName), app.GroupMembershipAddRequest_builder{
			Group: app.GroupRef_builder{Id: users}.Build(), Holder: erin,
		}.Build())
		x.NoError(err, "somebody put in, by alias")
		place = m.GetId()

		rs, err := s.Walled.Role().List(as(alice, app.RoleService_List_FullMethodName), app.RoleListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		for _, v := range rs.GetItems() {
			x.Equal(loft.Bytes(), v.GetApp().GetId(), "a role listed is not the app's: %s", v.GetAlias())
		}
		gs, err := s.Walled.Group().List(as(alice, app.GroupService_List_FullMethodName), app.GroupListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		x.Equal([][]byte{users}, idsOf(gs.GetItems()))

		// And asks what somebody holds at the app.
		v, err := s.Walled.Holder().Reaches(as(alice, app.HolderService_Reaches_FullMethodName), app.HolderReachesRequest_builder{
			Ref: erin, App: ofLoft,
		}.Build())
		x.NoError(err, "Reaches at the app")
		x.Equal([]string{"/loft.AssetService/Get"}, v.GetMethods())
		_, err = s.Walled.Holder().Reaches(as(alice, app.HolderService_Reaches_FullMethodName), app.HolderReachesRequest_builder{
			Ref: erin, Roster: proto.Bool(true),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "Reaches at roster")

		_, err = s.Walled.GroupMembership().Erase(as(alice, app.GroupMembershipService_Erase_FullMethodName), app.GroupMembershipRef_builder{Id: place}.Build())
		x.NoError(err, "and taken out")

		// The tenant's own are not theirs.
		_, err = s.Walled.Binding().Add(bind, app.BindingAddRequest_builder{Role: app.RoleRef_builder{Id: reader.GetId()}.Build(), Holder: erin}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "a role of the tenant's bound")
		_, err = s.Walled.GroupMembership().Add(as(alice, app.GroupMembershipService_Add_FullMethodName), app.GroupMembershipAddRequest_builder{
			Group: app.GroupRef_builder{Id: employees.GetId()}.Build(), Holder: erin,
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "somebody put in a group of the tenant's")
	})

	t.Run("and makes a second", func(t *testing.T) {
		x := require.New(t)

		admin := app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(core.AppAdministrator), Tenant: at}.Build()}.Build()
		bind := as(alice, app.BindingService_Add_FullMethodName)
		_, err := s.Walled.Binding().Add(bind, app.BindingAddRequest_builder{
			Role: admin, Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(), App: ofLoft,
		}.Build())
		x.NoError(err)
		_, err = s.Walled.Binding().Add(bind, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(cli.EveryRole("loft")), Tenant: at}.Build()}.Build(),
			Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		_, err = s.Walled.Role().Add(as(bob, app.RoleService_Add_FullMethodName), app.RoleAddRequest_builder{
			Tenant: at, Alias: "loft-second", Methods: []string{"/loft.AssetService/Get"}, App: ofLoft,
		}.Build())
		x.NoError(err, "the second makes a role of the app's")
	})

	t.Run("and the app reads its own as itself", func(t *testing.T) {
		x := require.New(t)

		bs, err := s.Walled.Binding().List(as(loft, app.BindingService_List_FullMethodName), app.BindingListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		x.NotEmpty(bs.GetItems())
		gs, err := s.Walled.Group().List(as(loft, app.GroupService_List_FullMethodName), app.GroupListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		x.Equal([][]byte{users}, idsOf(gs.GetItems()), "the app read groups that are not its own")
	})

	t.Run("and a second run changes nothing", func(t *testing.T) {
		x := require.New(t)

		count := func() (int, int) {
			rs, err := s.Ungated.Role().List(ctx, app.RoleListRequest_builder{Size: 100}.Build())
			x.NoError(err)
			bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{Size: 100}.Build())
			x.NoError(err)

			return len(rs.GetItems()), len(bs.GetItems())
		}
		roles, bindings := count()
		x.NoError(s.Close())

		x.NoError(install())

		s, err = cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })
		r, b := count()
		x.Equal(roles, r, "a second run wrote a role")
		x.Equal(bindings, b, "a second run wrote a binding")
	})
}

// TestInstallingBindsNoAppRoleThatIsNotRosters.
//
// `install` leaves a `NAME-app` that is there as the tenant has it, and binds
// it to the app's holder -- and one that was a role of another app's was bound
// as it was: the app's holder handed whatever it names at that app, and none
// of what `--allow` says at roster. Refused before anything is written, in the
// words a `NAME-everything` that is not the app's is. So is an
// `app-administrator` of the tenant's naming more than reaches an app's rows,
// which a binding for the app would refuse after the holder and the roles
// were written.
func TestInstallingBindsNoAppRoleThatIsNotRosters(t *testing.T) {
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	require.NoError(t, err, "init: %s", out)
	c.Client.As = "admin" // who `init` made, named at the box (#284)

	s, err := cmd.Build(ctx, c)
	require.NoError(t, err)
	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	require.NoError(t, err)
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()
	levyan, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "levyan"}.Build())
	require.NoError(t, err)
	_, err = s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: at, Alias: cli.AppRole("loft"), Methods: []string{"/levyan.*/*"}, App: app.HolderRef_builder{Id: levyan.GetId()}.Build(),
	}.Build())
	require.NoError(t, err)

	beta, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	require.NoError(t, err)
	_, err = s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: beta.GetId()}.Build(), Alias: core.AppAdministrator,
		Methods: []string{app.RoleService_List_FullMethodName, getHolder},
	}.Build())
	require.NoError(t, err)
	require.NoError(t, s.Close())

	for tenant, refused := range map[string]string{
		"acme": "acme already has a role loft-app, and it is not roster's",
		"beta": "beta's role app-administrator names " + getHolder + ", and an app's administrators hold it for the app",
	} {
		t.Run(tenant, func(t *testing.T) {
			x := require.New(t)

			err := cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", tenant,
				"--allow", "/roster.HolderService/Reaches", "--serves", "/loft.*/*", "--administer", "admin", "loft"})
			x.ErrorContains(err, refused)

			s, err := cmd.Build(ctx, c)
			x.NoError(err)
			defer s.Close()
			_, err = s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
				Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
					Alias: proto.String("loft"), Tenant: app.TenantRef_builder{Alias: proto.String(tenant)}.Build(),
				}.Build()}.Build(),
			}.Build())
			x.Equal(codes.NotFound, status.Code(err), "a refused install made the app's holder")
		})
	}
}
