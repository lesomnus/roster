package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestSomebodyInAClosedTenantReachesNothing: `HolderService/Reaches` read the
// person's suspension and nothing of their tenant. Asked from inside a closed
// tenant roster refuses the asker; asked from outside, by a deployment key as
// itself, it answered their bindings for as long as the tenant was closed --
// while `standing`, asked by the same key, refused them. Every credential of
// somebody in a closed tenant is refused before the gate is asked, as a
// suspended person's is, so what they reach is nothing; and a tenant reopened
// answers what it did.
func TestSomebodyInAClosedTenantReachesNothing(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, app.HolderService_Reaches_FullMethodName)
	ctx := t.Context()

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "staff",
		Methods: []string{app.HolderService_Get_FullMethodName},
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	as := bearing(ctx, b.Token)
	h := app.NewHolderServiceClient(b.Conn)
	req := app.HolderReachesRequest_builder{Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build()}.Build()
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	open, err := h.Reaches(as, req)
	x.NoError(err)
	x.Contains(open.GetMethods(), app.HolderService_Get_FullMethodName)

	_, err = b.Ungated.Tenant().Close(ctx, app.TenantCloseRequest_builder{Ref: tenant}.Build())
	x.NoError(err)
	closed, err := h.Reaches(as, req)
	x.NoError(err)
	x.Empty(closed.GetMethods(), "somebody in a closed tenant was answered what they are bound")
	x.Empty(closed.GetSites())
	x.False(closed.GetEverySite())
	x.Empty(closed.GetEverywhere())

	_, err = b.Ungated.Tenant().Restore(ctx, app.TenantRestoreRequest_builder{Ref: tenant}.Build())
	x.NoError(err)
	reopened, err := h.Reaches(as, req)
	x.NoError(err)
	x.Equal(open.GetMethods(), reopened.GetMethods(), "a tenant reopened answers what it did")
}
