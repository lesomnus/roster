package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// TestATenantIsNotErased: `Tenant` declares a hard erase, and nothing ever
// erased one. Every tenant `Add` makes has a person and two roles keyed to it,
// and a destroyed tenant keeps its people's blank rows, so the generated
// delete met a foreign key at every point of a tenant's life. The gated ports
// refuse the method in a sentence. `admin.addr` served it, and answered `500`
// with the database's words -- *FOREIGN KEY constraint failed* -- as
// `roster tenant erase` printed them at the box. Right is one answer: no port
// serves it, and what is said is what does end a tenant.
//
// A tenant is closed, which shuts its people out and can be undone
// (`TenantService/Close`), and destroyed after that by whoever runs the
// deployment (`roster forget @tenant`).
func TestATenantIsNotErased(t *testing.T) {
	b := keyedFor(t, nil, "/roster.*/*")
	ctx := t.Context()

	// Whoever holds most at each port: the key's holder and an operator, each
	// bound everything on the control plane.
	boundOnControl(t, ctx, b.Server, b.Service.Bytes(), "everything", "/roster.*/*")
	op := addHolder(t, ctx, b.Control, controlTenant(t, ctx, b.Server), "operator")
	boundOnControl(t, ctx, b.Server, op.Bytes(), "operators", "/roster.*/*")
	_, err := b.Control.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref: app.HolderRef_builder{Id: op.Bytes()}.Build(), Secret: []byte("an operator's own password"),
	}.Build())
	require.NoError(t, err)
	k := signIn(t, b.Server, "operator", "an operator's own password")
	require.NotNil(t, k, "the operator did not sign in")

	control, err := b.GrpcControl(ctx, b.Config)
	require.NoError(t, err)
	admin, err := b.GrpcAdmin(ctx, b.Config)
	require.NoError(t, err)

	ports := []struct {
		name string
		conn grpc.ClientConnInterface
		as   context.Context
	}{
		{"server.addr", b.Conn, bearing(ctx, b.Token)},
		{"control.addr", pdtest.Serve(t, control), bearing(ctx, b.Token)},
		{"admin.addr", pdtest.Serve(t, admin), metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", k.Name+"="+k.Value))},
	}

	// One tenant, asked at each point of its life.
	bolt := add(t, ctx, b.Server, "bolt")
	addHolder(t, ctx, b.Server, bolt, "somebody")
	ref := app.TenantRef_builder{Id: bolt.Bytes()}.Build()

	asked := func(t *testing.T) {
		t.Helper()

		for _, p := range ports {
			_, err := app.NewTenantServiceClient(p.conn).Erase(p.as, ref)
			require.Equal(t, codes.Unimplemented, status.Code(err), "%s: %v", p.name, err)
			require.NotContains(t, err.Error(), "constraint", "%s answered in the database's words", p.name)
		}

		// And beneath every port, where a command at the box and the
		// deployment's own work arrive: the layer says what does end one.
		_, err := b.Ungated.Tenant().Erase(ctx, ref)
		require.Equal(t, codes.Unimplemented, status.Code(err), "%v", err)
		require.ErrorContains(t, err, "roster tenant close")
		require.ErrorContains(t, err, "roster forget @")

		_, err = b.Ent.Tenant.Get(ctx, bolt.Uuid())
		require.NoError(t, err, "the row is gone")
	}

	t.Run("open", asked)

	t.Run("closed", func(t *testing.T) {
		closes(t, ctx, b.Ungated, bolt)
		asked(t)
	})

	t.Run("destroyed", func(t *testing.T) {
		_, err := forget.DestroyTenant(ctx, b.Ent, bolt, trail.Policy{}, audit.Contents{}, nil)
		require.NoError(t, err)
		asked(t)
	})

	t.Run("and with its alias given back", func(t *testing.T) {
		require.NoError(t, forget.FreeAlias(ctx, b.Ungated, bolt))
		asked(t)
	})

	// The deployment's own tenant, on the control plane, is the same entity
	// behind the same layer.
	t.Run("nor the deployment's own", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Control.Ungated.Tenant().Erase(ctx, app.TenantRef_builder{Id: controlTenant(t, ctx, b.Server).Bytes()}.Build())
		x.Equal(codes.Unimplemented, status.Code(err), "%v", err)
	})
}

// TestTheBoxOffersNoCommandThatErasesATenant: `roster tenant erase` is built
// with every other entity's, from the schema, and could only ever print a
// foreign-key error. It is not among what `roster tenant` offers; and typed
// all the same, as every other entity's is, it answers what to type instead
// and fails, having opened nothing.
func TestTheBoxOffersNoCommandThatErasesATenant(t *testing.T) {
	x := require.New(t)

	c := seeded(t)

	out, err := entities(t, c, "tenant", "erase", "@contoso")
	x.Error(err, "said nothing was wrong: %s", out)
	x.ErrorContains(err, "roster tenant close")
	x.ErrorContains(err, "roster forget @")
	x.NotContains(err.Error(), "constraint")

	out, err = entities(t, c, "tenant", "--help")
	x.NoError(err)
	x.Contains(out, "close", "the help read is not the tenant's")
	x.NotContains(out, "erase one Tenant", "a command that can never work is offered")

	// Every other entity keeps its own.
	out, err = entities(t, c, "holder", "--help")
	x.NoError(err)
	x.Contains(out, "erase one Holder")

	// And it is still there.
	out, err = entities(t, c, "tenant", "get", "@contoso")
	x.NoError(err)
	x.Contains(out, "contoso")

	// The control plane's one tenant is the deployment itself, which is not
	// closed either: there is nothing to type instead, and no command.
	_, err = entities(t, c, "control", "tenant", "erase", "@k")
	x.ErrorContains(err, "unknown subcommand")
	out, err = entities(t, c, "control", "tenant", "--help")
	x.NoError(err)
	x.NotContains(out, "erase one Tenant")
}
