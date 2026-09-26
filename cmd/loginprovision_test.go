package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// What `roster login provision` writes, and where the bound on it is.
//
// # What this was about, and what changed under it
//
// It was written for a defect with one sentence in it: *a delegation is the
// intersection of what the key allows and what the holder may do*, so a key and
// a role that disagree allow the narrower of the two. `enrol: enrolling` put
// `HolderService.Add` on the role and left it off the key, and the first person
// a directory vouched for reached the end of a whole sign-in and was refused at
// the one write that makes them somebody --
//
//	enrol …@…: PermissionDenied: /roster.HolderService/Add: this credential is not for that
//
// Nothing local saw it. `kustomize build` renders, `pd doctor` is about the
// schema, and the flow's own tests write with `Ungated`, which has no key.
//
// #36 moved where that bound is, and the assertion moved with it rather than
// being dropped. The app holds one **deployment** key now and narrows it per
// request with `roster-at`, and `keys.At` answers as the nominated holder with
// `frame.Whole()` -- the key's own method list is not carried through. So the two
// lists are deliberately **different**, and what each one has to be is what this
// pins:
//
//	the key's        the three reads made before the tenant is known
//	the role's       everything a call inside a flow does, widened by `enrolling`
//
// A key as wide as the role would be a key that could do all of it **without**
// naming a tenant, which is the wide frame `Host.acts_as` exists to take away.
func provisioned(t *testing.T, enrol string) (key, role []string) {
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
		Login:   cmd.LoginConfig{Enrol: enrol},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)

	// The name this tenant answers at, which is what `provision` walks: a
	// customer that registered one is a customer this app fronts (#42), so there
	// is no list of tenants anywhere for it to read.
	_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Name:   "contoso.example.com",
	}.Build())
	x.NoError(err)
	s.Close()

	x.NoError(cli.NewCmdLogin(&c).Run(ctx, []string{"provision", "--out", out}))

	// One file and not one per tenant, which is the whole of the change: an
	// `rk_` is the control plane's, so it needs no customer to exist and a first
	// start has no cycle to break.
	b, err := os.ReadFile(filepath.Join(out, "login-app.key"))
	x.NoError(err)
	x.True(strings.HasPrefix(strings.TrimSpace(string(b)), "rk_"),
		"the Login App's key is not a deployment key: %q", string(b))

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	return allowedByKey(t, ctx, s), allowedByRole(t, ctx, s, "contoso")
}

// allowedByKey is what the one deployment key allows, off the control plane.
func allowedByKey(t *testing.T, ctx context.Context, s *cmd.Server) []string {
	t.Helper()
	x := require.New(t)

	vs, err := s.Control.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1, "provision left more than one key, or none")

	return vs.GetItems()[0].GetMethods()
}

// allowedByRole is what the nominated holder may do, and that the name points at
// them.
func allowedByRole(t *testing.T, ctx context.Context, s *cmd.Server, tenant string) []string {
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

	// And the nomination, which is what makes any of it reachable: without it a
	// request carrying `roster-at` for this name is refused outright rather than
	// answered as the key (`server/keys/at.go`).
	host, err := s.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
		Ref:    app.HostRef_builder{Name: proto.String("contoso.example.com")}.Build(),
		Select: app.HostSelect_builder{ActsAs: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(who.GetId(), host.GetActsAs().GetId(),
		"the name does not borrow the holder provision wrote, so no flow can reach it")

	return role.GetMethods()
}

func TestTheProvisionedRoleAllowsWhatThePolicyAsksFor(t *testing.T) {
	const add = "/roster.HolderService/Add"

	t.Run("invited does not make people", func(t *testing.T) {
		x := require.New(t)

		_, role := provisioned(t, "")
		x.NotContains(role, add)

		_, role = provisioned(t, "invited")
		x.NotContains(role, add)
	})

	// `expected` matches somebody an operator entered and makes nobody, so it
	// needs no more than the base list either.
	t.Run("and neither does expected", func(t *testing.T) {
		_, role := provisioned(t, "expected")
		require.NotContains(t, role, add)
	})

	// The one line that widens it, and the defect this file was written for.
	t.Run("enrolling does, because it has to", func(t *testing.T) {
		_, role := provisioned(t, "enrolling")
		require.Contains(t, role, add)
	})
}

// TestTheProvisionedKeyIsNarrowerThanTheRole is the bound that replaced *the two
// lists must match*.
//
// The key may make the three reads that work out whose flow this is, and nothing
// else. Everything a flow actually does is the nominated holder's role, reached
// only by a request that **names a tenant** -- so a key as wide as the role would
// be one that could do all of it with the wide frame `Host.acts_as` exists to
// take away.
func TestTheProvisionedKeyIsNarrowerThanTheRole(t *testing.T) {
	x := require.New(t)

	key, role := provisioned(t, "enrolling")

	x.ElementsMatch(cli.LoginResolving, key)
	x.NotContains(key, "/roster.HolderService/Add")
	x.NotContains(key, "/roster.VouchService/Verify",
		"the key can verify a password without naming a tenant, which is the frame this removes")

	// And the role is the wider of the two, which is the direction that has to
	// hold: it is reached only through a nomination.
	x.Greater(len(role), len(key))
}

// TestProvisionMigratesBothPlanes is the first start of a fresh deployment, and
// it is here because CI found it and nothing local did.
//
// This command is an **init container**: it runs before the server has opened
// either database, on a volume with no tables. `ready` migrated the data plane,
// which was the whole of what the per-tenant keys touched -- and #36 put the key
// on a control-plane holder, so the first start failed in the init container on
// `no such table: tenant`, the pod never became ready, and what the rig reported
// was `timed out waiting for the condition` about a Deployment.
//
// The other tests here create both schemas first, which is why they were green.
// This one deliberately does not.
func TestProvisionMigratesBothPlanes(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	out := t.TempDir()

	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true},
		},
	}

	// Nothing has created a table on either plane, which is the state this
	// command exists for.
	x.NoError(cli.NewCmdLogin(&c).Run(ctx, []string{"provision", "--out", out}))

	b, err := os.ReadFile(filepath.Join(out, "login-app.key"))
	x.NoError(err)
	x.True(strings.HasPrefix(strings.TrimSpace(string(b)), "rk_"))

	// And no names to nominate on, which is said and not refused: a fresh volume
	// has no customers, and refusing here would be a deployment that cannot come
	// up because it has not come up.
}
