package cmd_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// A declared tenant may say its identifier.
//
// A deployment made again -- a new instance, or after the compatibility epoch
// moved -- declares its tenant in the file `serve` applies at start, and the
// tenant was made under an identifier minted then. Whatever keeps roster's
// tenant by its identifier (an app that anchors its rows on it, a policy file,
// a job configured with it) had to be anchored again. `roster tenant add`
// takes an `id`, but the file is applied before anybody can run a command, and
// the rows the file declares in the tenant need it there first.

// tenantIdFile is a file that declares @newco under `id`, and a connection and
// a host in it.
func tenantIdFile(id string) string {
	return fmt.Sprintf(`
resources:
  - kind: Tenant
    alias: newco
    id: %s
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/newco/v2.0
    client_id: the-app
  - kind: Host
    tenant: newco
    name: newco.example
`, id)
}

// tenantIdApplied reads `body` and applies it, a dry run where `dry`, and
// answers what the run answered.
func tenantIdApplied(t *testing.T, ctx context.Context, b *built, body string, dry bool) (cmd.Applied, error) {
	t.Helper()
	rs, err := cmd.ReadResources(declare(t, body))
	require.NoError(t, err)

	return cmd.ApplyResources(ctx, b.Server, rs, dry)
}

// TestADeclaredTenantIsMadeUnderTheIdentifierItSays: a tenant a file declares
// with an `id`, where no tenant of its alias is there, is made under that
// identifier, and the rows declared in it with it; the file applied again
// writes nothing. A tenant declared without one is minted one, as before.
//
// `id` was not a key a `Tenant` reads, and was refused where the file was read.
func TestADeclaredTenantIsMadeUnderTheIdentifierItSays(t *testing.T) {
	x := require.New(t)
	b := readBed(t)
	ctx := t.Context()
	id := pdid.New(pd.TenantDomain).String()

	// The dry run first: what the run does, and nothing written.
	was := rowsTrail(t, ctx, b)
	v, err := tenantIdApplied(t, ctx, b, tenantIdFile(id), true)
	x.NoError(err)
	x.Equal([]string{"@newco", "@newco/entra", "@newco/newco.example"}, v.Added)
	x.Equal(was, rowsTrail(t, ctx, b), "a dry run wrote")

	v, err = tenantIdApplied(t, ctx, b, tenantIdFile(id), false)
	x.NoError(err)
	x.Equal([]string{"@newco", "@newco/entra", "@newco/newco.example"}, v.Added)

	tn := tenantOf(t, ctx, b, "newco")
	x.Equal(id, pdid.Id(tn.GetId()).String())
	x.Equal(tn.GetId(), connectionOf(t, ctx, b, "newco", "entra").GetTenant().GetId())

	// Applied again, by a run and by a dry run, it is the same and writes
	// nothing.
	for _, dry := range []bool{true, false} {
		was := rowsTrail(t, ctx, b)
		v, err := tenantIdApplied(t, ctx, b, tenantIdFile(id), dry)
		x.NoError(err)
		x.Empty(v.Added, "dry %v", dry)
		x.Empty(v.Changed, "dry %v", dry)
		x.Len(v.Same, 3, "dry %v", dry)
		x.Equal(was, rowsTrail(t, ctx, b), "dry %v: applied again, it wrote", dry)
	}

	t.Run("a tenant declared without one is minted one", func(t *testing.T) {
		x := require.New(t)
		v, err := tenantIdApplied(t, ctx, b, "resources:\n  - kind: Tenant\n    alias: plain\n", false)
		x.NoError(err)
		x.Equal([]string{"@plain"}, v.Added)
		got := pdid.Id(tenantOf(t, ctx, b, "plain").GetId())
		x.Equal(pd.TenantDomain, got.Domain())
		x.NotEqual(id, got.String())
	})
}

// TestADeclaredTenantsIdentifierIsNotChanged: a file that says an identifier
// for a tenant that is there under another, or that is another tenant's, is
// refused at that row, by a run and by a dry run alike, naming the alias and
// both identifiers, and nothing is written.
//
// A tenant's identifier is what everything that keeps it keeps, so a file does
// not move one; and the identifier said of a tenant that is not there yet is
// not taken from another.
func TestADeclaredTenantsIdentifierIsNotChanged(t *testing.T) {
	x := require.New(t)
	b := readBed(t)
	ctx := t.Context()

	_, err := tenantIdApplied(t, ctx, b, "resources:\n  - kind: Tenant\n    alias: newco\n  - kind: Tenant\n    alias: other\n", false)
	x.NoError(err)
	newco := pdid.Id(tenantOf(t, ctx, b, "newco").GetId()).String()
	other := pdid.Id(tenantOf(t, ctx, b, "other").GetId()).String()
	said := pdid.New(pd.TenantDomain).String()

	for _, v := range []struct {
		what, file, says string
	}{
		{
			"the alias under another identifier",
			tenantIdFile(said),
			fmt.Sprintf("Tenant @newco: @newco is %s and the file says %s; a tenant's identifier is not changed", newco, said),
		},
		{
			"the identifier another tenant's",
			strings.Replace(tenantIdFile(other), "alias: newco", "alias: fresh", 1),
			fmt.Sprintf("Tenant @fresh: %s is @other's and the file says it of @fresh; a tenant's identifier is not changed", other),
		},
	} {
		for _, dry := range []bool{true, false} {
			was := rowsTrail(t, ctx, b)
			_, err := tenantIdApplied(t, ctx, b, v.file, dry)
			x.ErrorContains(err, v.says, "%s, dry %v", v.what, dry)
			x.Equal(was, rowsTrail(t, ctx, b), "%s, dry %v: it wrote", v.what, dry)
		}
	}

	_, err = b.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
		Ref:    app.HostRef_builder{Name: proto.String("newco.example")}.Build(),
		Select: app.HostSelect_builder{}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a row after the refusal was written")

	// Two tenants the files declare under one identifier: the run would make
	// the first and refuse the second, and a dry run, which makes neither,
	// would promise both. Refused naming both, before anything is written.
	t.Run("two entries of one identifier", func(t *testing.T) {
		x := require.New(t)
		_, err := cmd.ReadResources(declare(t, fmt.Sprintf(
			"resources:\n  - kind: Tenant\n    alias: one\n    id: %s\n  - kind: Tenant\n    alias: two\n    id: %s\n", said, said)))
		x.ErrorContains(err, "Tenant @two: ")
		x.ErrorContains(err, fmt.Sprintf("declares %s for @one, and one identifier is one tenant's", said))
	})
}

// TestADeclaredTenantsIdentifierIsOneRosterMakes: an `id` that is not a
// tenant's identifier as roster prints one is refused where the file is read,
// by its file, line and key, as a key of the wrong form is.
func TestADeclaredTenantsIdentifierIsOneRosterMakes(t *testing.T) {
	for _, v := range []struct{ what, id string }{
		{"not a UUID", "contoso"},
		{"the nil one", "00000000-0000-0000-0000-000000000000"},
		{"a UUID roster does not make", "6ba7b810-9dad-41d1-80b4-00c04fd430c8"},
		{"a holder's", pdid.New(pd.HolderDomain).String()},
		{"not as roster prints it", strings.ToUpper(pdid.New(pd.TenantDomain).String())},
		{"empty", `""`},
	} {
		t.Run(v.what, func(t *testing.T) {
			x := require.New(t)
			paths := declare(t, tenantIdFile(v.id))
			_, err := cmd.ReadResources(paths)
			x.ErrorContains(err, paths[0]+":5:5: resources[0].id: ")
			x.ErrorContains(err, "is not a tenant's identifier")
		})
	}
}
