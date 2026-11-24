package cmd_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestARoleOfRostersNamesWhatRosterServes.
//
// A role of roster's is read by roster's gate alone, and a role naming
// `/loft.*/*` with no `app` was written and bound, and granted nothing
// anywhere: roster never asks about a method of Loft's, and Loft is answered
// with Loft's roles (`Role.app`). Answered with the role; right is
// `FailedPrecondition`, saying where an app's methods are written -- at
// `Add` and at `Widen`, at a port and at the box alike, since the mistake is
// the same whoever makes it. One naming roster's is taken, and so is a role
// of an app's naming anything.
func TestARoleOfRostersNamesWhatRosterServes(t *testing.T) {
	b, ctx := build(t)

	const refused = `methods: "/loft.*/*" names no method of roster's; an app's methods are written in a role of that app (app)`

	admin := b.holder(t, ctx, b.Contoso, "admin")
	conn := served(t, b.Server)
	roles := app.NewRoleServiceClient(conn)
	wire := asOverTheWire(ctx, admin)
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	t.Run("at a port", func(t *testing.T) {
		x := require.New(t)

		_, err := roles.Add(wire, app.RoleAddRequest_builder{
			Tenant: tenant, Alias: "loft-staff", Methods: []string{"/loft.*/*"},
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Equal(refused, status.Convert(err).Message())

		v, err := roles.Add(wire, app.RoleAddRequest_builder{
			Tenant: tenant, Alias: "everything-again", Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err, "a role of roster's naming roster's")

		_, err = roles.Widen(wire, app.RoleWidenRequest_builder{
			Ref: app.RoleRef_builder{Id: v.GetId()}.Build(), Methods: []string{"/loft.*/*"},
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Equal(refused, status.Convert(err).Message())
	})

	t.Run("at the box", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant: tenant, Alias: "loft-staff", Methods: []string{"/loft.*/*"},
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Equal(refused, status.Convert(err).Message())

		r := b.role(t, ctx, "reader", "/roster.HolderService/Get")
		_, err = b.Ungated.Role().Widen(ctx, app.RoleWidenRequest_builder{
			Ref: app.RoleRef_builder{Id: r.Bytes()}.Build(), Methods: []string{"/loft.*/*"},
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Equal(refused, status.Convert(err).Message())

		// What roster serves of payday's own is roster's to name too.
		b.role(t, ctx, "introspects", "/payday.TokenService/Introspect")

		// And a role of an app's is taken as written.
		b.roleAtApp(t, ctx, b.holder(t, ctx, b.Contoso, "loft"), "loft-everything", "/loft.*/*")
	})
}

// TestWhatRosterServesIsWhatItsPortsRegister holds [cmd.Served], which a role
// of roster's is held to, to what the three ports put on the wire: a service
// registered on one and not listed would be one no role of roster's could
// name, and one listed and served nowhere one a role could name for nothing.
func TestWhatRosterServesIsWhatItsPortsRegister(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	_ = out

	ported := []string{}
	add := func(info map[string]grpc.ServiceInfo) {
		for name, v := range info {
			for _, m := range v.Methods {
				ported = append(ported, "/"+name+"/"+m.Name)
			}
		}
	}

	g, err := s.Grpc(t.Context(), cmd.Config{})
	x.NoError(err)
	add(g.GetServiceInfo())

	g, err = s.GrpcControl(t.Context(), c)
	x.NoError(err)
	add(g.GetServiceInfo())

	g, err = s.GrpcAdmin(t.Context(), c)
	x.NoError(err)
	x.NotNil(g)
	add(g.GetServiceInfo())

	slices.Sort(ported)
	ported = slices.Compact(ported)

	x.Greater(len(ported), 100, "the ports registered too few methods to mean anything")
	x.Equal(ported, cmd.Served(s.Walled))
}
