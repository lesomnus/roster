package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/rbac"
	"github.com/lesomnus/payday/rbac/rbacfile"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/internal/ent"
	enttenant "github.com/lesomnus/roster/internal/ent/tenant"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/console"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/me"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/prove"
	"github.com/lesomnus/roster/server/session"
	rostersync "github.com/lesomnus/roster/server/sync"
	"github.com/lesomnus/roster/server/vouch"
)

// Server is a built app: the database it runs on and the two stacks it answers
// through.
type Server struct {
	// PolicyFile is the deployment-owned source; accepted refreshes replace its
	// snapshot while the source set stays fixed across all authorization paths.
	PolicyFile    *rbacfile.File
	policySources []rbac.Source

	Db  *sql.DB
	Ent *ent.Client

	// Drv is what the client was built on, kept because a transaction is begun
	// on a driver and a `*ent.Client` does not hand out the one it holds: `init`
	// begins its own on it, and `migrate` makes the schema on it.
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

	// broker is what `watch.broker` named for this plane, for [Server.HearsOnlyItself].
	broker string

	// Queue is how a write made **past** the servers is put on the stream:
	// the outbox's recorder, told of it inside the transaction that makes it,
	// as the servers' own recorders are told of theirs. What it queues the
	// drain publishes ([Build] runs one wherever there is a broker).
	//
	// It is for the writes nothing would otherwise say -- `forget` destroying
	// somebody and `restore` bringing them back, both through ent on purpose
	// (`server/forget`) -- in the way `pd.Record` is what puts the same writes
	// on the trail. Nil where this plane publishes nowhere: a row queued there
	// is a row nothing drains.
	Queue bare.Recorder

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

	// narrow is the scope the walled server narrows by, for what has to read
	// the database the way it does (`rostersync.Erased`).
	narrow bare.Scope

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
	// It exists because nothing can work it out. Both planes are the same
	// `Build` and the same `Grpc`, and `Control == nil` is true of the control
	// plane *and* of a deployment that has none -- so the one place they differ
	// has to be said rather than inferred.
	//
	// What it changes is who a key is at this listener: on the plane keys are
	// rows of, one is asked what its holder was bound ([Server.policy]), and
	// the trail names them.
	//
	// It used to open a key's general writes here as well, where a deployment
	// said `allow_general_writes`. No port serves a general write now
	// (`closed`).
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

	// Lockout and Password are `vouch:`'s numbers with the defaults filled
	// in, for every server built beside this one -- the admin port, the
	// sandbox -- to serve with the same rules.
	Lockout  vouch.Lockout
	Password core.Password

	// recheck is `stream.recheck`: how often a stream these servers have open
	// is asked again who opened it ([AskedAgain]).
	recheck time.Duration

	// owners is who answers which control-plane holder a deployment key hangs
	// off, for this plane's rules ([keyOwners]).
	owners *keyOwners

	// stopsSeen, where it is set, is told as each listener begins its stop,
	// with the deadline it was given, and as it has stopped. Nothing of the
	// app sets it: a test does, so that one grace for every listener is read
	// off the deadlines rather than timed.
	stopsSeen func(listener string, until time.Time, stopped bool)

	// fronts are the front doors this process runs beside the data plane
	// ([Server.Front]), which call into it: its gRPC server is not stopped
	// under a request one of them is still answering.
	fronts doors

	// RestoreWithin is `tenant.restore_within`, for the same servers: a close
	// undone at the admin port answers to the same grace as one undone here.
	RestoreWithin time.Duration

	// People is the cookie a **roster user** carries, minted by `AuthService`
	// on the data plane and resolved against its holders. Nil unless
	// `sign_in.enabled`, which is what leaves that door shut by default.
	//
	// Separate from [Server.Sessions], which is the roster operator's: two
	// planes, two databases, and a session in one names nobody in the other.
	People *authsession.Sessions

	// Sessions is the admin console's cookie: the endpoint that mints one and the
	// handler that reads it back. Nil where there is no control plane, since
	// the people who sign in are its holders and there would be nobody to be.
	//
	// **A table**, not a map: `authsession.New(session.New(control.Ent, ...))`
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

	// contents is what the trail keeps of each kind's rows, `audit.content`,
	// for what is written on the trail by something other than the recorder:
	// a name taken at the admin listener ([Releasing]).
	contents audit.Contents

	// Access is this plane's access record writer (payday#111): what a read's
	// row waits in to be written with the others of its second. Its loop is in
	// [Server.Spin], and each process runs its own, since what it holds is in
	// this one's memory.
	Access *access.Writer

	// log is the process's log as [Build] was handed it, for what the access
	// record says of itself where payday hands it no context to find one in
	// ([filed]): the log a context with nothing in it reaches discards.
	log *slog.Logger

	// accessKept is how long `audit:` keeps a row of this plane's access
	// record, for [Server.Serve] to say when that is for good and nobody said
	// so.
	accessKept trail.Keep

	// Spin is whatever this deployment has to run besides answering requests.
	// It is a slice rather than a method because a server with nothing to run
	// should write nothing at all; see `payday/spin`.
	//
	// Every loop in it is named, both planes' (see [loop]): under a lease the
	// processes of a deployment agree on a loop by its name and one of them
	// runs it, and `spin.Run` refuses a loop with none (payday#54).
	Spin []any
}

// queued is the recorder that writes each write to the outbox, as
// `pd.OutboxRecorder` does, told which row was written and not what was
// written of it: the method, by what, and the key.
//
// payday's keeps the write's patch document in the row. Nothing that reads
// the queue reads it -- the drain hands on keys, and a stream reads the row
// again to say what it is now -- and a row waits until a `serve` drains it,
// with no window, while forgetting somebody takes nothing out of the queue.
// So a display name written at the box where no `serve` drains was at rest in
// a row of `outbox` until one did. This is what `forget` queues already.
func queued() bare.Recorder { return keysOnly{pd.OutboxRecorder()} }

type keysOnly struct{ next bare.Recorder }

func (r keysOnly) Record(ctx context.Context, s bare.Server, c bare.Change) error {
	return r.next.Record(ctx, s, bare.Change{Method: c.Method, By: c.By, Key: c.Key})
}

// brokerRefused is a plane's broker refusing to be built, which the caller
// that builds the control plane names the setting of when it was taken from
// the data plane; see where it is answered.
type brokerRefused struct{ error }

func (e brokerRefused) Unwrap() error { return e.error }

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
	return build(ctx, c, keys.PrefixTenant, nil)
}

// build is one plane. `leaked` is a corpus already opened and checked, handed
// to the control plane by the data plane that read it -- see where it recurses.
func build(ctx context.Context, c Config, prefix string, leaked vouch.Breached) (*Server, error) {

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
	if said(c.Admin.ServerConfig) && !c.Control.Serves() {
		return nil, errors.New(
			"admin: the port takes a session cookie and resolves it against the control plane's " +
				"holders, and control.db.driver names no database, so there is nobody to be and no " +
				"listener is opened. name a control plane, or take the block out")
	}
	// A file that opens general writes, on whichever listener. No port serves
	// one whatever it is told (`closed`), so this is not what keeps them shut:
	// it is that a file which says the setting believes something this server
	// will not do, and a server that starts anyway leaves that for somebody to
	// find the day they lean on it.
	for _, v := range []struct {
		block string
		said  bool
	}{
		{"server", c.Server.AllowGeneralWrites},
		{"control", c.Control.AllowGeneralWrites},
		{"admin", c.Admin.AllowGeneralWrites},
	} {
		if v.said {
			return nil, fmt.Errorf(
				"%s.allow_general_writes: roster serves no general write -- `Patch` and `Apply` are how "+
					"its servers write, and a row is written over the wire by the verbs that carry its "+
					"rules (`Update`, and each entity's own). take the setting out", v.block)
		}
	}
	// And payday's keys that nothing of roster's reads, for the same reason:
	// the manual promises that a key nothing reads is refused by name, and
	// these were taken and changed nothing. No port registers reflection,
	// roster has no `migrate plan` for a dev database to be worked with, and
	// `watch.namespace` is read by payday's NATS broker alone, which roster
	// does not link: `memory` and `postgres` ignore it.
	for _, v := range []struct {
		key  string
		said bool
		why  string
	}{
		{"server.allow_reflection", c.Server.AllowReflection, "roster serves no reflection on any port"},
		{"control.allow_reflection", c.Control.AllowReflection, "roster serves no reflection on any port"},
		{"admin.allow_reflection", c.Admin.AllowReflection, "roster serves no reflection on any port"},
		{"db.dev_dsn", c.Db.DevDsn != "", "roster plans no migration, and db.migrate is how its tables are made"},
		{"control.db.dev_dsn", c.Control.Db.DevDsn != "", "roster plans no migration, and control.db.migrate is how its tables are made"},
		{"watch.namespace", c.Watch.Namespace != "", "no broker roster links reads it; memory and postgres keep a deployment apart without one"},
		{"control.watch.namespace", c.Control.Watch.Namespace != "", "no broker roster links reads it; memory and postgres keep a deployment apart without one"},
	} {
		if v.said && prefix == keys.PrefixTenant {
			return nil, fmt.Errorf("%s: nothing reads it -- %s. take the setting out", v.key, v.why)
		}
	}
	// And an `audit:` block that cannot be meant: a setting nothing reads, or
	// two that contradict each other. Each of them started, and did something
	// other than what its file says -- a kind kept for ever, a trail that
	// destroys nothing, a check that finds every row -- with nothing anywhere
	// to say so. Refused here for the reason the blocks above are, and before
	// anything is opened; see [AuditMeant], which says of each which it is.
	//
	// The nested build that raises the control plane is handed what the
	// trail keeps of each kind and nothing else ([contentOf]), which is a
	// block this takes: a kind's content alone names no window, and keeps
	// the deployment's.
	if err := AuditMeant(c.Audit); err != nil {
		return nil, err
	}
	// A `scim:` block likewise; `roster scim serve` asks the same.
	if err := c.Scim.Meant(); err != nil {
		return nil, err
	}
	// And the rest of the file ([Config.Meant]), after those two, whose words
	// are their own. Once, where it is the deployment's: the nested build that
	// raises the control plane is handed a config this one made.
	if prefix == keys.PrefixTenant {
		if err := c.Meant(); err != nil {
			return nil, err
		}
	}
	// A directory's key is a tenant key, and a deployment with no control
	// plane reads none: its callers are believed by name (`auth.Plain`), so
	// the endpoint would answer whoever wrote one down.
	if c.Scim.Serves() && !c.Control.Serves() {
		return nil, errors.New(
			"scim: a directory signs in with a tenant key, and control.db.driver names no database, so " +
				"no key is read and every caller would be believed by name. name a control plane, or take the block out")
	}
	// A plane whose database is named by nothing. SQLite reads an empty name
	// as a private temporary database, a new one for each connection of the
	// pool: under `migrate: true` the tables were made on one connection, the
	// loops found none on the others, and whatever was written was thrown
	// away -- which is what a variable spelt wrong left, with one line on
	// standard error to say so.
	if prefix == keys.PrefixTenant {
		for _, v := range []struct {
			key string
			db  config.DbConfig
		}{
			{"db", c.Db},
			{"control.db", c.Control.Db},
		} {
			if v.db.Driver != "" && v.db.Dsn == "" {
				return nil, fmt.Errorf("%[1]s.dsn: names no database, and %[1]s.driver is %[2]q -- a driver "+
					"handed no name opens whatever it opens by default, and SQLite's is a database of its own for each "+
					"connection that keeps nothing. name the database (a variable that says it is ROSTER_%[3]s_DSN)",
					v.key, v.db.Driver, strings.ToUpper(strings.ReplaceAll(v.key, ".", "_")))
			}
		}
	}

	// And the two planes are two databases.
	//
	// Somebody who runs this deployment and somebody who is a customer of it
	// are told apart by nothing but which database their row is in: the
	// sessions of each plane are a table of its own, and so are the holders
	// and what they are bound. A file that names one database twice --
	// `control.db` pasted from `db`, which two blocks of one shape invite --
	// made a deployment that started and served like any other, in which a
	// customer's administrator sent the cookie the data plane gave them to
	// `admin.addr` and was an operator: their binding in their own tenant,
	// read where an operator's is, on the stack with no wall (#275).
	//
	// Here for the file that says so outright, before anything is opened or
	// written. [Apart] is the same refusal for one that says it by another
	// spelling, asked of the rows once there are any.
	if c.Control.Serves() && sameDatabase(c.Db, c.Control.Db) {
		return nil, errors.New(
			"control.db: the same database as db. The two planes are two databases -- who runs this " +
				"deployment and who is a customer of it are told apart by which one their row is in, " +
				"and in one a customer's session is an operator's at admin.addr. name another database for control.db")
	}

	// And the same shape once more, for the page a roster user opens.
	//
	// `sign_in.enabled` is what registers `AuthService` on this listener, so
	// without it the page is served, loads, draws a form, and is answered
	// `Unimplemented` by a method that is not on the wire. There is nothing in
	// that for anybody to read: the page looks served and the deployment looks
	// up.
	if c.Server.Console.Dir != "" && !c.SignIn.Enabled {
		return nil, errors.New(
			"server.console.dir: the page is where a roster user signs in, and sign_in.enabled is off " +
				"-- so AuthService is not on this listener and the form on it can never be answered. " +
				"turn sign_in.enabled on, or take the page out")
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

	var policyFile *rbacfile.File
	var policySources []rbac.Source
	if c.Policy.File != "" {
		var err error
		policyFile, err = rbacfile.Open(c.Policy, policySubjects(ctx, client, c.Policy.File))
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("policy: %w", err)
		}
		policySources = []rbac.Source{policyFile}
	}

	// The server that talks to the database, twice: once as it is, and once
	// with the wall on it.
	//
	// Two things are said to it rather than to the stack, and for the same
	// reason -- both are about the statement that runs. The trail is kept by
	// the servers that do the writing, since every RPC that changes anything
	// has to report itself from inside the transaction that changes it. The
	// wall is a predicate and a predicate belongs in the WHERE.
	b, err := c.Watch.Build(ctx, c.Db)
	if err != nil {
		db.Close()
		return nil, brokerRefused{err}
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

	// What a write made past the servers is queued through, where there is
	// anything to publish it to; see [Server.Queue].
	var queue bare.Recorder
	if b != nil {
		queue = queued()
	}

	// The recorders, in the order they are told. The trail is required -- a
	// write it could not account for is undone -- and the rest say for
	// themselves whether they are; see `bare.Recorders`.
	// Every write is told to these, in this order, inside the transaction that
	// makes it. It is one value rather than the option given three times
	// because `WithRecorder` refuses to be given twice: neither losing one nor
	// quietly appending in call order is a thing a framework should decide, so
	// the order is written here where it can be read.
	//
	// What the trail keeps of each kind's writes is the deployment's to say,
	// `audit.content` (payday#92) -- read here, where the trail is wired, so
	// that a setting nobody handed over is not a setting that does nothing.
	content, err := c.Audit.Contents()
	if err != nil {
		db.Close()
		return nil, err
	}
	// The access record's after the publish (payday#111): what it writes at
	// the commit is of the data the call's writes wrote, so it is told of each
	// and writes once, in the same transaction.
	rec := bare.Recorders{pd.Recorder(content), pd.WatchRecorder(w), pd.AccessRecorder()}
	if c.Watch.Outbox {
		// Last, so that a queue this could not be written to undoes a write
		// which the trail and the in-process publish have already agreed to
		// rather than the other way round. It makes no difference to what is
		// committed -- either way nothing is -- and it makes the log read in
		// the order things were tried.
		rec = append(rec, queued())
	}

	// And before all of them, the one question asked of a write once it is
	// made: whether whoever its row names was forgotten while it was being
	// decided (`forget.Nobody`, #280). First, so that a write refused for it
	// has been told to nobody -- not the trail, and not what remembers a
	// write to publish it.
	rec = append(bare.Recorders{forget.Nobody()}, rec...)

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
	//
	// And [People], for the rows that name no site: a person and what is
	// theirs, reached by a grant made in a site when they are in it.
	sets := Sets(client, policySources...)
	narrow := bare.Scopes{pd.Wall(), pd.Grouped(sets), People(sets)}

	walled, err := pd.NewSink(client, append(opts, bare.WithScope(narrow))...)
	if err != nil {
		db.Close()
		return nil, err
	}

	// The corpus, checked for the order it has to be in rather than trusted:
	// one that is not sorted answers *no* to things that are in it, which is
	// the direction that fails quietly.
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
	lockout := vouch.Lockout{Failures: c.Vouch.Lockout.Failures, For: c.Vouch.Lockout.For}.Or(vouch.DefaultLockout)
	password := core.Password{MinLength: c.Vouch.Password.MinLength, NoReuse: c.Vouch.Password.NoReuse}.Or()

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
	// `core.SelfBuild()` is one rule -- a caller writes their own password and
	// nobody else's -- and it is a link of its own. It was a link so that one
	// caller could be built below it: `Vouch.Reset` wrote *through*
	// `Credential.Set` from outside the stack, so it was handed a second build
	// of this same list with this link left out.
	//
	// That verb is `Credential.Issue` now and calls its own `Set` from inside
	// the layer, where `Self` -- which overrides `Set` and nothing else -- never
	// sees it. So the second stack is gone and this rule stays a link of its
	// own, which is where a reader finds it. See `server/core/self.go`.
	// Which control-plane holder a deployment key hangs off, asked of a
	// database that does not exist yet: the control plane is built below, by
	// this same function, and `owners` is pointed at it once it is. Until then
	// -- and in a deployment with none -- it answers that it cannot say.
	owners := &keyOwners{}
	rules := Rules(client, policySources...)
	rules.Borrower = owners.of
	// With what this deployment's trail keeps of a row, which `Rules` is not
	// handed: a name taken is on the trail of whoever held it.
	rules.Releasing = Releasing(client, content)

	stacked, err := app.Build(walled.WithWatch(w), core.Build(rules, core.On(drv, Locking(client)), core.WithBreached(core.Breached(leaked)), core.WithKeyring(keyring), core.WithPrefix(prefix), core.WithLockout(lockout), core.WithPassword(password), core.WithProving(c.Host.Asking()), core.WithRestoreWithin(c.Tenant.RestoreWithin)), core.SelfBuild(), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild(), core.NotErasedBuild())
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
	ungated, err := app.Build(sink.WithWatch(w), core.Build(rules, core.On(drv, Locking(client)), core.WithBreached(core.Breached(leaked)), core.WithKeyring(keyring), core.WithPrefix(prefix), core.WithLockout(lockout), core.WithPassword(password), core.WithProving(c.Host.Asking()), core.WithRestoreWithin(c.Tenant.RestoreWithin)), pd.AuditBuild())
	if err != nil {
		db.Close()
		return nil, err
	}

	s := &Server{
		PolicyFile: policyFile, policySources: policySources,
		Db: db, Ent: client, Drv: drv, Dialect: dialect, Watch: w, broker: c.Watch.Broker, Queue: queue, sink: sink, narrow: narrow,
		Walled: stacked, Ungated: ungated, Keyring: keyring, Breached: leaked, Lockout: lockout, Password: password,
		RestoreWithin: c.Tenant.RestoreWithin,
		recheck:       c.Stream.Recheck,
		owners:        owners,
		contents:      content,
		log:           log.From(ctx),
	}

	if policyFile != nil {
		s.Spin = append(s.Spin, policyFile)
	}

	// Who read or wrote whose personal data, on this plane (payday#111). A
	// read's row is written a second later with the others, by a loop each
	// process runs for itself and that writes what it holds once more as it
	// stops; a write's is in its own transaction.
	//
	// What a row keeps of the request it is of is how it was asked, and none
	// of the words it was asked by ([wordless]); and what the writer writes in
	// one go is written whole or not at all ([whole]).
	s.Access = &access.Writer{Sink: wordless(whole(client))}
	s.Spin = append(s.Spin, loop(prefix, "access", s.Access))

	// The door a roster user signs in at, where this deployment serves one.
	//
	// **This** plane's rows: the same table on the other database, so a session
	// minted here names somebody the wall can narrow -- which is the difference
	// from the operator's, built with the control plane below, and the reason
	// that one is not on this plane's chain.
	//
	// Built before the branch below because it is this plane's and not the
	// control plane's: a deployment with no `control` still has tenants, and
	// the people in them are who this is for.
	//
	// Handed what this plane's recorder is, since a session made, used and
	// ended is on this plane's trail as each (`server/session`).
	if c.SignIn.Enabled {
		s.People = authsession.New(session.New(client, content))
		s.Spin = append(s.Spin, loop(prefix, "sessions", session.Sweep(client, content, session.Swept)))

		// And with no control plane there is no chain below to join, so this
		// is the whole of one: a cookie, and then whatever a deployment that
		// believes its callers was going to do anyway. `Plain` is what nil
		// means here (see [Server.Auth]), written out because `Seq` needs
		// something to fall through to -- a request with no cookie must reach
		// it, or a deployment that turned this on would have turned every
		// other way of calling off.
		//
		// Only then: with a control plane the cookie joins the chain below,
		// and nobody is believed on their word. Built anyway, `Plain` would
		// say once in the log that every caller is believed -- of a
		// deployment that believes nobody, which is the one line an operator
		// reads to know which it is.
		if !c.Control.Serves() {
			s.Auth = auth.Seq(s.People.Handler(), auth.Plain())
		}
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

			// How often an open stream is asked again, which is one setting
			// for the deployment and so the control listener's too.
			Stream: c.Stream,

			// The same numbers for an operator's password as for anybody's:
			// `vouch.lockout` and `vouch.password` are one block in the file,
			// and a floor that held for a customer's people and not for the
			// person who runs the deployment would be backwards.
			//
			// And the same keyring, for that reason. It was left out -- *the
			// control plane holds no second factors* -- so whoever runs a
			// deployment, who is every customer's root at `admin.addr`, was
			// the one account in it that could not ask for a second proof:
			// enrolling answered that the deployment held no key to wrap a
			// seed with, whatever `vouch.keys` said (#275).
			Vouch: VouchConfig{Lockout: c.Vouch.Lockout, Password: c.Vouch.Password, Keys: c.Vouch.Keys},

			// What the trail keeps of a write, and nothing else of `audit`
			// (payday#92). It is one decision for the deployment's trail,
			// whichever plane wrote the row -- an operator's acts are the
			// control plane's rows, and a deployment that keeps rows keeps
			// theirs. The clocks and the archive stay the data plane's alone,
			// which is why this config had no `audit` before: see the policy
			// below, which this leaves off.
			Audit: contentOf(c.Audit),

			// And the corpus, which this left out while saying the line above
			// (#18): every tenant's password -- their own change in the
			// console, `init --password-stdin`, `control vouch set` -- was
			// the one write in the deployment nothing checked against it.
			// Handed over as the value this build already opened rather than
			// as `vouch.breached` again, because `vouch.Sorted` reads the whole
			// file and the published corpus is tens of gigabytes.
		}, keys.PrefixDeployment, leaked)
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

			// And the broker the control plane took refusing its database --
			// `postgres` over a SQLite `control.db` -- for the same reason.
			if errors.As(err, new(brokerRefused)) && c.Control.Watch.Broker == "" {
				return nil, fmt.Errorf(
					"control: %w (control.watch.broker is empty, so it took watch.broker; the control plane takes "+
						"the data plane's broker unless control.watch.broker names its own)", err)
			}

			return nil, fmt.Errorf("control: %w", err)
		}

		control.Keys = true
		control.owners.control = true

		s.Control = control
		owners.db = control.Ent

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
		chain := []auth.Handler{
			keys.Acting(control.Ungated, s.Ungated),

			// And a deployment key narrowed to the holder a `Host` nominates,
			// for the app in front of many tenants that holds one credential.
			// Ahead of `Bearer` for the reason `Acting` is: a request that
			// declares a name and gets it wrong must stop rather than fall
			// through to be answered as the key itself, which is the wide
			// frame it was trying to replace.
			keys.At(control.Ungated, s.Ungated),

			auth.Bearer(keys.Store(control.Ungated, s.Ungated)),
		}

		// And a roster user's own cookie after all three, where this
		// deployment serves the door that mints one.
		//
		// **This** plane's rows: the same table on the other database, so a
		// session here names somebody the wall can narrow -- which is the
		// difference from the operator's below, and the reason that one is not
		// on this chain.
		//
		// **Last**, and it was first. A cookie and a header are read from
		// different places, so the order decides only which answers a request
		// carrying both -- and that was said to decide nothing. It decides
		// what was checked. Answered by the cookie, a request's `roster-as`
		// and its key were looked at by no handler, and `keys.Authenticated`
		// reads those headers for when somebody signed in: a session, which
		// says nothing of when, was fresh by a delegation nothing had checked
		// (#276). A header is a caller saying who is asking, so it is what
		// answers, and a wrong one refuses; a browser sends neither.
		if s.People != nil {
			chain = append(chain, s.People.Handler())
		}

		s.Auth = auth.Seq(chain...)

		// What it serves with, said once as `Plain` says itself where it is
		// what serves: so that the log says which of the two this is.
		if s.People != nil {
			log.From(ctx).InfoContext(ctx, "auth: callers are who a key from the control plane says, or the person a session cookie was minted for; nobody is believed on their word")
		} else {
			log.From(ctx).InfoContext(ctx, "auth: callers are who a key from the control plane says; nobody is believed on their word")
		}

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
		//
		// What it records of a session -- made, used, ended, and collected by
		// the sweep below -- is what the control plane's own recorder is
		// handed: `contentOf`, which keeps no digest.
		controlContent, err := contentOf(c.Audit).Contents()
		if err != nil {
			db.Close()
			return nil, err
		}
		s.Sessions = authsession.New(session.New(control.Ent, controlContent))

		// And somebody to collect them, because nobody else will. A store
		// behind `authsession.Store` has `Put`, `Get` and `Del` and no pass
		// over everything -- and `Del` is a soft erase, so even signing out
		// leaves the row. Without this the table is one row per sign-in since
		// the deployment started.
		//
		// Appended to **this** server's `Spin` and not the control plane's, on
		// its client. See below: what the nested `Build` collected was never
		// run.
		//
		// Named as the control plane's, whose rows it collects, though it is
		// appended by this one.
		s.Spin = append(s.Spin, loop(keys.PrefixDeployment, "sessions", session.Sweep(control.Ent, controlContent, session.Swept)))

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
		loop(prefix, "keys", keys.Sweep(s.Ent, content, keys.Swept)),
		loop(prefix, "vouch", vouch.Sweep(s.Ent, content, vouch.Swept)),
		// And abandoned hostname claims, which arrive at human speed rather
		// than per sign-in -- so a day rather than the hour the two above use.
		loop(prefix, "prove", prove.Sweep(s.Ent, content, prove.Swept)),
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
	//
	// The access record is a kind of the trail's, so the same holds of it: a
	// window for it (`audit.by.access`) is this plane's, and the control
	// plane's own record -- operators reading operators -- is never swept. The
	// manual says so (#280), and [Server.unbounded] says when this plane's has
	// no window either.
	//
	// With the deployment's lease on it, for the turns the passes take with
	// what erases from the trail beside them; see [TrailPolicy].
	p, err := TrailPolicy(ctx, c)
	if err != nil {
		db.Close()

		return nil, err
	}

	// And roster's holds, which every act that destroys answers to (#98):
	// an operator's hold on a tenant keeps what the trail holds of it, as
	// payday's own holds do. Asked whether there is a retention policy
	// **before** they are added, since a policy that answers per tenant reads
	// as one that is on -- and a deployment that named no window must not
	// start a sweep because roster learnt to hold.
	retains := p.On()
	s.accessKept = p.For(pd.AccessDomain)
	p = forget.Held(p, s.Ent)
	if retains {
		log.From(ctx).InfoContext(ctx, "trail: retention", "policy", p.String())

		s.Spin = append(s.Spin, loop(prefix, "trail", trail.Sweep(pd.TrailStore(s.Ent), p)))
	} else {
		// And where there is no pass, what a pass does first and nothing
		// else of one: the erasures the trail owes, paid.
		//
		// A forget owes what it could not erase at once -- above all what a
		// hold kept, until the hold lifts -- and the trail's pass was the only
		// thing that paid afterwards. A deployment that names no window has
		// no pass, which is the default, so there a debt was never paid and a
		// hold that lifted was followed by nothing (#280). This applies no
		// window: it erases what a forget already decided to, and a trail
		// kept for ever is still kept for ever (`forget.SweepOwed`).
		//
		// In every `Build`, for the reason the expiry sweeps are: nobody of
		// the control plane is forgotten today, so its loop reads an empty
		// table once a day, and a plane that comes to owe with no loop behind
		// it is an erasure that is never made.
		s.Spin = append(s.Spin, loop(prefix, "owed", forget.SweepOwed(s.Ent, p)))
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

		// The trail's own policy, so that what it erases of somebody answers
		// to the same holds the retention sweep does.
		s.Spin = append(s.Spin, loop(prefix, "forget", forget.Sweep(s.Ent, c.Holder.ForgetAfter, p, content, s.Queue, c.Holder.Every)))
	}

	// And what somebody consented to, kept past them as proof for
	// `consent.keep` and then destroyed -- by the deployment, so that no app
	// sweeps a ledger it shares with others. Nothing is appended when no
	// window is named: kept for good.
	if c.Consent.Keep > 0 {
		log.From(ctx).InfoContext(ctx, "consent: after an erase", "keep", c.Consent.Keep)

		s.Spin = append(s.Spin, loop(prefix, "consent", forget.SweepConsents(s.Ent, c.Consent.Keep, content, c.Consent.Every)))
	}

	// And a tenant's, one level up (#98): the closed whose grace has run out,
	// and the aliases whose hold is over. Always, because the second clock
	// has a default -- a deployment that destroys by hand still frees the
	// names a year later -- and only on the data plane, whose tenants are
	// customers; the control plane's one tenant is never closed.
	if prefix == keys.PrefixTenant {
		if c.Tenant.RestoreWithin > 0 {
			log.From(ctx).InfoContext(ctx, "forget: a tenant, after it closes",
				"after", c.Tenant.RestoreWithin, "alias held", c.Tenant.Hold())
		}

		s.Spin = append(s.Spin, loop(prefix, "tenants", forget.SweepTenants(s.Ent, s.Ungated, c.Tenant.RestoreWithin, c.Tenant.Hold(), p, content, s.Queue, c.Tenant.Every)))
	}

	if b != nil {
		// The loop that publishes what is queued. It is not a layer and not a
		// method on any server -- `spin.Run` finds it in whatever is handed
		// over, which is what keeps a server that has no background work from
		// carrying an empty method saying so.
		//
		// **Wherever there is a broker**, and not on `watch.outbox` alone,
		// because this process is not the only one that writes to the queue.
		// Every command at a shell beside it does, whatever this file says of
		// the outbox -- a process that ends with its command has nobody to
		// publish to, so it leaves its writes there for this one
		// ([Config.Queued], #214) -- and so does whatever is written past the
		// servers ([Server.Queue]). Spun on the setting alone, the drain was
		// missing from exactly the deployments those rows are for: one replica
		// on `memory`, where a write made at the shell was heard by nobody
		// until an app asked again.
		//
		// What it costs a deployment that queues nothing is a read of an empty
		// table every `watch.drain_every`, against an index.
		s.Spin = append(s.Spin, loop(prefix, "outbox", pd.Drain(client, b, c.Watch.Every())))
	}

	return s, nil
}

// loop names one of a plane's loops, which is what a lease holds it by
// (`spin.lease`, payday#54): the processes of a deployment agree on a loop by
// its name, and one of them runs it.
//
// By plane, because the planes are two databases and each has its own copy of
// most of these. `roster.keys` and `roster.control.keys` sweep two tables, and
// were they one name the lease would have them take turns -- each plane's table
// unswept for as long as a process held the other's. It is also why the names
// payday gives its own (`payday.trail`, `payday.outbox`) are not kept: both
// planes may have an outbox, and two loops of one name are refused before any
// of them starts.
func loop(prefix string, name string, s spin.Spinner) spin.Spinner {
	if prefix == keys.PrefixTenant {
		return spin.Called("roster."+name, s)
	}

	return spin.Called("roster.control."+name, s)
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

// policy is what the gate asks at this server's listener: [Policy], and
// [PolicyOfKeys] on the plane the deployment's keys are rows of.
func (s *Server) policy() gate.Policy {
	if s.Keys {
		return PolicyOfKeys(s.Ent, s.policySources...)
	}

	return Policy(s.Ent, s.policySources...)
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
	// The two nils are told apart by [Server.Keys], which says the plane is
	// the one that holds them: the control plane, and nothing else. It is not
	// told apart by `Auth`, which is how a credential is read and says nothing
	// about where keys are -- a deployment with no control plane is given one
	// too, by its sign-in or by an app that runs it in its own process -- and
	// such a deployment's own rows are not keys. Under Plain an identifier is
	// whatever the caller writes, including one of this domain naming a
	// **tenant** key's row in `s.Ungated` -- a row that exists, which `keyed`
	// would take for a deployment key and the policy would hand
	// `frame.Everything`. That is the escalation `keys.Store` resolves tenant
	// keys to their holder to prevent, so here it stays what it always was: no
	// control plane, no keys, refused.
	var keyrows app.Server
	switch {
	case s.Control != nil:
		keyrows = s.Control.Ungated
	case s.Keys:
		keyrows = s.Ungated
	}

	r := Resolver(s.Ungated, keyrows)

	// One function, installed twice, because a stream and a unary call are two
	// interceptors and `closed` is one answer. Built here rather than called
	// twice below so that the two cannot come to disagree.
	shut := s.closed()

	// One limiter, handed to both halves.
	//
	// `Limiter()` builds a bucket, so calling it twice is two limits with the
	// same numbers on them and neither counting what the other let through --
	// which is a rate of `n` per second answering `2n`, silently. Held in a
	// variable for the reason `shut` is: one thing the chain is told, rather
	// than a constructor called wherever it is needed.
	rate := c.Server.Limiter()

	// One policy, asked when a call arrives and asked again of a stream that
	// is still open ([AskedAgain]).
	policy := s.policy()

	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Server.CallTimeout())).
		With(s.policyInterceptors()).
		WithStream(AskedAgain(s.recheck, h, r, Public, policy)).
		WithUnary(auth.InterceptorUnary(h, r, Public)).
		WithStream(auth.InterceptorStream(h, r, Public)).
		WithUnary(grpcx.LimitUnary(rate, gate.ByTenant(), pd.Cost)).
		WithStream(grpcx.LimitStream(rate, gate.ByTenant())).
		With(s.accessed().Interceptor()).
		WithUnary(callingUnary()).
		WithStream(callingStream()).
		With(gate.Interceptor(policy)).
		With(s.Watch.Interceptor()).
		WithUnary(grpcx.ClosedUnary(shut)).
		WithStream(grpcx.ClosedStream(shut)).
		WithUnary(Undeclared()).
		With(NotByVerifier()).
		WithUnary(Undated())

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
	Register(g, s.Walled)

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
	//
	// And the database beside it, for the two things the walled server cannot
	// answer: whether somebody it no longer shows was erased where this caller
	// may see, narrowed by the same scope; and whom a row of the access model
	// reached, erased or not, which is read through the tenant's wall alone.
	// The person it reaches is then read through the whole narrowing, so a
	// follower bound in a site hears of a grant made across the tenant to
	// somebody it may ask about, and of nobody it may not (`server/sync`).
	app.RegisterSyncServiceServer(g, rostersync.Service(s.Walled, s.Watch,
		rostersync.Erased(s.Ent, s.narrow), rostersync.Reaching(s.Ent, pd.Wall())))

	// A customer minting a key for themselves, which until now needed a shell
	// on the box.
	//
	// The control plane serves the same service, by the same `Register`, and
	// mints the other kind there: which kind is the stack's (`core.WithPrefix`),
	// and [Server.Keys] is the one flag that tells the two planes apart because
	// `Control == nil` cannot.
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
	// What a caller is, in one round trip -- registered below -- built here
	// because what leaving is belongs to it, and a link that confirms somebody
	// leaving is the same leaving (`server/vouch/leave.go`).
	//
	// With the stack its writes go through. The reads go to ent because the
	// missing subject has already narrowed them; the writes have rules on them,
	// and the rules live in a layer. And the forget window, for `Leave` to say
	// when somebody who left will be forgotten.
	mine := me.New(s.Ent, Everything(s.Ent, s.policySources...), me.WithWrites(s.Walled), me.WithOwn(Own(s.Ent)),
		me.WithForgetAfter(c.Holder.ForgetAfter))

	// The one service that is not an entity: it answers yes or no about a row
	// nothing else may read. See `server/vouch`.
	// The rule about who may write whose credential travels with the service,
	// because `VouchService` is hand-written and no layer wraps it. Same rules
	// the gate reads, handed over rather than asked for a second time.
	//
	// And the gate's own policy, for the one rule that asks what a caller may
	// call rather than whose row it is: a delegation is within that
	// (`vouch.WithGate`).
	app.RegisterVouchServiceServer(g, vouch.New(s.Ungated, s.Walled, vouch.WithLockout(s.Lockout),
		vouch.WithKeys(s.Keyring), vouch.WithLeaving(mine), vouch.WithGate(s.policy())))

	// What a front door asks before it knows anything, and therefore through
	// the server the wall was never installed on. Neither RPC answers with a
	// row -- one identifier or one provider name -- which is what keeps that
	// from being a hole; `server/front` says it at length.
	app.RegisterFrontServiceServer(g, front.New(s.Ungated))

	// And the door a **roster user** signs in at, where this deployment serves
	// one. Registered rather than served-and-refusing: a method that answers no
	// is a method somebody can count answers from, and `sign_in.enabled` is off
	// by default (see [SignInConfig]).
	//
	// Which tenant comes from the name the browser arrived at, because on this
	// plane there is nowhere else it could come from -- `AuthSignInRequest` has
	// no tenant field, and one added would be a caller naming the tenant it
	// would like to be checked against. `Hosted` is that resolution and a name
	// nothing claims is a refusal.
	if s.People != nil {
		app.RegisterAuthServiceServer(g, console.Auth(s.Ungated, s.Ent, s.People,
			console.WithTenant(Hosted(s.Ent)), console.WithArrival(ArrivedAt),
			console.WithVouch(vouch.WithLockout(s.Lockout), vouch.WithKeys(s.Keyring))))
	}

	// And what a caller is, in one round trip. None of its methods takes a
	// subject, so none can be pointed at anybody else; see `server/me` and
	// [aboutYourself].
	app.RegisterMeServiceServer(g, mine)

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
	// anybody has been resolved, so there is no frame to narrow by. No read a
	// caller has would do: `ApiKeyService/Get` and `/List` are served behind
	// the gate and the wall, which ask for the person this is asked to find,
	// answer no verifier, and refuse a key named by one ([NotByVerifier]).
	pdpb.RegisterTokenServiceServer(g, keys.Service(s.Ungated))

	return g, nil
}

// register puts every service on the wire, and it is written out rather than
// being `app.RegisterServer` because of the ones that are missing.
//
// `SessionService`, `LinkService` and `ContinuationService` are not here, and
// `CredentialService`, `ApiKeyService` and `DelegationService` are here for
// their verbs. Each of the six has a generated `Get` that answers with whatever
// columns it was asked for, and one of them is a verifier -- a password hash, a
// key hash, the hash of a delegation, the digest of a session cookie, the
// secret of a link, the secret of a continuation -- every one of them a column
// declared `secret`. The first three are served by nothing: the rows exist and
// this app reads them, in process, but there is no method on this server that
// answers with one. Of the other three, `closed` shuts a credential's reads and
// its `Add`, and a key's and a delegation's `Add`; their reads are served, and
// the column is cleared out of every answer.
//
// For the first three this is the **only** control that closes them, and that
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
func Register(g grpc.ServiceRegistrar, s app.Server) {
	// `CredentialService` is served for its overlays (`Set`, `Issue`, `Unlock`,
	// `Enrol`) and its `Erase`, and its generated reads and raw `Add` are shut
	// by method in `closed` -- so nothing on the wire answers with the `secret`
	// column.
	app.RegisterCredentialServiceServer(g, s.Credential())
	// `DelegationService` for its overlays (`Revoke`, `Exchange`) and its
	// `Get`, `List` and `Erase`, which answer no token: the column is cleared
	// out of every answer. Its raw `Add` is shut by method in `closed`.
	app.RegisterDelegationServiceServer(g, s.Delegation())
	// `ApiKeyService` for its `Issue` overlay -- the mint `IssueService.IssueKey`
	// was, and its `Get`, `List` and `Erase`. Its raw `Add` and its general
	// writes are shut by method in `closed`, on every port, so a key is made by
	// `Issue` alone and the prefix it stamps is the stack's, not the caller's.
	app.RegisterApiKeyServiceServer(g, s.ApiKey())
	app.RegisterTenantServiceServer(g, s.Tenant())
	app.RegisterHostServiceServer(g, s.Host())
	// Served, reads and all: the token is what a tenant has to go and look at
	// while they are in somebody else's web form, and it is not a secret --
	// `host.proto` says why it is the one token here that is stored as it is
	// compared. The wall narrows a claim to whoever made it.
	app.RegisterHostProofServiceServer(g, s.HostProof())
	app.RegisterMailDomainServiceServer(g, s.MailDomain())
	// Served, reads and all: a nomination names a tenant's holder and a
	// control-plane identifier, neither of them a secret, and a tenant
	// administrator is who decides which app acts as whom in their tenant.
	// `server/core` holds the writes to the rule a way into an account is.
	app.RegisterNominationServiceServer(g, s.Nomination())
	app.RegisterConnectionServiceServer(g, s.Connection())
	app.RegisterHolderServiceServer(g, s.Holder())
	app.RegisterIdentityServiceServer(g, s.Identity())
	app.RegisterEmailServiceServer(g, s.Email())
	// Served, reads and all: a number is where somebody is reached, the wall
	// narrows it to their tenant, and it is no secret. Whose account one is
	// written into is `server/core/phone.go`'s (`phone.proto`).
	app.RegisterPhoneServiceServer(g, s.Phone())
	// Served, reads and all: who somebody is to somebody else is a record of
	// two people the wall already narrows, and no rule reads it
	// (`relation.proto`).
	app.RegisterRelationServiceServer(g, s.Relation())
	app.RegisterSiteServiceServer(g, s.Site())
	app.RegisterTeamServiceServer(g, s.Team())
	app.RegisterRoleServiceServer(g, s.Role())
	app.RegisterGroupServiceServer(g, s.Group())
	app.RegisterGroupMembershipServiceServer(g, s.GroupMembership())
	app.RegisterGroupExposureServiceServer(g, s.GroupExposure())
	app.RegisterBindingServiceServer(g, s.Binding())
	app.RegisterSiteMembershipServiceServer(g, s.SiteMembership())
	app.RegisterTeamMembershipServiceServer(g, s.TeamMembership())
	app.RegisterAuditServiceServer(g, s.Audit())
	// Not `OutboxService`: it has no method, and what keeps one tenant's
	// writes from another's is that nothing serves it at all
	// (`outbox.proto`). Registered, a release of payday that gave it a read
	// would be served on every port by moving a pin.
	// For its overlays (`Agree`, `Withdraw`, `Settled`) and its reads, which
	// `server/core` narrows to the calling app's own. Its generated writes are
	// shut by method in `closed`: a consent is never written as a row.
	app.RegisterConsentServiceServer(g, s.Consent())
}

// closed is what this server does not answer at all.
//
// No general write, and the methods below that are shut one at a time on a
// service that is registered for its others: a service that is not registered
// at all needs nothing here, since gRPC dispatches by name and has nothing to
// dispatch to.
//
// # No general write is served, on any port, whatever a file says
//
// `Patch` takes a field per property and `Apply` a patch document, and between
// them they write anything the schema has. They are how the servers write.
// What a caller may change, and under what conditions, is on a verb of its
// own -- `Update`, `Set`, `Issue`, `Resubject`, `Narrow` -- and that is where
// every rule about a row was put.
//
// payday closes both unless a deployment says `allow_general_writes`, and
// calls saying it *a decision about the API rather than about a deployment*.
// This is roster's: it is not a setting here. It was read, and each time this
// repository was read for what the setting opened, it had gone round a rule
// that lives on a verb: a credential's row written past the reach rule and the
// corpus (#275); a key's verifier chosen by its caller (#276); a name renamed
// past its proof (#277); and then a connection's secret and issuer, somebody
// else's account at a provider, a proved address, a role widened to
// everything through `Apply`, and the name a relying party grants on (#278).
// Each of the first three was shut by name as it was found. The fourth time is
// the rule: an entity nobody had read yet was open by default, and a rule
// written tomorrow would have had to be written three times -- on the verb, on
// `Patch`, and on a document `Apply` would have to be read to refuse.
//
// So the setting is refused at start ([Build]) rather than ignored, because a
// file that says it believes something this server will not do; and this
// closes both whatever configuration it is handed, so a server built by a
// test, or by a caller of this package, is not a second answer.
// `TestNoGeneralWriteIsServed` asks every service that is registered.
//
// The shell at the box is not this: it reaches the servers with no transport
// in between, and `roster holder patch` there is somebody who already holds
// the database (`cli/entity.go`).
//
// It is installed as **both** interceptors, in the pair `auth` is installed as
// two lines above. Only the unary one was here, which was F10: a deployment that
// closed a registered, watchable service -- `Holder` is one -- closed its reads
// and left its stream open, and a `WatchItem` carries the whole message with no
// `select` to narrow it. Nothing said so, in the one word whose whole job is to
// mean *not served*.
func (s *Server) closed() func(method string) bool {
	// `ApiKey.Add` takes a verifier from the caller, so serving it beside
	// `Issue` would be offering the thing `Issue` exists to stop: a key whose
	// secret somebody else chose, in a prefix they picked.
	//
	// `CredentialService` is registered for its verbs (`Set`, `Issue`,
	// `Unlock`, `Enrol`, `Erase`), so the rest of it is shut one method at a
	// time rather than the whole name: the reads answer the `secret` column
	// and `Add` takes a caller-chosen verifier.
	byMethod := []string{
		app.ApiKeyService_Add_FullMethodName,
		app.CredentialService_Get_FullMethodName,
		app.CredentialService_List_FullMethodName,
		app.CredentialService_Watch_FullMethodName,
		app.CredentialService_Add_FullMethodName,
		// `Credential.Erase` is served: it answers with no verifier and takes
		// none, and `server/core` gives it the rules it needs (a password is
		// replaced and not removed; not the last way in).
		// `Delegation.Get`/`List`/`Erase` are served too: the token in `secret`
		// is stripped on the way out like every other secret, and the layer
		// holds them to `mayReach` on the row's holder. What stays closed is
		// `Add` -- a caller-chosen token. `DelegationService` is registered
		// for those reads and its `Revoke` and `Exchange` overlays.
		app.DelegationService_Add_FullMethodName,
		// `Consent` is a ledger: `Agree` and `Withdraw` add to it, stamping
		// what a request must not say, and nothing changes or removes a row of
		// it -- the deployment destroys them once their account is gone.
		app.ConsentService_Add_FullMethodName,
		app.ConsentService_Erase_FullMethodName,
	}

	// `ApiKey.Get`, `List` and `Erase` are served: the verifier is stripped on
	// the way out like every other secret, `server/core` holds the rows to
	// `mayReach`, and a person listing and ending their own keys is what the
	// account screen is -- which is why `Holder.RevokeKey` is gone.

	return func(method string) bool {
		for _, v := range byMethod {
			if method == v {
				return true
			}
		}

		return grpcx.GeneralWrite(method)
	}
}

// public is what this app answers without asking who is calling.
//
// **Nothing.** `VouchService/Verify` was briefly listed here, and the reason is
// a mistake worth leaving written down: it confused the person signing in with
// the **caller**.
//
// The person signing in has no credential yet -- that is the thing they are
// asking for. But on the data plane they are not who is **calling**: the caller
// is a product app, or a Login App, and each is a machine holding a credential
// long before any of this.
//
// This used to end *roster is called by machines and never by a browser*, which
// is not true of the deployment and was never true of the line below it. The
// console **is** a browser and reaches `control.http` directly, cookie and all
// (`ts/console/main.tsx`, `credentials: 'include'`; `HttpOnly` and `SameSite`
// pinned in `cmd/console_test.go`). What is true is per plane, which is why
// this is a function and not a sentence: the control plane serves exactly one
// browser-facing method, below, and the data plane serves none. A person a
// customer signs in is reached through an app that holds a key, and that key is
// what the tenant the app names is held against -- `server/vouch/vouch.go`.
//
// Public gave up two things for nothing. Anybody who could reach the port could
// guess passwords at the whole organisation -- and not slowly, since
// `grpcx.Limit` counts per tenant off the frame and a public call has no frame.
// And the trail could not say which service asked, so a compromised product app
// looked exactly like a stranger.
//
// The lockout in `server/vouch` is unaffected and was never meant to be the
// first line.
func Public(method string) bool {
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

	// And what the form is, which a page has to know before it draws one. A
	// caller asking how to sign in has not signed in, which is `SignIn`'s
	// argument exactly.
	//
	// It costs less than `SignIn` does, and the reason is the subject: this one
	// answers about **the tenant the request arrived at**, with no field to point
	// it elsewhere. The name it arrived at is still the caller's to say -- it is
	// a header -- so this answers of any tenant's name; what it answers is what
	// that tenant's sign-in page shows anybody who opens it. The same fact the
	// account app's `GET /providers` has answered anonymously at a tenant's own
	// host for as long as it existed.
	if method == app.AuthService_Offers_FullMethodName {
		return true
	}

	// And which session the browser's cookie names, which a page asks before it
	// knows whether anybody is signed in -- and asks again when a call is
	// refused, which is exactly when the cookie may name nobody. It answers a
	// name for a store and nothing about anybody, for the cookie the call
	// came with.
	if method == app.AuthService_Which_FullMethodName {
		return true
	}

	return auth.PublicDefault(method)
}

// Serve answers on `l` until the context is done.
func (s *Server) Serve(ctx context.Context, c Config, l net.Listener) error {
	c.Server.Keepalive = c.ServerKeepalive()
	g, err := s.Grpc(ctx, c)
	if err != nil {
		return err
	}

	s.unbounded(ctx)
	s.aged(ctx, c)
	s.alone(ctx, c)
	s.disks(ctx, c)
	s.plain(ctx, c)

	// The stops, collected rather than deferred one at a time, and all of
	// them under one deadline.
	//
	// A `defer` per listener runs them in series, and each waits up to
	// [ShutdownGrace] -- so five listeners is five graces end to end, and the
	// number that justifies the grace is `docker stop`'s ten seconds. So is a
	// data plane stopped and waited for before the others begin, which is how
	// a stream on it and one on the admin listener came to two graces. Every
	// stop is begun at the signal, and every one is given the deadline [budget]
	// fixed then: the budget is the grace whatever a deployment opened.
	//
	// Each plane is its gRPC server and what calls into it. A page's call
	// reaches the gRPC server through the HTTP listener in front of it, and a
	// `GracefulStop` closes such a call at once, where it lets one made over
	// gRPC finish: so the HTTP listeners stop first, taking no new connection
	// and answering what they hold, and the gRPC server is stopped once they
	// have, in what is left of the same deadline.
	var (
		at    budget
		stops shutdown
		once  sync.Once
	)
	halt := func() { once.Do(stops.run) }
	defer func() {
		halt()
		s.flushAccess(ctx)
	}()

	stopHttp, err := s.serveHttp(ctx, c, g)
	if err != nil {
		return err
	}

	// The data plane's own, with the front doors this process runs beside it,
	// which call into it as well.
	stops.add(s.plane(&at, "grpc", g, stopHttp, s.frontsStopped))

	var control *grpc.Server
	if s.Control != nil {
		control, err = s.GrpcControl(ctx, c)
		if err != nil {
			return err
		}
	}

	if err := s.serveControl(ctx, c, control); err != nil {
		return err
	}

	stopControlHttp, err := s.serveControlHttp(ctx, c, control)
	if err != nil {
		return err
	}
	stops.add(s.plane(&at, "control", control, stopControlHttp))

	admin, err := s.GrpcAdmin(ctx, c)
	if err != nil {
		return err
	}

	if err := s.serveAdmin(ctx, c, admin); err != nil {
		return err
	}

	stopAdminHttp, err := s.serveAdminHttp(ctx, c, admin)
	if err != nil {
		return err
	}
	stops.add(s.plane(&at, "admin", admin, stopAdminHttp))

	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		at.deadline()
		halt()
		close(stopped)
	}()

	err = g.Serve(l)
	if ctx.Err() != nil {
		// Asked to stop: what was being answered ends before the access
		// record is written once more, as it is on the way out.
		<-stopped
	}

	return err
}

// flushAccess writes what each plane's access record holds once more, once
// nothing answers: what the listeners answered while they drained is queued
// after the writers' own last write, which they made as their loops stopped.
func (s *Server) flushAccess(ctx context.Context) {
	last, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownGrace)
	defer cancel()

	for _, v := range []*Server{s, s.Control} {
		if v == nil || v.Access == nil {
			continue
		}
		if err := v.Access.Flush(last); err != nil {
			log.From(ctx).ErrorContext(ctx, "access: the record waiting to be written was lost as the server stopped",
				"rows", v.Access.Pending(), "err", err)
		}
	}
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

// budget is the one deadline every listener's stop is given: [ShutdownGrace]
// from the first time a stop asks for it, which is the signal.
//
// One deadline and not one grace each, so that what a stop is given does not
// depend on when it began: a listener that waits for another before it stops
// its own server waits inside the budget rather than adding to it.
type budget struct {
	once  sync.Once
	until time.Time
}

func (v *budget) deadline() time.Time {
	v.once.Do(func() { v.until = time.Now().Add(ShutdownGrace) })

	return v.until
}

// plane is the stop of one gRPC server and of what calls into it: each of
// `before` together, and the server once they have all returned, under the
// one deadline `at` holds.
//
// A nil server is a plane this deployment does not have, and its stop is
// only the others'.
func (s *Server) plane(at *budget, name string, g *grpc.Server, before ...func(context.Context)) func() {
	return func() {
		until := at.deadline()
		ctx, cancel := context.WithDeadline(context.Background(), until)
		defer cancel()

		s.seen(name, until, false)
		defer s.seen(name, until, true)

		var wg sync.WaitGroup
		for _, f := range before {
			wg.Go(func() { f(ctx) })
		}
		wg.Wait()

		if g != nil {
			stopping(ctx, g)
		}
	}
}

// seen tells [Server.stopsSeen], where a test set it.
func (s *Server) seen(listener string, until time.Time, stopped bool) {
	if s.stopsSeen != nil {
		s.stopsSeen(listener, until, stopped)
	}
}

// Front runs `serve`, a front door this process runs beside the data plane,
// and counts it until it returns, which a front door does once what it was
// answering is answered ([ServeUntil]): the data plane's gRPC server, which
// the front door calls, is not stopped under a request it still holds.
//
// Counted as this is called and not as `serve` begins, so that a stop that
// comes before the goroutine has run still waits for it.
func (s *Server) Front(serve func() error) func() error {
	s.fronts.add()

	return func() error {
		defer s.fronts.done()

		return serve()
	}
}

// frontsStopped waits for the front doors counted by [Server.Front], and no
// longer than ctx.
func (s *Server) frontsStopped(ctx context.Context) { s.fronts.wait(ctx) }

// doors is a count of what is running that a wait can be bounded on.
//
// Not a `sync.WaitGroup`, which may not be added to from zero while somebody
// waits: a front door is counted after `Serve` has begun, and a signal may
// come in between.
type doors struct {
	mu   sync.Mutex
	n    int
	none chan struct{}
}

func (v *doors) add() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.n == 0 {
		v.none = make(chan struct{})
	}
	v.n++
}

func (v *doors) done() {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.n--
	if v.n == 0 {
		close(v.none)
	}
}

func (v *doors) wait(ctx context.Context) {
	v.mu.Lock()
	if v.n == 0 {
		v.mu.Unlock()
		return
	}
	none := v.none
	v.mu.Unlock()

	select {
	case <-none:
	case <-ctx.Done():
	}
}

// ServeUntil answers on `l` with `srv` until ctx is done, and then stops it as
// every listener of roster's stops: it takes no new connection, a request in
// flight is answered, and what is still open [ShutdownGrace] after the signal
// is cut. It returns once that is so, and not as the listener closes.
func ServeUntil(ctx context.Context, srv *http.Server, l net.Listener) error {
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()

		last, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownGrace)
		defer cancel()
		if srv.Shutdown(last) != nil {
			_ = srv.Close()
		}
	}()

	err := srv.Serve(l)
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped

	return nil
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
// wants its streams cut sooner has `keepalive.max_connection_age` with
// `keepalive.max_connection_age_grace` beside it, which says something truer
// about a long-lived stream than a shutdown timeout does. The age alone ends no
// stream that is open: gRPC waits for one as long as the grace says, which is
// for ever where nothing does ([Server.aged]).
//
// Exported so that a test can say *bounded by what this app chose* rather than
// by a number copied beside it, which is the whole claim.
const ShutdownGrace = 5 * time.Second

// What the data plane's listener asks of an idle connection, and lets a
// client ask, where `server.keepalive` says nothing of it: see
// [Config.ServerKeepalive].
const (
	KeepaliveTime    = time.Minute
	KeepaliveTimeout = 20 * time.Second
	KeepaliveMinTime = 20 * time.Second
)

// ServerKeepalive is what the data plane's listener is built with of
// `server.keepalive`: what the file says, and where it leaves the server's
// own ping, its wait or the least interval a client is allowed unsaid,
// [KeepaliveTime], [KeepaliveTimeout] and [KeepaliveMinTime].
//
// gRPC's own are a ping after two hours and a client allowed one every five
// minutes, and roster sends nothing on a sync stream nothing happens on. So a
// follower could not tell a connection that died from a quiet tenant within
// any number worth having, and one that asked more often than every five
// minutes was hung up on, `too_many_pings`. These let a follower ask every
// thirty seconds, which is what `docs/operating.md` tells it to dial with, and
// have the server find a dead one within a minute and twenty seconds.
//
// Nothing is defaulted for an age or its grace, which are a deployment's to
// choose ([Server.aged]). The control plane and the admin listener are left as
// their files say: no follower holds a sync stream there.
func (c Config) ServerKeepalive() config.KeepaliveConfig {
	k := c.Server.Keepalive
	if k.Time == 0 {
		k.Time = KeepaliveTime
	}
	if k.Timeout == 0 {
		k.Timeout = KeepaliveTimeout
	}
	if k.MinTime == 0 {
		k.MinTime = KeepaliveMinTime
	}

	return k
}

// aged says, where it is so, that a listener's `keepalive.max_connection_age`
// has no `max_connection_age_grace` beside it, and so ends no stream that is
// open: gRPC asks the client to move and waits for what is in flight as long
// as the grace says, which is for ever where nothing does. A sync stream is in
// flight for as long as its follower holds it, so an age alone moves no
// follower to a new replica.
//
// Said rather than defaulted: how long a stream may be cut short for a
// connection's age is the deployment's to choose. By `serve` as it starts, as
// [Server.unbounded] is.
func (s *Server) aged(ctx context.Context, c Config) {
	for _, v := range []struct {
		key string
		k   config.KeepaliveConfig
	}{
		{"server", c.Server.Keepalive},
		{"control", c.Control.Keepalive},
		{"admin", c.Admin.Keepalive},
	} {
		if v.k.MaxConnectionAge <= 0 || v.k.MaxConnectionAgeGrace > 0 {
			continue
		}

		log.From(ctx).WarnContext(ctx, fmt.Sprintf("%s.keepalive.max_connection_age is said and "+
			"%s.keepalive.max_connection_age_grace is not, so a connection that reaches its age ends no stream that is open on it: "+
			"gRPC waits for one for ever, and a follower of the sync stream is never moved. "+
			"Say how long a stream is given once its connection is that old", v.key, v.key))
	}
}

// plain says, where it is so, that a listener block says `tls:` and an
// `http.addr`, and the second is plain: `tls:` is the block's gRPC listener's
// alone. Right for a proxy in front that holds the certificate, so said and
// not refused; a block whose certificate is asked of every caller is refused
// ([Config.Meant]). By `serve` as it starts, as [Server.unbounded] is.
func (s *Server) plain(ctx context.Context, c Config) {
	for _, b := range c.listeners() {
		if !b.Tls.Active() || b.Http.Addr == "" {
			continue
		}
		if b.key != "server" && !c.Control.Serves() {
			continue
		}

		log.From(ctx).WarnContext(ctx, fmt.Sprintf("%[1]s.tls is %[1]s.addr's alone, and %[1]s.http.addr is plain: "+
			"what it answers travels in the clear unless a proxy in front of it holds a certificate", b.key))
	}
}

// alone says, where it is so, that a plane's broker publishes inside the
// process while the lease shares the loops among several: `watch.broker:
// memory` beside `spin.lease: postgres`, or any lease but `local` and `none`.
// The file says one process and several at once, and under it a follower of
// one replica hears nothing written at another, on a stream that looks open.
//
// Both planes, in one line: the control plane's broker is a setting of its own
// (`control.watch.broker`), named where it is the one, and a control plane
// that left it unsaid took the data plane's, which `watch.broker` names.
//
// Said and not refused: one process may hold a lease of several, and is right
// to while it is one. By `serve` as it starts, as [Server.unbounded] is; the
// box asks [Server.HearsOnlyItself] of its own.
func (s *Server) alone(ctx context.Context, c Config) {
	switch c.Spin.Lease {
	case "", config.LeaseLocal, config.LeaseNone:
		return
	}

	said := []string{}
	if s.HearsOnlyItself() {
		said = append(said, "watch.broker")
	}
	if s.Control != nil && s.Control.HearsOnlyItself() && c.Control.Watch.Broker != "" {
		said = append(said, "control.watch.broker")
	}
	if len(said) == 0 {
		return
	}

	log.From(ctx).WarnContext(ctx, fmt.Sprintf("%s %q and spin.lease is %q, which shares the loops among several processes: "+
		"a follower of the sync stream, and of any Watch, hears only the process it is connected to. "+
		"Name a broker every process hears (postgres), or run one process",
		strings.Join(said, " and ")+map[bool]string{true: " are", false: " is"}[len(said) > 1], config.BrokerMemory, c.Spin.Lease))
}

// disks says, where it may be so, that the trail is written to a directory of
// this process's own disk by a loop several processes share.
//
// `audit.archive`, `audit.checkpoints.dir` and a `store:` witness are paths,
// and the pass that moves lines of the trail out of the database into them
// runs in whichever process holds its lease. With a disk each, the next holder
// writes into its own directory: it finds the archive is not what its manifest
// says, and goes on, and the trail is then in two places of which neither
// verifies. Said and not refused, as [Server.alone] is, since roster cannot see
// whether a path is one every process mounts -- which is what it has to be.
func (s *Server) disks(ctx context.Context, c Config) {
	switch c.Spin.Lease {
	case "", config.LeaseLocal, config.LeaseNone:
		return
	}

	said := []string{}
	if c.Audit.Archive != "" {
		said = append(said, "audit.archive")
	}
	if c.Audit.Checkpoints.Dir != "" {
		said = append(said, "audit.checkpoints.dir")
	}
	if slices.ContainsFunc(c.Audit.Checkpoints.Witnesses, func(v string) bool { return strings.HasPrefix(v, "store:") }) {
		said = append(said, "audit.checkpoints.witnesses")
	}
	if len(said) == 0 {
		return
	}

	log.From(ctx).WarnContext(ctx, fmt.Sprintf("%s a directory on this process's disk and spin.lease is %q, which shares the loops among several processes: "+
		"the trail's pass writes to it in whichever process holds its lease. "+
		"Make each one directory every process mounts, or run one process",
		strings.Join(said, ", ")+map[bool]string{true: " are each", false: " is"}[len(said) > 1], c.Spin.Lease))
}

// stopping takes a server down, and does not wait past ctx's deadline to.
//
// Graceful first, so a call made over gRPC that is in flight finishes and its
// caller gets an answer rather than a broken connection. A call that came
// through an HTTP listener is not among them -- `GracefulStop` closes those at
// once -- which is why [Server.plane] stops the HTTP listeners in front of `g`
// before it calls this. Hard at the deadline, because waiting on a stream that
// has no reason to end is not waiting, it is hanging.
//
// `Stop` beside a `GracefulStop` already in flight is what unblocks it: it
// closes the transports out from under the handlers, the connections drain, and
// the graceful call returns -- which is why the goroutine is still waited for
// rather than abandoned. Anything left running after this is running under a
// process that is on its way out anyway.
func stopping(ctx context.Context, g *grpc.Server) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.GracefulStop()
	}()

	select {
	case <-done:
	case <-ctx.Done():
		g.Stop()
		<-done
	}
}

// unbounded says, where it is so, that nothing bounds this deployment's
// access record (#280).
//
// The record takes a row for every call that shows somebody, writes them or
// is refused them, and a row stays in the database until a window says
// otherwise. No window is the default, and stays the default for the trail's
// reason: roster destroys nothing nobody asked it to, and a version that
// began to would be found out by somebody looking for a row. But a default
// nobody was told of is a table somebody finds by its size, so it is said --
// as a warning, which is the level a deployment watches, and with the key
// that changes it.
//
// By `serve` as it starts and not by [Build], which every command at the box
// runs too: said there, it would be said to whoever listed a tenant.
//
// And not to a deployment that named a profile for it, `forever` among them:
// that is somebody having said, and a warning nothing answers is one nobody
// reads by the time it matters.
func (s *Server) unbounded(ctx context.Context) {
	if s.accessKept.Retain > 0 || s.accessKept.Note != "" {
		return
	}

	log.From(ctx).WarnContext(ctx, "access: the access record has no window, so a row of it stays in the database for good -- "+
		"one for every call that shows, writes or is refused somebody's data. "+
		"Say how long they are kept: audit.by.access, with retain, and discard: true to keep no copy or audit.archive to keep one; "+
		"or profile: forever, to say for good is meant")
}

// accessed is what the access record's interceptor is told of this plane
// (payday#111): whose data a message carries, read off what the schema
// declared `personal`, and where a call came from -- the connection's, since
// roster is told of no proxy it would believe. Nothing roster serves hands
// personal data out to be kept elsewhere, so it names no download.
//
// Whose data, and in which tenant: where a message does not say which tenant
// a row is in, the row does ([filed]), so that a row of the record is under
// the tenant of whoever was read and not of whoever read them.
func (s *Server) accessed() access.Config {
	return access.Config{
		Subjects: filed(s.Ent, s.log, pd.Subjects),
		Refs:     filed(s.Ent, s.log, pd.SubjectRefs),
		Address:  access.Peer,
		Writer:   s.Access,
	}
}

// GrpcControl is the control plane's own server, for a console.
//
// Separate from [Server.serveControl] for the reason [Server.Grpc] is separate
// from [Server.Serve]: a test can travel exactly this and answer on a listener
// that is a channel. It was briefly not, and then the one registration that
// makes this port worth opening was reachable only by opening it.
func (s *Server) GrpcControl(ctx context.Context, c Config, opts ...grpc.ServerOption) (*grpc.Server, error) {
	g, err := s.Control.Grpc(ctx, Config{Server: ServerConfig{ServerConfig: c.Control.ServerConfig}}, opts...)
	if err != nil {
		return nil, err
	}

	// The keys themselves, which are the deployment's own on this plane. It
	// takes no registration: `Register` puts `ApiKeyService` on every server
	// for its `Issue` overlay, and what differs here is the kind it mints
	// ([Server.Keys]) and whose rows it reads. A key's general writes are shut
	// here as everywhere (`closed`). This port must still not be reachable by
	// anybody who is not administering the deployment. Nothing in this process
	// can enforce that; the address is what enforces it.

	// No `AuthService` here, and no page either. Both moved to `admin.http`,
	// which is where a roster operator opens a browser: a session cookie is
	// `__Host-` prefixed and host-only, so the page and the listener it signs
	// in at have to be one host, and the tenants screen is on that listener
	// (#27, #32). What is left on this one is what a shell makes -- the RPCs
	// behind `roster control …` -- and those carry an `rk_` rather than a
	// cookie.

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
// the gate, the trail. What differs is which database is behind it: the
// `ApiKeyService` here, as on every server (`Register`), mints and reads the
// deployment's own keys, an `rk_`, where the data plane's are a tenant's.
//
// Its stop is `Serve`'s, with the listener in front of it, under the one
// deadline: this port serves the same watchable services, so a console with a
// stream open would otherwise hold the whole shutdown.
func (s *Server) serveControl(ctx context.Context, c Config, g *grpc.Server) error {
	if !c.Control.Answers() || g == nil {
		return nil
	}

	l, err := Listen(ctx, c.Control.Addr)
	if err != nil {
		return err
	}

	go func() { _ = g.Serve(l) }()

	log.From(ctx).InfoContext(ctx, "control", slog.String("addr", l.Addr().String()))

	return nil
}

// serveAdmin is where an operator administers customers; see `admin.go`.
func (s *Server) serveAdmin(ctx context.Context, c Config, g *grpc.Server) error {
	if c.Admin.Addr == "" || g == nil {
		return nil
	}

	l, err := Listen(ctx, c.Admin.Addr)
	if err != nil {
		return err
	}

	go func() { _ = g.Serve(l) }()

	log.From(ctx).InfoContext(ctx, "admin", slog.String("addr", l.Addr().String()))

	return nil
}

// serveHttp is the second listener, for whatever cannot speak gRPC -- which is
// every browser. Nothing is opened unless the configuration named an address.
//
// It is the **same** `g`: a page reaches the handlers a gRPC client reaches,
// through the interceptors a gRPC client goes through, behind the same wall.
// There is no second stack here for a rule to be missing from.
//
// And it is where the **user console** is served, at `/`, beside the RPCs it
// calls: a roster user signs in here, the wall narrows them to their own
// tenant, and which tenant that is comes from the name they arrived at. See
// [ServerConfig.Console], which is refused unless `sign_in.enabled` puts the door
// on this listener.
func (s *Server) serveHttp(ctx context.Context, c Config, g *grpc.Server) (func(context.Context), error) {
	return s.http(ctx, "http", c.Server.Http, g, ConsoleMount(c.Server.Console))
}

// serveControlHttp is the control plane's own browser surface.
//
// The first console is this one: who runs the deployment, which services call
// it, what each key may do. All of that is here and none of it is on either of
// the other two ports.
func (s *Server) serveControlHttp(ctx context.Context, c Config, g *grpc.Server) (func(context.Context), error) {
	if g == nil {
		return func(context.Context) {}, nil
	}

	return s.http(ctx, "control.http", c.Control.Http, g)
}

// ConsoleMount mounts the built page at `/`, beside the RPCs it calls.
//
// It was `/console/`, and the reason given here was that "a page and an RPC
// cannot share a root". That was never true and this file said so three lines
// later: the account app shares one, with the page at `/` and the RPCs being
// what the prefix `/roster.` leaves. A gRPC path is `/<service>/<method>` and
// nothing else is one.
//
// What was actually in the way is that payday's `web.New` mounted the
// transcoder at `/`, which in a `ServeMux` is the catch-all -- so it held every
// path on this listener, including every path it could never answer, and a
// second registration of `/` panics. It mounts under each service's own prefix
// now (lesomnus/payday#13), and the root is the app's.
//
// Which is where a page belongs. Somebody who types the host and no path gets
// the front door of whatever is there -- the admin console on `admin.http`, the
// user console on `server.http`; `/console/` was a thing to know rather than a
// thing to find.
//
// One function for both, which is what makes the two pages the same kind of
// thing: a directory, served at the root of the listener whose RPCs it calls.
//
// A path that is not a file is the index, which is what a page that routes in
// the browser needs on reload. What the files are served with is [Page]'s,
// which is where what a browser may keep of them is decided.
func ConsoleMount(c ConsoleConfig) func(*web.Mux) {
	return func(m *web.Mux) {
		if c.Dir == "" {
			return
		}
		page := Page(c.Dir)
		m.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			at := filepath.Join(c.Dir, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
			if st, err := os.Stat(at); err != nil || st.IsDir() {
				// The index by its own path rather than `ServeFile` on the
				// file, so that what [Page] says about it is said about the
				// document: a bundle that is gone is answered with the index,
				// and must not be answered with the index kept for a year
				// under the bundle's name.
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
			page.ServeHTTP(w, r)
		}))
	}
}

// serveAdminHttp is the same for the admin listener, and it is what a console
// actually talks to.
//
// A browser cannot speak gRPC, so a port without one of these is a port a
// console cannot reach -- and until this, the only transcoder was in front of
// the **data plane**, where an operator's session names nobody. The admin console
// could sign in and then had nothing to call.
//
// A second listener rather than a route on the first, because both servers
// register `roster.HolderService` and a transcoder routes by the method's own
// path. There is nowhere to put a prefix that the name does not already occupy.
func (s *Server) serveAdminHttp(ctx context.Context, c Config, g *grpc.Server) (func(context.Context), error) {
	if g == nil {
		return func(context.Context) {}, nil
	}

	return s.http(ctx, "admin.http", c.Admin.Http, g, ConsoleMount(c.Admin.Console))
}

func (s *Server) http(ctx context.Context, name string, c config.HttpConfig, g *grpc.Server, mounts ...func(*web.Mux)) (func(context.Context), error) {
	if !c.Serves() {
		return func(context.Context) {}, nil
	}

	h, err := web.New(c, g)
	if err != nil {
		return nil, err
	}

	// No sign-in route here, and the absence is the point.
	//
	// `POST /session` was mounted on every listener that had HTTP, because
	// "issuing is an HTTP endpoint" -- and it is not. A cookie is a response
	// header, `set-cookie` is response metadata, and `web.Transcode` hands one
	// to the browser as the other, which is what `AuthService.SignIn` does
	// (`server/console`, `proto/app/auth.proto`). That service was added and
	// the route was left, so the app carried two implementations of one act,
	// down to copied comment paragraphs.
	//
	// Deleting it also closed what the route cost: `AuthService` is registered
	// on the control listener alone, while this ran wherever there was HTTP --
	// so the data plane's customer-facing port answered an operator's password
	// with 204 and a cookie that opens nothing, a trap that had to be
	// documented and pinned rather than removed.
	//
	// Whatever a listener serves over HTTP now is a service registered on its
	// own `g`, and there is nowhere for a second answer to hide.

	for _, mount := range mounts {
		mount(h)
	}

	l, err := Listen(ctx, c.Addr)
	if err != nil {
		return nil, err
	}

	srv := &http.Server{Handler: Arrived(h)}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.From(ctx).ErrorContext(ctx, name, slog.Any("err", err))
		}
	}()

	log.From(ctx).InfoContext(ctx, name, slog.String("addr", l.Addr().String()))

	// `Shutdown` and not `Close`: no new connection is taken, and a call in
	// flight is answered, as one over gRPC is. What is still open at the
	// deadline -- a stream, which never ends on its own -- is cut.
	return func(ctx context.Context) {
		until, _ := ctx.Deadline()
		s.seen(name, until, false)
		defer s.seen(name, until, true)

		if srv.Shutdown(ctx) != nil {
			_ = srv.Close()
		}
	}, nil
}

// Arrived carries the name a browser came in on into the request's headers, so
// that a handler behind the transcoder can read it.
//
// It is not there otherwise. Go keeps the `Host` header out of
// `Request.Header` and on `Request.Host`, and metadata is built from the header
// map -- so a sign-in resolving a tenant from the name it was reached at had
// nothing to read, and gRPC will not carry a `host` of its own either.
//
// Only where a proxy has not already said: behind a terminator the public name
// is `X-Forwarded-Host` and `Host` is whatever the internal Service is called,
// and the proxy's answer is the true one.
//
// Exported because it is half of what the user console rests on and a test that
// wrapped the transcoder by hand would be testing the other half. A page that
// reaches roster through no proxy carries the name in `Host` alone, which is
// the case this is here for.
func Arrived(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-Host") == "" && r.Host != "" {
			r = r.Clone(r.Context())
			r.Header.Set("X-Forwarded-Host", r.Host)
		}

		h.ServeHTTP(w, r)
	})
}

// sameDatabase is whether two `db` blocks name one database in the same words.
//
// A database kept in memory and named by nothing is a new one each time it is
// opened, so two blocks that say `:memory:` are two databases and are not
// refused; a test and the sandbox say that.
func sameDatabase(a, b config.DbConfig) bool {
	if a.Driver != b.Driver || a.Dsn != b.Dsn {
		return false
	}

	return a.Dsn != "" && !strings.Contains(a.Dsn, ":memory:") && !strings.Contains(a.Dsn, "mode=memory")
}

// Apart refuses a deployment whose two planes are one database, by the row
// that says who runs it: the control plane's tenant, which no customer is.
//
// [Build] refuses a file that names one database twice in the same words. Two
// spellings of one -- a relative path and an absolute one, a parameter more --
// are the same mistake and are not seen there, and nothing about a connection
// says which database it reached in a way two engines agree on. A row does:
// the tenant `init` wrote on the control plane is found on the data plane only
// if they are one.
//
// Asked when a deployment is about to serve, since before `init` there is no
// such row and before the schema is there no table to ask.
func Apart(ctx context.Context, s *Server) error {
	if s.Control == nil {
		return nil
	}

	owner, err := s.Control.Ent.Tenant.Query().First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// Nobody has run `init`, so there is nobody to be taken for.
			return nil
		}

		return err
	}

	here, err := s.Ent.Tenant.Query().Where(enttenant.IdEQ(owner.Id)).Exist(ctx)
	if err != nil {
		return err
	}
	if here {
		return fmt.Errorf(
			"control.db: the same database as db, by another spelling -- the tenant %q that runs this "+
				"deployment is a row of the data plane too. The two planes are two databases: in one, a "+
				"customer's session is an operator's at admin.addr. name another database for control.db, "+
				"run `roster init` on it, and erase %q from the data plane", owner.Alias, owner.Alias)
	}

	return nil
}

func (s *Server) policyInterceptors() grpcx.Chain {
	if s.PolicyFile == nil {
		return grpcx.Chain{}
	}
	return s.PolicyFile.Interceptor()
}

// contentOf is `a` with nothing in it but what the trail keeps of each kind's
// writes, for the control plane: see where it is built.
func contentOf(a config.AuditConfig) config.AuditConfig {
	out := config.AuditConfig{Content: a.Content}
	for name, v := range a.By {
		if v.Content == "" {
			continue
		}
		if out.By == nil {
			out.By = map[string]config.AuditKeepConfig{}
		}
		out.By[name] = config.AuditKeepConfig{Content: v.Content}
	}

	return out
}
