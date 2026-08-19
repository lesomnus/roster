package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
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
	"github.com/lesomnus/roster/server/console"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/me"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/session"
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

	// Control is who may call this deployment: roster again, on its own
	// database, holding keys rather than people. Nil when nothing named one,
	// which is a deployment that believes its callers.
	Control *Server

	// Auth is what reads a credential off a request. Nil is `auth.Plain`, and
	// it says so once in the log -- see [ControlConfig].
	Auth auth.Handler

	// Keys is whether this instance is the one that holds them, which is the
	// control plane and nothing else.
	//
	// It exists because `closed` cannot work it out. Both planes are the same
	// `Build` and the same `Grpc`, and `Control == nil` is true of the control
	// plane *and* of a deployment that has none -- so the one place they differ
	// has to be said rather than inferred.
	//
	// What it changes is one service. `ApiKeyService` is closed everywhere by
	// default because its generated `Get` answers with the verifier column; on
	// the port where the deployment's own operator manages keys, that is the
	// point. `CredentialService` stays closed on both, because a password hash
	// is nobody's to read.
	Keys bool

	// Keyring is what this deployment can wrap a seed with, from `vouch.keys`.
	//
	// Zero is a deployment that holds no second factor, which is every one that
	// has not said otherwise -- and asking it to hold one is refused rather
	// than answered with a seed in the clear.
	Keyring vouch.Keyring

	// Breached is whether a secret is one somebody has already lost, or nil
	// where this deployment has no way to know.
	Breached vouch.Breached

	// Sessions is the console's cookie: the endpoint that mints one and the
	// handler that reads it back. Nil where there is no control plane, since
	// the people who sign in are its holders and there would be nobody to be.
	//
	// In memory, which is right for one replica and **silently wrong** for two
	// -- a browser is signed in or out depending on which one the load balancer
	// picked, per request, with nothing in any log saying so. It is the same
	// trap the memory broker carries, and it is named here rather than
	// defaulted somewhere unread.
	Sessions *authsession.Sessions

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

	// The wall and the second axis, composed here because payday refuses two
	// `WithScope` calls -- which is what makes a stack that narrows by two
	// things a line somebody wrote rather than an accident of ordering.
	//
	// `pd.Grouped` has existed since the first generation and had nothing to
	// ask until now. What answers it is a caller's bindings: no site means the
	// whole tenant, and otherwise the sites they were bound in.
	narrow := bare.Scopes{pd.Wall(), pd.Grouped(Sets(client))}

	walled, err := pd.NewSink(client, append(opts, bare.WithScope(narrow))...)
	if err != nil {
		db.Close()
		return nil, err
	}

	// The stack a caller reaches. `pd.Gate` is outermost, so nothing behind it
	// asks again.
	// `core` is inside the gate and outside the sink: it reads through the wall
	// to make its judgements, so it must be behind whatever installs one, and it
	// refuses before the write happens rather than after.
	// `pd.Secret` is on the walled stack and on no other. What it clears is
	// what a caller is answered with; `vouch` and `keys` read the same columns
	// through `Ungated`, deliberately, because comparing a verifier is the whole
	// of their job.
	//
	// What keeps it out of the **trail** is not this layer -- the recorder is
	// behind every layer -- but the declaration on the field, which the recorder
	// reads for itself. See `Credential.secret`.
	stacked, err := app.Build(walled.WithWatch(w), core.Build(Rules(client)), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
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
	ungated, err := app.Build(sink.WithWatch(w), core.Build(Rules(client)), pd.AuditBuild())
	if err != nil {
		db.Close()
		return nil, err
	}

	// Read here rather than where it is used, so that a deployment whose keys
	// are malformed finds out when it starts rather than when somebody enrols.
	keyring, err := vouch.NewKeyring(c.Vouch.Keys)
	if err != nil {
		db.Close()
		return nil, err
	}

	// The corpus, checked for the order it has to be in rather than trusted:
	// one that is not sorted answers *no* to things that are in it, which is
	// the direction that fails quietly.
	var leaked vouch.Breached
	if c.Vouch.Breached != "" {
		if err := vouch.Sorted(c.Vouch.Breached); err != nil {
			db.Close()
			return nil, err
		}

		leaked, err = vouch.BreachedIn(c.Vouch.Breached)
		if err != nil {
			db.Close()
			return nil, err
		}
	}

	s := &Server{
		Db: db, Ent: client, Drv: drv, Dialect: dialect, Watch: w,
		Walled: stacked, Ungated: ungated, Keyring: keyring, Breached: leaked,
	}

	// The control plane: roster again, on its own database, holding keys rather
	// than people. See PLAN.md, D15 and `ControlConfig`.
	//
	// Built after this one and from a config with no `control` of its own,
	// which is what stops the recursion -- one level, and the innermost
	// instance answers to nothing but the CLI that seeds it.
	if c.Control.Serves() {
		control, err := Build(ctx, Config{
			Db: c.Control.Db,

			// Its own broker. A control plane publishing into the data plane's
			// would have a key change look like a person changing, to every
			// client watching.
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
		})
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("control: %w", err)
		}

		control.Keys = true

		s.Control = control
		// Two handlers, and the order is the rule `Seq` runs on: the first
		// that finds anything answers. `Acting` is only interested in a
		// request carrying `roster-as`, and passes on everything else, so a
		// request with just a key reaches `Bearer` exactly as before.
		//
		// It is first because a request carrying both is a delegated one: the
		// key in `authorization` is there to say who is presenting, not to be
		// the caller. Put second, `Bearer` would answer with the app and the
		// delegation would be ignored -- an app would silently read as itself,
		// across every tenant, on the page it wrote a delegation to narrow.
		s.Auth = auth.Seq(
			keys.Acting(control.Ungated, s.Ungated),
			auth.Bearer(keys.Store(control.Ungated, s.Ungated)),
		)

		// And the control plane authenticates with **its own** keys, which is
		// what makes it servable on a port at all.
		//
		// Without this it is `auth.Plain` -- built from a config with no
		// `control` of its own, which is what stops the recursion -- and
		// `Plain` believes whatever a caller writes. Right for something only
		// reachable by a Go call, and not something to put on a listener.
		//
		// Self-referential and it terminates for the same reason the outer one
		// does: looking a key up is a call into `control.Ungated`, not a
		// request to the port this handler is protecting.
		//
		// Only deployment keys, because there is no second plane below this
		// one. `roster key add` mints them; a customer's `rt_` is a data plane
		// thing and has no meaning here.
		// In a **table**, on the plane whose holders sign in.
		//
		// `MemStore` is what this was, and payday's own comment says what that
		// means: right for one replica and *silently wrong* for two, since a
		// cookie minted on one is unknown to the other -- intermittently, per
		// request, with nothing in any log saying why. `session.proto` carries
		// the rest, including why the table is roster's and not payday's.
		s.Sessions = authsession.New(session.New(control.Ent))

		// A console's cookie in front of a service's key, and **on this plane
		// only**.
		//
		// A session names a control plane holder, so it can be resolved only
		// where that row is. Put on the data plane's chain it would
		// authenticate and then resolve to nobody, since the two are separate
		// databases with no query between them -- which is also the answer to
		// why an operator has no standing in a customer's tenant. They
		// administer the deployment; a customer's people are the customer's.
		//
		// `Seq` takes the first handler that finds anything. The two read
		// different places -- a cookie and an `authorization` header -- so a
		// request carries at most one and the order decides nothing but which
		// answers a request carrying both. A cookie naming **nobody** does not
		// fall through: it is a credential that is there and wrong.
		control.Auth = auth.Seq(
			s.Sessions.Handler(),
			auth.Bearer(keys.Store(control.Ungated, nil)),
		)
	}
	// Collecting expired delegations, and only where there are any: they are
	// minted on this plane and the control plane has no such rows. It is not
	// what makes an expired one refused -- [keys.findDelegation] is -- and the
	// comment on `Sweep` says which half is which.
	s.Spin = append(s.Spin,
		keys.Sweep(s.Ent, keys.Swept),
		vouch.Sweep(s.Ent, vouch.Swept),
	)

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
func (s *Server) Grpc(ctx context.Context, c Config, opts ...grpc.ServerOption) (*grpc.Server, error) {
	// Who is calling comes first, since everything after it reads the frame.
	// `Plain` believes what the caller writes, which is right for a sandbox
	// and for tests and is not something to serve where anyone can reach it.
	// `Plain` unless a control plane was named, which believes whatever a
	// caller writes -- right for a checkout and not something to serve where
	// anyone can reach it. It says so once per process.
	h := s.Auth
	if h == nil {
		h = auth.Plain()
	}

	// The resolver is given the control plane, which is what makes an api-key
	// identifier a row rather than a shape: see `keyed`. Nil here is a
	// deployment with no keys, and then there is no such identifier to resolve.
	var keyrows app.Server
	if s.Control != nil {
		keyrows = s.Control.Ungated
	}

	r := Resolver(s.Ungated, keyrows)

	// One function, installed twice, because a stream and a unary call are two
	// interceptors and `closed` is one answer. Built here rather than called
	// twice below so that the two cannot come to disagree.
	shut := s.closed(c)

	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Server.CallTimeout())).
		WithUnary(auth.InterceptorUnary(h, r, public)).
		WithStream(auth.InterceptorStream(h, r, public)).
		WithUnary(grpcx.LimitUnary(c.Server.Limiter(), gate.ByTenant())).
		With(gate.Interceptor(Policy(s.Ent))).
		With(s.Watch.Interceptor()).
		WithUnary(grpcx.ClosedUnary(shut)).
		WithStream(grpcx.ClosedStream(shut))

	// A certificate that cannot be read is a server that must not start, which
	// is why this answers with an error: `GrpcOptions` reads the files
	// `server.tls` names. `GrpcAdmin` has had the shape all along.
	vs, err := c.Server.GrpcOptions()
	if err != nil {
		return nil, err
	}

	os := append(opts, chain.ServerOptions()...)
	os = append(os, vs...)

	g := grpc.NewServer(os...)
	register(g, s.Walled)

	// The one service that is not an entity: it answers yes or no about a row
	// nothing else may read. See `server/vouch`.
	// The rule about who may write whose credential travels with the service,
	// because `VouchService` is hand-written and no layer wraps it. Same rules
	// the gate reads, handed over rather than asked for a second time.
	app.RegisterVouchServiceServer(g, vouch.New(s.Ungated, s.Walled,
		vouch.WithReach(core.Reaching(Rules(s.Ent))),
		vouch.WithKeys(s.Keyring),
		vouch.WithBreached(s.Breached)))

	// What a front door asks before it knows anything, and therefore through
	// the server the wall was never installed on. Neither RPC answers with a
	// row -- one identifier or one provider name -- which is what keeps that
	// from being a hole; `server/front` says it at length.
	app.RegisterFrontServiceServer(g, front.New(s.Ungated))

	// And what a caller is, in one round trip. It takes nothing, so there is
	// nobody but the caller to ask about; see `server/me`.
	// With the stack its two writes go through. The reads go to ent because the
	// missing subject has already narrowed them; the writes have rules on them,
	// and the rules live in a layer.
	app.RegisterMeServiceServer(g, me.New(s.Ent, Everything(s.Ent), me.WithWrites(s.Walled)))

	// What a token stands for, for a product app that was handed one.
	//
	// payday's contract rather than this app's, so an app in front changes one
	// line of wiring if it ever changes identity stores; see `auth.Remote`.
	//
	// Built on **this** plane's server, which is what decides the population it
	// can answer about: a control-plane key lives in the other database and
	// there is no query from here to there, so it is not that this refuses to
	// introspect one -- it cannot see one. That is the same property the two
	// databases were separated for.
	//
	// `Ungated` for the reason `vouch.Verify` uses one: this is asked before
	// anybody has been resolved, so there is no frame to narrow by, and the row
	// is unreadable through the wall anyway -- `ApiKeyService` is unregistered
	// and closed because its generated `Get` answers with the verifier.
	pdpb.RegisterTokenServiceServer(g, keys.Service(s.Ungated))

	// The batch, with the same rules the chain above enforces -- read off the
	// same configuration rather than written out again, which is the only way
	// the two stay in step. What they enforce by looking at the method gRPC
	// dispatched, this enforces per operation.
	//
	// `Closed` is replaced with the same function the chain got, and that is not
	// tidiness: `ServerConfig.Guard` fills it from the configuration alone, so a
	// guard left as it came would let a batch carry the credential reads that
	// are closed everywhere else.
	// The same policy the chain got. `batch.Guard` names it as one of the four
	// rules a batch would otherwise reach past -- and it is not hypothetical
	// here: a key's scope comes from the policy, so a guard without one would
	// serve a key in a batch as a caller who may see nothing.
	guard := c.Server.Guard(Policy(s.Ent))
	guard.Closed = s.closed(c)

	if b, err := pd.Batch(s.Walled, s.Drv, guard); err == nil {
		pdpb.RegisterBatchServiceServer(g, b)
	} else {
		// A deployment that closed nothing, limits nothing and has no policy.
		// It is a real thing to be and it is also what a guard nobody filled in
		// looks like, so the batch is not served rather than served open.
		log.From(ctx).WarnContext(ctx, "no batch", slog.String("why", err.Error()))
	}

	return g, nil
}

// register puts every service on the wire, and it is written out rather than
// being `app.RegisterServer` because of the ones that are missing.
//
// `CredentialService`, `ApiKeyService` and `DelegationService` are not here.
// Each has a generated `Get` that answers with whatever columns it was asked
// for, and in all three cases one of them is a verifier -- a password hash, a
// key hash, the hash of a delegation. Serving them is publishing those to
// anybody the wall lets read a row. The rows still exist and this app still
// reads them, in process, but there is no method on this server that answers
// with one.
//
// For `DelegationService` this is the **only** control that closes it, and that
// is worth knowing before somebody adds a line here for tidiness.
//
// `closed` below now reaches a stream as well, because both interceptors are
// installed rather than the unary one -- which is F10's roster half, and it was
// a hole in the one word whose whole job is to mean *not served*.
// `Delegation` still declares no `watch:`, because a schema that says so is
// better than a wiring that has to be remembered.
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
	app.RegisterHostServiceServer(g, s.Host())
	app.RegisterMailDomainServiceServer(g, s.MailDomain())
	app.RegisterConnectionServiceServer(g, s.Connection())
	app.RegisterHolderServiceServer(g, s.Holder())
	app.RegisterIdentityServiceServer(g, s.Identity())
	app.RegisterEmailServiceServer(g, s.Email())
	app.RegisterSiteServiceServer(g, s.Site())
	app.RegisterTeamServiceServer(g, s.Team())
	app.RegisterRoleServiceServer(g, s.Role())
	app.RegisterGroupServiceServer(g, s.Group())
	app.RegisterGroupMembershipServiceServer(g, s.GroupMembership())
	app.RegisterBindingServiceServer(g, s.Binding())
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
//
// It is installed as **both** interceptors, in the pair `auth` is installed as
// two lines above. Only the unary one was here, which was F10: a deployment that
// closed a registered, watchable service -- `Holder` is one -- closed its reads
// and left its stream open, and a `WatchItem` carries the whole message with no
// `select` to narrow it. Nothing said so, in the one word whose whole job is to
// mean *not served*.
func (s *Server) closed(c Config) func(method string) bool {
	was := c.Server.Closed()

	// And `DelegationService` beside it, for the same reason and with no
	// exception: unlike `ApiKeyService` there is no port whose reason for
	// existing is managing these. Nobody manages a credential that lives for
	// minutes; what a person does with one is stop using it.
	shut := []string{
		app.CredentialService_ServiceDesc.ServiceName,
		app.DelegationService_ServiceDesc.ServiceName,
	}

	// `ApiKey.Add` takes a verifier from the caller, so serving it beside
	// `IssueService` would be offering the thing `Issue` exists to stop: a key
	// whose secret somebody else chose, in a prefix they picked.
	//
	// It stays what the servers write through, which is the convention `Patch`
	// and `Apply` already have. One more method of one entity joining them is
	// not a new rule.
	byMethod := []string{app.ApiKeyService_Add_FullMethodName}
	if !s.Keys {
		// Everywhere but the one port whose reason for existing is managing
		// them; see [Server.Keys].
		shut = append(shut, app.ApiKeyService_ServiceDesc.ServiceName)
	}

	return func(method string) bool {
		for _, v := range shut {
			if strings.HasPrefix(method, "/"+v+"/") {
				return true
			}
		}
		for _, v := range byMethod {
			if method == v {
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
func public(method string) bool {
	// Signing in, and only on the port that serves it. A caller asking for a
	// credential does not have one, which is the whole of the argument -- the
	// same one `/session` made when this was an HTTP endpoint.
	//
	// What it costs is what that cost: anybody who reaches the port may guess
	// passwords, and `grpcx.Limit` counts per tenant off a frame a public call
	// has none of. The answers are the port being private and the lockout in
	// `server/vouch`, which is what makes guessing expensive rather than this.
	//
	// `SignOut` is **not** here. Ending a session needs the session, so a caller
	// without one is asking about somebody else's.
	if method == app.AuthService_SignIn_FullMethodName {
		return true
	}

	return auth.PublicDefault(method)
}

// Serve answers on `l` until the context is done.
func (s *Server) Serve(ctx context.Context, c Config, l net.Listener) error {
	g, err := s.Grpc(ctx, c)
	if err != nil {
		return err
	}

	stop, err := s.serveHttp(ctx, c, g)
	if err != nil {
		return err
	}
	defer stop()

	var control *grpc.Server
	if s.Control != nil {
		control, err = s.GrpcControl(ctx, c)
		if err != nil {
			return err
		}
	}

	stopControl, err := s.serveControl(ctx, c, control)
	if err != nil {
		return err
	}
	defer stopControl()

	stopControlHttp, err := s.serveControlHttp(ctx, c, control)
	if err != nil {
		return err
	}
	defer stopControlHttp()

	admin, err := s.GrpcAdmin(ctx, c)
	if err != nil {
		return err
	}

	stopAdmin, err := s.serveAdmin(ctx, c, admin)
	if err != nil {
		return err
	}
	defer stopAdmin()

	stopAdminHttp, err := s.serveAdminHttp(ctx, c, admin)
	if err != nil {
		return err
	}
	defer stopAdminHttp()

	go func() {
		<-ctx.Done()
		g.GracefulStop()
	}()

	return g.Serve(l)
}

// GrpcControl is the control plane's own server, for a console.
//
// Separate from [Server.serveControl] for the reason [Server.Grpc] is separate
// from [Server.Serve]: a test can travel exactly this and answer on a listener
// that is a channel. It was briefly not, and then the one registration that
// makes this port worth opening was reachable only by opening it.
func (s *Server) GrpcControl(ctx context.Context, c Config, opts ...grpc.ServerOption) (*grpc.Server, error) {
	g, err := s.Control.Grpc(ctx, Config{Server: c.Control.ServerConfig}, opts...)
	if err != nil {
		return nil, err
	}

	// The keys themselves, which the data plane refuses to serve and this port
	// exists to serve. `Get` still answers with the verifier column if it is
	// asked for -- that is why it is unregistered everywhere else -- so this
	// port must not be reachable by anybody who is not administering the
	// deployment. Nothing in this process can enforce that; the address is what
	// enforces it.
	app.RegisterApiKeyServiceServer(g, s.Control.Walled.ApiKey())

	// What a console asks that no entity answers: a session, and a secret that
	// is readable exactly once. See `server/console`.
	//
	// `Auth` reads the **ungated** server, because a sign-in runs before there
	// is anybody to be walled by. `Issue` reads the walled one, so that minting
	// a key is held to the rule every other grant is -- nobody hands out a
	// method they do not hold.
	app.RegisterAuthServiceServer(g, console.Auth(s.Control.Ungated, s.Control.Ent, s.Sessions))
	app.RegisterIssueServiceServer(g, console.Issue(s.Control.Walled, s.Control.Ent))

	return g, nil
}

// serveControl is the control plane on a port, for a console.
//
// Nothing is opened unless an address was written down, and the address should
// be one only a console can reach. The rows here are the deployment's own --
// which services may call it, what each may call, who runs it -- and none of
// them is any customer's business.
//
// It is roster again, so it is the same `Grpc` with the same chain: the wall,
// the gate, the trail. What differs is which database is behind it and one
// registration, since `ApiKeyService` is the whole point of the port and is
// closed on the other one.
func (s *Server) serveControl(ctx context.Context, c Config, g *grpc.Server) (func(), error) {
	if !c.Control.Answers() || g == nil {
		return func() {}, nil
	}

	l, err := net.Listen("tcp", c.Control.Addr)
	if err != nil {
		return nil, err
	}

	go func() { _ = g.Serve(l) }()

	log.From(ctx).InfoContext(ctx, "control", slog.String("addr", l.Addr().String()))

	return g.GracefulStop, nil
}

// serveAdmin is where an operator administers customers; see `admin.go`.
func (s *Server) serveAdmin(ctx context.Context, c Config, g *grpc.Server) (func(), error) {
	if c.Admin.Addr == "" || g == nil {
		return func() {}, nil
	}

	l, err := net.Listen("tcp", c.Admin.Addr)
	if err != nil {
		return nil, err
	}

	go func() { _ = g.Serve(l) }()

	log.From(ctx).InfoContext(ctx, "admin", slog.String("addr", l.Addr().String()))

	return g.GracefulStop, nil
}

// serveHttp is the second listener, for whatever cannot speak gRPC -- which is
// every browser. Nothing is opened unless the configuration named an address.
//
// It is the **same** `g`: a page reaches the handlers a gRPC client reaches,
// through the interceptors a gRPC client goes through, behind the same wall.
// There is no second stack here for a rule to be missing from.
func (s *Server) serveHttp(ctx context.Context, c Config, g *grpc.Server) (func(), error) {
	return s.http(ctx, "http", c.Server.Http, g)
}

// serveControlHttp is the control plane's own browser surface.
//
// The first console is this one: who runs the deployment, which services call
// it, what each key may do. All of that is here and none of it is on either of
// the other two ports.
func (s *Server) serveControlHttp(ctx context.Context, c Config, g *grpc.Server) (func(), error) {
	if g == nil {
		return func() {}, nil
	}

	return s.http(ctx, "control.http", c.Control.Http, g)
}

// serveAdminHttp is the same for the admin listener, and it is what a console
// actually talks to.
//
// A browser cannot speak gRPC, so a port without one of these is a port a
// console cannot reach -- and until this, the only transcoder was in front of
// the **data plane**, where an operator's session names nobody. The console
// could sign in and then had nothing to call.
//
// A second listener rather than a route on the first, because both servers
// register `roster.HolderService` and a transcoder routes by the method's own
// path. There is nowhere to put a prefix that the name does not already occupy.
func (s *Server) serveAdminHttp(ctx context.Context, c Config, g *grpc.Server) (func(), error) {
	if g == nil {
		return func() {}, nil
	}

	return s.http(ctx, "admin.http", c.Admin.Http, g)
}

func (s *Server) http(ctx context.Context, name string, c config.HttpConfig, g *grpc.Server) (func(), error) {
	if !c.Serves() {
		return func() {}, nil
	}

	h, err := web.New(c, g)
	if err != nil {
		return nil, err
	}

	// The console's sign-in, which is the seam payday left and could not fill:
	// `auth` reads a credential and does not issue one, and issuing is an HTTP
	// endpoint. See `Login`.
	//
	// A gRPC path is `/<service>/<method>`, so an ordinary route cannot collide
	// with one -- and `ServeMux` panics rather than shadowing if one somehow
	// does.
	//
	// On every listener that has one, because a console reaches exactly one
	// origin and signing in has to be there. What it is a session **for**
	// differs -- the data plane's port serves it and then answers nobody with
	// it, which is worth knowing rather than discovering.
	//
	// Only where there is a control plane, because that is where the people who
	// sign in live. A deployment without one has no console and nobody to be;
	// serving this there would be an endpoint that can only ever answer no.
	if s.Sessions != nil && s.Control != nil {
		v := Login(s.Control)
		h.Handle("POST /session", s.Sessions.Serve(v))
		h.Handle("DELETE /session", s.Sessions.Serve(v))
	}

	l, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return nil, err
	}

	srv := &http.Server{Handler: h}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.From(ctx).ErrorContext(ctx, name, slog.String("err", err.Error()))
		}
	}()

	log.From(ctx).InfoContext(ctx, name, slog.String("addr", l.Addr().String()))

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
