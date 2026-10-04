package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/front"
)

// TestInstallingAnAppIsItsRowsInATenantAndTheRightToManageThem is #75.
//
// A roster-hosted product acts in each tenant as a holder of that tenant's own,
// nominated for its key -- and nobody inside the tenant can set that up, because
// a tenant's first administrator is bound `/roster.*/*` and holds none of the
// app's methods to hand out. So a roster operator installs it: the holder, its
// role, the binding, the nomination, and the app's methods added to what the
// tenant's administrators may hand on.
func TestInstallingAnAppIsItsRowsInATenantAndTheRightToManageThem(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	token := stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--narrowed", "kamino")

	install := func() error {
		return cli.NewCmdApp(&c).Run(ctx, []string{"install",
			"--tenant", "acme",
			"--role", "/hday.oasys.RobotService/*",
			"--administer", "/hday.oasys.*/*",
			"kamino"})
	}
	x.NoError(install())

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()

	who, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String("kamino"), Tenant: at}.Build()}.Build(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	x.NoError(err, "no holder for the app in the tenant")

	t.Run("its key is answered as that holder there", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewMeServiceClient(served(t, s)).Get(arrivedAt(ctx, token, front.AtTenant("acme")), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal(who.GetId(), v.GetId())
		x.Equal([]string{"/hday.oasys.RobotService/*"}, v.GetMethods())
	})

	t.Run("and the nomination says which app it is", func(t *testing.T) {
		x := require.New(t)

		vs, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		x.Equal("kamino", vs.GetItems()[0].GetName())
	})

	t.Run("and the tenant's administrators may hand on the app's methods", func(t *testing.T) {
		x := require.New(t)

		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(core.Everyverb), Tenant: at}.Build()}.Build(),
			Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.ElementsMatch([]string{core.EveryMethod, "/hday.oasys.*/*"}, r.GetMethods())
	})

	// What the tenant decided afterwards stays decided.
	t.Run("and installing again changes nothing the tenant changed", func(t *testing.T) {
		x := require.New(t)

		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String("kamino-app"), Tenant: at}.Build()}.Build(),
			Select: app.RoleSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Role().Patch(ctx, app.RolePatchRequest_builder{
			Ref:         app.RoleRef_builder{Id: r.GetId()}.Build(),
			Methods:     []string{"/hday.oasys.RobotService/Get"},
			DateUpdated: r.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		x.NoError(s.Close())

		x.NoError(install())

		s, err = cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		got, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Id: r.GetId()}.Build(),
			Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal([]string{"/hday.oasys.RobotService/Get"}, got.GetMethods(), "installing again rewrote the tenant's choice")

		bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who.GetId()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(bs.GetItems(), 1, "installing again bound the role twice")
	})

	t.Run("and uninstalling refuses its key there", func(t *testing.T) {
		x := require.New(t)

		x.NoError(cli.NewCmdApp(&c).Run(ctx, []string{"uninstall", "--tenant", "acme", "kamino"}))

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		_, err = app.NewMeServiceClient(served(t, s)).Get(arrivedAt(ctx, token, front.AtTenant("acme")), app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("and an app that may do nothing is not installed", func(t *testing.T) {
		err := cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme", "kamino"})
		require.ErrorContains(t, err, "--role")
	})
}

// An app names the role its people are given after itself -- kamino's staff
// role is `kamino` -- and installing the app into a tenant that already uses it
// must not bind that role to the app's holder. It did: the role install made
// was called the app's name, and a role already there is adopted.
func TestInstallingAnAppLeavesARoleCalledAfterItAlone(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()
	staff, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: at, Alias: "kamino", Methods: []string{"/hday.oasys.*/*"},
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	token := stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--narrowed", "kamino")
	x.NoError(cli.NewCmdApp(&c).Run(ctx, []string{"install",
		"--tenant", "acme",
		"--role", "/roster.HolderService/Reaches",
		"kamino"}))

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	v, err := app.NewMeServiceClient(served(t, s)).Get(arrivedAt(ctx, token, front.AtTenant("acme")), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal([]string{"/roster.HolderService/Reaches"}, v.GetMethods(), "the app was answered with its staff role")

	bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
		Filters: []*app.BindingFilter{app.BindingFilter_builder{Role: app.RoleRef_builder{Id: staff.GetId()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	x.Empty(bs.GetItems(), "the staff role was bound to the app's holder")

	r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(cli.AppRole("kamino")), Tenant: at}.Build()}.Build(),
		Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal([]string{"/roster.HolderService/Reaches"}, r.GetMethods())
}
