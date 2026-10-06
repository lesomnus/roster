package cmd_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// TestADirectorysKeyIsMintedForOneTenantAndOneConnection: `roster scim
// provision` marks the connection the tenant's directory provisions through,
// makes the directory's holder with a role holding exactly what it calls, and
// mints a tenant key -- replaced on every run, so a second run is a rotation.
// A connection a file declares is the file's to mark.
func TestADirectorysKeyIsMintedForOneTenantAndOneConnection(t *testing.T) {
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
	for _, v := range []*app.ConnectionAddRequest{
		app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/acme/v2.0", ClientId: "the-app",
			SubjectClaim: "oid",
		}.Build(),
		app.ConnectionAddRequest_builder{
			Tenant: at, Name: "okta", Issuer: "https://acme.okta.example", ClientId: "the-app",
			Labels: map[string]string{cmd.Declared: "resources.yaml"},
		}.Build(),
	} {
		_, err = s.Ungated.Connection().Add(ctx, v)
		x.NoError(err)
	}
	x.NoError(s.Close())

	provision := func(connection string) string {
		t.Helper()

		return strings.TrimSpace(stdoutOf(t, cli.NewCmdScim(&c), "provision", "--tenant", "acme", "--connection", connection))
	}
	keysOf := func(s *cmd.Server) []*app.ApiKey {
		t.Helper()
		h, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String(cli.ScimHolder), Tenant: at}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		vs, err := s.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{
			Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{Holder: app.HolderRef_builder{Id: h.GetId()}.Build()}.Build()},
		}.Build())
		x.NoError(err)

		return vs.GetItems()
	}

	first := provision("entra")
	x.True(strings.HasPrefix(first, "rt_"), "a tenant key, printed: %q", first)

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	conn, err := s.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref:    app.ConnectionRef_builder{At: app.ConnectionRefByAt_builder{Tenant: at, Name: proto.String("entra")}.Build()}.Build(),
		Select: app.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.True(conn.GetProvisions())

	role, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String(cli.ScimHolder), Tenant: at}.Build()}.Build(),
		Select: app.RoleSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.ElementsMatch(scim.Methods, role.GetMethods())
	for _, m := range []string{
		app.IdentityService_Add_FullMethodName, app.EmailService_Add_FullMethodName,
		app.EmailService_Attest_FullMethodName, app.HolderService_Add_FullMethodName, app.HolderService_Erase_FullMethodName,
	} {
		x.False(slices.Contains(role.GetMethods(), m), "the directory's role holds %s", m)
	}

	was := keysOf(s)
	x.Len(was, 1)
	x.ElementsMatch(scim.Methods, was[0].GetMethods())
	x.NoError(s.Close())

	second := provision("entra")
	x.NotEqual(first, second)

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	now := keysOf(s)
	x.Len(now, 1, "a second run added a key rather than replacing it")
	x.NotEqual(was[0].GetId(), now[0].GetId())

	err = cli.NewCmdScim(&c).Run(ctx, []string{"provision", "--tenant", "acme", "--connection", "okta"})
	x.ErrorContains(err, "declares")
}

// TestAnEndpointWithNoControlPlaneIsRefused: a directory signs in with a
// tenant key, and a deployment with no control plane reads none -- its
// callers are believed by name. So `scim:` without `control:` is a start-up
// failure, not an endpoint that answers whoever writes a name down.
func TestAnEndpointWithNoControlPlaneIsRefused(t *testing.T) {
	drv, dsn := pdtest.DB(t)
	_, err := cmd.Build(context.Background(), cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Scim:  cmd.ScimConfig{Addr: "127.0.0.1:0"},
	})
	require.ErrorContains(t, err, "scim:")
}
