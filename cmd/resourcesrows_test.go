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

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// rowsTrail is how many lines each plane's trail holds, for a test that asks
// whether a run wrote anything.
func rowsTrail(t *testing.T, ctx context.Context, b *built) [2]int {
	t.Helper()
	data, err := b.Ent.Audit.Query().Count(ctx)
	require.NoError(t, err)
	control, err := b.Control.Ent.Audit.Query().Count(ctx)
	require.NoError(t, err)

	return [2]int{data, control}
}

// rowsApplied reads `body` as a file and applies it.
func rowsApplied(t *testing.T, ctx context.Context, b *built, body string) cmd.Applied {
	t.Helper()
	rs, err := cmd.ReadResources(declare(t, body))
	require.NoError(t, err)
	v, err := cmd.ApplyResources(ctx, b.Server, rs, false)
	require.NoError(t, err)

	return v
}

// TestAKeyLeftOutOfADeclaredRowIsEmpty: a declared row is what the file says,
// and a key the file leaves out is empty -- for every key but a tenant's
// `config`, whose keys left out leave the setting as it is.
//
// # What it was
//
// Three meanings. A tenant's absent `config` key left the setting, which the
// manual says; an absent `name`, `desc`, `secret_ref`, `subject_claim` or
// `routes` was written as empty, which no page said; and an absent `scopes`
// was compared as empty and patched as *not said*, so the row kept its scopes,
// was found changed, and was written again at every start.
//
// Empty, because a file that could not empty a field could not take back a
// scope it once asked for; and the one exception because `password` has
// presence on purpose, and a file that read its absence as `false` would turn
// off the one credential roster holds itself by not mentioning it.
func TestAKeyLeftOutOfADeclaredRowIsEmpty(t *testing.T) {
	x := require.New(t)
	b := readBed(t)
	ctx := t.Context()

	rowsApplied(t, ctx, b, `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
    desc: a customer
    config:
      password: false
  - kind: Connection
    tenant: newco
    name: entra
    desc: the directory
    issuer: https://login.microsoftonline.com/newco/v2.0
    client_id: the-app
    scopes: [openid, email]
    secret_ref: env:ENTRA
    subject_claim: oid
  - kind: MailDomain
    tenant: newco
    name: newco.example
    desc: mail
    routes: entra
`)

	v := rowsApplied(t, ctx, b, `
resources:
  - kind: Tenant
    alias: newco
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/newco/v2.0
    client_id: the-app
  - kind: MailDomain
    tenant: newco
    name: newco.example
`)
	x.Len(v.Changed, 3)

	tn := tenantOf(t, ctx, b, "newco")
	x.Empty(tn.GetName())
	x.Empty(tn.GetDesc())
	x.True(tn.GetConfig().HasPassword(), "config left out touched the settings")
	x.False(tn.GetConfig().GetPassword())

	cn := connectionOf(t, ctx, b, "newco", "entra")
	x.Empty(cn.GetDesc())
	x.Empty(cn.GetScopes(), "scopes left out were kept")
	x.Empty(cn.GetSecretRef())
	x.Empty(cn.GetSubjectClaim())

	md, err := b.Ungated.MailDomain().Get(ctx, app.MailDomainGetRequest_builder{
		Ref: app.MailDomainRef_builder{At: app.MailDomainRefByAt_builder{
			Tenant: app.TenantRef_builder{Alias: proto.String("newco")}.Build(), Name: proto.String("newco.example"),
		}.Build()}.Build(),
		Select: app.MailDomainSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Empty(md.GetDesc())
	x.Empty(md.GetProvider())

	t.Run("a row made by hand and then declared is the file's", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{
			Alias: "byhand", Name: "By Hand", Desc: "made at the box",
			Labels: map[string]string{"billing": "gold"},
		}.Build())
		x.NoError(err)

		rowsApplied(t, ctx, b, "resources:\n  - kind: Tenant\n    alias: byhand\n")
		tn := tenantOf(t, ctx, b, "byhand")
		x.Empty(tn.GetName())
		x.Empty(tn.GetDesc())
		x.NotContains(tn.GetLabels(), "billing")
		x.Len(tn.GetLabels(), 2)
		x.Contains(tn.GetLabels(), cmd.Declared)
		x.Contains(tn.GetLabels(), cmd.DeclaredConfig)
	})
}

// TestAFileAppliedTwiceWritesNothingTheSecondTime: a second run of a file
// nothing has changed writes no line on either plane's trail, for every kind
// and for a row whose file leaves a key out.
//
// A connection whose file did not say `scopes` was written at every start: its
// scopes were compared as empty and patched as not said, so they stayed and it
// was found changed again. A write at every start is a line on the trail per
// row per restart, and a `~` that tells somebody reading a dry run of nothing.
func TestAFileAppliedTwiceWritesNothingTheSecondTime(t *testing.T) {
	const file = `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
    config:
      password: false
      front_door: https://account.newco.example
      profile: { fill: true }
      leave: true
  - kind: Tenant
    alias: plain
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/newco/v2.0
    client_id: the-app
    scopes: [openid, email]
    secret_ref: env:ENTRA
    subject_claim: oid
  - kind: Connection
    tenant: newco
    name: bare
    issuer: https://idp.example.test
    client_id: another
  - kind: Host
    tenant: newco
    name: newco.example
  - kind: MailDomain
    tenant: newco
    name: newco.example
    routes: entra
`
	for _, tc := range []struct {
		name  string
		first string
	}{
		{"from nothing", ""},
		{
			// The case that was written at every start: scopes the file
			// once said and says no more.
			"after a file that said more",
			`
resources:
  - kind: Tenant
    alias: plain
    desc: said once
  - kind: Tenant
    alias: newco
  - kind: Connection
    tenant: newco
    name: bare
    issuer: https://idp.example.test
    client_id: another
    scopes: [openid]
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			b := readBed(t)
			ctx := t.Context()

			if tc.first != "" {
				rowsApplied(t, ctx, b, tc.first)
			}
			rowsApplied(t, ctx, b, file)

			was := rowsTrail(t, ctx, b)
			v := rowsApplied(t, ctx, b, file)
			x.Equal(was, rowsTrail(t, ctx, b), "the second run wrote: changed %v", v.Changed)
			x.Empty(v.Added)
			x.Empty(v.Changed)
			x.Len(v.Same, 6)
		})
	}
}

// TestTwoEntriesThatDeclareOneRowAreRefused: two entries that declare one row,
// in one file or in two, are refused naming both, before anything is written.
//
// They were applied in turn: `+ @contoso` and then `~ @contoso`, the second
// written over the first, and `~` twice at every start after -- two lines on
// the trail per restart, and a row that is whichever entry came last.
func TestTwoEntriesThatDeclareOneRowAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		one, two string
	}{
		{
			"a tenant twice in one file",
			"resources:\n  - kind: Tenant\n    alias: acme\n    name: One\n  - kind: Tenant\n    alias: acme\n    name: Two\n",
			"",
		},
		{
			"a connection in two files",
			"resources:\n  - kind: Tenant\n    alias: acme\n  - kind: Connection\n    tenant: acme\n    name: entra\n    issuer: https://idp.example.test\n    client_id: one\n",
			"resources:\n  - kind: Connection\n    tenant: acme\n    name: entra\n    issuer: https://idp.example.test\n    client_id: two\n",
		},
		{
			"a name under two tenants",
			"resources:\n  - kind: Tenant\n    alias: acme\n  - kind: Host\n    tenant: acme\n    name: acme.example\n",
			"resources:\n  - kind: Tenant\n    alias: other\n  - kind: Host\n    tenant: other\n    name: acme.example\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			b := readBed(t)
			ctx := t.Context()

			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "one.yaml")}
			x.NoError(os.WriteFile(paths[0], []byte(tc.one), 0o600))
			if tc.two != "" {
				paths = append(paths, filepath.Join(dir, "two.yaml"))
				x.NoError(os.WriteFile(paths[1], []byte(tc.two), 0o600))
			}

			_, err := cmd.ReadResources(paths)
			x.Error(err)
			x.ErrorContains(err, "declare one row")
			x.ErrorContains(err, paths[len(paths)-1], "the second is not named")
			x.ErrorContains(err, paths[0], "the first is not named")

			// And from a caller that hands the rows in, before anything is
			// written.
			rs := []cmd.Resource{{Kind: "Tenant", Alias: "acme"}, {Kind: "Tenant", Alias: "acme", Name: "Two"}}
			_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
			x.ErrorContains(err, "declare one row")
			_, err = b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
				Ref:    app.TenantRef_builder{Alias: proto.String("acme")}.Build(),
				Select: app.TenantSelect_builder{}.Build(),
			}.Build())
			x.Equal(codes.NotFound, status.Code(err), "the first was written")
		})
	}
}
