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
// # Two ways in, and `client.addr` chooses -- or `--HAL` does
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
// `--HAL` on the root forces the local one whatever the file says, for
// somebody with a shell on the box who wants to look at the rows under a
// deployment configured for the wire. Editing the configuration to do that is a
// change that outlives the look.
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
// It also carries **no `closed` interceptor**, which is worth saying out loud
// because the wire does. `grpcx.GeneralWrite` closes `/Patch` and `/Apply`
// unless a deployment opens them, and none of that is installed here -- so
// `roster holder patch` reaches the general write at a shell, and can null a
// person's profile or overwrite their alias.
//
// That is not a boundary being crossed: an operator running this already has
// the database credentials, and everything they could do through the general
// write they could do with a SQL client. It is written down because *local is
// Ungated* was documented and *and general writes are open there* was not, and
// the two are different sentences.
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
	t := pdcmd.NewIn(connector{c}, "roster")

	// And this app's own, on the same connection.
	//
	// `Tree.Add` and `Tree.WithConn` are the seam payday put here for exactly
	// this: a command an app wrote reaches the server the generated ones reach,
	// rather than opening a second socket with a second credential to get right
	// -- which for a connector that hands out an in-process server would be a
	// second server.
	//
	// `me` is the first of them and will not be the last. Every RPC should have
	// a command, because *what can be done without a console* has one correct
	// answer; docs/roadmap.md's D58 row is the list and why.
	// The connection on the **parent**, which is where it belongs: `withConn`
	// fires on `Run` and a command with a subcommand under it runs as
	// `Run|Pass`, so one dial covers whichever verb was typed and closes after
	// it. `RequireSubcommand` because `roster me` alone is not a question.
	me := newCmdMe(c)
	me.Handler = xli.Chain(t.WithConn(), xli.RequireSubcommand())

	if err := t.Add("me", me); err != nil {
		// A path that is already there, which is a mistake in this file rather
		// than something a deployment can cause.
		panic(err)
	}

	return t.Commands()
}

// connector is [local] or [remote], decided when the command runs.
//
// Decided then and not here, because "here" is before the configuration file
// has been read -- see the note on [NewCmdEntities]. So this holds the pointer
// and asks it at the last moment.
type connector struct{ c *Config }

func (v connector) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	// `--HAL` first, so it means what it says: the wire is skipped whatever the
	// file has in it, including a credential. Somebody who passed it is asking
	// for the database and knows they are.
	if v.c.Client.Local {
		return local{v.c}.Connect(ctx)
	}

	if v.c.Client.Addr != "" {
		return remote{v.c}.Connect(ctx)
	}

	// A credential and nowhere to send it. Refused rather than run, because
	// what it would otherwise do is read the database directly while the
	// deployment believes it is calling a server -- a mounted secret going
	// unused, and one line on stderr to say so.
	//
	// Naming `auth` is what says the intent was the wire. A file with no
	// `client` block at all means the local one and is left alone.
	if v.c.Client.Auth.IsSet() {
		return nil, nil, fmt.Errorf(
			"client.auth is set and client.addr is not, so there is nowhere to send it; " +
				"name an address, or drop client.auth to read the database directly")
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
	why := "set client.addr to go over the wire"
	if l.c.Client.Addr != "" {
		// `--HAL` against a deployment that is configured for the wire, which
		// is the case the flag exists for and the one worth naming: the file
		// says one thing and this invocation is doing another.
		why = "--HAL, so " + l.c.Client.Addr + " was not used"
	}

	fmt.Fprintln(os.Stderr,
		"roster: reading this deployment's database directly; "+why)

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
// `client.auth`, which says the scheme as well as the value -- because roster
// serves more than one and which it serves depends on the rest of the file: a
// deployment with a control plane reads `Bearer` and checks an API key, and one
// without reads `Plain` and believes what the caller writes.
//
// There is no `--credential` flag and there will not be, for the reason
// `roster key add` takes no key: an argument is in the shell history and in the
// process list. `client.auth.credential_file` is what a mounted secret uses.
type remote struct{ c *Config }

func (r remote) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	// Before anything is dialed, so a configuration this cannot be built from
	// is refused with the name of the field that is wrong rather than with a
	// refusal from the far end.
	p, err := r.c.Client.Auth.Provider()
	if err != nil {
		return nil, nil, err
	}

	opts := []grpc.DialOption{}
	if r.c.Client.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}
	if p != nil {
		opts = append(opts, auth.Inject(p)...)
	}

	conn, err := grpc.NewClient(r.c.Client.Addr, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("client.addr: %w", err)
	}

	return conn, func() { conn.Close() }, nil
}
