package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/roster/cmd"
)

// generalWrites asks a server for every `Patch` and `Apply` it has registered,
// as a caller who may call anything, and answers how many it asked.
//
// Every one, read off the server rather than written down here: a list in a
// test is a list somebody forgets to add the next entity to, and the next
// entity is what this is for.
func generalWrites(t *testing.T, g *grpc.Server, conn grpc.ClientConnInterface, as func() context.Context) int {
	t.Helper()
	x := require.New(t)

	asked := 0
	for name, info := range g.GetServiceInfo() {
		for _, m := range info.Methods {
			if m.Name != "Patch" && m.Name != "Apply" {
				continue
			}
			asked++

			full := "/" + name + "/" + m.Name
			err := conn.Invoke(as(), full, &emptypb.Empty{}, &emptypb.Empty{})
			x.Equal(codes.Unimplemented, status.Code(err), "%s: %v", full, err)
			x.ErrorContains(err, "is not served", full)
		}
	}

	return asked
}

// TestNoGeneralWriteIsServed.
//
// `Patch` and `Apply` are how the servers write. What a caller may change is
// on a verb of its own, and that is where every rule about a row was put.
//
// payday closes both unless a deployment says `allow_general_writes`. roster
// read that setting, and each part of the review before the freeze (#273)
// found it going round a rule: a credential's row (#275), a key's verifier
// (#276), a name past its proof (#277). Each was shut by name as it was found.
// Reading the Login App (#278) found the rest: with the setting on, somebody
// who administers a tenant wrote a connection's `secret_ref` and issuer in one
// `Patch`, which `Update` refuses; somebody bound `IdentityService/*` alone
// rewrote the subject of an administrator's account at a provider, which `Add`
// and `Erase` refuse them; `EmailService/Patch` changed a proved address and
// left it proved; `RoleService/Apply` wrote the caller's own role to
// everything; and `GroupService/Patch`, held in one site, renamed the group a
// relying party grants on.
//
// So it is not a setting any more. This asks every service a port registers,
// with the setting handed to the server directly, which is the one way a
// server can still be told (a file that says it is refused before one is
// built; see the test below).
func TestNoGeneralWriteIsServed(t *testing.T) {
	t.Run("on the data plane", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		b.mayAnything(b.ContosoUser, b.Contoso)

		g, err := b.Grpc(ctx, cmd.Config{Server: cmd.ServerConfig{ServerConfig: config.ServerConfig{AllowGeneralWrites: true}}})
		x.NoError(err)

		asked := generalWrites(t, g, pdtest.Serve(t, g), func() context.Context { return asOverTheWire(ctx, b.ContosoUser) })

		// Not a number to keep exact: enough that a walk which found nothing,
		// or found one service, is not a pass.
		x.Greater(asked, 40, "the walk asked too few methods to mean anything")
	})

	t.Run("on the port the deployment's own rows are managed at", func(t *testing.T) {
		x := require.New(t)
		b := keyedFor(t, nil, "/roster.*/*")
		ctx := t.Context()

		boundOnControl(t, ctx, b.Server, b.Service.Bytes(), "everything", "/roster.*/*")

		c := b.Config
		c.Control.AllowGeneralWrites = true
		g, err := b.GrpcControl(ctx, c)
		x.NoError(err)

		asked := generalWrites(t, g, pdtest.Serve(t, g), func() context.Context { return bearing(ctx, b.Token) })
		x.Greater(asked, 40, "the walk asked too few methods to mean anything")
	})
}

// TestAFileThatOpensGeneralWritesIsRefused.
//
// The setting is still a field of the block payday gives a listener, so a file
// can still say it. A server that started anyway would be one whose file
// believes something it will not do, which is the shape `control:` with no
// driver had (#276): refused, and the refusal says which block said it.
func TestAFileThatOpensGeneralWritesIsRefused(t *testing.T) {
	on := config.ServerConfig{AllowGeneralWrites: true}

	for name, with := range map[string]func(*cmd.Config){
		"server":  func(c *cmd.Config) { c.Server.ServerConfig = on },
		"control": func(c *cmd.Config) { c.Control.ServerConfig = on },
		"admin":   func(c *cmd.Config) { c.Admin.ServerConfig = on },
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			drv, dsn := pdtest.DB(t)
			cdrv, cdsn := pdtest.DB(t)
			c := cmd.Config{
				Db:      config.DbConfig{Driver: drv, Dsn: dsn},
				Watch:   config.WatchConfig{Broker: config.BrokerMemory},
				Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
			}
			with(&c)

			s, err := cmd.Build(t.Context(), c)
			if s != nil {
				s.Close()
			}
			x.ErrorContains(err, name+".allow_general_writes")
			x.ErrorContains(err, "serves no general write")
		})
	}
}
