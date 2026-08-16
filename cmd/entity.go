package cmd

import (
	"context"
	"net"

	"github.com/lesomnus/xli"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/pdcmd"

	app "github.com/lesomnus/roster/rstr"
)

// NewCmdEntities is `tenant`, `holder`, `identity` and the rest -- `get`, `ls`,
// `add`, `patch` and `erase` for every entity this schema declares, built from
// the descriptors rather than written out.
//
// Nothing here says which entities there are or which verbs they have. `ls`
// exists for an entity that declared `list:` and not otherwise, so the commands
// and the schema cannot disagree; see `payday/pdcmd`.
//
// # It runs on a server this process holds
//
// The same answer `roster init` and `roster key` give, for the same reason.
// There is no address to dial and no credential to present: what these commands
// reach is a server instance this process built, over an in-process pipe, and
// the only way to get one is to be running this binary on a machine with the
// database credentials.
//
// [Server.Ungated] and not [Server.Walled], which is the decision worth being
// explicit about. The wall narrows every read to the tenants the caller may
// see, and an operator is not in a tenant -- so `tenant ls` behind the wall
// would answer with nothing, correctly, and there would be no way round it.
// Going around it is a server instance somebody was handed, which is a line of
// wiring a reader can find, rather than a rule that opens up whenever nobody is
// asking.
//
// What this is **not** is a way in from outside. Nothing about it is served:
// the ungated server is registered on a listener with no address, torn down
// when the command finishes.
//
// # Why the connection is made by a [pdcmd.Connector]
//
// Because the tree is built here, while the command set is being assembled, and
// the configuration has not been read yet -- `pdcmd.Load` runs on the root and
// this is a child of it. So the `*Config` [local] holds is still empty at this
// moment and filled in by the time `Connect` is called, which is when somebody
// actually runs one of these.
//
// It also means `roster tenant ls --help` opens no database.
func NewCmdEntities(c *Config) xli.Commands {
	// Named rather than found. `pdcmd.New` refuses a process holding more than
	// one payday app, and roster links exactly one -- but saying it here means
	// a second one arriving is a compile-time fact rather than an error at
	// startup.
	return pdcmd.NewIn(local{c}, "roster").Commands()
}

// local is this deployment, in this process: the server built from the
// configuration, on a pipe with no address.
//
// It is the whole stack, which is what makes these commands worth having. An
// `add` goes through the same layers, the same minter and the same trail a
// request does -- a command that reached the ent client directly would be a
// second way to write a row, and the first thing a second way does is disagree
// with the first.
//
// A type rather than a closure because this is where the three decisions
// `pdcmd` refuses to make for an app are written down: no address, no
// credential, and the ungated stack. A reader looking for "what do these
// commands connect to" finds them here.
type local struct{ c *Config }

func (l local) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	s, err := Build(ctx, *l.c)
	if err != nil {
		return nil, nil, err
	}

	g := grpc.NewServer()
	app.RegisterServer(g, s.Ungated)

	lis := bufconn.Listen(1 << 20)
	go g.Serve(lis)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		g.Stop()
		s.Close()

		return nil, nil, err
	}

	// In this order: the client first, so nothing is in flight; then the
	// server, so the listener is done with; then the database. Closing the
	// database under a server that is still answering is a panic rather than
	// an error.
	return conn, func() {
		conn.Close()
		g.Stop()
		s.Close()
	}, nil
}
