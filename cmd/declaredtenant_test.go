package cmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestADeclaredTenantRefusesWhatItsFileSaysAndNoMore: a row a file declares
// refuses every write but the file's, because applying the file writes the
// whole row and a change made anywhere else is put back at the next start. A
// tenant's `config` is the one field it does not write whole: a key the entry
// leaves out is left as it is (#480), so nothing puts it back, and it is not
// the file's.
//
// `TenantService/Update` on a declared tenant was refused whole, for `desc`
// and for `config` alike -- `failed_precondition: ref: this row is declared in
// … and is written from there, not here` -- so the tenant's own administrator
// could change no setting the file never mentions, and only an operator at the
// box could. Right is a refusal of what the file says (its `name`, `desc`,
// `labels`, and each key of `config` the entry says, which the row records in
// [app.LabelDeclaredConfig]) and a write of the rest; a field sent back with
// the value it has is no change, since a page sends the whole object back.
func TestADeclaredTenantRefusesWhatItsFileSaysAndNoMore(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	path := filepath.Join(t.TempDir(), "resources.yaml")
	apply := func(body string) cmd.Applied {
		t.Helper()
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		rs, err := cmd.ReadResources([]string{path})
		require.NoError(t, err)
		got, err := cmd.ApplyResources(ctx, b.Server, rs, false)
		require.NoError(t, err)

		return got
	}
	const says = `
resources:
  - kind: Tenant
    alias: acme
    name: Acme
    config:
      password: false
`
	apply(says)

	ref := app.TenantRef_builder{Alias: z.Ptr("acme")}.Build()
	read := func() *app.Tenant {
		t.Helper()
		v, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref: ref, Select: app.TenantSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}
	x.Equal("password", read().GetLabels()[app.LabelDeclaredConfig], "the row does not record which keys of config the file says")

	// The tenant's own administrator, the one `Tenant.Add` made and bound to
	// everything, at a port.
	admin, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
			Tenant: ref, Alias: z.Ptr("admin"),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	as := bearing(ctx, mintFor(t, ctx, b, mustId(t, admin.GetId()), "theirs", []string{"/roster.*/*"}, time.Time{}))
	tenants := app.NewTenantServiceClient(b.Conn)
	update := func(req app.TenantUpdateRequest_builder) error {
		req.Ref, req.DateUpdated = ref, read().GetDateUpdated()
		_, err := tenants.Update(as, req.Build())

		return err
	}
	refused := func(err error, field string) {
		t.Helper()
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%s, which the file says, was changed at a port: %v", field, err)
		require.ErrorContains(t, err, field+": ")
		require.ErrorContains(t, err, path, "the refusal does not name the file")
	}

	t.Run("a key the file leaves out is the tenant's", func(t *testing.T) {
		x := require.New(t)
		x.NoError(update(app.TenantUpdateRequest_builder{
			Config: app.TenantConfig_builder{Password: z.Ptr(false), FrontDoor: "https://account.acme.example"}.Build(),
		}))
		x.Equal("https://account.acme.example", read().GetConfig().GetFrontDoor())
	})

	t.Run("a key the file says is not", func(t *testing.T) {
		x := require.New(t)
		refused(update(app.TenantUpdateRequest_builder{
			Config: app.TenantConfig_builder{Password: z.Ptr(true), FrontDoor: "https://account.acme.example"}.Build(),
		}), "config.password")
		x.False(read().GetConfig().GetPassword())
	})

	t.Run("and the whole row sent back, with a new front door, is written", func(t *testing.T) {
		x := require.New(t)
		x.NoError(update(app.TenantUpdateRequest_builder{
			Name: z.Ptr("Acme"), Desc: z.Ptr(""),
			Config: app.TenantConfig_builder{
				Password: z.Ptr(false), FrontDoor: "https://door.acme.example", Leave: false,
			}.Build(),
		}))
		x.Equal("https://door.acme.example", read().GetConfig().GetFrontDoor())
	})

	t.Run("its name and its note are the file's", func(t *testing.T) {
		x := require.New(t)
		refused(update(app.TenantUpdateRequest_builder{Name: z.Ptr("Renamed")}), "name")
		refused(update(app.TenantUpdateRequest_builder{Desc: z.Ptr("a note")}), "desc")
		x.Equal("Acme", read().GetName())
	})

	// The box writes a declared row because whoever is at the shell has the
	// file too: what the file says, the next start puts back.
	t.Run("the box writes what the file says, and the next start puts it back", func(t *testing.T) {
		x := require.New(t)
		c := b.Config
		out, err := entities(t, &c, "--as", "loft", "tenant", "update", "@acme", fmt.Sprintf(
			`{"date_updated":%q,"name":"By hand","config":{"password":true,"front_door":"https://door.acme.example"}}`,
			read().GetDateUpdated().AsTime().Format(time.RFC3339Nano)))
		x.NoError(err, out)
		x.True(read().GetConfig().GetPassword())
		x.Equal("By hand", read().GetName())

		apply(says)
		x.False(read().GetConfig().GetPassword())
		x.Equal("Acme", read().GetName())
	})

	t.Run("a key taken out of the file is the tenant's from the next start", func(t *testing.T) {
		x := require.New(t)
		got := apply(`
resources:
  - kind: Tenant
    alias: acme
    name: Acme
`)
		x.Equal([]string{"@acme"}, got.Changed, "applying did not write that the file says no key of config any more")
		v := read()
		x.Contains(v.GetLabels(), app.LabelDeclaredConfig)
		x.Empty(v.GetLabels()[app.LabelDeclaredConfig])
		x.True(v.GetConfig().HasPassword(), "the value a key had is not left as it was")
		x.False(v.GetConfig().GetPassword())

		x.NoError(update(app.TenantUpdateRequest_builder{
			Config: app.TenantConfig_builder{Password: z.Ptr(true), FrontDoor: "https://door.acme.example"}.Build(),
		}))
		x.True(read().GetConfig().GetPassword())
		refused(update(app.TenantUpdateRequest_builder{Name: z.Ptr("Renamed")}), "name")
	})

	// Written by a build before this one, until its next start: every key of
	// its config is the file's, as it was.
	t.Run("a declared tenant with no keys label is the file's whole", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
			Ref: ref, Labels: map[string]string{cmd.Declared: path}, DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)
		x.NotContains(read().GetLabels(), app.LabelDeclaredConfig)

		refused(update(app.TenantUpdateRequest_builder{
			Config: app.TenantConfig_builder{Password: z.Ptr(true), FrontDoor: "https://elsewhere.acme.example"}.Build(),
		}), "config.front_door")
		refused(update(app.TenantUpdateRequest_builder{Name: z.Ptr("Renamed")}), "name")
		x.Equal("https://door.acme.example", read().GetConfig().GetFrontDoor())

		// And its next start records what the file says.
		got := apply(`
resources:
  - kind: Tenant
    alias: acme
    name: Acme
`)
		x.Equal([]string{"@acme"}, got.Changed)
		x.Contains(read().GetLabels(), app.LabelDeclaredConfig)
	})
}
