package cli

import (
	"context"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"net"
	"os"
	"strings"

	"github.com/lesomnus/xli"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/grpcx"
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
// # What local writes is heard, by way of the outbox
//
// A served call is published by an interceptor once it has answered, to the
// broker of the process that served it. This process is not the one that
// serves, and it ends with the command -- so it builds the deployment with
// every write queued in the outbox instead ([built]), and the process that
// serves publishes them. `roster holder disable` at a shell reaches an app
// following the stream that way, within `watch.drain_every`, where it reached
// nobody until the app asked again (#214).
//
// The pipe carries none of the serving chain's publishing for that reason:
// what it would publish, it would publish to this process's own broker. See
// [cmd.Config.Queued].
//
// # What local reads is on the access record, naming who is at the shell
//
// A read over the wire is a row of payday's access record (payday#111), naming
// the caller its frame names. A read here named nobody, because nothing on the
// pipe recorded it -- so `roster holder ls` at the box showed people's rows to
// whoever ran it and left no trace that it had (#221). The pipe records now,
// with what an RPC's record reads ([cmd.Server.Shell]):
//
//   - who: the operator `--as` names, an operator of the control plane,
//     looked up and refused when it names nobody. A command here that is not
//     told is refused before anything is opened, wherever there are operators
//     to name ([cmd.ErrUnnamed]); a deployment with no control plane has none,
//     and its rows name the deployment and the shell.
//   - when: before the answer leaves. A read whose row cannot be written is
//     refused, so what this prints is never data the record did not hear of.
//
// It is the record and nothing else: the call carries no frame, so what it may
// do is the deployment's, as it was. See `cmd/shell.go` for why the operator
// is not a frame, and why the person names themselves.
//
// # A watch here, and the broker it hears
//
// `watch` subscribes to this process's broker, and under `memory` -- what
// `deploy/` and `roster.yaml` run -- that is a broker nothing but this command
// publishes to: after its first answer it would hear nothing the deployment
// did. So the pipe refuses it there, naming the wire (`client.addr`), where the
// serving process answers it. Under a broker every process hears (`postgres`)
// it hears the deployment, and each message is recorded before it is sent.
// What it does not do is follow the outbox: the serving process deletes what
// it drains, so a command reading it would race the drain, and one draining it
// would take the events away from the subscribers the server publishes to.
//
// # Why the connection is made by a [pdcmd.Connector]
//
// Because the tree is built here, while the command set is being assembled, and
// the configuration has not been read yet -- `cfg.Load` runs on the root and
// this is a child of it. So the `*cmd.Config` [local] holds is still empty at this
// moment and filled in by the time `Connect` is called, which is when somebody
// actually runs one of these.
//
// It also means `roster tenant ls --help` opens no database.
func NewCmdEntities(c *cmd.Config) xli.Commands {
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
	me.Handler = xli.Chain(wireOnly(c, t), xli.RequireSubcommand())

	if err := t.Add("me", me); err != nil {
		// A path that is already there, which is a mistake in this file rather
		// than something a deployment can cause.
		panic(err)
	}

	overlays(t)

	// And this app's other hand-written services, remote only for `me`'s
	// reason each: they are questions to a served deployment, and the local
	// pipe mounts the entity services alone.
	front := newCmdFront(c)
	front.Handler = xli.Chain(wireOnly(c, t), xli.RequireSubcommand())
	if err := t.Add("front", front); err != nil {
		panic(err)
	}

	sy := newCmdSync(c)
	sy.Handler = xli.Chain(wireOnly(c, t), xli.RequireSubcommand())
	if err := t.Add("sync", sy); err != nil {
		panic(err)
	}

	return t.Commands()
}

// wireOnly is `t.WithConn()` for a tree of questions only a served deployment
// answers -- `me`, `front`, `sync` -- which opens nothing in local mode, where
// each of its commands says what to name instead (`calling`).
//
// It used to open the deployment there anyway, a database and both planes'
// servers built to say "name client.addr"; and opening it is now also where a
// command at the box is asked who is at the shell (#221), which a command that
// reads nobody and answers with that sentence has no reason to be asked.
func wireOnly(c *cmd.Config, t *pdcmd.Tree) xli.Handler {
	conn := t.WithConn()

	return xli.Handle(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
		if c.Client.Local || c.Client.Addr == "" {
			return next(ctx)
		}

		return conn.Handle(ctx, cl, next)
	})
}

// overlays mounts the methods the overlays declared on `t`, which the six
// generated verbs cannot know about.
//
// A function because two trees want them: this one, and the control plane's in
// `control.go`, where `holder disable` and `invalidate` are how an operator's
// account is stopped.
func overlays(t *pdcmd.Tree) {
	// `Tree.Unary` is the other half of the seam `Tree.Add` is -- everything around the call is payday's,
	// and what a method *means* is this app's, which is why the briefs are
	// written out here: "Disable" as its own description is a command somebody
	// has to guess at.
	//
	// A method added to an overlay tomorrow is one line here, not a command
	// to write -- and it is a line that has to be written, or the method is
	// one a console can reach and a terminal cannot (D58).
	for _, v := range []struct{ path, method, brief string }{
		{"holder/update", "roster.HolderService.Update", "replace somebody's profile, whole"},
		{"holder/realias", "roster.HolderService.Realias", "change what somebody is written as; the old alias comes free"},
		{"holder/disable", "roster.HolderService.Disable", "refuse somebody everywhere, now"},
		{"holder/enable", "roster.HolderService.Enable", "let a disabled somebody back in"},
		{"holder/invalidate", "roster.HolderService.Invalidate", "void everything issued to somebody before now"},
		{"holder/signs-in", "roster.HolderService.SignsIn", "how somebody signs in: identities, credentials, keys"},
		{"holder/reaches", "roster.HolderService.Reaches", "what somebody may call, from every role they hold"},
		{"holder/search", "roster.HolderService.Search", "find people by a fragment of a name or alias, a department, an employee number"},
		{"holder/portray", "roster.HolderService.Portray", "keep an image as somebody's picture, or take theirs away; roster makes the sizes"},
		{"holder/fill", "roster.HolderService.Fill", "write a name or a picture where somebody's profile has none, and nothing else"},
		{"tenant/update", "roster.TenantService.Update", "what a customer says about itself: name, note, labels; never the alias"},
		{"tenant/close", "roster.TenantService.Close", "shut everybody of a customer out, now; nothing is destroyed"},
		{"tenant/restore", "roster.TenantService.Restore", "undo a close while tenant.restore_within lasts"},
		{"host/update", "roster.HostService.Update", "a host's note; never its name"},
		{"mail-domain/update", "roster.MailDomainService.Update", "where a domain routes, and its note; never its name"},
		{"role/widen", "roster.RoleService.Widen", "add methods to a role and take none away; never what you may not grant"},
		{"role/narrow", "roster.RoleService.Narrow", "take methods out of a role and add none; only what you hold, and never the tenant's last administrator"},
		{"connection/update", "roster.ConnectionService.Update", "a provider's issuer, client id, scopes, secret ref, subject claim, note; never its name"},
		{"identity/resubject", "roster.IdentityService.Resubject", "move somebody's identity to the claim their connection now names; once, and only from `sub`"},
		{"email/verify", "roster.EmailService.Verify", "mint a link that proves an address, printed once; delivering it is yours"},
		{"email/confirm", "roster.EmailService.Confirm", "spend a verify link, and stamp the address it was minted for"},
		{"consent/agree", "roster.ConsentService.Agree", "record somebody agreeing to an app's documents, each at the version they read; as the app"},
		{"consent/withdraw", "roster.ConsentService.Withdraw", "record somebody withdrawing their agreement to an optional document; as the app"},
		{"consent/settled", "roster.ConsentService.Settled", "which of an app's documents somebody has not agreed to in a scope; as the app"},
	} {
		u, err := t.Unary(v.method)
		if err != nil {
			// A method that is not in the schema, which is a mistake in this
			// file rather than something a deployment can cause.
			panic(err)
		}

		u.Brief = v.brief
		if err := t.Add(v.path, u); err != nil {
			panic(err)
		}
	}
}

// connector is [local] or [remote], decided when the command runs.
//
// Decided then and not here, because "here" is before the configuration file
// has been read -- see the note on [NewCmdEntities]. So this holds the pointer
// and asks it at the last moment.
type connector struct{ c *cmd.Config }

func (v connector) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	// `--HAL` first, so it means what it says: the wire is skipped whatever the
	// file has in it, including a credential. Somebody who passed it is asking
	// for the database and knows they are.
	if v.c.Client.Local {
		return local{v.c}.Connect(ctx)
	}

	if v.c.Client.Addr != "" {
		// Who is at a shell is the box's question. Over the wire the
		// credential says who is calling and the record names it, so a name
		// given here would be read by nothing -- and somebody who gave it
		// would believe the record says it.
		if strings.TrimSpace(v.c.Client.As) != "" {
			return nil, nil, fmt.Errorf(
				"--as names who is at a shell reading the database directly, and this goes over the wire to %s, "+
					"where the credential says who is calling; drop --as, or add --HAL to read the database", v.c.Client.Addr)
		}

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
// with the first. And the same stream, through the outbox: see
// [NewCmdEntities].
//
// A type rather than a closure because this is where the three decisions
// `pdcmd` refuses to make for an app are written down: no address, no
// credential, and the ungated stack. A reader looking for "what do these
// commands connect to" finds them here.
//
// And the fourth, which is not `pdcmd`'s either: who is at the shell, which the
// access record names for what a command here reads or writes ([atTheBox]).
type local struct{ c *cmd.Config }

func (l local) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	// Before anything is opened, so a command that was not told who is at the
	// shell is refused without a database being touched.
	if err := saidWho(l.c); err != nil {
		return nil, nil, err
	}

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

	s, err := built(ctx, *l.c)
	if err != nil {
		return nil, nil, err
	}

	by, err := atTheBox(ctx, l.c, s)
	if err != nil {
		s.Close()
		return nil, nil, err
	}

	return piped(s, s, by)
}

// saidWho refuses a command at the box that was not told who is at the shell,
// where the deployment has operators to name: one with a control plane.
//
// Asked of the configuration, before anything is opened. Without a control
// plane there is nobody `--as` could name, and [atTheBox] says the deployment.
func saidWho(c *cmd.Config) error {
	if c.Control.Serves() && strings.TrimSpace(c.Client.As) == "" {
		return cmd.ErrUnnamed
	}

	return nil
}

// atTheBox is who is at this shell, as `--as` named them on the control plane,
// and the shell's user and host beside them -- what the access record names for
// what a command here reads or writes (#221).
//
// A deployment with no control plane has no operators, and nobody is named:
// its rows are the deployment's, and say which shell. `--as` there is refused,
// since it names somebody who cannot be there.
func atTheBox(ctx context.Context, c *cmd.Config, s *cmd.Server) (cmd.Operator, error) {
	where := thisShell()
	if s.Control == nil && strings.TrimSpace(c.Client.As) == "" {
		return cmd.Operator{Where: where}, nil
	}

	by, err := cmd.OperatorNamed(ctx, s, c.Client.As)
	if err != nil {
		return cmd.Operator{}, err
	}
	by.Where = where

	return by, nil
}

// piped is [pipe] on `at`, a plane of `s`, and closes `s` with it: what a
// connector hands `pdcmd`, which calls the close once the command is done.
func piped(s *cmd.Server, at *cmd.Server, by cmd.Operator) (pdcmd.Conn, func(), error) {
	conn, stop, err := pipe(at, by)
	if err != nil {
		s.Close()

		return nil, nil, err
	}

	// The database after the server, which `stop` has stopped: closing it under
	// a server that is still answering is a panic rather than an error.
	return conn, func() {
		stop()
		s.Close()
	}, nil
}

// pipe is `at`'s ungated server on a listener with no address, and a
// connection to it, with what reaches it recorded as `by`'s (#221). The
// returned func closes the connection and the server, and not `at`.
//
// Two things are in front of the server, and nothing else of the serving
// chain is: the access record, as [cmd.Server.Shell] configures it for a call
// at a shell, and -- where this plane's broker hears no other process -- a
// refusal of every stream, which here is a watch that would hear nothing.
// `TestEveryPipeIsRecorded` holds every server a command stands up to this
// function.
func pipe(at *cmd.Server, by cmd.Operator) (pdcmd.Conn, func(), error) {
	chain := grpcx.Chain{}
	if at.HearsOnlyItself() {
		chain = chain.WithStream(unheard)
	}
	chain = chain.With(at.Shell(by).Interceptor())

	g := grpc.NewServer(chain.ServerOptions()...)
	app.RegisterServer(g, at.Ungated)

	lis := bufconn.Listen(1 << 20)
	go g.Serve(lis)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		g.Stop()

		return nil, nil, err
	}

	// In this order: the client first, so nothing is in flight; then the
	// server, so the listener is done with.
	return conn, func() {
		conn.Close()
		g.Stop()
	}, nil
}

// unheard refuses a watch at the box whose broker hears only this process: it
// would answer once, and then hear nothing the deployment did (#221).
//
// `FailedPrecondition`, because it is the configuration that makes it so, and
// the sentence names both ways out.
func unheard(_ any, _ grpc.ServerStream, info *grpc.StreamServerInfo, _ grpc.StreamHandler) error {
	return status.Errorf(codes.FailedPrecondition,
		"%s: a watch here would hear only this command. watch.broker is memory, which publishes inside "+
			"the process that serves, and this one does not -- after its first answer nothing the deployment "+
			"did would reach it. Watch over the wire instead: set client.addr and client.auth, and the serving "+
			"process answers it (and records what it shows); or name a broker every process hears, watch.broker: postgres",
		info.FullMethod)
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
type remote struct{ c *cmd.Config }

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
