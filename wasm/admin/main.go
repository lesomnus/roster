//go:build js && wasm

// The admin listener, in the page: the second instance the sandbox runs.
//
// `wasm/main.go` is the control plane -- what `roster serve` answers on
// `control.http`, where an operator signs in and manages the deployment. The
// customers screen reaches a **third** listener, `admin.http`, which serves the
// data plane with no wall on it (`cmd/admin.go`). One wasm instance answers
// one message port, so the sandbox runs two: this is the other one, and the
// page opens it beside the first exactly as it dials `admin.http` beside
// `control.http` against a real deployment.
//
// # Two instances are two deployments, and why that does not show
//
// Each instance carries its own databases. So the operator who signed in on
// the control instance is a different row from the operator this instance
// resolves the caller to, and a customer stood up here is in this instance's
// data plane and nowhere in the other's. Nothing on the console can tell: the
// control plane never reads the data plane (that is the wall), the customers
// screen reads only this listener, and what joins them in a real deployment is
// the session cookie, which a message port does not carry either way --
// `auth.Plain` stands in for it on both. The one thing lost is the audit
// `Intent` writes into the control plane before each write here, which the
// deployment screen's trail on the other instance will not show.
//
// Sharing one instance between two ports would need `jsport` to serve two
// entry points from one worker, which it does not today; when it does, this
// file folds into `wasm/main.go` and nothing on the page changes but a URL.
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

	_ "github.com/lesomnus/payday/config/dbsqlite3wasm"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
	"github.com/lesomnus/roster/wasm/sandbox"
)

// The same operator as `wasm/main.go`, so that the caller this instance
// resolves is the one the page signed in as on the other.
const (
	operator = "ops"
	password = "sandbox"
)

func main() {
	// With a logger in it, or what the stack has to say -- a resolver that
	// failed, and why -- goes nowhere, and the page shows a status code.
	ctx := otlog.Into(context.Background(), slog.Default())

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:/data?vfs=memdb"},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:/control?vfs=memdb"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	if err := s.Ent.Schema.Create(ctx); err != nil {
		log.Fatal(err)
	}
	if err := s.Control.Ent.Schema.Create(ctx); err != nil {
		log.Fatal(err)
	}

	// The operator, and one customer to open -- a customers screen with nobody
	// on it shows nothing worth working on.
	if _, err := cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: operator, Password: password}); err != nil {
		log.Fatal(err)
	}

	admin, err := cmd.Admin(s)
	if err != nil {
		log.Fatal(err)
	}

	gw := jsport.NewGateway()

	// The sign-in happened on the other instance, so there is nothing here to
	// remember: the caller is the operator `Seed` wrote, from the start.
	op := &sandbox.Operator{}
	name, err := sandbox.Own(ctx, s.Control.Ent, operator)
	if err != nil {
		log.Fatal(err)
	}
	op.Set(name)
	who := sandbox.Believe(op)

	// `cmd.GrpcAdmin`'s chain, less what a message port has no use for -- the
	// deadline, the limiter, the closed-off methods -- and with the remembered
	// operator where the session cookie would be read, for the reason
	// `wasm/main.go` gives. `Intent` stays: it is what makes a write here leave
	// a row in the control plane first, and a sandbox that skipped it would be
	// exercising a different stack.
	srv := drpc.NewServer(gw,
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

	cmd.Register(srv, admin)
	app.RegisterVouchServiceServer(srv, vouch.New(admin, admin, vouch.WithKeys(s.Keyring)))

	log.Fatal(gw.Serve(ctx, srv))
}
