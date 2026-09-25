//go:build js && wasm

// The consoles' server, in the page.
//
// A reload is a fresh deployment: two new databases, `roster init` run again,
// nothing left over. Somebody working on either console starts no backend,
// migrates nothing, and does not have to remember what state they left it in.
//
// # What is the same, and what is not
//
// The Go half is the server the process runs -- the same generated services,
// the same stack, the same wall from the same schema, and both planes built
// the way `Build` builds them. Two things differ and both are one line: the
// databases are SQLite in a Worker rather than files, and calls arrive over a
// message port rather than HTTP/2.
//
// # Signing in, and the two things a page cannot have here
//
// `AuthService` is served for real: the password is checked by the same
// `vouch`, and a wrong one is refused. What does not work is the **cookie** --
// a message port has no browser cookie jar, so `set-cookie` in response
// metadata reaches nobody, and every call after the sign-in arrives naming
// nobody.
//
// So the instance remembers who signed in (`wasm/sandbox`): `auth.Plain`
// behind it believes a caller that writes a name, and a call that writes none
// is taken to be whoever the last accepted sign-in named, until a sign-out.
// The page needs no branch: it calls `AuthService.SignIn` exactly as it does
// against a real server, and what follows is that person.
//
// The second is the **name the browser arrived at**, which is how the data
// plane decides which tenant a sign-in is about (`cmd.Hosted`). A message port
// carries no `Host`, so `sandbox.ArrivedAt` writes one and the deployment's own
// lookup does the rest -- through the `Host` row `seed` puts there, which is
// why that row is seeded rather than the tenant being answered directly.
//
// There is one caller in the page and this is a note of who they said they
// were; it is a sandbox being a sandbox, and the reason `Plain` is here at all.
//
// # One instance, a server per listener
//
// One download, one compile, one pair of databases, and a `drpc.Server` under
// an entry point per listener a deployment would open. A page dials by name on
// the same socket, which is what `admin.http` and `server.http` are to a
// browser. It was two instances for a while, each with databases of its own,
// until `jsport` could serve two names from one worker.
//
//	(default)    the control plane: the admin console's own rows
//	drpcAdmin    the data plane with no wall, behind the operator's session --
//	             `cmd.GrpcAdmin`, which is what the customers screen reaches
//	drpcUser     the data plane **walled**, behind a roster user's session --
//	             `Server.Grpc`, which is what the user console reaches
//	drpcUngated… the same stacks with no wall, for the devtools panel
//
// The caller is remembered per server pair rather than once: an operator and a
// roster user are holders of different planes, and a page is one or the other.
package main

import (
	"context"
	"log"
	"log/slog"

	drpc "github.com/lesomnus/grpc-dgram"
	"github.com/lesomnus/grpc-dgram/transport/jsport"

	"github.com/lesomnus/otx"
	otlog "github.com/lesomnus/otx/log"
	"github.com/lesomnus/otx/otxgrpc"
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
	"github.com/lesomnus/roster/wasm/schema"
)

// Who the pages sign in as, and the password they do it with.
//
// Written down rather than generated, because a sandbox nobody can sign in to
// is a sandbox nobody uses -- and there is no second channel here to print a
// generated one on. It is not a credential for anything: there is one of these
// servers, it is inside the page, and it is gone on reload.
//
// The same alias on both planes, and deliberately: `admin` on the control plane
// is the roster operator the admin console signs in, and `admin` in `contoso`
// is the tenant administrator `Tenant.Add` wrote, which is who the user console
// signs in. Two rows in two databases that have nothing to do with each other,
// which is the thing the two pages are there to show.
const (
	operator = "admin"
	password = "admin"

	// tenant is the customer `seed` stands up, and hostAt is the name it
	// answers at -- the one `sandbox.ArrivedAt` writes, because a message port
	// carries no `Host` for `cmd.Hosted` to read.
	//
	// `.example` is reserved and resolves nowhere, which is what a name in a
	// sandbox should be: nothing here is ever dialed, and a plausible one would
	// be a name somebody tries.
	tenant = "contoso"
	hostAt = "contoso.roster.example"

	// AdminEntryPoint is the name the admin server is published under, and
	// what `ts/console/main.tsx` dials for the customers screen.
	AdminEntryPoint = "drpcAdmin"

	// UserEntryPoint is the data plane **walled**, which is what the user
	// console is (#34): a roster user signs in there and sees their own tenant,
	// narrowed by the wall rather than by what the page chose to draw.
	//
	// A server of its own rather than the default one with a different caller,
	// because they are different planes: the default entry point answers over
	// the control plane's database, where a roster user has no row at all.
	UserEntryPoint = "drpcUser"

	// The stacks with no wall -- the control plane's, the data plane's as the
	// admin listener reaches it, and the data plane's as the user console does
	// -- for the devtools panel's "past the wall" switch and nothing else.
	//
	// `Ungated` is never handed to anything a caller can reach (CLAUDE.md), and
	// this is not that: the server is inside the page, the page's caller is the
	// page, and what the wall would protect here is the page from itself. What
	// it buys is the question the walled path cannot answer -- a row that is
	// not there and a row that is not visible look the same through the wall
	// -- and a served deployment registers no such thing, so a page that was
	// never handed the transport cannot offer the switch (payday's
	// `react/devtools`).
	//
	// `drpcAdminUngated` and `drpcUserUngated` are the **same** rows behind the
	// same `s.Ungated`, under two names: a page dials the one beside the server
	// it is reading, and neither page has to know the other exists.
	UngatedEntryPoint      = "drpcUngated"
	AdminUngatedEntryPoint = "drpcAdminUngated"
	UserUngatedEntryPoint  = "drpcUserUngated"
)

func main() {
	// With a logger in it, or what the stack has to say -- a resolver that
	// failed, and why -- goes nowhere, and the page shows a status code. This
	// is the same telemetry a deployment gets from `otel:` left unsaid: the
	// pretty exporter, one line per call from `otxgrpc`'s logger.
	//
	// Nothing here says where those lines go. On Wasm `pretty` writes to
	// `console` rather than to `stderr` and leaves `fatih/color` on, and
	// `mkot` registers the writer that turns the escape codes into the `%c`
	// and CSS a browser console paints with. roster wrote both of those and
	// they are payday's now, which is where they belong -- there is one right
	// answer and every sandbox wanted it.
	ctx, o, err := (&config.OtelConfig{}).Build(context.Background(), config.Service{Name: "roster-sandbox", Scope: "github.com/lesomnus/roster"})
	if err != nil {
		log.Fatal(err)
	}
	logger := slog.New(o.SlogHandler())
	slog.SetDefault(logger)
	ctx = otlog.Into(ctx, logger)
	if err := otx.Start(ctx); err != nil {
		log.Fatal(err)
	}

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
		// One connection, because there is one of it. The engine is a single
		// JS thread in a worker, so a second connection buys no parallelism
		// and costs the lock: this build has no WAL, a writer excludes
		// everybody, and the loser is told SQLITE_BUSY rather than made to
		// wait -- a busy handler would sleep on the very thread that has to
		// deliver the other connection's COMMIT. payday measured eight
		// concurrent adds and three landed.
		Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:/data?vfs=memdb", MaxOpenConns: 1},

		// Named, because payday refuses a deployment that leaves it unsaid --
		// `memory` is right for one replica and silently wrong for two, so the
		// answer has to be written rather than defaulted. Here it is right by
		// construction: there is exactly one of this server and it is inside
		// the page.
		Watch: config.WatchConfig{Broker: config.BrokerMemory},

		// The floor is eight and the password above is five, so the sandbox
		// says so rather than carrying a password nobody remembers. A
		// deployment's own setting, set the way a deployment would set it, and
		// nothing the page could change.
		Vouch: cmd.VouchConfig{Password: cmd.PasswordConfig{MinLength: len(password)}},

		// The door the user console signs in at, which is the whole of what
		// `sign_in.enabled` turns on: `AuthService` on the data plane, over the
		// data plane's own rows, and a session table to mint into. Off by
		// default in a deployment for the reason `cmd.Public` gives about the
		// method it makes public; on here because a page with no door is a page
		// nobody can open.
		SignIn: cmd.SignInConfig{Enabled: true},

		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:/control?vfs=memdb", MaxOpenConns: 1},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	// The schema, as a script rather than as a migration. In a process that
	// would be the wrong way round -- versioned migrations are what a
	// deployment runs -- but there is no database here that outlives the page,
	// so there is nothing for a migration to move, and ent's migration engine
	// is Atlas: a diff planner, three SQL dialects and an HCL parser, ten
	// megabytes of what a browser downloads to decide what to do to a database
	// that does not exist yet. `wasm/schema` is the same tables as SQL, and a
	// test keeps it the same tables.
	//
	// Both planes take it, because they are the same entities in two databases.
	if err := schema.Load(ctx, s.Db); err != nil {
		log.Fatal(err)
	}
	if err := schema.Load(ctx, s.Control.Db); err != nil {
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
	op := &sandbox.Caller{}
	who := sandbox.Believe(op)
	srv := drpc.NewServer(gw,
		drpc.WithStatsHandler(otxgrpc.NewServerLogger(o)),
		drpc.ChainUnaryInterceptor(
			pdauth.InterceptorUnary(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Unary(cmd.Policy(s.Control.Ent)),
		),
		drpc.ChainStreamInterceptor(
			pdauth.InterceptorStream(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Stream(cmd.Policy(s.Control.Ent)),
		),
	)

	// The control plane's rows, which is what the admin console is for: what
	// `cmd.GrpcControl` puts on `control.http`, less what a page never calls.
	cmd.Register(srv, s.Control.Walled)
	app.RegisterMeServiceServer(srv, me.New(s.Control.Ent, cmd.Everything(s.Control.Ent), me.WithWrites(s.Control.Walled)))
	app.RegisterAuthServiceServer(srv, sandbox.Auth(
		console.Auth(s.Control.Ungated, s.Control.Ent, s.Sessions),
		sandbox.TheOneTenant(s.Control.Ent), op))

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
		drpc.WithStatsHandler(otxgrpc.NewServerLogger(o)),
		drpc.ChainUnaryInterceptor(
			pdauth.InterceptorUnary(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Unary(cmd.Policy(s.Control.Ent)),
			cmd.Intent(s.Control.Ent),
		),
		drpc.ChainStreamInterceptor(
			pdauth.InterceptorStream(who, sandbox.Resolver(cmd.Resolver(s.Control.Ungated, nil)), cmd.Public),
			gate.Stream(cmd.Policy(s.Control.Ent)),
		),
	)
	cmd.Register(asrv, admin)
	app.RegisterVouchServiceServer(asrv, vouch.New(admin, admin, vouch.WithKeys(s.Keyring), vouch.WithLockout(s.Lockout)))

	// The user console's server: the **walled** data plane, which is what
	// `Server.Grpc` puts on `server.http` -- less the deadline, the limiter and
	// the closed-off methods a message port has no use for, exactly as the
	// admin server above drops them.
	//
	// Its own remembered caller, because a roster user is not the operator: the
	// two are holders of two planes, and a page is signed in to one of them.
	// Sharing `op` would have an operator who signed in on the admin console be
	// the caller here, resolved against a database their row is not in.
	//
	// `cmd.Hosted` over `sandbox.ArrivedAt`, which is the second half of what a
	// message port cannot carry: the tenant a sign-in is about is the one whose
	// `Host` row claims the name the browser came in on, and here the sandbox
	// writes the name and the deployment's own lookup answers.
	user := &sandbox.Caller{}
	theirs := sandbox.Believe(user)
	at := sandbox.ArrivedAt(hostAt, cmd.Hosted(s.Ent))
	ugw := jsport.NewGateway(jsport.WithEntryPoint(UserEntryPoint))
	usrv := drpc.NewServer(ugw,
		drpc.WithStatsHandler(otxgrpc.NewServerLogger(o)),
		drpc.ChainUnaryInterceptor(
			pdauth.InterceptorUnary(theirs, sandbox.Resolver(cmd.Resolver(s.Ungated, s.Control.Ungated)), cmd.Public),
			gate.Unary(cmd.Policy(s.Ent)),
		),
		drpc.ChainStreamInterceptor(
			pdauth.InterceptorStream(theirs, sandbox.Resolver(cmd.Resolver(s.Ungated, s.Control.Ungated)), cmd.Public),
			gate.Stream(cmd.Policy(s.Ent)),
		),
	)
	cmd.Register(usrv, s.Walled)
	app.RegisterMeServiceServer(usrv, me.New(s.Ent, cmd.Everything(s.Ent), me.WithWrites(s.Walled)))
	app.RegisterAuthServiceServer(usrv, sandbox.Auth(
		console.Auth(s.Ungated, s.Ent, s.People, console.WithTenant(at)), at, user))
	app.RegisterVouchServiceServer(usrv, vouch.New(s.Ungated, s.Walled,
		vouch.WithKeys(s.Keyring), vouch.WithLockout(s.Lockout)))

	// The three unwalled stacks, for the panel: the same call log, no auth and
	// no gate, because there is nobody to be and nothing to refuse.
	cgw := jsport.NewGateway(jsport.WithEntryPoint(UngatedEntryPoint))
	csrv := drpc.NewServer(cgw, drpc.WithStatsHandler(otxgrpc.NewServerLogger(o)))
	cmd.Register(csrv, s.Control.Ungated)
	augw := jsport.NewGateway(jsport.WithEntryPoint(AdminUngatedEntryPoint))
	ausrv := drpc.NewServer(augw, drpc.WithStatsHandler(otxgrpc.NewServerLogger(o)))
	cmd.Register(ausrv, s.Ungated)
	uugw := jsport.NewGateway(jsport.WithEntryPoint(UserUngatedEntryPoint))
	uusrv := drpc.NewServer(uugw, drpc.WithStatsHandler(otxgrpc.NewServerLogger(o)))
	cmd.Register(uusrv, s.Ungated)

	// Publishing the first entry point is the readiness signal, so nothing may
	// be published before the registration above is done. The second may come
	// up after the page is ready: a dial to a name not yet published waits for
	// it. Exactly one Serve blocks main -- a main that returns takes the
	// instance down -- and the other's error is reported rather than dropped,
	// since a name collision is refused without publishing and would otherwise
	// reach the page only as a dial that times out.
	for _, v := range []struct {
		gw  *jsport.Gateway
		srv *drpc.Server
	}{{agw, asrv}, {ugw, usrv}, {cgw, csrv}, {augw, ausrv}, {uugw, uusrv}} {
		go func() {
			if err := v.gw.Serve(ctx, v.srv); err != nil {
				log.Fatal(err)
			}
		}()
	}
	log.Fatal(gw.Serve(ctx, srv))
}

// seed is `roster init` and the two writes a first customer takes, with a
// password somebody can actually type.
//
// The command generates one and prints it once, which is right where there is a
// terminal to print to and useless where there is not.
func seed(ctx context.Context, s *cmd.Server) error {
	// The password is given rather than generated, because a page has no
	// terminal to print a generated one on. The tenant arrives with the holder
	// that administers it, the `everything` role and the binding between them:
	// `Tenant.Add` writes all four (`server/core/tenant.go`).
	v, err := cmd.Seed(ctx, s, cmd.Seeding{Tenant: tenant, Holder: operator, Operator: operator, Password: password})
	if err != nil {
		return err
	}

	// The name the tenant answers at, which is what makes the user console's
	// sign-in resolvable at all: `cmd.Hosted` reads the name off the request
	// and finds the tenant whose `Host` row claims it. `sandbox.ArrivedAt`
	// writes the name; **this** is what turns it into a tenant, and it is a row
	// rather than a constant so that the sandbox exercises the lookup a
	// deployment does.
	if _, err := s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: v.Tenant.Bytes()}.Build(),
		Name:   hostAt,
	}.Build()); err != nil {
		return err
	}

	// And a way in for the tenant administrator, which `Seed` does not write:
	// `roster init` seeds the deployment's own operator and leaves a customer's
	// people to the operator who stands them up (`ts/console/customers.tsx`,
	// `stand`). Here there is nobody to do that and nowhere to print what they
	// would be handed, so it is the same written-down password as above.
	if _, err := s.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: v.Holder.Bytes()}.Build(),
		Secret: []byte(password),
	}.Build()); err != nil {
		return err
	}

	return nil
}
