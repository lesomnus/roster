//go:build js && wasm

// The console's server, in the page.
//
// A reload is a fresh deployment: two new databases, `roster init` run again,
// nothing left over. Somebody working on the console starts no backend,
// migrates nothing, and does not have to remember what state they left it in.
//
// # What is the same, and what is not
//
// The Go half is the server the process runs -- the same generated services,
// the same stack, the same wall from the same schema, and the **control
// plane** built the way `Build` builds it. Two things differ and both are one
// line: the databases are SQLite in a Worker rather than files, and calls
// arrive over a message port rather than HTTP/2.
//
// # Signing in, and the one thing a page cannot have here
//
// `AuthService` is served for real: the password is checked by the same
// `vouch`, and a wrong one is refused. What does not work is the **cookie** --
// a message port has no browser cookie jar, so `set-cookie` in response
// metadata reaches nobody, and every call after the sign-in arrives naming
// nobody.
//
// So the instance remembers who signed in (`wasm/sandbox`): `auth.Plain`
// behind it believes a caller that writes a name, and a call that writes none
// is taken to be the operator the last accepted sign-in named, until a
// sign-out. The page needs no branch: it calls `AuthService.SignIn` exactly as
// it does against a real server, and what follows is that person.
//
// There is one caller in the page and this is a note of who they said they
// were; it is a sandbox being a sandbox, and the reason `Plain` is here at all.
//
// # Two servers, one instance
//
// The customers screen reaches a third listener against a real deployment,
// `admin.http`: the data plane with no wall, behind the operator's session,
// with its own interceptor chain (`cmd.GrpcAdmin`). Here it is a second
// `drpc.Server` under a second entry point (`drpcAdmin`), which the page
// dials by name on the same socket -- one download, one compile, one pair of
// databases, and the operator who signed in on the first server is the caller
// on the second, because the memory of who signed in is one value they share.
// It was two instances for a while, each with databases of its own, until
// `jsport` could serve two names from one worker.
package main

import (
	"context"
	"log"
	"log/slog"

	drpc "github.com/lesomnus/grpc-dgram"
	"github.com/lesomnus/grpc-dgram/transport/jsport"

	otlog "github.com/lesomnus/otx/log"
	pdauth "github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/gate"

	// SQLite in a worker of its own. The other driver runs the engine on
	// wazero, which is a wasm runtime written in Go, so here it would be wasm
	// inside wasm.
	_ "github.com/lesomnus/payday/config/dbsqlite3wasm"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/console"
	"github.com/lesomnus/roster/server/me"
	"github.com/lesomnus/roster/server/vouch"
	"github.com/lesomnus/roster/wasm/sandbox"
)

// Who the page signs in as, and the password it does it with.
//
// Written down rather than generated, because a sandbox nobody can sign in to
// is a sandbox nobody uses -- and there is no second channel here to print a
// generated one on. It is not a credential for anything: there is one of these
// servers, it is inside the page, and it is gone on reload.
const (
	operator = "ops"
	password = "sandbox"

	// AdminEntryPoint is the name the admin server is published under, and
	// what `ts/console/main.tsx` dials for the customers screen.
	AdminEntryPoint = "drpcAdmin"
)

func main() {
	// With a logger in it, or what the stack has to say -- a resolver that
	// failed, and why -- goes nowhere, and the page shows a status code.
	ctx := otlog.Into(context.Background(), slog.Default())

	// Both planes, held in memory rather than in OPFS -- which is the decision
	// that makes a reload a fresh deployment. A sandbox that remembered would
	// be a sandbox somebody has to clear.
	//
	// Two DSNs, because they are two databases and that is the whole of what
	// the control plane is. One name would be one database with both planes'
	// rows in it, which is the arrangement roster spends a decision refusing.
	//
	// The leading slash is load-bearing: `memdb` shares a database between
	// connections only under a name that begins with one, and without it every
	// connection in the pool has an invisible database of its own. The schema
	// was created on one, the seed written on it, and the first query that
	// happened to be answered on another said `no such table: holder`.
	s, err := cmd.Build(ctx, cmd.Config{
		Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:/data?vfs=memdb"},

		// Named, because payday refuses a deployment that leaves it unsaid --
		// `memory` is right for one replica and silently wrong for two, so the
		// answer has to be written rather than defaulted. Here it is right by
		// construction: there is exactly one of this server and it is inside
		// the page.
		Watch: config.WatchConfig{Broker: config.BrokerMemory},

		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:/control?vfs=memdb"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	// The schema is created rather than migrated. In a process that would be
	// the wrong way round -- versioned migrations are what a deployment runs --
	// but there is no database here that outlives the page, so there is nothing
	// for a migration to move.
	if err := s.Ent.Schema.Create(ctx); err != nil {
		log.Fatal(err)
	}
	if err := s.Control.Ent.Schema.Create(ctx); err != nil {
		log.Fatal(err)
	}

	if err := seed(ctx, s); err != nil {
		log.Fatal(err)
	}

	// A server that is not gRPC's, taking the same services.
	gw := jsport.NewGateway()

	// The same interceptors the process serves with, because the stack behind
	// them is the same stack: `Walled` reads a frame and refuses a request that
	// has none, so a server registered without these answers "who is asking?"
	// to everything. `cmd.Public` rather than payday's default, or the sign-in
	// itself is refused for having no caller.
	op := &sandbox.Operator{}
	who := sandbox.Believe(op)
	srv := drpc.NewServer(gw,
		drpc.ChainUnaryInterceptors(
			pdauth.InterceptorUnary(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Unary(cmd.Policy(s.Control.Ent)),
		),
		drpc.ChainStreamInterceptors(
			pdauth.InterceptorStream(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Stream(cmd.Policy(s.Control.Ent)),
		),
	)

	// The control plane's rows, which is what the console is for: what
	// `cmd.GrpcControl` puts on `control.http`, less what a page never calls.
	cmd.Register(srv, s.Control.Walled)
	app.RegisterMeServiceServer(srv, me.New(s.Control.Ent, cmd.Everything(s.Control.Ent), me.WithWrites(s.Control.Walled)))
	app.RegisterAuthServiceServer(srv, sandbox.Auth(console.Auth(s.Control.Ungated, s.Control.Ent, s.Sessions), s.Control.Ent, op))
	app.RegisterIssueServiceServer(srv, console.Issue(s.Control.Walled, s.Control.Ent))

	// The admin server: `cmd.GrpcAdmin`'s chain, less what a message port has
	// no use for -- the deadline, the limiter, the closed-off methods -- with
	// the same remembered operator where the session cookie would be read.
	// `Intent` stays: it is what makes a write here leave a row in the control
	// plane first, and a sandbox that skipped it would be exercising a
	// different stack -- and here that row lands in the control plane the
	// deployment screen reads, as it does in a real deployment.
	admin, err := cmd.Admin(s)
	if err != nil {
		log.Fatal(err)
	}
	agw := jsport.NewGateway(jsport.WithEntryPoint(AdminEntryPoint))
	asrv := drpc.NewServer(agw,
		drpc.ChainUnaryInterceptors(
			pdauth.InterceptorUnary(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Unary(cmd.Policy(s.Control.Ent)),
			cmd.Intent(s.Control.Ent),
		),
		drpc.ChainStreamInterceptors(
			pdauth.InterceptorStream(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Stream(cmd.Policy(s.Control.Ent)),
		),
	)
	cmd.Register(asrv, admin)
	app.RegisterVouchServiceServer(asrv, vouch.New(admin, admin, vouch.WithKeys(s.Keyring)))

	// Publishing the first entry point is the readiness signal, so nothing may
	// be published before the registration above is done. The second may come
	// up after the page is ready: a dial to a name not yet published waits for
	// it. Exactly one Serve blocks main -- a main that returns takes the
	// instance down -- and the other's error is reported rather than dropped,
	// since a name collision is refused without publishing and would otherwise
	// reach the page only as a dial that times out.
	go func() {
		if err := agw.Serve(ctx, asrv); err != nil {
			log.Fatal(err)
		}
	}()
	log.Fatal(gw.Serve(ctx, srv))
}

// seed is `roster init` with a password somebody can actually type.
//
// The command generates one and prints it once, which is right where there is a
// terminal to print to and useless where there is not.
func seed(ctx context.Context, s *cmd.Server) error {
	// The password is given rather than generated, because a page has no
	// terminal to print a generated one on.
	if _, err := cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: operator, Password: password}); err != nil {
		return err
	}

	return nil
}
