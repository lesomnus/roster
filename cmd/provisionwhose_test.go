package cmd_test

import (
	"testing"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli/cfg"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// TestAProvisionerWritesNothingOnSomebody: a provisioner that finds a person
// of its app's name, and declines to answer the app as them, leaves them as it
// found them.
//
// # What it was
//
// `roster login provision` and `roster account provision` make the app's
// holder in each tenant by name, and a tenant may already have somebody of
// that name. Somebody who holds a role or a way in is said to be *already
// somebody's* and the tenant is skipped, rather than answering the app as
// them. The label was written **before** that was decided: finding the holder
// and stamping `roster.declared` on it were one call.
//
// A declared row is written from the box and from nowhere else. So the person
// the provisioner had just declined to touch was out of their own
// administrator's reach from then on -- not erased, not suspended, not
// edited, *this row is declared in config: account* -- and kept everything
// they held (#276).
//
// `roster scim provision` had no such check at all: a person called `scim` was
// handed the directory's role and a key was minted that acts as them.
func TestAProvisionerWritesNothingOnSomebody(t *testing.T) {
	ctx := t.Context()

	bed := func(t *testing.T) (cmd.Config, *app.TenantRef) {
		t.Helper()
		x := require.New(t)

		drv, dsn := pdtest.DB(t)
		cdrv, cdsn := pdtest.DB(t)
		c := cmd.Config{
			Db:      config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
			Watch:   config.WatchConfig{Broker: config.BrokerMemory},
			Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true}},
		}
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
		x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
		tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
		x.NoError(err)
		at := app.TenantRef_builder{Id: tn.GetId()}.Build()
		_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: "contoso.example"}.Build())
		x.NoError(err)
		x.NoError(s.Close())

		return c, at
	}

	// somebody is a person of the tenant who is already called what an app
	// will be, and holds a role of their own.
	somebody := func(t *testing.T, c cmd.Config, at *app.TenantRef, alias string) []byte {
		t.Helper()
		x := require.New(t)

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()

		who, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: alias}.Build())
		x.NoError(err)
		role, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant: at, Alias: "reads", Methods: []string{"/roster.HolderService/List"},
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: who.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		return who.GetId()
	}

	// untouched is that they are as they were: no label, no more than they
	// held, no key, and their administrator's to erase.
	untouched := func(t *testing.T, c cmd.Config, at *app.TenantRef, who []byte) {
		t.Helper()
		x := require.New(t)

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()

		v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who}.Build(),
			Select: app.HolderSelect_builder{Labels: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Empty(v.GetLabels()[cmd.Declared], "somebody the provisioner declined to use was marked as the configuration's")

		bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(bs.GetItems(), 1, "they were bound what the app is")

		ks, err := s.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{
			Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{Holder: app.HolderRef_builder{Id: who}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Empty(ks.GetItems(), "a key was minted that acts as them")

		ns, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
		x.NoError(err)
		x.Empty(ns.GetItems(), "an app is answered as them")

		// Their administrator, at a port.
		admin, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "boss"}.Build())
		x.NoError(err)
		everything, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Alias: "boss", Methods: []string{"/roster.*/*"}}.Build())
		x.NoError(err)
		_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: everything.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: admin.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		tenant := cmd.MustFrom(at.GetId())
		as := frame.Into(ctx, frame.New(cmd.MustFrom(admin.GetId()), tenant, frame.Whole()).WithScope(frame.Only(tenant)))

		_, err = s.Walled.Holder().Erase(as, app.HolderRef_builder{Id: who}.Build())
		x.NoError(err, "their own administrator may not erase them")
	}

	t.Run("the Login App", func(t *testing.T) {
		c, at := bed(t)
		who := somebody(t, c, at, "login-app")

		require.NoError(t, cli.NewCmdLogin(cfg.New(cmd.Name, &c), &c).Run(ctx, []string{"provision", "--out", t.TempDir()}))
		untouched(t, c, at, who)
	})

	t.Run("the account app", func(t *testing.T) {
		c, at := bed(t)
		who := somebody(t, c, at, "account")

		require.NoError(t, cli.NewCmdAccount(cfg.New(cmd.Name, &c), &c).Run(ctx, []string{"provision", "--out", t.TempDir()}))
		untouched(t, c, at, who)
	})

	t.Run("a directory", func(t *testing.T) {
		x := require.New(t)
		c, at := bed(t)
		who := somebody(t, c, at, cli.ScimHolder)

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		_, err = s.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
			SubjectClaim: "oid",
		}.Build())
		x.NoError(err)
		x.NoError(s.Close())

		// Refused, and said: this is one tenant asked for by name at a
		// terminal, not an init container walking every tenant.
		err = cli.NewCmdScim(cfg.New(cmd.Name, &c), &c).Run(ctx, []string{"provision", "--tenant", "contoso", "--connection", "entra"})
		x.Error(err)
		x.ErrorContains(err, "already somebody's")

		untouched(t, c, at, who)
	})
}
