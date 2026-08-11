package cmd

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/migrate"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/internal/ent"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/vouch"
)

// Server is a built app: the database it runs on and the two stacks it answers
// through.
type Server struct {
	Db  *sql.DB
	Ent *ent.Client

	// Drv is what the client was built on, kept because a transaction is begun
	// on a driver and a `*ent.Client` does not hand out the one it holds. It is
	// what `pd.Batch` puts a whole stack onto.
	Drv dialect.Driver

	// Dialect is what that driver speaks, which `Open` worked out and nothing
	// else can. It is kept because the guard below needs it, and re-deriving it
	// from the configuration would be a second answer to a question already
	// answered.
	Dialect string

	// Watch is what a change is published to once the call that made it has
	// answered. The broker is named rather than defaulted: the one that
	// publishes in this process is right for one replica and **silently wrong**
	// for two, since a subscriber on one never hears about a write on another.
	Watch *watch.Watch

	// Walled is what a caller reaches, and Ungated is what the deployment does
	// its own work through -- putting the first tenant there, working out who
	// is calling. Neither is a privilege anybody holds: the second is a server
	// instance somebody was handed, so going around the wall is a line of
	// wiring a reader can find rather than a rule that opens up whenever
	// nobody is asking.
	Walled  app.Server
	Ungated app.Server

	// Spin is whatever this deployment has to run besides answering requests.
	// It is a slice rather than a method because a server with nothing to run
	// should write nothing at all; see `payday/spin`.
	Spin []any
}

// Build opens the database and stacks the servers.
//
// The two hooks are the whole of what payday puts in the write and read paths,
// and both come out of what the schema declared: [pd.Minter] stamps a new row
// with the domain of its entity and refuses one of another, [pd.Wall] narrows
// every read to the tenants the caller may see.
func Build(ctx context.Context, c Config) (*Server, error) {
	db, dialect, err := c.Db.Open(ctx)
	if err != nil {
		return nil, err
	}

	// The ent client is the app's to build, which is why config hands back a
	// *sql.DB and the dialect rather than a client: the client is generated
	// into this app from this app's schema, and payday has no name for it.
	drv := entsql.OpenDB(dialect, db)
	client := ent.NewClient(ent.Driver(drv))

	// The server that talks to the database, twice: once as it is, and once
	// with the wall on it.
	//
	// Two things are said to it rather than to the stack, and for the same
	// reason -- both are about the statement that runs. The trail is kept by
	// the servers that do the writing, since every RPC that changes anything
	// has to report itself from inside the transaction that changes it. The
	// wall is a predicate and a predicate belongs in the WHERE.
	b, err := c.Watch.Build()
	if err != nil {
		db.Close()
		return nil, err
	}

	w := watch.New(b)

	// The recorders, in the order they are told. The trail is required -- a
	// write it could not account for is undone -- and the rest say for
	// themselves whether they are; see `bare.Recorders`.
	// Every write is told to these, in this order, inside the transaction that
	// makes it. It is one value rather than the option given three times
	// because `WithRecorder` refuses to be given twice: neither losing one nor
	// quietly appending in call order is a thing a framework should decide, so
	// the order is written here where it can be read.
	rec := bare.Recorders{pd.Recorder(), pd.WatchRecorder(w)}
	if c.Watch.Outbox {
		// Last, so that a queue this could not be written to undoes a write
		// which the trail and the in-process publish have already agreed to
		// rather than the other way round. It makes no difference to what is
		// committed -- either way nothing is -- and it makes the log read in
		// the order things were tried.
		rec = append(rec, pd.OutboxRecorder())
	}

	opts := []bare.Option{bare.WithMinter(pd.Minter()), bare.WithRecorder(rec)}

	sink, err := pd.NewSink(client, opts...)
	if err != nil {
		db.Close()
		return nil, err
	}

	walled, err := pd.NewSink(client, append(opts, bare.WithScope(pd.Wall()))...)
	if err != nil {
		db.Close()
		return nil, err
	}

	// The stack a caller reaches. `pd.Gate` is outermost, so nothing behind it
	// asks again.
	// `core` is inside the gate and outside the sink: it reads through the wall
	// to make its judgements, so it must be behind whatever installs one, and it
	// refuses before the write happens rather than after.
	stacked, err := app.Build(walled.WithWatch(w), core.Build(), pd.AuditBuild(), pd.GateBuild())
	if err != nil {
		db.Close()
		return nil, err
	}

	// And the same servers with no wall and no gate, which is what the
	// deployment does its own work through. It is not a privilege anybody
	// holds: it is an instance somebody was handed, so going around the wall
	// is a line of wiring a reader can find rather than a rule that opens up
	// whenever nobody is asking.
	// The same rules with no wall. Going around the wall is not going around
	// what this app means -- an identity linked by `init` or by an admin console
	// is still an identity, and a subject that is an email address is still
	// wrong.
	ungated, err := app.Build(sink.WithWatch(w), core.Build(), pd.AuditBuild())
	if err != nil {
		db.Close()
		return nil, err
	}

	s := &Server{Db: db, Ent: client, Drv: drv, Dialect: dialect, Watch: w, Walled: stacked, Ungated: ungated}
	if c.Watch.Outbox && b != nil {
		// The loop that makes an event durable. It is not a layer and not a
		// method on any server -- `spin.Run` finds it in whatever is handed
		// over, which is what keeps a server that has no background work from
		// carrying an empty method saying so.
		s.Spin = append(s.Spin, pd.Drain(client, b, c.Watch.Every()))
	}

	return s, nil
}

func (s *Server) Close() error { return s.Db.Close() }

// Grpc builds the server every call arrives at.
//
// The chain is payday's and the order is this app's to read: what records a
// call is outside everything, then the recovery, then the deadline a call that
// named none is given, then how often one caller may ask, then what is closed
// to callers entirely.
//
// It is separate from [Server.Serve] so that a test can travel exactly this
// and answer on a listener that is a channel; see pdtest.
func (s *Server) Grpc(ctx context.Context, c Config, opts ...grpc.ServerOption) *grpc.Server {
	// Who is calling comes first, since everything after it reads the frame.
	// `Plain` believes what the caller writes, which is right for a sandbox
	// and for tests and is not something to serve where anyone can reach it.
	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Server.CallTimeout())).
		WithUnary(auth.InterceptorUnary(auth.Plain(), Resolver(s.Ungated), public)).
		WithStream(auth.InterceptorStream(auth.Plain(), Resolver(s.Ungated), public)).
		WithUnary(grpcx.LimitUnary(c.Server.Limiter(), gate.ByTenant())).
		With(gate.Interceptor(nil)).
		With(s.Watch.Interceptor()).
		WithUnary(grpcx.ClosedUnary(closed(c)))

	os := append(opts, chain.ServerOptions()...)
	os = append(os, c.Server.GrpcOptions()...)

	g := grpc.NewServer(os...)
	register(g, s.Walled)

	// The one service that is not an entity: it answers yes or no about a row
	// nothing else may read. See `server/vouch`.
	app.RegisterVouchServiceServer(g, vouch.New(s.Ungated, s.Walled))

	// The batch, with the same rules the chain above enforces -- read off the
	// same configuration rather than written out again, which is the only way
	// the two stay in step. What they enforce by looking at the method gRPC
	// dispatched, this enforces per operation.
	//
	// `Closed` is replaced with the same function the chain got, and that is not
	// tidiness: `ServerConfig.Guard` fills it from the configuration alone, so a
	// guard left as it came would let a batch carry the credential reads that
	// are closed everywhere else.
	guard := c.Server.Guard(nil)
	guard.Closed = closed(c)

	if b, err := pd.Batch(s.Walled, s.Drv, guard); err == nil {
		pdpb.RegisterBatchServiceServer(g, b)
	} else {
		// A deployment that closed nothing, limits nothing and has no policy.
		// It is a real thing to be and it is also what a guard nobody filled in
		// looks like, so the batch is not served rather than served open.
		log.From(ctx).WarnContext(ctx, "no batch", slog.String("why", err.Error()))
	}

	return g
}

// register puts every service on the wire, and it is written out rather than
// being `app.RegisterServer` because of the ones that are missing.
//
// `CredentialService` and `ApiKeyService` are not here. Each has a generated
// `Get` that answers with whatever columns it was asked for, and in both cases
// one of them is a verifier -- a password hash, a key hash. Serving them is
// publishing those to anybody the wall lets read a row. The rows still exist
// and this app still reads them, in process, but there is no method on this
// server that answers with one.
//
// It is said here rather than in the schema because there is nowhere in the
// schema to say it: payday extends `MessageOptions` only, so no field can be
// declared written-and-never-read. See PLAN.md, D13 and F6.
//
// Written out has a cost worth naming: an entity added to the schema tomorrow
// is not served until somebody adds a line here. That is the direction to fail
// in. The other arrangement -- serve everything, then take one away -- fails by
// publishing something nobody meant to, and it fails silently.
func register(g grpc.ServiceRegistrar, s app.Server) {
	app.RegisterTenantServiceServer(g, s.Tenant())
	app.RegisterHolderServiceServer(g, s.Holder())
	app.RegisterIdentityServiceServer(g, s.Identity())
	app.RegisterEmailServiceServer(g, s.Email())
	app.RegisterSiteServiceServer(g, s.Site())
	app.RegisterTeamServiceServer(g, s.Team())
	app.RegisterSiteMembershipServiceServer(g, s.SiteMembership())
	app.RegisterTeamMembershipServiceServer(g, s.TeamMembership())
	app.RegisterAuditServiceServer(g, s.Audit())
	app.RegisterOutboxServiceServer(g, s.Outbox())
}

// closed is what this server does not answer at all.
//
// Whatever the configuration closed, and `CredentialService` on top of it. Not
// registering it is already enough for gRPC, which dispatches by name and has
// nothing to dispatch to -- this is for the batch, which arrives as one method
// carrying many and would otherwise be a way to ask for exactly the reads that
// were taken off the wire.
//
// So the two are one function used twice rather than two lists that agree
// today.
func closed(c Config) func(method string) bool {
	was := c.Server.Closed()

	return func(method string) bool {
		for _, v := range []string{
			app.CredentialService_ServiceDesc.ServiceName,
			app.ApiKeyService_ServiceDesc.ServiceName,
		} {
			if strings.HasPrefix(method, "/"+v+"/") {
				return true
			}
		}

		return was != nil && was(method)
	}
}

// public is what this app answers without asking who is calling.
//
// **Nothing.** `VouchService/Verify` was briefly listed here, and the reason is
// a mistake worth leaving written down: it confused the person signing in with
// the **caller**.
//
// The person signing in has no credential yet -- that is the thing they are
// asking for. But they are not who is calling. The caller is custody, or a
// Login App, or an admin console, and every one of those is a machine holding a
// certificate long before any of this. roster is called by machines and never
// by a browser, which PLAN.md decided before any of it was written.
//
// Public gave up two things for nothing. Anybody who could reach the port could
// guess passwords at the whole organisation -- and not slowly, since
// `grpcx.Limit` counts per tenant off the frame and a public call has no frame.
// And the trail could not say which service asked, so a compromised product app
// looked exactly like a stranger.
//
// The lockout in `server/vouch` is unaffected and was never meant to be the
// first line.
func public(method string) bool { return auth.PublicDefault(method) }

// Serve answers on `l` until the context is done.
func (s *Server) Serve(ctx context.Context, c Config, l net.Listener) error {
	g := s.Grpc(ctx, c)

	stop, err := s.serveHttp(ctx, c, g)
	if err != nil {
		return err
	}
	defer stop()

	go func() {
		<-ctx.Done()
		g.GracefulStop()
	}()

	return g.Serve(l)
}

// serveHttp is the second listener, for whatever cannot speak gRPC -- which is
// every browser. Nothing is opened unless the configuration named an address.
//
// It is the **same** `g`: a page reaches the handlers a gRPC client reaches,
// through the interceptors a gRPC client goes through, behind the same wall.
// There is no second stack here for a rule to be missing from.
func (s *Server) serveHttp(ctx context.Context, c Config, g *grpc.Server) (func(), error) {
	if !c.Server.Http.Serves() {
		return func() {}, nil
	}

	h, err := web.New(c.Server.Http, g)
	if err != nil {
		return nil, err
	}

	// Whatever this app serves over HTTP goes here, on the same mux and behind
	// the same cross-origin answer. A login endpoint is the case payday left a
	// seam for and cannot fill: `auth` reads a credential and does not issue
	// one, and issuing is an HTTP endpoint.
	//
	//	h.Handle("/login", login(s.Ungated))
	//
	// A gRPC path is `/<service>/<method>`, so an ordinary route cannot collide
	// with one -- and `ServeMux` panics rather than shadowing if one somehow
	// does.

	l, err := net.Listen("tcp", c.Server.Http.Addr)
	if err != nil {
		return nil, err
	}

	srv := &http.Server{Handler: h}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.From(ctx).ErrorContext(ctx, "http", slog.String("err", err.Error()))
		}
	}()

	log.From(ctx).InfoContext(ctx, "http", slog.String("addr", l.Addr().String()))

	return func() { srv.Close() }, nil
}

// NewCmdServe is `<app> serve`.
//
// It is the app's own and not payday's, for the reason at the top of
// `cmd/config.go`: the body of this command is the stack, and a framework that
// supplied it would be hiding the one thing a reader of an app most needs to
// see.
func NewCmdServe(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer requests",

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			s, err := Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// The database, before anything is served on it.
			//
			// payday owns some of this app's schema, so a field added to a
			// holder there arrives in `internal/ent` the next time this app
			// generates -- and nothing about that is loud. It compiles, the
			// tests pass against a database the tests just created, and the
			// first sign of trouble is a column that is not there in the one
			// handler that reads it.
			//
			// So one of the two happens here, and which one is the operator's
			// to say. `db.migrate: true` hands the serving process the right to
			// alter tables, which is right for development and is a thing to
			// decide on purpose; anything else and the shapes have to agree
			// already.
			if c.Db.Migrate {
				if err := s.Ent.Schema.Create(ctx); err != nil {
					return err
				}
			} else if err := migrate.Check(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
				return err
			}

			l, err := net.Listen("tcp", c.Server.ListenAddr())
			if err != nil {
				return err
			}

			log.From(ctx).InfoContext(ctx, "grpc", slog.String("addr", l.Addr().String()))

			// The background work and the server, together: whichever stops
			// first stops the other. A loop that keeps running under a server
			// that is going down is a process that will not exit, and a server
			// that goes on answering after its outbox drain has died is one
			// that accepts writes it will never publish.
			g, ctx := errgroup.WithContext(ctx)
			g.Go(func() error { return spin.Run(ctx, slices.Values(s.Spin)) })
			g.Go(func() error { return s.Serve(ctx, *c, l) })

			return g.Wait()
		}),
	}
}
