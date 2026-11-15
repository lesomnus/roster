package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestNobodyAtAPortSaysAFileDeclaredARow: `roster.declared` is the label a row
// carries that a file owns, and its presence is the whole of what makes a row
// read-only at every port. So it is what applies files that writes it, and
// never a caller.
//
// # What it was
//
// A label like any other: every `Add` takes `labels`, and so does
// `Tenant.Update`. A tenant's administrator made a person, a role and a
// binding, each carrying it, and had an administrator nobody could suspend,
// erase, unbind or narrow -- not another administrator of the tenant, and not
// an operator at the admin listener, who were each told the row is written
// from a file that does not exist. And labelled their own `Tenant` row, which
// the admin console then could not close (#274).
func TestNobodyAtAPortSaysAFileDeclaredARow(t *testing.T) {
	b := keyFor(t, verify)
	ctx := t.Context()

	const all = "/roster.*/*"

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "administers",
		Methods: []string{all},
	}.Build())
	require.NoError(t, err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	as := bearing(ctx, mintFor(t, ctx, b, b.Who, "theirs", []string{all}, time.Time{}))
	said := map[string]string{cmd.Declared: "nowhere.yaml"}
	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	t.Run("not on a row they make", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewHolderServiceClient(b.Conn).Add(as, app.HolderAddRequest_builder{
			Tenant: contoso, Alias: "ghost", Labels: said,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.ErrorContains(err, cmd.Declared)

		_, err = app.NewRoleServiceClient(b.Conn).Add(as, app.RoleAddRequest_builder{
			Tenant: contoso, Alias: "ghostly", Methods: []string{all}, Labels: said,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		_, err = app.NewBindingServiceClient(b.Conn).Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			Labels: said,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("nor on their own tenant", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewTenantServiceClient(b.Conn).Update(as, app.TenantUpdateRequest_builder{
			Ref: contoso, Labels: said,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		got, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: contoso}.Build())
		x.NoError(err)
		x.NotContains(got.GetLabels(), cmd.Declared)
	})

	t.Run("and a label of their own is theirs to write", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewHolderServiceClient(b.Conn).Add(as, app.HolderAddRequest_builder{
			Tenant: contoso, Alias: "labelled", Labels: map[string]string{"team": "payroll"},
		}.Build())
		x.NoError(err)
		x.Equal("payroll", v.GetLabels()["team"])
	})

	// What applies a file is not at a port, and is what the label is for.
	t.Run("and what applies a file still says so", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: contoso, Alias: "declared", Labels: said,
		}.Build())
		x.NoError(err)
		x.Equal("nowhere.yaml", v.GetLabels()[cmd.Declared])
	})
}
