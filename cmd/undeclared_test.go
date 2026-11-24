package cmd_test

import (
	"testing"
	"time"

	"github.com/lesomnus/z"
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

// TestNobodyAtAPortSaysAHolderIsAnApps: `roster.app` is the label `roster app
// install` writes on the holder it makes for an app, and a directory built on
// roster leaves such a holder out of its people. So it is kept as
// `roster.declared` is: written by what installs an app, at the box, and by no
// caller -- or a person could leave the directory by saying they are an app,
// and nothing would tell an app's holder from them.
//
// A request that carries it is refused where requests arrive, as one carrying
// `roster.declared` is; and no verb served at a port writes a holder's labels
// but `Add`, so nobody at one takes it off an app's holder either (`Patch` is
// served nowhere, `TestNoGeneralWriteIsServed`). Answered before: `OK`, and
// a holder that said it was an app's; right is `InvalidArgument` naming the
// label.
func TestNobodyAtAPortSaysAHolderIsAnApps(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	const all = "/roster.*/*"

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "administers",
		Methods: []string{all},
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	as := bearing(ctx, mintFor(t, ctx, b, b.Who, "theirs", []string{all}, time.Time{}))
	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	said := map[string]string{appLabelKey: "loft"}

	// An app's holder, as `roster app install` writes it at the box.
	loft, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: contoso, Alias: "loft", Labels: said}.Build())
	x.NoError(err)
	x.Equal("loft", loft.GetLabels()[appLabelKey])

	_, err = app.NewHolderServiceClient(b.Conn).Add(as, app.HolderAddRequest_builder{
		Tenant: contoso, Alias: "ghost", Labels: said,
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a holder was made at a port saying it is an app's: %v", err)
	x.ErrorContains(err, appLabelKey)

	_, err = app.NewRoleServiceClient(b.Conn).Add(as, app.RoleAddRequest_builder{
		Tenant: contoso, Alias: "ghostly", Methods: []string{all}, Labels: said,
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

	// Nor on their own row, nor off the app's: neither is written at a port.
	_, err = app.NewHolderServiceClient(b.Conn).Patch(as, app.HolderPatchRequest_builder{
		Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(), Labels: said,
	}.Build())
	x.Error(err, "a person said they were an app's")
	_, err = app.NewHolderServiceClient(b.Conn).Patch(as, app.HolderPatchRequest_builder{
		Ref: app.HolderRef_builder{Id: loft.GetId()}.Build(), Labels: map[string]string{"note": "a person"},
	}.Build())
	x.Error(err, "an app's holder was relabelled")

	who, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(), Select: app.HolderSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.NotContains(who.GetLabels(), appLabelKey)
	got, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: loft.GetId()}.Build(), Select: app.HolderSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("loft", got.GetLabels()[appLabelKey])
}
