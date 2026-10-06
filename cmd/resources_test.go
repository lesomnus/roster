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
    config:
      password: false
      front_door: https://account.newco.example
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
	paths := declare(t, file)
	rs, err := cmd.ReadResources(paths)
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

	// The mark that says a file owns it, and which file, so that a refusal
	// from a port can say where the row is written.
	x.Equal(paths[0], got.GetLabels()[cmd.Declared])

	// And the tenant's own settings, which are how it signs in (#67): the
	// switch `Vouch.Verify` enforces, and the origin the user console sends a
	// browser to. Both written, both carrying presence.
	tn := tenantOf(t, ctx, b, "newco")
	x.True(tn.GetConfig().HasPassword())
	x.False(tn.GetConfig().GetPassword())
	x.False(tn.OffersPassword())
	x.Equal("https://account.newco.example", tn.GetConfig().GetFrontDoor())

	t.Run("a second run with the same file changes nothing", func(t *testing.T) {
		x := require.New(t)
		v, err := cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)
		x.Empty(v.Added)
		x.Empty(v.Changed)
		x.Len(v.Same, 4)

		// Nor binds the provisioner again: `Binding` has no unique index to
		// refuse a second, and a deployment's control plane was found holding
		// one per start.
		who, err := cmd.ControlHolder(ctx, b.Server.Control, "provisioner")
		x.NoError(err)
		bs, err := b.Server.Control.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(bs.GetItems(), 1, "a second run bound the provisioner twice")
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

	t.Run("a connection names its people by a claim, from a file too", func(t *testing.T) {
		x := require.New(t)
		with := func(claim string) (cmd.Applied, error) {
			t.Helper()
			rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/other/v2.0
    client_id: the-app
    scopes: [email, profile]
    secret_ref: env:ENTRA
`+claim))
			x.NoError(err)

			return cmd.ApplyResources(ctx, b.Server, rs, false)
		}

		v, err := with("    subject_claim: oid\n")
		x.NoError(err)
		x.Equal([]string{"@newco/entra"}, v.Changed)
		x.Equal("oid", connectionOf(t, ctx, b, "newco", "entra").GetSubjectClaim())

		_, err = with("    subject_claim: upn\n")
		x.Error(err, "a claim no front door reads")
		x.Equal("oid", connectionOf(t, ctx, b, "newco", "entra").GetSubjectClaim())

		// And back, which a tenant may not do and the file -- the
		// deployment -- may.
		v, err = with("")
		x.NoError(err)
		x.Equal([]string{"@newco/entra"}, v.Changed)
		x.Empty(connectionOf(t, ctx, b, "newco", "entra").GetSubjectClaim())
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

		// And a `config:` the file stopped mentioning is left as it was,
		// which is the same rule one level down: absent is *leave it*.
		tn := tenantOf(t, ctx, b, "newco")
		x.False(tn.OffersPassword(), "a setting was reset for not being mentioned")
		x.Equal("https://account.newco.example", tn.GetConfig().GetFrontDoor())
	})

	// `password` has presence on purpose -- *unset is yes* -- so the one thing
	// a file must not do is read an absent key as `false`. Field by field: a
	// file that mentions one setting leaves the other as it is, and a tenant
	// declared without the key keeps the credential roster holds itself.
	t.Run("a setting the file does not mention is left alone, and unset stays yes", func(t *testing.T) {
		x := require.New(t)
		rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Tenant
    alias: fresh
    name: Fresh
    config:
      front_door: https://account.fresh.example
`))
		x.NoError(err)
		_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)

		tn := tenantOf(t, ctx, b, "fresh")
		x.False(tn.GetConfig().HasPassword(), "an absent key was written as a decision")
		x.True(tn.OffersPassword(), "a tenant declared without the key lost its passwords")
		x.Equal("https://account.fresh.example", tn.GetConfig().GetFrontDoor())

		// The other way round: the switch alone, and the front door stays.
		rs, err = cmd.ReadResources(declare(t, `
resources:
  - kind: Tenant
    alias: fresh
    name: Fresh
    config:
      password: false
`))
		x.NoError(err)
		v, err := cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)
		x.Equal([]string{"@fresh"}, v.Changed)

		tn = tenantOf(t, ctx, b, "fresh")
		x.False(tn.OffersPassword())
		x.Equal("https://account.fresh.example", tn.GetConfig().GetFrontDoor(), "a setting was reset for not being mentioned")

		// And said again, it is the same.
		v, err = cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)
		x.Equal([]string{"@fresh"}, v.Same)
	})

	// How the tenant's profiles are filled is declared like the rest of its
	// settings, a key at a time -- the Slack reference included, which the
	// file is where it is written from.
	t.Run("a profile is declared field by field, its Slack reference too", func(t *testing.T) {
		x := require.New(t)
		apply := func(file string) {
			t.Helper()
			rs, err := cmd.ReadResources(declare(t, file))
			x.NoError(err)
			_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
			x.NoError(err)
		}

		apply(`
resources:
  - kind: Tenant
    alias: pictured
    name: Pictured
    config:
      profile:
        fill: true
        slack: env:PICTURED_SLACK
`)
		p := tenantOf(t, ctx, b, "pictured").GetConfig().GetProfile()
		x.True(p.GetFill())
		x.Equal("env:PICTURED_SLACK", p.GetSlackSecretRef())

		// Not mentioned, left alone.
		apply("resources:\n  - kind: Tenant\n    alias: pictured\n    name: Pictured\n    config:\n      password: false\n")
		p = tenantOf(t, ctx, b, "pictured").GetConfig().GetProfile()
		x.True(p.GetFill(), "fill was reset for not being mentioned")
		x.Equal("env:PICTURED_SLACK", p.GetSlackSecretRef())

		// Taken away, and only that.
		apply("resources:\n  - kind: Tenant\n    alias: pictured\n    name: Pictured\n    config:\n      profile:\n        slack: \"\"\n")
		p = tenantOf(t, ctx, b, "pictured").GetConfig().GetProfile()
		x.True(p.GetFill())
		x.Empty(p.GetSlackSecretRef())
	})

	// The one rule about a front door meets a file exactly as it meets a form,
	// because the file writes through the same verb the consoles call.
	t.Run("a front door is an origin and nothing more, from a file too", func(t *testing.T) {
		x := require.New(t)
		rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Tenant
    alias: fresh
    name: Fresh
    config:
      front_door: https://account.fresh.example/login
`))
		x.NoError(err)
		_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.ErrorContains(err, "front_door")
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

func tenantOf(t *testing.T, ctx context.Context, b *built, alias string) *app.Tenant {
	t.Helper()
	v, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Alias: proto.String(alias)}.Build(),
		Select: app.TenantSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	require.NoError(t, err)

	return v
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

	// Nor erased from a port: it came back at the next start, which is the same
	// futile edit. Turning it off is taking it out of the file and then erasing
	// it as the deployment -- a shell on the box, with no frame.
	_, err = b.Walled.Connection().Erase(as, ref)
	x.Equal(codes.FailedPrecondition, status.Code(err), "a declared row was erased from a port")
	_, err = b.Ungated.Connection().Erase(ctx, ref)
	x.NoError(err, "the deployment could not erase what it declared")

	// Every kind a file can declare, and not the one the rule was first written
	// for: `Tenant`, `Host` and `MailDomain` each have an `Update` a console
	// calls, and for a while only `Connection`'s looked at the label. A tenant's
	// is the one that matters most, since its settings are the security-relevant
	// half -- a password switch flipped in a console that the next start flips
	// back is exactly the Tuesday-and-Wednesday `declared.go` is about.
	t.Run("and so is every other kind a file declares", func(t *testing.T) {
		x := require.New(t)
		rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
  - kind: Host
    tenant: newco
    name: newco.example
  - kind: MailDomain
    tenant: newco
    name: newco.example
`))
		x.NoError(err)
		_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)

		tn, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref:    app.TenantRef_builder{Alias: proto.String("newco")}.Build(),
			Select: app.TenantSelect_builder{All: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		newco := mustId(t, tn.GetId())

		// One of newco's own, holding everything, from a port.
		who := b.holder(t, ctx, newco, "someone")
		as := b.as(ctx, who, newco)

		_, err = b.Walled.Tenant().Update(as, app.TenantUpdateRequest_builder{
			Ref:         app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Name:        proto.String("Renamed"),
			Config:      app.TenantConfig_builder{Password: proto.Bool(false)}.Build(),
			DateUpdated: tn.GetDateUpdated(),
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "a declared tenant was edited from a port: %v", err)
		x.ErrorContains(err, "declared")

		h, err := b.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
			Ref:    app.HostRef_builder{Name: proto.String("newco.example")}.Build(),
			Select: app.HostSelect_builder{All: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = b.Walled.Host().Update(as, app.HostUpdateRequest_builder{
			Ref:         app.HostRef_builder{Id: h.GetId()}.Build(),
			Desc:        proto.String("edited"),
			DateUpdated: h.GetDateUpdated(),
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "a declared host was edited from a port: %v", err)

		m, err := b.Ungated.MailDomain().Get(ctx, app.MailDomainGetRequest_builder{
			Ref: app.MailDomainRef_builder{
				At: app.MailDomainRefByAt_builder{
					Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Name: proto.String("newco.example"),
				}.Build(),
			}.Build(),
			Select: app.MailDomainSelect_builder{All: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = b.Walled.MailDomain().Update(as, app.MailDomainUpdateRequest_builder{
			Ref:         app.MailDomainRef_builder{Id: m.GetId()}.Build(),
			Desc:        proto.String("edited"),
			DateUpdated: m.GetDateUpdated(),
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "a declared mail domain was edited from a port: %v", err)

		// And the file still writes all three, which is the point of the refusals.
		rs, err = cmd.ReadResources(declare(t, `
resources:
  - kind: Tenant
    alias: newco
    name: Newco Inc
  - kind: Host
    tenant: newco
    name: newco.example
    desc: the front door
  - kind: MailDomain
    tenant: newco
    name: newco.example
    desc: where the people are
`))
		x.NoError(err)
		v, err := cmd.ApplyResources(ctx, b.Server, rs, false)
		x.NoError(err)
		x.Len(v.Changed, 3, "%v", v)
	})

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
