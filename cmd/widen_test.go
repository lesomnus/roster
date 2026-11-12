package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

const widen = "/roster.RoleService/Widen"

// TestAWidenAddsWhatIsNewAndTakesNothingAway is the verb an app gives the
// roles it shipped a method of its next release with (`role_svc.ext.proto`):
// what is named and not already covered goes after what the role holds, once;
// what it holds stays; nothing new is no write; nothing named is a mistake.
// Over the wire, where `Patch` is closed and this is not.
func TestAWidenAddsWhatIsNewAndTakesNothingAway(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	shipped := b.grantee(t, ctx, "shipper", widen, "/loft.*/*")
	roles := app.NewRoleServiceClient(served(t, b.Server))
	as := asOverTheWire(ctx, shipped)

	ref := app.RoleRef_builder{Id: b.role(t, ctx, "staff", "/loft.AssetService/Get").Bytes()}.Build()
	v, err := roles.Widen(as, app.RoleWidenRequest_builder{Ref: ref, Methods: []string{
		"/loft.AssetService/List", "/loft.AssetService/Get", "/loft.AssetService/List",
	}}.Build())
	x.NoError(err)
	x.Equal([]string{"/loft.AssetService/Get", "/loft.AssetService/List"}, v.GetMethods(), "what is new, once, after what it held")

	again, err := roles.Widen(as, app.RoleWidenRequest_builder{Ref: ref, Methods: []string{"/loft.AssetService/Get"}}.Build())
	x.NoError(err)
	x.Equal(v.GetMethods(), again.GetMethods())
	x.Equal(v.GetDateUpdated().AsTime(), again.GetDateUpdated().AsTime(), "nothing new, and something was written")

	// A pattern covers what it names: added once, and nothing it covers after.
	v, err = roles.Widen(as, app.RoleWidenRequest_builder{Ref: ref, Methods: []string{"/loft.*/*", "/loft.Permission/RunWork"}}.Build())
	x.NoError(err)
	x.Equal([]string{"/loft.AssetService/Get", "/loft.AssetService/List", "/loft.*/*"}, v.GetMethods())

	_, err = roles.Widen(as, app.RoleWidenRequest_builder{Ref: ref}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a widen that names nothing")
}

// TestNobodyWidensARoleBeyondWhatTheyHold: a widen is a grant, and it is held
// to the rule `Patch` is held to, because it is one -- every method the role
// would hold must be the caller's to grant, and not only the ones it adds. So
// a role somebody else made wider than the caller is no role for the caller
// to widen, whatever is named.
func TestNobodyWidensARoleBeyondWhatTheyHold(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := framed(ctx, b.grantee(t, ctx, "editor", widen, "/loft.AssetService/*"), b.Contoso)

	ref := app.RoleRef_builder{Id: b.role(t, ctx, "staff", "/loft.AssetService/Get").Bytes()}.Build()
	_, err := b.Walled.Role().Widen(as, app.RoleWidenRequest_builder{Ref: ref, Methods: []string{"/loft.AssetService/List"}}.Build())
	x.NoError(err, "what they hold")

	_, err = b.Walled.Role().Widen(as, app.RoleWidenRequest_builder{Ref: ref, Methods: []string{"/loft.Permission/RunWork"}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a role was widened to a method its editor does not hold")
	v, err := b.Walled.Role().Get(as, app.RoleGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.Equal([]string{"/loft.AssetService/Get", "/loft.AssetService/List"}, v.GetMethods(), "and something of it was written")

	wider := app.RoleRef_builder{Id: b.role(t, ctx, "wider", "/loft.AssetService/Get", "/roster.HolderService/Erase").Bytes()}.Build()
	_, err = b.Walled.Role().Widen(as, app.RoleWidenRequest_builder{Ref: wider, Methods: []string{"/loft.AssetService/List"}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a role holding what its editor does not, widened by what they do")
}

// TestADeclaredRoleIsNobodysToWiden: a role a deployment declared is written
// from where it is declared, so a widen at a port is refused -- even one that
// would add nothing, which answers the same way rather than a role that looks
// writable -- and the deployment's own work is not.
func TestADeclaredRoleIsNobodysToWiden(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := framed(ctx, b.grantee(t, ctx, "editor", widen, "/loft.*/*"), b.Contoso)

	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "front-door",
		Labels:  map[string]string{cmd.Declared: "roster.yaml"},
		Methods: []string{"/loft.AssetService/Get"},
	}.Build())
	x.NoError(err)
	ref := app.RoleRef_builder{Id: r.GetId()}.Build()

	for _, m := range []string{"/loft.AssetService/List", "/loft.AssetService/Get"} {
		_, err = b.Walled.Role().Widen(as, app.RoleWidenRequest_builder{Ref: ref, Methods: []string{m}}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%s, into a declared role", m)
	}

	v, err := b.Ungated.Role().Widen(ctx, app.RoleWidenRequest_builder{Ref: ref, Methods: []string{"/loft.AssetService/List"}}.Build())
	x.NoError(err, "the deployment's own work")
	x.Equal([]string{"/loft.AssetService/Get", "/loft.AssetService/List"}, v.GetMethods())
}

// TestARoleIsWidenedFromATerminal: `roster role widen`, from a shell over the
// wire as anybody holding the method and what it adds -- the line in
// `cli/entity.go` that makes a method a console can reach one a terminal can.
func TestARoleIsWidenedFromATerminal(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, widen, "/loft.*/*")
	r, err := b.Server.Ungated.Role().Add(t.Context(), app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Tenant.GetId()}.Build(),
		Alias:   "support",
		Methods: []string{"/loft.AssetService/Get"},
	}.Build())
	x.NoError(err)

	_, err = cliRun(t, &b.Hers, "role", "widen", "@newco/support", `{"methods":["/loft.AssetService/List"]}`)
	x.NoError(err)
	v, err := b.Server.Ungated.Role().Get(t.Context(), app.RoleGetRequest_builder{Ref: app.RoleRef_builder{Id: r.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal([]string{"/loft.AssetService/Get", "/loft.AssetService/List"}, v.GetMethods())
}
