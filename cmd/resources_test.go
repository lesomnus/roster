package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// Rows that are configuration, declared in a file.
//
// What is being pinned is the three rules `cmd/resources.go` argues for: it
// adds and updates and **never erases**, it writes as somebody the trail can
// name, and what it writes it owns.

func declare(t *testing.T, body string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "resources.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	return []string{p}
}

func TestAFileDeclaresRowsThatAreConfiguration(t *testing.T) {
	const file = `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/common/v2.0
    client_id: the-app
    scopes: [email, profile]
    secret_ref: env:ENTRA
  - kind: Host
    tenant: newco
    name: newco.example
  - kind: MailDomain
    tenant: newco
    name: newco.example
    routes: entra
`

	x := require.New(t)
	// A control plane, because the provisioner is a holder in it.
	cdrv, cdsn := pdtest.DB(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	// `build` migrates the plane it is about; the second one is this test's.
	x.NoError(entmigrate.NewSchema(b.Control.Drv).Create(ctx))
	rs, err := cmd.ReadResources(declare(t, file))
	x.NoError(err)
	x.Len(rs, 4)

	// A dry run says what it would do and writes none of it.
	v, err := cmd.ApplyResources(ctx, b.Server, rs, true)
	x.NoError(err)
	x.Len(v.Added, 4)
	_, err = b.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref: app.ConnectionRef_builder{
			At: app.ConnectionRefByAt_builder{
				Tenant: app.TenantRef_builder{Alias: proto.String("newco")}.Build(),
				Name:   proto.String("entra"),
			}.Build(),
		}.Build(),
		Select: app.ConnectionSelect_builder{}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a dry run wrote a row")

	// And then it does.
	v, err = cmd.ApplyResources(ctx, b.Server, rs, false)
	x.NoError(err)
	x.Len(v.Added, 4)

	got := connectionOf(t, ctx, b, "newco", "entra")
	x.Equal("https://login.microsoftonline.com/common/v2.0", got.GetIssuer())
	x.Equal("the-app", got.GetClientId())
	x.Equal([]string{"email", "profile"}, got.GetScopes())

	// The reference and never the secret: roster stores this string and never
	// reads it, which is what makes a `Connection` safe to declare at all.
	x.Equal("env:ENTRA", got.GetSecretRef())

	// The mark that says a file owns it.
	x.Contains(got.GetLabels(), cmd.Declared)

	t.Run("a second run with the same file changes nothing", func(t *testing.T) {
		x := require.New(t)
		v, err := cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)
		x.Empty(v.Added)
		x.Empty(v.Changed)
		x.Len(v.Same, 4)
	})

	t.Run("and an edited field is written", func(t *testing.T) {
		x := require.New(t)
		rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/other/v2.0
    client_id: the-app
    scopes: [email, profile]
    secret_ref: env:ENTRA
`))
		x.NoError(err)

		v, err := cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)
		x.Equal([]string{"@newco/entra"}, v.Changed)
		x.Equal("https://login.microsoftonline.com/other/v2.0",
			connectionOf(t, ctx, b, "newco", "entra").GetIssuer())
	})

	// The rule that reads as an omission and is a decision: `Connection.Update`
	// exists because erase-and-add on a provider orphans every identity through
	// it, and a reconciler that deleted what it no longer saw would do that on
	// a bad merge.
	t.Run("a resource dropped from the file is left where it is", func(t *testing.T) {
		x := require.New(t)
		rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
`))
		x.NoError(err)
		_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)

		x.NotNil(connectionOf(t, ctx, b, "newco", "entra"), "a row was erased for not being mentioned")
	})

	// A kind nobody implemented is named rather than skipped: it is a typo or a
	// thing somebody expected to work, and a silent skip is the worst answer.
	t.Run("an unknown kind is refused by name", func(t *testing.T) {
		x := require.New(t)
		rs, err := cmd.ReadResources(declare(t, "resources:\n  - kind: Holder\n    tenant: newco\n    alias: erin\n"))
		x.NoError(err)

		_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
		x.ErrorContains(err, "Holder")
	})
}

func connectionOf(t *testing.T, ctx context.Context, b *built, tenant, name string) *app.Connection {
	t.Helper()
	v, err := b.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref: app.ConnectionRef_builder{
			At: app.ConnectionRefByAt_builder{
				Tenant: app.TenantRef_builder{Alias: proto.String(tenant)}.Build(),
				Name:   proto.String(name),
			}.Build(),
		}.Build(),
		Select: app.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	require.NoError(t, err)

	return v
}

// TestARowAFileDeclaredIsWrittenFromThere: the third rule.
//
// A console edit to a declared row survives until the next restart and then
// vanishes, and the restart is a config change or a node draining -- neither of
// which looks related. Refused, the operator finds out immediately and the
// message says where the row lives.
func TestARowAFileDeclaredIsWrittenFromThere(t *testing.T) {
	x := require.New(t)

	cdrv, cdsn := pdtest.DB(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	x.NoError(entmigrate.NewSchema(b.Control.Drv).Create(ctx))

	rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Connection
    tenant: contoso
    name: entra
    issuer: https://login.microsoftonline.com/common/v2.0
    client_id: the-app
`))
	x.NoError(err)
	_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
	x.NoError(err)

	got := connectionOf(t, ctx, b, "contoso", "entra")
	ref := app.ConnectionRef_builder{Id: got.GetId()}.Build()

	// A caller from a port: resolved to a tenant, and narrowed to it.
	as := b.as(ctx, b.ContosoUser, b.Contoso)
	b.mayAnything(b.ContosoUser, b.Contoso)

	_, err = b.Walled.Connection().Update(as, app.ConnectionUpdateRequest_builder{
		Ref:         ref,
		Issuer:      proto.String("https://somewhere.else/"),
		DateUpdated: got.GetDateUpdated(),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "a declared row was edited from a port")
	x.ErrorContains(err, "declared")

	// And the row did not move.
	x.Equal("https://login.microsoftonline.com/common/v2.0",
		connectionOf(t, ctx, b, "contoso", "entra").GetIssuer())

	// The provisioner writes it, which is the whole point of the refusal above.
	rs, err = cmd.ReadResources(declare(t, `
resources:
  - kind: Connection
    tenant: contoso
    name: entra
    issuer: https://somewhere.else/
    client_id: the-app
`))
	x.NoError(err)
	_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
	x.NoError(err)
	x.Equal("https://somewhere.else/", connectionOf(t, ctx, b, "contoso", "entra").GetIssuer())

	// A row nobody declared is edited as it always was.
	t.Run("and a row nobody declared is untouched by this", func(t *testing.T) {
		x := require.New(t)
		made, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Name:   "by-hand", Issuer: "https://a/", ClientId: "b",
		}.Build())
		x.NoError(err)

		_, err = b.Walled.Connection().Update(as, app.ConnectionUpdateRequest_builder{
			Ref:         app.ConnectionRef_builder{Id: made.GetId()}.Build(),
			Issuer:      proto.String("https://c/"),
			DateUpdated: made.GetDateUpdated(),
		}.Build())
		x.NoError(err)
	})
}
