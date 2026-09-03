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
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
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
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/internal/ent"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/console"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/me"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/session"
	rostersync "github.com/lesomnus/roster/server/sync"
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

	// sink is this plane with no wall and no gate: the minter and the recorders
	// exactly as [Build] settled them, **including whether the outbox is among
	// them**.
	//
	// Kept because the admin port builds its own stack over these same rows,
	// and it used to re-type the list. A second literal is a second answer to
	// `watch.outbox` -- and the one it gave was `no`, so an operator's writes
	// were the only ones with nothing behind them if this process stopped
	// between the commit and the publish. The list is also **ordered** -- the
	// outbox recorder is last, for the reason [Build] gives -- and an order
	// written twice is an order that drifts.
	//
	// Unexported, unlike everything else here: it is `Ungated` without even
	// `core`'s rules, so it is held rather than handed out.
	sink pd.Sink

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
	// What it changes is two services. `ApiKeyService` is closed everywhere by
	// default because its generated `Get` answers with the verifier column; on
	// the port where the deployment's own operator manages keys, that is the
	// point. `CredentialService` stays closed on both, because a password hash
	// is nobody's to read.
	//
	// And `IssueService`, which both planes serve and which mints a different
	// **kind** on each -- `rk_` there and `rt_` here. `Grpc` registers the
	// customer's one unless this flag says otherwise, and `GrpcControl` adds
	// the deployment's own after; without the flag they would both land on the
	// control plane's server, and gRPC refuses a service registered twice. It
	// did, loudly, which is the good direction for that mistake to fail in.
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
	// **A table**, not a map: `authsession.New(session.New(control.Ent))`
	// below, over `server/session`. So a cookie minted by one replica resolves
	// on another, which is what it has to do -- payday's `MemStore` is right
	// for one replica and silently wrong for two, a browser signed in or out
	// depending on which one the load balancer picked, per request, with
	// nothing in any log saying so.
	//
	// The seam is `authsession.Store`, which is where a deployment that would
	// rather keep these somewhere else puts them. Nothing caches: the handler
	// asks the store on every request, and the cookie is 32 bytes of
	// `crypto/rand` rather than something signed, so there is no per-process
	// secret to keep in step either.
	Sessions *authsession.Sessions

	// Spin is whatever this deployment has to run besides answering requests.
	// It is a slice rather than a method because a server with nothing to run
	// should write nothing at all; see `payday/spin`.
	Spin []any
}

// ErrOutboxHasNowhereToGo is `watch.outbox` on with no broker to publish into.
//
// A sentinel because the caller that builds the **control** plane has one thing
// to add to this and nothing to add to any other failure: which of the two
// `watch.broker` settings the name came from. Without it that note rode on
// every error the nested build could answer with, so a database that would not
// answer was reported as a database that would not answer, plus a sentence
// about brokers.
var ErrOutboxHasNowhereToGo = errors.New("watch.outbox: nothing would ever publish or delete what it queues")

// Build opens the database and stacks the servers.
//
// The two hooks are the whole of what payday puts in the write and read paths,
// and both come out of what the schema declared: [pd.Minter] stamps a new row
// with the domain of its entity and refuses one of another, [pd.Wall] narrows
// every read to the tenants the caller may see.
func Build(ctx context.Context, c Config) (*Server, error) {
	// The data plane, and a standalone with no control: people and customers
	// mint `rt_`. The control plane recurses with `keys.PrefixDeployment` below.
	return build(ctx, c, keys.PrefixTenant)
}

func build(ctx context.Context, c Config, prefix string) (*Server, error) {
	// The two blocks that are answered by silence when the one field they hang
	// off is missing, refused before anything is opened.
	//
	// `Serves()` reads `control.db.driver` alone, which is right: a control
	// plane is a database and an address is only how it is reached. What that
	// leaves is a `control:` block with every field but that one, and it builds
	// a **working server with no control plane at all** -- no key of either
	// kind is read, no delegation is honoured, no console session is minted,
	// and `control.addr` opens nothing. The whole of what says so is payday's
	// `auth: serving with Plain` warning, which a deployment that wrote no
	// `control:` prints as well. So the one line that could have told somebody
	// is the line that cannot tell the two apart.
	//
	// `admin:` is the same shape one step further out: the port authenticates a
	// session cookie against the control plane's holders, so `Admin` answers
	// with nothing where there is no control plane and `serveAdmin` opens no
	// listener. An address that is written down and not served is worse than
	// one that is refused, because it is a port somebody believes is there.
	//
	// Refused rather than logged, for the reason `watch.outbox` with no broker
	// is: the file says two things that cannot both be meant, and picking one
	// silently is how this got here.
	if c.Control.Said() && !c.Control.Serves() {
		return nil, errors.New(
			"control: the block says things and control.db.driver names no database, so no control " +
				"plane is built -- no api key is read, no console session is minted, and control.addr " +
				"opens nothing. name a database, or take the block out")
	}
	if said(c.Admin) && !c.Control.Serves() {
		return nil, errors.New(
			"admin: the port takes a session cookie and resolves it against the control plane's " +
				"holders, and control.db.driver names no database, so there is nobody to be and no " +
				"listener is opened. name a control plane, or take the block out")
	}

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
	b, err := c.Watch.Build(c.Db)
	if err != nil {
		db.Close()
		return nil, err
	}
	if c.Watch.Outbox && b == nil {
		// A queue with nothing at the other end of it.
		//
		// The recorder below is installed on `watch.outbox` alone and the drain
		// further down spins only where there is a broker to publish into, so
		// this combination -- two plain environment variables, and the loader
		// accepts both -- wrote a row inside every transaction that nothing
		// would ever publish or delete. `OutboxService` answers no RPC, so not
		// even an operator could drain it by hand; the table grows until the
		// database is full, which is the failure `outbox.proto` warns about in
		// as many words.
		//
		// Refused rather than logged and skipped, because the two settings
		// contradict each other and the deployment that wrote them meant one of
		// the two. Skipping the recorder would pick one silently, which is how
		// this got here.
		db.Close()
		return nil, fmt.Errorf(
			"%w: watch.broker is %q, so every write would leave a row behind and the "+
				"table would grow without end. name a broker, or turn the outbox off",
			ErrOutboxHasNowhereToGo, config.BrokerNone)
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

	// Read here rather than where it is used, so that a deployment whose keys
	// are malformed finds out when it starts rather than when somebody enrols.
	keyring, err := vouch.NewKeyring(c.Vouch.Keys)
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
	stacked, err := app.Build(walled.WithWatch(w), core.Build(Rules(client), core.On(drv, Locking(client)), core.WithBreached(core.Breached(leaked)), core.WithKeyring(keyring), core.WithPrefix(prefix)), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
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
	ungated, err := app.Build(sink.WithWatch(w), core.Build(Rules(client), core.On(drv, Locking(client)), core.WithBreached(core.Breached(leaked)), core.WithKeyring(keyring), core.WithPrefix(prefix)), pd.AuditBuild())
	if err != nil {
		db.Close()
		return nil, err
	}

	s := &Server{
		Db: db, Ent: client, Drv: drv, Dialect: dialect, Watch: w, sink: sink,
		Walled: stacked, Ungated: ungated, Keyring: keyring, Breached: leaked,
	}

	// The control plane: roster again, on its own database, holding keys rather
	// than people. See `ControlConfig`, and docs/position.md, 'Two planes, one
	// schema'.
	//
	// Built after this one and from a config with no `control` of its own,
	// which is what stops the recursion -- one level, and the innermost
	// instance answers to nothing but the CLI that seeds it.
	if c.Control.Serves() {
		control, err := build(ctx, Config{
			Db: c.Control.Db,

			// Its own broker, and now its own **setting**. A control plane
			// publishing into the data plane's would have a key change look
			// like a person changing, to every client watching -- so they are
			// two brokers. They were also two decisions and only one of them
			// was written down: this said `memory` in the code, which made the
			// console the one screen a second replica broke without saying so.
			Watch: c.Control.watch(c.Watch),
		}, keys.PrefixDeployment)
		if err != nil {
			db.Close()

			if errors.Is(err, ErrOutboxHasNowhereToGo) && c.Control.Watch.Broker == "" {
				// Named, because the setting it is about is one the operator
				// did not write. The broker in that refusal is the data
				// plane's, inherited exactly as the field invites -- so
				// `control: watch.broker is "none"` reads as being about a line
				// that is not in the file.
				//
				// Only for that refusal, which is what the sentinel is for.
				// Said of everything the nested build can answer with, it put a
				// sentence about brokers on a database that would not answer.
				return nil, fmt.Errorf(
					"control: %w (control.watch.broker is empty, so it took watch.broker)", err)
			}

			return nil, fmt.Errorf("control: %w", err)
		}

		control.Keys = true

		s.Control = control

		// What the nested `Build` arranged for itself, carried up.
		//
		// `Build` is recursive, so the control plane assembled its own sweeps
		// and its own drain into `control.Spin` -- and nothing ever ran them:
		// `spin.Run` is handed the outer `s.Spin` alone. So a deployment with a
		// control plane had a second set of background work that was configured,
		// constructed, and silently never started.
		//
		// Here rather than at the append below, because that one runs in every
		// `Build` including this nested one, and the whole failure was work
		// going somewhere nothing reads.
		s.Spin = append(s.Spin, control.Spin...)
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

		// And somebody to collect them, because nobody else will. A store
		// behind `authsession.Store` has `Put`, `Get` and `Del` and no pass
		// over everything -- and `Del` is a soft erase, so even signing out
		// leaves the row. Without this the table is one row per sign-in since
		// the deployment started.
		//
		// Appended to **this** server's `Spin` and not the control plane's, on
		// its client. See below: what the nested `Build` collected was never
		// run.
		s.Spin = append(s.Spin, session.Sweep(control.Ent, session.Swept))

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
	// Collecting expired delegations, attempts and links.
	//
	// It is not what makes an expired one refused -- [keys.findDelegation] is,
	// and the comment on `Sweep` says which half is which. What this is about is
	// the size of a table nothing else deletes from.
	//
	// Appended in **every** `Build`, so the nested one arranges the same two
	// sweeps over the control plane's copies of these tables. Those are empty
	// today -- delegations and attempts are minted on the data plane -- and the
	// loops are two DELETEs an hour against nothing. Written where they are
	// rather than behind a check for which plane this is, because a plane that
	// grows one of these rows and has no sweep is a table that grows silently,
	// and that is the failure worth being wrong towards.
	s.Spin = append(s.Spin,
		keys.Sweep(s.Ent, keys.Swept),
		vouch.Sweep(s.Ent, vouch.Swept),
	)

	// And the trail's retention, which is the one sweep here that is a
	// **mechanism** rather than a tidy-up.
	//
	// The two above collect rows that are already refused; an outage of either
	// costs disk. Nothing else applies a retention window, so an outage of this
	// one is a deployment keeping records it told somebody it would not. That
	// is why the policy is checked here, where a refusal stops the process,
	// rather than at the first pass a day later.
	//
	// Nothing is appended when no policy is named, and the nested build that
	// raises the control plane is handed a config with no `audit` at all -- so
	// the deployment's own trail is never on a clock. Unlike the two above,
	// that is the direction to be wrong in: the failure of a sweep that does
	// not exist is a table that grows, and the failure of one that does is
	// evidence that is gone.
	p, err := c.Audit.Policy()
	if err != nil {
		db.Close()

		return nil, err
	}
	if p.On() {
		log.From(ctx).InfoContext(ctx, "trail: retention", "policy", p.String())

		s.Spin = append(s.Spin, trail.Sweep(pd.TrailStore(s.Ent), p))
	}

	// And the other window, which is about a person rather than about age.
	//
	// Same shape and the same reason for being loud: nothing else destroys what
	// is held about somebody who has left, so a sweep that has been failing is
	// a deployment holding what it said it would destroy. Nothing is appended
	// when no window is named, and the nested control-plane build is handed a
	// config with no `holder` -- its people are the deployment's own operators,
	// not customers with a right to be forgotten.
	if c.Holder.ForgetAfter > 0 {
		log.From(ctx).InfoContext(ctx, "forget: after an erase",
			"after", c.Holder.ForgetAfter, "archive", c.Audit.Archive != "")

		s.Spin = append(s.Spin, forget.Sweep(s.Ent, c.Holder.ForgetAfter, c.Audit.Archive, c.Holder.Every))
	}

	if c.Watch.Outbox {
		// The loop that makes an event durable. It is not a layer and not a
		// method on any server -- `spin.Run` finds it in whatever is handed
		// over, which is what keeps a server that has no background work from
		// carrying an empty method saying so.
		//
		// No second look at whether there is a broker: the refusal above is the
		// one place that decides, and a condition here as well would be the
		// same rule written twice -- which is how a queue with nothing at the
		// other end of it got written in the first place.
		s.Spin = append(s.Spin, pd.Drain(client, b, c.Watch.Every()))
	}

	return s, nil
}

// Close gives back what this deployment holds, which is **both** planes.
//
// `Build` is recursive, so a deployment that names a `control:` plane is two
// servers with a database and a pool each. This was one line closing the outer
// one, and the inner pool was never given back.
//
// A process on its way out does not care, which is why it went unnoticed: the
// one caller in production calls this once and then exits. A suite is the
// caller that does care -- every test that needs keys, a console or an operator
// builds both planes -- and against PostgreSQL with the hundred connections its
// image allows, the package ran out part way through. What came back was
// `too many clients already` against whichever tests were running when the last
// connection was taken: a different set every run, which reads exactly like a
// flaky suite and is not one.
//
// Both are attempted and the first error is answered, rather than returning on
// the first: a data plane that would not close is not a reason to leave the
// control plane's connections held as well.
func (s *Server) Close() error {
	err := s.Db.Close()
	if s.Control != nil {
		if e := s.Control.Close(); err == nil {
			err = e
		}
	}

	return err
}

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

	// The rows an api-key identifier is checked against, which is what makes it
	// a row rather than a shape: see `keyed`.
	//
	// A deployment's keys live on its control plane. **The control plane's own
	// live on itself**, and that is the second case here: `GrpcControl` builds
	// from a `Server` whose `.Control` is nil, because a nil one is what stops
	// the recursion. Read from `s.Control` alone this was nil there, and every
	// key presented to the control plane was refused with "this deployment has
	// no keys" -- by the plane the keys are in.
	//
	// The two nils are told apart by `Auth`: the control plane is built with
	// one (see `Build`), and the deployment with no control plane is the only
	// Server without -- that absence is what wires `Plain` above. Under Plain
	// an identifier is whatever the caller writes, including one of this
	// domain naming a **tenant** key's row in `s.Ungated` -- a row that
	// exists, which `keyed` would take for a deployment key and the policy
	// would hand `frame.Everything`. That is the escalation `keys.Store`
	// resolves tenant keys to their holder to prevent, so here it stays what
	// it always was: no control plane, no keys, refused.
	var keyrows app.Server
	switch {
	case s.Control != nil:
		keyrows = s.Control.Ungated
	case s.Auth != nil:
		keyrows = s.Ungated
	}

	r := Resolver(s.Ungated, keyrows)

	// One function, installed twice, because a stream and a unary call are two
	// interceptors and `closed` is one answer. Built here rather than called
	// twice below so that the two cannot come to disagree.
	shut := s.closed(c)

	// One limiter, handed to both halves.
	//
	// `Limiter()` builds a bucket, so calling it twice is two limits with the
	// same numbers on them and neither counting what the other let through --
	// which is a rate of `n` per second answering `2n`, silently. Held in a
	// variable for the reason `shut` is: one thing the chain is told, rather
	// than a constructor called wherever it is needed.
	rate := c.Server.Limiter()

	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Server.CallTimeout())).
		WithUnary(auth.InterceptorUnary(h, r, public)).
		WithStream(auth.InterceptorStream(h, r, public)).
		WithUnary(grpcx.LimitUnary(rate, gate.ByTenant())).
		WithStream(grpcx.LimitStream(rate, gate.ByTenant())).
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

	// The one stream an app that trusts roster holds open: *a decision you made
	// has stopped being good.*
	//
	// The **walled** server, so what an app hears is narrowed exactly as a read
	// is -- a deployment key hears every tenant, a credential resolving to a
	// person hears theirs -- and there is no field beside that narrowing.
	//
	// A deployment with no broker refuses the call rather than opening a stream
	// that will never carry anything, which `watch.Stream` decides before it
	// reads and which is the one failure a client cannot tell from a quiet
	// system.
	app.RegisterSyncServiceServer(g, rostersync.Service(s.Walled, s.Watch))

	// A customer minting a key for themselves, which until now needed a shell
	// on the box.
	//
	// Not on the control plane, which mints the other kind and registers its
	// own `IssueService` after this -- see [Server.Keys], which is the one flag
	// that tells the two apart because `Control == nil` cannot.
	//
	// The **walled** server, so `core.ApiKey.Add` runs both rules a key is held
	// to: nobody hands out a method they do not hold, and nobody writes a way
	// into an account wider than their own -- a key resolves to its holder, so
	// a call made with it is made as them.
	//
	// It hands out nothing new, which is what makes it safe to offer here: an
	// `rt_` is at most a second copy of a credential its holder already has,
	// and less, since it names methods. What it replaces is `roster key add`,
	// which is a shell, and a shell is not a thing a customer has.
	// The one service that is not an entity: it answers yes or no about a row
	// nothing else may read. See `server/vouch`.
	// The rule about who may write whose credential travels with the service,
	// because `VouchService` is hand-written and no layer wraps it. Same rules
	// the gate reads, handed over rather than asked for a second time.
	app.RegisterVouchServiceServer(g, vouch.New(s.Ungated, s.Walled,
		vouch.WithKeys(s.Keyring)))

	// What a front door asks before it knows anything, and therefore through
	// the server the wall was never installed on. Neither RPC answers with a
	// row -- one identifier or one provider name -- which is what keeps that
	// from being a hole; `server/front` says it at length.
	app.RegisterFrontServiceServer(g, front.New(s.Ungated))

	// And what a caller is, in one round trip. None of its three methods takes
	// a subject, so none can be pointed at anybody else -- `Unlink` names a
	// *which* and never a *whose*; see `server/me` and [aboutYourself].
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
// key hash, the hash of a delegation, the digest of a session cookie, the
// secret of a link, the secret of a continuation -- six services in all, and
// every one of them holds a column declared `secret`. Serving any of them is
// publishing that column to anybody the wall lets read a row. The rows still
// exist and this app still reads them, in process, but there is no method on
// this server that answers with one.
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
// The schema says it too, since F6: `(payday.field).secret` is declared on
// every one of those columns, and it is what keeps them out of the trail. The
// registration is kept beside it because it is the stronger statement -- a
// cleared field is a column an answer omits; a service that is not on the wire
// cannot answer at all.
//
// Written out has a cost worth naming: an entity added to the schema tomorrow
// is not served until somebody adds a line here. That is the direction to fail
// in. The other arrangement -- serve everything, then take one away -- fails by
// publishing something nobody meant to, and it fails silently.
func register(g grpc.ServiceRegistrar, s app.Server) {
	// `CredentialService` is served for its overlays (`Set`, `Unlock`, `Enrol`), and
	// its generated reads and raw `Add`/`Erase` are shut by method in
	// `closed` -- so nothing on the wire answers with the `secret` column.
	app.RegisterCredentialServiceServer(g, s.Credential())
	// `DelegationService`, like `CredentialService`, for its overlay (`Revoke`)
	// -- its reads and raw writes are shut by method in `closed`, so nothing on
	// the wire answers with the token column.
	app.RegisterDelegationServiceServer(g, s.Delegation())
	// `ApiKeyService` for its `Issue` overlay -- the mint `IssueService.IssueKey`
	// was. Its reads and raw `Add`/`Erase` are shut by method in `closed` (and
	// the management verbs everywhere but the keys port), so only `Issue`
	// answers here and the prefix it stamps is the stack's, not the caller's.
	app.RegisterApiKeyServiceServer(g, s.ApiKey())
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

	// `DelegationService` is registered now for its `Revoke` overlay, so it too
	// is shut a method at a time rather than whole: the reads answer the token
	// column and the raw writes take a caller-chosen one, and only `Revoke` --
	// which answers nothing -- is meant to reach the wire.
	shut := []string{}

	// `ApiKey.Add` takes a verifier from the caller, so serving it beside
	// `IssueService` would be offering the thing `Issue` exists to stop: a key
	// whose secret somebody else chose, in a prefix they picked.
	//
	// It stays what the servers write through, which is the convention `Patch`
	// and `Apply` already have. One more method of one entity joining them is
	// not a new rule.
	// `CredentialService` is registered now, so its verbs are shut one at a
	// time rather than the whole name: the reads answer the `secret` column and
	// the raw writes take a caller-chosen verifier, and only the overlays
	// (`Set`, `Unlock`, `Enrol`) are meant to be reachable.
	// `Patch`/`Apply` are already off by `GeneralWrite`.
	byMethod := []string{
		app.ApiKeyService_Add_FullMethodName,
		app.CredentialService_Get_FullMethodName,
		app.CredentialService_List_FullMethodName,
		app.CredentialService_Watch_FullMethodName,
		app.CredentialService_Add_FullMethodName,
		app.CredentialService_Erase_FullMethodName,
		app.DelegationService_Get_FullMethodName,
		app.DelegationService_List_FullMethodName,
		app.DelegationService_Add_FullMethodName,
		app.DelegationService_Erase_FullMethodName,
		app.DelegationService_Patch_FullMethodName,
		app.DelegationService_Apply_FullMethodName,
	}
	if !s.Keys {
		// Everywhere but the one port whose reason for existing is managing
		// them (see [Server.Keys]), the reads and the management writes are shut
		// -- but not the whole service: `Issue` is the mint, and it is served on
		// every port that a caller reaches, stamping the stack's own prefix.
		byMethod = append(byMethod,
			app.ApiKeyService_Get_FullMethodName,
			app.ApiKeyService_List_FullMethodName,
			app.ApiKeyService_Erase_FullMethodName,
			app.ApiKeyService_Patch_FullMethodName,
			app.ApiKeyService_Apply_FullMethodName,
		)
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
// by a browser, decided before any of it was written -- docs/position.md.
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

	// The stops, collected rather than deferred one at a time.
	//
	// A `defer` per listener runs them in series, and each waits up to
	// [ShutdownGrace] -- so five listeners is five graces end to end, and the
	// number that justifies the grace is `docker stop`'s ten seconds. Five of
	// them is twenty-five, which is SIGKILL with four of the five never having
	// been asked. Run together, the budget is the grace whatever a deployment
	// opened.
	var stops shutdown
	defer stops.run()

	stop, err := s.serveHttp(ctx, c, g)
	if err != nil {
		return err
	}
	stops.add(stop)

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
	stops.add(stopControl)

	stopControlHttp, err := s.serveControlHttp(ctx, c, control)
	if err != nil {
		return err
	}
	stops.add(stopControlHttp)

	admin, err := s.GrpcAdmin(ctx, c)
	if err != nil {
		return err
	}

	stopAdmin, err := s.serveAdmin(ctx, c, admin)
	if err != nil {
		return err
	}
	stops.add(stopAdmin)

	stopAdminHttp, err := s.serveAdminHttp(ctx, c, admin)
	if err != nil {
		return err
	}
	stops.add(stopAdminHttp)

	go func() {
		<-ctx.Done()
		stopping(g)
	}()

	return g.Serve(l)
}

// shutdown is every listener's stop, run together rather than one after
// another.
//
// Each of them waits up to [ShutdownGrace] for the calls still in flight, and
// they have nothing to say to each other -- one listener draining is not a
// reason for the next one to still be accepting. Deferred separately they ran
// in series, so a deployment with a control plane and an admin port spent five
// graces where the grace is chosen against a ten-second budget.
//
// It is a type rather than a slice of `func()` and a loop at the end, because
// what has to be true is that a stop added is a stop run: the loop was the
// thing that was there and the `defer` beside each listener was what got
// written instead.
type shutdown struct{ fs []func() }

func (v *shutdown) add(f func()) { v.fs = append(v.fs, f) }

func (v *shutdown) run() {
	var wg sync.WaitGroup
	for _, f := range v.fs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}

	wg.Wait()
}

// ShutdownGrace is how long a stop waits for the calls that are still in
// flight.
//
// `GracefulStop` on its own waits for all of them, and one kind never ends: a
// Watch loops until the **client** hangs up, the deadline the chain installs is
// unary-only, and no connection is aged out unless a deployment configured
// keepalive. So one product app holding the sync channel -- which is what
// roadmap.md's item 4 tells a product app to do -- meant `g.Serve` never
// returned:
// the deferred stops for the other listeners never ran, the errgroup below
// never finished, and `signal.NotifyContext` had already fired so a second
// Ctrl-C did nothing. The process had to be killed, which is exactly the thing
// `NewCmdServe`'s comment says the wiring is arranged to prevent.
//
// Five seconds, and a constant rather than a setting because the number that
// decides this is somebody else's: `docker stop` waits ten and then sends
// SIGKILL, so a longer grace is one nothing lives to use. A deployment that
// wants its streams cut sooner has `keepalive.max_connection_age`, which says
// something truer about a long-lived stream than a shutdown timeout does.
//
// Exported so that a test can say *bounded by what this app chose* rather than
// by a number copied beside it, which is the whole claim.
const ShutdownGrace = 5 * time.Second

// stopping takes a server down, and does not wait forever to.
//
// Graceful first, so a unary call in flight finishes and its caller gets an
// answer rather than a broken connection. Hard after [ShutdownGrace], because
// waiting on a stream that has no reason to end is not waiting, it is hanging.
//
// `Stop` beside a `GracefulStop` already in flight is what unblocks it: it
// closes the transports out from under the handlers, the connections drain, and
// the graceful call returns -- which is why the goroutine is still waited for
// rather than abandoned. Anything left running after this is running under a
// process that is on its way out anyway.
func stopping(g *grpc.Server) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.GracefulStop()
	}()

	select {
	case <-done:
	case <-time.After(ShutdownGrace):
		g.Stop()
		<-done
	}
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

	// Bounded like the one above, and for the same reason: this port serves the
	// same watchable services, so a console with a stream open would otherwise
	// hold the whole shutdown -- from a `defer`, after the data plane had
	// already stopped and with nothing left to report it.
	return func() { stopping(g) }, nil
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

	return func() { stopping(g) }, nil
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

// Ready is the databases, before anything is served on them.
//
// payday owns some of this app's schema, so a field added to a holder there
// arrives in `internal/ent` the next time this app generates -- and nothing
// about that is loud. It compiles, the tests pass against a database the tests
// just created, and the first sign of trouble is a column that is not there in
// the one handler that reads it.
//
// So one of two things happens here, and which one is the operator's to say.
// `db.migrate: true` hands the serving process the right to alter tables, which
// is right for development and is a thing to decide on purpose; anything else
// and the shapes have to agree already.
//
// **Both** planes, and the second is why this is a method rather than four
// lines in `serve`. Only the data plane was ever looked at: the control plane
// -- a second roster, on its own database, holding the keys every request is
// authenticated against -- was opened and served in whatever shape it was
// found. `control.db.migrate` was listed by `roster config env`, set by
// `compose.yaml`, promised by `docs/operating.md`, and read by nothing.
//
// What it cost is an upgrade past a release that adds a control-plane table --
// `session` is one -- on a deployment whose entrypoint skips `init` because the
// marker beside its databases says it is already seeded. The data plane
// migrates, the control plane does not, and the process starts and says
// nothing: the sweep logs the missing table and carries on, and the first
// report is an operator who cannot sign in. That is the quiet failure the
// check exists to turn into a refusal at startup.
//
// Both planes generate from the same `internal/ent`, so there is one set of
// tables to check against and no second answer to keep in step.
func (s *Server) Ready(ctx context.Context, c Config) error {
	if err := s.ready(ctx, c.Db); err != nil {
		return err
	}
	if s.Control == nil {
		return nil
	}

	// Named, because the two databases are configured in two blocks and an
	// error saying a table is missing says nothing about which of them to go
	// and look at.
	if err := s.Control.ready(ctx, c.Control.Db); err != nil {
		return fmt.Errorf("control: %w", err)
	}

	return nil
}

// ready is [Server.Ready] for one plane, told what that plane's `db` block
// says.
//
// A method on the server rather than a function taking one, because what it
// needs is the three things `Build` worked out and kept -- the connection, the
// dialect it speaks and the client -- and re-deriving any of them from the
// configuration would be a second answer to a question already answered.
func (s *Server) ready(ctx context.Context, c config.DbConfig) error {
	if c.Migrate {
		return s.Ent.Schema.Create(ctx)
	}

	return migrate.Check(ctx, s.Db, s.Dialect, entmigrate.Tables)
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

			if err := s.Ready(ctx, *c); err != nil {
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
