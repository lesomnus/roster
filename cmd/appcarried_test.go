package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// TestAProgramThatCarriesRosterInstallsAnAppInItsTenant is installing an app
// where roster is carried inside another program: a [cmd.Server] built from a
// configuration that names a database and no broker, so no control plane.
//
// Since apps hold their own roles (#567), such a program's operators are
// operators by a role of the app's, and it had no way to write that role: the
// install was unexported, and read `s.Control` for the app's key, which a
// deployment with no control plane does not have. So the tenant's half is done
// whole and the control plane's half is not attempted, and the install says so.
func TestAProgramThatCarriesRosterInstallsAnAppInItsTenant(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	// As shale builds it: a database, and no broker, since nothing watches
	// roster's rows from inside the program.
	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerNone},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.Nil(s.Control, "a deployment built from a database alone has a control plane")
	x.NoError(cli.Migrate(ctx, s))

	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()
	for _, alias := range []string{"alice", "erin"} {
		_, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: alias, Name: alias}.Build())
		x.NoError(err)
	}

	in := cli.AppInstall{
		Name:       "shale",
		Tenant:     "acme",
		Allow:      []string{"/roster.HolderService/Reaches"},
		Serves:     []string{"/shale.*/*"},
		Administer: []string{"alice"},
	}
	said, err := cli.InstallApp(ctx, s, in)
	x.NoError(err)

	noControl := "this deployment has no control plane, so nothing nominates shale in acme and no key is answered as @acme/shale."
	x.Contains(said, noControl)

	named := func(alias string) pdid.Id {
		v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String(alias), Tenant: at}.Build()}.Build(),
			Select: app.HolderSelect_builder{Labels: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		if alias == "shale" {
			x.Equal("shale", v.GetLabels()[app.LabelApp], "the app's holder does not say it is the app's")
		}

		return mustId(t, v.GetId())
	}
	shale, alice, erin := named("shale"), named("alice"), named("erin")

	reachesCarried := func(who pdid.Id) []string {
		v, err := s.Ungated.Holder().Reaches(ctx, app.HolderReachesRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), App: app.HolderRef_builder{Id: shale.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		return v.GetEverywhere()
	}

	t.Run("the app and whoever administers it hold its methods at it", func(t *testing.T) {
		x := require.New(t)

		x.Equal([]string{"/shale.*/*"}, reachesCarried(shale))
		x.Equal([]string{"/shale.*/*"}, reachesCarried(alice))
		x.Empty(reachesCarried(erin), "somebody bound nothing")
	})

	t.Run("and the app holds its role of roster's and roster's verbs for itself", func(t *testing.T) {
		x := require.New(t)

		for _, alias := range []string{cli.AppRole("shale"), cli.EveryRole("shale"), core.AppAdministrator} {
			_, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
				Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(alias), Tenant: at}.Build()}.Build(),
				Select: app.RoleSelect_builder{}.Build(),
			}.Build())
			x.NoError(err, "no role %s", alias)
		}
		bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: shale.Bytes()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(bs.GetItems(), 3)

		bs, err = s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(bs.GetItems(), 2, "the app's own role, and roster's verbs for the app")
	})

	t.Run("and no nomination is written", func(t *testing.T) {
		vs, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
		require.NoError(t, err)
		require.Empty(t, vs.GetItems())
	})

	t.Run("and a second run changes nothing and says so", func(t *testing.T) {
		x := require.New(t)

		again, err := cli.InstallApp(ctx, s, in)
		x.NoError(err)
		x.Contains(again, "acme already has role shale-app; left as the tenant has it.")
		x.Contains(again, "acme already has role shale-everything; left as the tenant has it.")
		x.Contains(again, "acme already has role app-administrator; left as the tenant has it.")
		x.Contains(again, noControl)

		for _, who := range []pdid.Id{shale, alice} {
			bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
				Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build()},
			}.Build())
			x.NoError(err)
			x.Len(bs.GetItems(), map[pdid.Id]int{shale: 3, alice: 2}[who], "a role was bound twice")
		}
	})

	t.Run("and refuses what the command refuses", func(t *testing.T) {
		x := require.New(t)

		_, err := cli.InstallApp(ctx, s, cli.AppInstall{Name: "shale", Tenant: "acme"})
		x.ErrorContains(err, "--allow")
		_, err = cli.InstallApp(ctx, s, cli.AppInstall{Name: "shale", Tenant: "acme", Allow: in.Allow, Administer: []string{"bob"}, Serves: in.Serves})
		x.ErrorContains(err, "--administer bob: nobody of acme is called that")
	})
}
