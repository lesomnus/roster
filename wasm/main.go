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
// metadata reaches nobody.
//
// So the server behind it is `auth.Plain`, which believes what the caller
// writes. Every call after the sign-in is vouched for whether or not the cookie
// stuck, and the page needs no branch: it calls `AuthService.SignIn` exactly as
// it does against a real server, and what follows works.
//
// What that costs is worth saying plainly: **a wrong password refuses the
// sign-in and does not lock the rest of the page**, because nothing after it is
// checking a session. That is a sandbox being a sandbox. There is nobody else
// in the page to lie to, and the same arrangement is why `Plain` is here at all.
package main

import (
	"context"
	"fmt"
	"log"

	drpc "github.com/lesomnus/grpc-dgram"
	"github.com/lesomnus/grpc-dgram/transport/jsport"

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
)

func main() {
	ctx := context.Background()

	// Both planes, held in memory rather than in OPFS -- which is the decision
	// that makes a reload a fresh deployment. A sandbox that remembered would
	// be a sandbox somebody has to clear.
	//
	// Two DSNs, because they are two databases and that is the whole of what
	// the control plane is. One name would be one database with both planes'
	// rows in it, which is the arrangement roster spends a decision refusing.
	s, err := cmd.Build(ctx, cmd.Config{
		Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:data?vfs=memdb"},

		// Named, because payday refuses a deployment that leaves it unsaid --
		// `memory` is right for one replica and silently wrong for two, so the
		// answer has to be written rather than defaulted. Here it is right by
		// construction: there is exactly one of this server and it is inside
		// the page.
		Watch: config.WatchConfig{Broker: config.BrokerMemory},

		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:control?vfs=memdb"},
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
	// to everything.
	srv := drpc.NewServer(gw,
		drpc.ChainUnaryInterceptors(
			pdauth.InterceptorUnary(pdauth.Plain(), cmd.Resolver(s.Control.Ungated, nil), pdauth.PublicDefault),
			gate.Unary(cmd.Policy(s.Control.Ent)),
		),
		drpc.ChainStreamInterceptors(
			pdauth.InterceptorStream(pdauth.Plain(), cmd.Resolver(s.Control.Ungated, nil), pdauth.PublicDefault),
			gate.Stream(cmd.Policy(s.Control.Ent)),
		),
	)

	// The control plane's rows, which is what the console is for.
	app.RegisterServer(srv, s.Control.Walled)
	app.RegisterApiKeyServiceServer(srv, s.Control.Walled.ApiKey())
	app.RegisterAuthServiceServer(srv, console.Auth(s.Control.Ungated, s.Control.Ent, s.Sessions))
	app.RegisterIssueServiceServer(srv, console.Issue(s.Control.Walled, s.Control.Ent))

	// Publishing the entry point is the readiness signal, so nothing may be
	// published before the registration above is done -- and it blocks, because
	// a main that returns takes the instance down and the page sees its calls
	// start failing.
	log.Fatal(gw.Serve(ctx, srv))
}

// seed is `roster init`, and then a password somebody can actually type.
//
// The command generates one and prints it once, which is right where there is a
// terminal to print to and useless where there is not. This overwrites it with
// a constant, through the RPC that hashes one, so the argon2 parameters stay in
// one place.
func seed(ctx context.Context, s *cmd.Server) error {
	v, err := cmd.Seed(ctx, s, "acme", "admin", operator)
	if err != nil {
		return err
	}

	if err := console.SetPassword(ctx, s.Control.Ungated, v.Operator, password); err != nil {
		return fmt.Errorf("their password: %w", err)
	}

	return nil
}
