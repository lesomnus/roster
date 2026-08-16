package cmd

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/lesomnus/xli"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/auth"
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
// # Two ways in, and `client.addr` chooses
//
// Empty, which is the default, is [local]: a server this process builds, on a
// pipe with no address, reading the database directly. The same answer
// `roster init` and `roster key` give, and the only way to get one is to be
// running this binary on a machine with the database credentials.
//
// Set, it is [remote]: this deployment over the wire, as whoever `client.token`
// names. Nothing is read directly, and what comes back is what that credential
// may see.
//
// Local is the default rather than remote -- which is the opposite of what
// `oas` does -- because the two commands beside these already are. `roster
// init` and `roster key` have no remote form at all, and a binary whose
// commands disagree about where they run is worse than one that is
// consistently the awkward way round. Going local writes a line to the log
// saying so.
//
// # Local is Ungated, and that is the decision worth being explicit about
//
// The wall narrows every read to the tenants the caller may see, and an
// operator is not in a tenant -- so `tenant ls` behind the wall would answer
// with nothing, correctly, and there would be no way round it. Going around
// the wall stays what it already is here: a server instance somebody was
// handed, which is a line of wiring a reader can find.
//
// Nothing about it is served. The ungated server is registered on a listener
// with no address and torn down when the command finishes.
//
// **Remote is not that.** It is a caller like any other, so `tenant ls` over
// the wire answers with the tenants that credential may see -- which for an
// API key is one. The two are not the same view and are not meant to be; see
// [remote] for which port it reaches and why the operator's is not it yet.
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
	return pdcmd.NewIn(connector{c}, "roster").Commands()
}

// connector is [local] or [remote], decided when the command runs.
//
// Decided then and not here, because "here" is before the configuration file
// has been read -- see the note on [NewCmdEntities]. So this holds the pointer
// and asks it at the last moment.
type connector struct{ c *Config }

func (v connector) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	if v.c.Client.Addr != "" {
		return remote{v.c}.Connect(ctx)
	}

	return local{v.c}.Connect(ctx)
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
	// Said out loud, every time, the way `oas` says it. Reading the database
	// under a deployment is a thing somebody does on purpose and should not be
	// able to do by forgetting -- and the way it happens by accident is a
	// configuration file that was meant to name a server and did not.
	//
	// To stderr and not through `log`, which is the part worth explaining:
	// nothing installs a logger on a command in this app -- `log.From` on a
	// bare context discards -- so a warning written that way is one nobody ever
	// sees, which is worse than none. Stderr also keeps it out of a pipe, so
	// `roster tenant ls -o json | jq` is unaffected.
	fmt.Fprintln(os.Stderr,
		"roster: reading this deployment's database directly; set client.addr to go over the wire")

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

// remote is this deployment over the wire, as whoever `client.token` names.
//
// # Which port it reaches
//
// Whichever `client.addr` names, and what that means differs. `server.addr` is
// the data plane and is **walled**: an API key belongs to a tenant, so
// `tenant ls` there answers with that tenant and not with every one. That is
// correct and it is not the operator's view.
//
// The operator's view is `admin.addr` -- the data plane with no wall -- and a
// command cannot reach it yet. That port takes a **session cookie**, which is
// what a console holds after signing in, and there is nothing here that signs
// in. Giving it a second scheme is a decision about an unwalled port and is not
// one to make in passing; until it is made, an operator's `tenant ls` is the
// local one.
//
// # The credential
//
// `client.token`, or `client.token_file` for a deployment that mounts it as a
// secret. There is no `--token` flag and there will not be, for the reason
// `roster key add` takes no key: an argument is in the shell history and in the
// process list.
type remote struct{ c *Config }

func (r remote) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	token, err := r.c.Client.Bearer()
	if err != nil {
		return nil, nil, err
	}

	opts := []grpc.DialOption{}
	if r.c.Client.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}
	if token != "" {
		opts = append(opts, auth.Inject(auth.BearerProvider(token))...)
	}

	conn, err := grpc.NewClient(r.c.Client.Addr, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("client.addr: %w", err)
	}

	return conn, func() { conn.Close() }, nil
}
