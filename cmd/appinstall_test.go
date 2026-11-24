package cmd_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

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
// nobody holds anything at an app that has just arrived (`Role.app`). So a
// roster operator installs it: the holder, its role of roster's, the binding,
// the nomination -- and the app's first grant at itself, a role of the app's
// naming what it serves, held by it and by the people named to administer it.
// The tenant's administrators' own role is not written to.
func TestInstallingAnAppIsItsRowsInATenantAndTheRightToManageThem(t *testing.T) {
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
	x.NoError(s.Close())

	token := stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--narrowed", "loft")

	install := func() error {
		return cli.NewCmdApp(&c).Run(ctx, []string{"install",
			"--tenant", "acme",
			"--allow", "/roster.HolderService/Reaches,/roster.SyncService/Watch",
			"--serves", "/loft.*/*",
			"--administer", "admin",
			"loft"})
	}
	x.NoError(install())

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()

	who, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String("loft"), Tenant: at}.Build()}.Build(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	x.NoError(err, "no holder for the app in the tenant")

	t.Run("its key is answered as that holder there", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewMeServiceClient(served(t, s)).Get(arrivedAt(ctx, token, front.AtTenant("acme")), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal(who.GetId(), v.GetId())

		// What `--allow` names, and roster's verbs over what is its own, which
		// it holds for itself (`app-administrator`).
		want := append([]string{"/roster.HolderService/Reaches", "/roster.SyncService/Watch"}, core.ForAnApp...)
		slices.Sort(want)
		x.Equal(slices.Compact(want), v.GetMethods())
	})

	t.Run("and the nomination says which app it is", func(t *testing.T) {
		x := require.New(t)

		vs, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		x.Equal("loft", vs.GetItems()[0].GetName())
	})

	t.Run("and the tenant's administrators' role is as it was", func(t *testing.T) {
		x := require.New(t)

		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(core.Everyverb), Tenant: at}.Build()}.Build(),
			Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal([]string{core.EveryMethod}, r.GetMethods())
	})

	// What the tenant decided afterwards stays decided.
	t.Run("and installing again changes nothing the tenant changed", func(t *testing.T) {
		x := require.New(t)

		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String("loft-app"), Tenant: at}.Build()}.Build(),
			Select: app.RoleSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Role().Patch(ctx, app.RolePatchRequest_builder{
			Ref:         app.RoleRef_builder{Id: r.GetId()}.Build(),
			Methods:     []string{"/roster.HolderService/Reaches"},
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
		x.Equal([]string{"/roster.HolderService/Reaches"}, got.GetMethods(), "installing again rewrote the tenant's choice")

		bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who.GetId()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(bs.GetItems(), 3, "installing again bound a role twice: the app holds its role of roster's, its own, and roster's verbs for itself")
	})

	t.Run("and uninstalling refuses its key there", func(t *testing.T) {
		x := require.New(t)

		x.NoError(cli.NewCmdApp(&c).Run(ctx, []string{"uninstall", "--tenant", "acme", "loft"}))

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		_, err = app.NewMeServiceClient(served(t, s)).Get(arrivedAt(ctx, token, front.AtTenant("acme")), app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("and an app that may do nothing is not installed", func(t *testing.T) {
		err := cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme", "loft"})
		require.ErrorContains(t, err, "--allow")
	})
}

// An app may name the role its people are given after itself -- Loft's staff
// in a role called `loft` -- and installing the app into a tenant that already
// uses it must not bind that role to the app's holder. It did: the role install
// made was called the app's name, and a role already there is adopted.
func TestInstallingAnAppLeavesARoleCalledAfterItAlone(t *testing.T) {
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
	staff, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: at, Alias: "loft", Methods: []string{"/roster.HolderService/Get"},
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	token := stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--narrowed", "loft")
	x.NoError(cli.NewCmdApp(&c).Run(ctx, []string{"install",
		"--tenant", "acme",
		"--allow", "/roster.HolderService/Reaches",
		"loft"}))

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
		Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(cli.AppRole("loft")), Tenant: at}.Build()}.Build(),
		Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal([]string{"/roster.HolderService/Reaches"}, r.GetMethods())
}

// TestInstallingAnAppTakesNoHolderThatIsSomebodys is a holder of the app's
// name that was already there.
//
// It was taken silently: the app's key was answered with whatever that holder
// held, and whoever signs in as it was handed the app's role. `install admin`
// answered the key as the tenant's administrator. Now it is refused unless it
// holds nothing but the app's own role, and `--adopt` says to take it anyway.
func TestInstallingAnAppTakesNoHolderThatIsSomebodys(t *testing.T) {
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

	// Somebody called loft, who holds the staff role.
	loft, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "loft"}.Build())
	x.NoError(err)
	staff, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Alias: "staff", Methods: []string{"/roster.HolderService/Get"}}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: staff.GetId()}.Build(), Holder: app.HolderRef_builder{Id: loft.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	install := func(args ...string) error {
		return cli.NewCmdApp(&c).Run(ctx, append([]string{"install", "--tenant", "acme", "--allow", "/roster.HolderService/Reaches"}, args...))
	}

	err = install("loft")
	x.ErrorContains(err, "--adopt")

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	vs, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(vs.GetItems(), "a refused install nominated somebody")
	_, err = cmd.ControlHolder(ctx, s.Control, "loft")
	x.Equal(codes.NotFound, status.Code(err), "a refused install made the app's control-plane holder")
	x.NoError(s.Close())

	x.NoError(install("--adopt", "loft"))

	t.Run("and a tenant or an app nobody has is refused, making nothing", func(t *testing.T) {
		x := require.New(t)

		x.Error(cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "nosuch", "--allow", "/roster.HolderService/Reaches", "levyan"}))
		x.Error(cli.NewCmdApp(&c).Run(ctx, []string{"uninstall", "--tenant", "nosuch", "loft"}))
		x.Error(cli.NewCmdApp(&c).Run(ctx, []string{"uninstall", "--tenant", "acme", "lfot"}))

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		for _, name := range []string{"levyan", "lfot"} {
			_, err = cmd.ControlHolder(ctx, s.Control, name)
			x.Equal(codes.NotFound, status.Code(err), "%s was made by a command that failed", name)
		}
	})
}

// TestAnAppIsInstalledWithItsOwnRoleAndWhoAdministersIt.
//
// What somebody holds is held at an app (`Role.app`), and `--administer` added
// the app's methods to the tenant's administrators' role -- a role of
// roster's, which hands out nothing at the app: they held `/loft.*/*` by name
// and could bind none of Loft's roles. `--serves` writes a role of the app's
// naming what it serves, bound to the app's holder and to the people
// `--administer` names, who then hold it at the app and hand it on there. A
// second run changes nothing; a name that is nobody of the tenant is refused
// with nothing written; and `--administer` without `--serves` is refused.
func TestAnAppIsInstalledWithItsOwnRoleAndWhoAdministersIt(t *testing.T) {
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
	for _, alias := range []string{"alice", "erin"} {
		_, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: alias}.Build())
		x.NoError(err)
	}
	x.NoError(s.Close())

	install := func(args ...string) error {
		return cli.NewCmdApp(&c).Run(ctx, append([]string{"install", "--tenant", "acme",
			"--allow", "/roster.HolderService/Reaches"}, args...))
	}

	t.Run("a name that is nobody is refused, and nothing is written", func(t *testing.T) {
		x := require.New(t)

		err := install("--serves", "/loft.*/*", "--administer", "alice,nobody", "loft")
		x.ErrorContains(err, "--administer nobody")

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		_, err = cmd.ControlHolder(ctx, s.Control, "loft")
		x.Equal(codes.NotFound, status.Code(err), "a refused install made the app's control-plane holder")
		_, err = s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String("loft"), Tenant: at}.Build()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a refused install made the app's holder")
	})

	t.Run("and --administer with nothing served is refused", func(t *testing.T) {
		require.ErrorContains(t, install("--administer", "alice", "loft"), "--serves")
	})

	x.NoError(install("--serves", "/loft.*/*", "--administer", "admin", "--administer", "alice", "loft"))

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	named := func(alias string) pdid.Id {
		v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String(alias), Tenant: at}.Build()}.Build(),
			Select: app.HolderSelect_builder{}.Build(),
		}.Build())
		x.NoError(err)

		return mustId(t, v.GetId())
	}
	loft, admin, alice, erin := named("loft"), named("admin"), named("alice"), named("erin")
	tenant := mustId(t, acme.GetId())

	reaches := func(who pdid.Id) []string {
		v, err := s.Ungated.Holder().Reaches(ctx, app.HolderReachesRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), App: app.HolderRef_builder{Id: loft.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		return v.GetEverywhere()
	}

	t.Run("the app and the people named hold its methods at it", func(t *testing.T) {
		x := require.New(t)

		for _, who := range []pdid.Id{loft, admin, alice} {
			x.Equal([]string{"/loft.*/*"}, reaches(who))
		}
		x.Empty(reaches(erin), "somebody not named")

		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref: app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(cli.EveryRole("loft")), Tenant: at}.Build()}.Build(),
			Select: app.RoleSelect_builder{
				Methods: proto.Bool(true), App: app.HolderSelect_builder{}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(loft.Bytes(), r.GetApp().GetId(), "the role is the app's")
	})

	t.Run("and hand them on there", func(t *testing.T) {
		x := require.New(t)

		as := framed(ctx, admin, tenant)
		v, err := s.Walled.Role().Add(as, app.RoleAddRequest_builder{
			Tenant: at, Alias: "loft-viewer", Methods: []string{"/loft.AssetService/Get"},
			App: app.HolderRef_builder{Id: loft.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		_, err = s.Walled.Binding().Add(as, app.BindingAddRequest_builder{
			Role: app.RoleRef_builder{Id: v.GetId()}.Build(), Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal([]string{"/loft.AssetService/Get"}, reaches(erin))
	})

	t.Run("and the tenant's administrators' role is as it was", func(t *testing.T) {
		x := require.New(t)

		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(core.Everyverb), Tenant: at}.Build()}.Build(),
			Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal([]string{core.EveryMethod}, r.GetMethods())
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

		x.NoError(install("--serves", "/loft.*/*", "--administer", "admin,alice", "loft"))

		s, err = cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })
		r, b := count()
		x.Equal(roles, r, "a second run wrote a role")
		x.Equal(bindings, b, "a second run wrote a binding")
	})
}

// TestInstallingBindsNoEveryRoleThatIsNotTheApps.
//
// `install --serves` leaves a role called `NAME-everything` that is there as
// the tenant has it, and binds it -- and one that was roster's, or another
// app's, was bound to the app's holder and to every person named: whatever it
// named, at roster or at that other app, handed out by a command that meant to
// hand out the app's own. Answered with the bindings; right is a refusal
// before anything is written.
func TestInstallingBindsNoEveryRoleThatIsNotTheApps(t *testing.T) {
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	require.NoError(t, err, "init: %s", out)
	c.Client.As = "admin" // who `init` made, named at the box (#284)

	s, err := cmd.Build(ctx, c)
	require.NoError(t, err)
	for alias, of := range map[string]string{"acme": "", "beta": "levyan"} {
		tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
		require.NoError(t, err)
		at := app.TenantRef_builder{Id: tn.GetId()}.Build()
		req := app.RoleAddRequest_builder{Tenant: at, Alias: cli.EveryRole("loft"), Methods: []string{"/roster.HolderService/*"}}
		if of != "" {
			h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: of}.Build())
			require.NoError(t, err)
			req.App = app.HolderRef_builder{Id: h.GetId()}.Build()
		}
		_, err = s.Ungated.Role().Add(ctx, req.Build())
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())

	for _, tenant := range []string{"acme", "beta"} {
		t.Run(tenant, func(t *testing.T) {
			x := require.New(t)

			err := cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", tenant,
				"--allow", "/roster.HolderService/Reaches", "--serves", "/loft.*/*", "--administer", "admin", "loft"})
			x.EqualError(err, tenant+" already has a role loft-everything, and it is not @"+tenant+"/loft's")

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
