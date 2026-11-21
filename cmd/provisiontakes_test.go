package cmd_test

import (
	"testing"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli/cfg"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// takesBed is a deployment of both planes with one tenant, contoso, that has a
// name, so that a front door's provisioner fronts it.
func takesBed(t *testing.T) (cmd.Config, *app.TenantRef) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true}},
	}
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: tn.GetId()}.Build()
	_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: "contoso.example"}.Build())
	x.NoError(err)

	return c, at
}

// takesProvision runs a front door's `provision`, and answers what it said on
// standard error.
func takesProvision(t *testing.T, c cmd.Config, app string) string {
	t.Helper()
	k := cli.NewCmdAccount(cfg.New(cmd.Name, &c), &c)
	if app == "login-app" {
		k = cli.NewCmdLogin(cfg.New(cmd.Name, &c), &c)
	}
	said, err := stderrOf(t, k, "provision", "--out", t.TempDir())
	require.NoError(t, err)

	return said
}

// TestAProvisionerTakesOnlyWhatItMade: a role or a holder of a front door's
// name that does not carry the provisioner's own label is the tenant's, bound
// or not: it is skipped and said on standard error, and left as it was.
//
// # What it was
//
// A role of the app's name was passed unless it was bound to others, and then
// patched to the app's methods and labelled declared, whatever it held. So a
// tenant's own role `account`, described and bound to nobody yet, became the
// account app's forty methods under the tenant's own description, read-only at
// every port from then on; and standard error said only the usual line. A
// holder of the name that held nothing was taken the same way. Whether a row is
// bound today is not whose it is: the label the provisioner writes on what it
// makes is.
func TestAProvisionerTakesOnlyWhatItMade(t *testing.T) {
	for _, alias := range []string{"account", "login-app"} {
		t.Run("a role of "+alias+"'s name", func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			c, at := takesBed(t)

			s, err := cmd.Build(ctx, c)
			x.NoError(err)
			role, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
				Tenant: at, Alias: alias, Desc: "the tenant's own", Methods: []string{"/roster.HolderService/Get"},
			}.Build())
			x.NoError(err)
			x.NoError(s.Close())

			said := takesProvision(t, c, alias)
			x.Contains(said, "@contoso/"+alias, "nothing said the tenant was skipped")

			s, err = cmd.Build(ctx, c)
			x.NoError(err)
			defer s.Close()
			got, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
				Ref:    app.RoleRef_builder{Id: role.GetId()}.Build(),
				Select: app.RoleSelect_builder{Methods: proto.Bool(true), Labels: proto.Bool(true)}.Build(),
			}.Build())
			x.NoError(err)
			x.Equal([]string{"/roster.HolderService/Get"}, got.GetMethods(), "the tenant's role was rewritten to the app's")
			x.Empty(got.GetLabels()[cmd.Declared], "the tenant's role was marked as the configuration's")
		})

		t.Run("a holder of "+alias+"'s name that holds nothing", func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			c, at := takesBed(t)

			s, err := cmd.Build(ctx, c)
			x.NoError(err)
			who, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: alias}.Build())
			x.NoError(err)
			x.NoError(s.Close())

			said := takesProvision(t, c, alias)
			x.Contains(said, "@contoso/"+alias, "nothing said the tenant was skipped")

			s, err = cmd.Build(ctx, c)
			x.NoError(err)
			defer s.Close()
			got, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
				Ref:    app.HolderRef_builder{Id: who.GetId()}.Build(),
				Select: app.HolderSelect_builder{Labels: proto.Bool(true)}.Build(),
			}.Build())
			x.NoError(err)
			x.Empty(got.GetLabels()[cmd.Declared], "the tenant's holder was marked as the configuration's")
			ns, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
			x.NoError(err)
			x.Empty(ns.GetItems(), "the app is answered as the tenant's holder")
		})
	}
}

// TestAProvisionerWritesARoleOnlyWhenItDiffers: a front door's provisioner run
// again with nothing changed writes no line on the data plane's trail.
//
// Each run patched the app's role in every tenant it fronts, as it should be
// or not -- a `/roster.RoleService/Patch` per tenant per start -- where the
// rest of what it ensures was already kept from being a row changed in every
// tenant at a restart. The key on the control plane is minted again at every
// run, which is the rotation it is for, and is not asked about here.
func TestAProvisionerWritesARoleOnlyWhenItDiffers(t *testing.T) {
	for _, alias := range []string{"account", "login-app"} {
		t.Run(alias, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			c, _ := takesBed(t)

			lines := func() int {
				s, err := cmd.Build(ctx, c)
				x.NoError(err)
				defer s.Close()
				n, err := s.Ent.Audit.Query().Count(ctx)
				x.NoError(err)

				return n
			}

			takesProvision(t, c, alias)
			was := lines()
			takesProvision(t, c, alias)
			x.Equal(was, lines(), "the second run wrote in the tenant")
		})
	}
}
