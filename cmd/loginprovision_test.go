package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// What `roster login provision` mints, and what a policy widens.
//
// A delegation is the **intersection** of what the key allows and what the
// holder may do, so a key and a role that disagree allow the narrower of the
// two. This is here because they did: `enrol: enrolling` put
// `HolderService.Add` on the role and left it off the key, and the first person
// a directory vouched for reached the end of a whole sign-in and was refused at
// the one write that makes them somebody --
//
//	enrol …@…: PermissionDenied: /roster.HolderService/Add: this credential is not for that
//
// Nothing local saw it. `kustomize build` renders, `pd doctor` is about the
// schema, and the flow's own tests write with `Ungated`, which has no key.

func provisioned(t *testing.T, enrol string) []string {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	out := t.TempDir()

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Login: cmd.LoginConfig{
			Enrol:   enrol,
			Clients: map[string][]string{"contoso": {"contoso-web"}},
		},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	_, err = s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	s.Close()

	x.NoError(cli.NewCmdLogin(&c).Run(ctx, []string{"provision", "--out", out}))

	// The key the command wrote, read back for what it allows.
	b, err := os.ReadFile(filepath.Join(out, "contoso.key"))
	x.NoError(err)
	x.NotEmpty(b)

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	return allowedBy(t, ctx, s, "contoso")
}

// allowedBy is what the provisioned key allows, and what the provisioned role
// allows, as one list each -- which must agree, because a delegation is their
// intersection.
func allowedBy(t *testing.T, ctx context.Context, s *cmd.Server, tenant string) []string {
	t.Helper()
	x := require.New(t)

	at := app.TenantRef_builder{Alias: proto.String(tenant)}.Build()
	role, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{
			Slug: app.RoleRefBySlug_builder{Alias: proto.String("login-app"), Tenant: at}.Build(),
		}.Build(),
		Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)

	who, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{Alias: proto.String("login-app"), Tenant: at}.Build(),
		}.Build(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	x.NoError(err)

	keys, err := s.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{
		Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{
			Holder: app.HolderRef_builder{Id: who.GetId()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(keys.GetItems(), 1, "provision left more than one key, or none")

	// The two lists have to be the same, and that is the assertion rather than
	// a detail: what a delegation allows is the intersection, so a key narrower
	// than its role is a role that lied.
	x.ElementsMatch(role.GetMethods(), keys.GetItems()[0].GetMethods(),
		"the key and the role allow different things, so the narrower one wins silently")

	return role.GetMethods()
}

func TestTheProvisionedKeyAllowsWhatThePolicyAsksFor(t *testing.T) {
	const add = "/roster.HolderService/Add"

	t.Run("invited does not make people", func(t *testing.T) {
		x := require.New(t)
		x.NotContains(provisioned(t, ""), add)
		x.NotContains(provisioned(t, "invited"), add)
	})

	// `expected` matches somebody an operator entered and makes nobody, so it
	// needs no more than the base list either.
	t.Run("and neither does expected", func(t *testing.T) {
		require.NotContains(t, provisioned(t, "expected"), add)
	})

	// The one line that widens the key, and it widens **both**.
	t.Run("enrolling does, because it has to", func(t *testing.T) {
		require.Contains(t, provisioned(t, "enrolling"), add)
	})
}
