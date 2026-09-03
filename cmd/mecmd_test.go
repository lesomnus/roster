package cmd_test

import (
	"net"
	"strings"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestTheCliIsAlsoACustomersPerson is the framing this repository had wrong in
// its own documentation.
//
// `docs/usage/` called the CLI an operator's tool and put `client.addr` in a
// footnote about pointing commands at a server. It is a **client**, and one of
// its two modes happens to be a shell on the box: a person inside a tenant runs
// the same binary with their own `rt_`, against a configuration with no `db:`
// block at all, and is walled and gated like any other caller.
//
// So this asserts the caller rather than the operator: what a customer's person
// sees, what they do not, and that `me` -- the one service that is about them
// and had no command until D58 -- answers.
func TestTheCliIsAlsoACustomersPerson(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// One customer with somebody in it, and a second tenant to prove the wall.
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "alice",
	}.Build())
	x.NoError(err)

	other, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)

	_, err = s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "eve",
	}.Build())
	x.NoError(err)

	const listHolders = "/roster.HolderService/List"

	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "support",
		Methods: []string{listHolders, "/roster.MeService/Get", "/roster.ApiKeyService/Issue"},
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	token := stdoutOf(t, cmd.NewCmdKey(&c), "add",
		"--tenant", "newco", "--holder", "alice", "--allow", "/roster.*/*")

	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s2.Close() })

	// A real listener rather than a `bufconn`, because what is being asserted
	// is the mode a person is in and that mode is *dial an address*.
	g, err := s2.Grpc(ctx, cmd.Config{})
	x.NoError(err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	t.Cleanup(func() { g.Stop() })

	go func() { _ = g.Serve(l) }()

	// Alice's own configuration: where to call and what to call with, and
	// **no database**. There is nothing for her to open.
	hers := cmd.Config{
		Client: cmd.ClientConfig{
			Addr:     l.Addr().String(),
			Insecure: true,
			Auth:     cmd.ClientAuthConfig{Scheme: "bearer", Credential: token},
		},
	}
	x.Empty(hers.Db.Driver, "a customer's person needs a database")

	t.Run("she sees her tenant's people and no others", func(t *testing.T) {
		x := require.New(t)

		vs := stdoutOf(t, root(t, &hers), "holder", "ls", "-o", "name")
		x.Len(strings.Fields(vs), 1, "the wall did not narrow a customer's read:\n%s", vs)
	})

	t.Run("and is refused what her role does not name", func(t *testing.T) {
		x := require.New(t)

		err := root(t, &hers).Run(ctx, []string{"tenant", "ls"})
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("and `me` answers, which is the one service that is about her", func(t *testing.T) {
		x := require.New(t)

		v := stdoutOf(t, root(t, &hers), "me", "get")
		x.Contains(v, "alice")
		x.Contains(v, listHolders, "the pattern a page draws is not what she holds")
	})

	t.Run("and she mints herself a key with no subject anywhere in the request", func(t *testing.T) {
		x := require.New(t)

		v := stdoutOf(t, root(t, &hers), "me", "issue-key",
			"--name", "laptop", "--allow", "/roster.MeService/Get")
		x.True(strings.HasPrefix(v, "rt_"), "%q", v)

		// And what came off stdout is a working credential for **her**, which
		// is the sentence the command is run for: `$(roster me issue-key …)`
		// straight into a laptop's configuration. Narrowed to what it names,
		// besides -- her role lists holders and this key must not.
		laptop := hers
		laptop.Client.Auth = cmd.ClientAuthConfig{Scheme: "bearer", Credential: v}

		out := stdoutOf(t, root(t, &laptop), "me", "get")
		x.Contains(out, "alice", "the minted key answers as somebody else")

		err := root(t, &laptop).Run(ctx, []string{"holder", "ls"})
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the minted key is wider than what it was allowed")
	})

	t.Run("and `me` locally is a refusal rather than an empty answer", func(t *testing.T) {
		x := require.New(t)

		// The local mode has no caller: it opens the database. Answering
		// `Unimplemented` would be true and useless.
		err := root(t, &c).Run(ctx, []string{"me", "get"})
		x.Error(err)
		x.ErrorContains(err, "client.addr")
	})
}

// root is `roster` with a configuration already loaded, which is what a command
// running under the real root has.
//
// `cmd.Cmd` reads the file on the way down, so a test that used it would be a
// test about `pdcmd.Load`. What is wanted here is the tree below it.
func root(t *testing.T, c *cmd.Config) *xli.Command {
	t.Helper()

	return &xli.Command{Name: "roster", Commands: cmd.NewCmdEntities(c)}
}
