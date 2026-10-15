package cmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/payday/rbac/rbacfile"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFilePolicyJoinsEveryPermissionReader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "policy.yaml")
	write := func(v string) { require.NoError(t, os.WriteFile(p, []byte(v), 0600)) }
	write("{}\n")
	b, ctx := build(t, func(c *cmd.Config) { c.Policy = rbacfile.Config{File: p} })
	tenant, subject := b.Contoso.String(), b.ContosoUser.String()
	write(fmt.Sprintf(`roles:
  '@%[1]s/reader':
    methods: [/roster.HolderService/Get]
groups:
  '@%[1]s/readers':
    members: ['@%[1]s/%[2]s']
bindings:
  - role: '@%[1]s/reader'
    group: '@%[1]s/readers'
`, tenant, subject))
	require.NoError(t, b.PolicyFile.Refresh())
	require.NoError(t, b.PolicyFile.Refresh())
	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)
	get := func() error {
		_, err := app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build()}.Build())
		return err
	}
	require.NoError(t, get())
	grants, err := cmd.Granted(b.Ent, b.PolicyFile)(ctx, b.ContosoUser)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Contains(t, grants[0].Methods, getHolder)
	holding, err := cmd.Holding(b.Ent, b.PolicyFile)(ctx, b.ContosoUser)
	require.NoError(t, err)
	require.Equal(t, grants, holding)
	reaches, err := b.Ungated.Holder().Reaches(ctx, app.HolderReachesRequest_builder{Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build()}.Build())
	require.NoError(t, err)
	require.Contains(t, reaches.GetEverywhere(), getHolder)
	// Renaming either identity cannot move the grant to a user who takes its old alias.
	before, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build()}.Build())
	require.NoError(t, err)
	_, err = b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{Ref: before.Ref(), Alias: z.Ptr("renamed"), DateUpdated: before.GetDateUpdated()}.Build())
	require.NoError(t, err)
	other := b.holder(t, ctx, b.Contoso, "someone")
	gs, err := cmd.Granted(b.Ent, b.PolicyFile)(ctx, other)
	require.NoError(t, err)
	require.Empty(t, gs)
	require.NoError(t, get())
	_, err = b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{Ref: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: z.Ptr("renamed-tenant"), DateUpdatedForce: z.Ptr(true)}.Build())
	require.NoError(t, err)
	require.NoError(t, get())
	// Removing this source cannot erase a separate database binding.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "db-reader", getHolder), nil)
	write("{}\n")
	require.NoError(t, b.PolicyFile.Refresh())
	require.NoError(t, b.PolicyFile.Refresh())
	require.NoError(t, get())
	// A syntactically valid ID must already identify a holder in that tenant.
	for _, target := range []string{b.Contoso.String(), b.ContosoUser.String()} {
		write(fmt.Sprintf("roles:\n  '@%s/reader':\n    methods: [/roster.HolderService/Get]\nbindings:\n - role: '@%s/reader'\n   holder: '@%s/%s'\n", b.Fabrikam, b.Fabrikam, b.Fabrikam, target))
		_ = b.PolicyFile.Refresh()
		require.ErrorContains(t, b.PolicyFile.Refresh(), "no live holder")
	}
	// Unknown aliases fail validation rather than silently granting through a mutable name.
	write("roles:\n  '@contoso/reader':\n    methods: [/roster.HolderService/Get]\n")
	_ = b.PolicyFile.Refresh()
	require.ErrorContains(t, b.PolicyFile.Refresh(), "immutable UUID")
}

func TestFilePrivilegesProtectCredentialTargets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0600))
	b, ctx := build(t, func(c *cmd.Config) { c.Policy = rbacfile.Config{File: p} })
	boss := b.holder(t, ctx, b.Contoso, "boss")
	body := fmt.Sprintf("roles:\n  '@%s/admin':\n    methods: [/roster.HolderService/Erase]\nbindings:\n - role: '@%s/admin'\n   holder: '@%s/%s'\n", b.Contoso, b.Contoso, b.Contoso, boss)
	require.NoError(t, os.WriteFile(p, []byte(body), 0600))
	require.NoError(t, b.PolicyFile.Refresh())
	require.NoError(t, b.PolicyFile.Refresh())
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{Ref: app.HolderRef_builder{Id: boss.Bytes()}.Build(), Secret: []byte("correct horse battery staple")}.Build())
	require.NoError(t, err)
	ops := b.holder(t, ctx, b.Contoso, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", getHolder)
	_, err = b.Walled.Credential().Unlock(asOps, app.CredentialUnlockRequest_builder{Ref: app.HolderRef_builder{Id: boss.Bytes()}.Build()}.Build())
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "/roster.HolderService/Erase")
	// A valid empty policy actually revokes the file grant.
	require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0600))
	require.NoError(t, b.PolicyFile.Refresh())
	require.NoError(t, b.PolicyFile.Refresh())
	_, err = b.Walled.Credential().Unlock(asOps, app.CredentialUnlockRequest_builder{Ref: app.HolderRef_builder{Id: boss.Bytes()}.Build()}.Build())
	require.NoError(t, err)
}
