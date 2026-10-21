package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestAVerbAboutTheWholeTenantIsHeldAcrossIt: closing a tenant, restoring it,
// and setting how everybody in it signs in are each about all of it, so a role
// that reaches somebody only in a site, or only in a team, does not reach them
// -- however its methods read. The gate counts both, which is right for a verb
// on rows a site narrows and is not for the tenant's own row
// (`server/core/close.go`, `Core.acrossTheTenant`).
func TestAVerbAboutTheWholeTenantIsHeldAcrossIt(t *testing.T) {
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	ours := b.team(t, ctx, seoul, "ours")
	admin := b.role(t, ctx, "tenant-admin", "/roster.TenantService/*")

	conn := served(t, b.Server)
	tenants := app.NewTenantServiceClient(conn)
	ref := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	// update turns passwords off for everybody, which is the setting the
	// finding was about besides closing: how the whole tenant signs in.
	update := func(as context.Context) error {
		was, err := tenants.Get(as, app.TenantGetRequest_builder{
			Ref: ref, Select: app.TenantSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			return err
		}
		_, err = tenants.Update(as, app.TenantUpdateRequest_builder{
			Ref:         ref,
			DateUpdated: was.GetDateUpdated(),
			Config:      app.TenantConfig_builder{Password: z.Ptr(false)}.Build(),
		}.Build())

		return err
	}
	closing := func(as context.Context) error {
		_, err := tenants.Close(as, app.TenantCloseRequest_builder{Ref: ref}.Build())

		return err
	}
	// Restore is asked of the stack itself: once a tenant is closed nobody of
	// it is let in at a port, so this is the rule standing behind that one.
	restore := func(who pdid.Id) error {
		return restores(b.asNobody(ctx, who, b.Contoso), b.Walled, b.Contoso)
	}
	open := func(t *testing.T) {
		t.Helper()
		v, err := b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
		require.NoError(t, err)
		require.Nil(t, v.DateClosed, "the tenant was closed by somebody who holds that in a part of it")
		require.True(t, v.Config.GetPassword() || !v.Config.HasPassword(), "the tenant's sign-in was set by somebody who holds that in a part of it")
	}

	refused := func(t *testing.T, who pdid.Id) {
		t.Helper()
		x := require.New(t)
		as := asOverTheWire(ctx, who)

		x.Equal(codes.PermissionDenied, status.Code(closing(as)))
		x.Equal(codes.PermissionDenied, status.Code(update(as)))
		open(t)

		closes(t, ctx, b.Ungated, b.Contoso)
		x.Equal(codes.PermissionDenied, status.Code(restore(who)))
		x.NoError(restores(ctx, b.Ungated, b.Contoso))
	}

	t.Run("a role bound in a site", func(t *testing.T) {
		who := b.holder(t, ctx, b.Contoso, "seoul-admin")
		b.binds(t, who, admin, &seoul)
		refused(t, who)
	})

	t.Run("a role held in a team", func(t *testing.T) {
		who := b.holder(t, ctx, b.Contoso, "team-admin")
		b.inTeam(t, ctx, who, ours, admin)
		refused(t, who)
	})

	// The same role bound across the tenant is what it says.
	t.Run("the same role across the tenant", func(t *testing.T) {
		x := require.New(t)
		who := b.holder(t, ctx, b.Contoso, "admin")
		b.binds(t, who, admin, nil)
		as := asOverTheWire(ctx, who)

		x.NoError(update(as))
		x.NoError(closing(as))
		x.NoError(restore(who))
	})
}
