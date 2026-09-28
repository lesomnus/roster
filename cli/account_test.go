package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// deployment is roster with a control plane, built and migrated, the way a
// process builds it.
func deployment(t *testing.T) *cmd.Server {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	seed := make([]byte, 32)
	_, err := rand.Read(seed)
	x.NoError(err)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Vouch:   cmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(seed)}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))

	return s
}

func tenantCalled(t *testing.T, s *cmd.Server, alias string) []byte {
	t.Helper()

	v, err := s.Ungated.Tenant().Add(t.Context(), rstr.TenantAddRequest_builder{Alias: alias}.Build())
	require.NoError(t, err)

	return v.GetId()
}

func answersAt(t *testing.T, s *cmd.Server, tenant []byte, name string) {
	t.Helper()

	_, err := s.Ungated.Host().Add(t.Context(), rstr.HostAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: tenant}.Build(),
		Name:   name,
	}.Build())
	require.NoError(t, err)
}

// served is the data plane on a listener that is a channel, dialed: what a
// front door reaches over the wire, keys and wall included.
func served(t *testing.T, s *cmd.Server) *grpc.ClientConn {
	t.Helper()

	g, err := s.Grpc(t.Context(), cmd.Config{})
	require.NoError(t, err)

	return pdtest.Serve(t, g)
}

func bearing(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// TestAFrontDoorsKeysAreMadeWhereTheyAreUsed is `roster account provision`,
// and the half of it `roster serve` runs at start when `account.keys` says
// nothing.
//
// A front door's key was a runbook step: a holder, a role with the right
// thirty methods, a binding, `roster key add`, and the token carried into the
// app's environment -- per tenant, by hand, `docker/customer.sh` being the
// written-down version. `roster login provision` replaced that for the Login
// App; this is the same four rows for the account app, and what each of them
// has to be for the key to be the app's and nobody wider's.
func TestAFrontDoorsKeysAreMadeWhereTheyAreUsed(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	s := deployment(t)

	// contoso has a name and fabrikam does not, which is the difference
	// between a tenant a front door fronts and one nobody can reach.
	contoso := tenantCalled(t, s, "contoso")
	tenantCalled(t, s, "fabrikam")
	answersAt(t, s, contoso, "contoso.example")

	made, err := provisionAccount(ctx, s, "", "")
	x.NoError(err)
	x.Len(made, 1, "a tenant with no name was fronted: %v", made)
	key := made["contoso"]
	x.True(strings.HasPrefix(key, keys.PrefixTenant), "not a tenant key: %q", key)

	conn := served(t, s)
	tc := rstr.NewTenantServiceClient(conn)

	t.Run("the key is contoso's, and reaches contoso alone", func(t *testing.T) {
		x := require.New(t)

		got, err := tc.Get(bearing(ctx, key), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: z.Ptr("contoso")}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("contoso", got.GetAlias())

		_, err = tc.Get(bearing(ctx, key), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: z.Ptr("fabrikam")}.Build(),
		}.Build())
		x.Error(err, "contoso's front door read fabrikam")
	})

	t.Run("and holds what the app calls as itself", func(t *testing.T) {
		x := require.New(t)

		// `Accept` is the grant `roster key add` warns about, and the one a
		// front door cannot do without. A claim reaching nobody is `NotFound`;
		// `PermissionDenied` would be the key not holding it.
		_, err := rstr.NewVouchServiceClient(conn).Accept(bearing(ctx, key), rstr.VouchAcceptRequest_builder{
			Claim:   rstr.VouchClaim_builder{Tenant: contoso, Provider: "entra", Subject: "nobody"}.Build(),
			Methods: []string{rstr.MeService_Get_FullMethodName},
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "%v", err)

		// And not what it does not: making people is `enrolling`'s grant.
		_, err = rstr.NewHolderServiceClient(conn).Add(bearing(ctx, key), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: contoso}.Build(), Alias: "somebody",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and a second run is a rotation", func(t *testing.T) {
		x := require.New(t)

		again, err := provisionAccount(ctx, s, "", "")
		x.NoError(err)
		x.NotEqual(key, again["contoso"])

		_, err = tc.Get(bearing(ctx, key), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: z.Ptr("contoso")}.Build(),
		}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "the key from the last run still answers")

		_, err = tc.Get(bearing(ctx, again["contoso"]), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: z.Ptr("contoso")}.Build(),
		}.Build())
		x.NoError(err)
	})

	t.Run("and enrolling widens the role by one method", func(t *testing.T) {
		x := require.New(t)

		made, err := provisionAccount(ctx, s, "enrolling", "")
		x.NoError(err)

		_, err = rstr.NewHolderServiceClient(conn).Add(bearing(ctx, made["contoso"]), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: contoso}.Build(), Alias: "somebody",
		}.Build())
		x.NoError(err, "a deployment that wrote `enrolling` down cannot make people")

		// Back to the default, which the next run writes over the role
		// rather than leaving the wider list behind.
		made, err = provisionAccount(ctx, s, "", "")
		x.NoError(err)
		_, err = rstr.NewHolderServiceClient(conn).Add(bearing(ctx, made["contoso"]), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: contoso}.Build(), Alias: "somebody-else",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "the grant outlived the setting")
	})

	t.Run("and into a file, for a process of its own to read", func(t *testing.T) {
		x := require.New(t)
		dir := filepath.Join(t.TempDir(), "keys")

		made, err := provisionAccount(ctx, s, "", dir)
		x.NoError(err)

		path := filepath.Join(dir, "contoso.key")
		b, err := os.ReadFile(path)
		x.NoError(err)
		x.Equal(made["contoso"], strings.TrimSpace(string(b)))

		st, err := os.Stat(path)
		x.NoError(err)
		x.Equal(os.FileMode(0o600), st.Mode().Perm(), "a credential readable by anybody on the box")

		// And the reference a deployment writes resolves to it.
		got, err := keysOf(map[string]string{"contoso": "file:" + path}, AccountKeyPrefix, nil)
		x.NoError(err)
		x.Equal(made["contoso"], got["contoso"])
	})
}
