// Package cmd is this app's own wiring, and it is short on purpose.
//
// Everything that does not change from one app to the next is in payday. What
// is left is here, and it is deliberately **not** hidden behind a
// `payday.Serve(cfg)`: the stack, the order of the interceptors and which
// server the wall is on are the decisions a reader of an app most needs to be
// able to see, and a framework that hid them would be hiding the only part
// worth reading.
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/mode"

	"github.com/lesomnus/payday/pdcmd"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"

	// The two engines this app runs on, blank-imported here rather than by
	// payday so that an app does not carry one it never opens.
	//
	// Both, because both are used: `compose.yaml` runs it on PostgreSQL and a
	// test runs it on SQLite. Linking only the second made `docker compose up`
	// -- the quickstart `docs/operating.md` gives -- fail at
	// `unknown driver "pgx"`, which is a sentence about a name rather than
	// about a missing import and reads as a typo in the configuration.
	_ "github.com/lesomnus/payday/config/dbpgx"
	_ "github.com/lesomnus/payday/config/dbsqlite3"

	// And the broker that rides the first of them, so that
	// `watch.broker: postgres` is a name this binary has.
	//
	// The one thing that stopped roster running more than one replica was that
	// a client watching against one never heard about a write that landed on
	// another -- and the answer for a deployment already on PostgreSQL needs no
	// second piece of infrastructure. See `docs/operating.md`, "Running more
	// than one".
	//
	// Linked whatever this deployment runs on, like the drivers above: what it
	// costs is a `LISTEN` client in the binary, and what the other arrangement
	// costs is `watch.broker: postgres` reading as a typo.
	_ "github.com/lesomnus/payday/config/brokerpg"
)

// Name is what this app is called, and it is the only place it is written.
// The environment prefix and the names of the configuration files are derived
// from it -- APPTEST_DB_DSN, apptest.yaml -- so there is nothing to keep in
// step.
const Name = "roster"

// Loader reads this app's configuration.
var Loader = config.For(Name)

// Config is what this app is configured with.
//
// The framework cannot own this struct, since what an app is configured with is
// the app's. What it owns is the pieces: each of these is a payday type, and
// what is written here is only which of them this app has.
type Config struct {
	Server config.ServerConfig `yaml:"server"`
	Db     config.DbConfig     `yaml:"db"`
	Otel   config.OtelConfig   `yaml:"otel"`
	Watch  config.WatchConfig  `yaml:"watch"`

	// Control is who may call this deployment, and it is roster again -- a
	// second instance, in this process, on its own database. See PLAN.md, D15.
	//
	// Nothing written down is a deployment that believes its callers, which
	// `auth.Plain` announces once in the log. It is the default custody takes
	// with no issuer named, and for the same reason: an app that cannot be run
	// until a control plane exists is an app nobody runs. So it is easy, and it
	// is loud.
	Control ControlConfig `yaml:"control"`

	// Admin is where an operator administers **customers**: the data plane,
	// with no wall, behind a session. Empty is nowhere.
	//
	// A third listener because it can be nothing else. The data plane's port is
	// walled and an operator has no tenant there, so it shows them nothing; and
	// the control plane's port already registers `roster.HolderService` over its
	// own rows, so this cannot join it under the same name.
	//
	// Bind it where only a console reaches. It answers with no wall at all.
	Admin config.ServerConfig `yaml:"admin"`

	// Client is where the entity commands -- `tenant ls`, `holder get` -- send
	// their calls. **Empty is this process**, which is the default.
	//
	// It is the app's and not payday's for the reason `pdcmd` takes a
	// connector rather than a connection: where to connect, as whom, and what
	// that credential may do are the three decisions that make an admin command
	// safe or unsafe, and a framework making them would make them the same way
	// for every app.
	Client ClientConfig `yaml:"client"`

	// Vouch is what this deployment needs to hold a secret it must read back.
	//
	// Only one thing so far and it is the first of its kind: a TOTP seed is not
	// a verifier -- computing the code somebody is about to type means holding
	// the seed -- so the row **is** the secret, and it is wrapped with a key
	// this deployment keeps somewhere the database is not.
	//
	// Empty is a deployment that holds no second factor, and asking it to is
	// refused rather than answered with a seed in the clear.
	Vouch VouchConfig `yaml:"vouch"`

	// Audit is how long the trail is kept, and where what leaves it goes.
	//
	// payday's, like every block above it: the `Audit` entity is payday's, the
	// recorder that fills it is payday's, and *this table grows forever* is
	// every payday app's problem rather than roster's. What is roster's is
	// which of payday's pieces it has, which is this line. See
	// `config.AuditConfig` and payday's `trail`.
	//
	// It is the data plane's alone. The nested build that raises the control
	// plane is handed a config with no `audit` -- see [Build] -- so nothing
	// sweeps the trail of the deployment's own operations, which is a table
	// that grows by the key and is the last one anybody wants a clock deleting
	// from.
	Audit config.AuditConfig `yaml:"audit"`

	// Holder is what happens to somebody after they leave.
	//
	// roster's and not payday's, and that is where the line falls: payday holds
	// the trail and offers the half of forgetting that has no judgement in it,
	// and *which rows are a person's* is a fact about this app's schema that no
	// framework can read. See `server/forget` and `docs/RUNTIME.md` §7.
	Holder HolderConfig `yaml:"holder"`
}

// HolderConfig is how long an erased person is kept before what is held about
// them is destroyed.
//
// # Two clocks again, and only one of them is here
//
// `Holder.Erase` makes somebody unreachable and destroys nothing. This is the
// window between that and destruction, and it is **operational rather than
// legal**: a mistaken deletion, a compromised account deleting things, a
// billing dispute. Thirty days is the ordinary answer and it fits inside GDPR's
// month; a deployment under 개인정보보호법's five days for an explicit request
// wants it shorter, or uses `roster forget <who>`, which has no window at all
// because the person asked.
//
// The other clock -- an explicit request -- is not configuration. It is a
// command, because it is somebody exercising a right rather than a policy
// running.
//
// Empty is **never**, which is the same default the trail's retention has and
// for the same reason: a version upgrade is not the right thing to start
// destroying a deployment's data.
type HolderConfig struct {
	// ForgetAfter is how long after an erase somebody is destroyed. Empty is
	// never.
	ForgetAfter time.Duration `yaml:"forget_after"`

	// Every is how often that is checked. Empty is a day, which is what
	// `trail.Swept` is and for the same reason: what this period decides is
	// only how far past the window somebody may sit.
	Every time.Duration `yaml:"every"`
}

// Archive is where the trail's archive lives, which forgetting has to reach as
// well as the database.
//
// Read off `audit:` rather than repeated here, because there is one archive and
// two things that write to it. A deployment that kept them apart would have a
// person destroyed in one copy and not the other, which is the failure this
// whole act exists to prevent.
func (c HolderConfig) Archive(from Config) string { return from.Audit.Archive }

// VouchConfig is what checking secrets needs beyond the rows.
type VouchConfig struct {
	// Breached is a file of leaked password hashes, and empty is a deployment
	// that does not check.
	//
	// The format the well-known corpus is published in: SHA-1, uppercase hex,
	// one per line, **sorted**. A file rather than a service because the
	// deployment this app is most careful about has no network at all, and the
	// search is a binary one over the file -- nothing loaded, nothing indexed,
	// and `sort -u` is enough to make one.
	//
	// It is read once at startup to check the order, because a file that is not
	// sorted answers *no* to things that are in it -- the direction that fails
	// quietly, in the one feature whose whole job is to say yes.
	Breached string `yaml:"breached"`

	// Keys wrap the seeds, written `name:base64` with the current one first.
	//
	// A list rather than one key because rotation is the only reason a
	// ciphertext carries a name at all: new rows take the first, and old rows
	// go on reading with whichever made them. A deployment with one key writes
	// one line and never thinks about it again.
	//
	// Thirty-two bytes each, base64. `server/vouch/seed.go` says what the key
	// buys -- protection against a copy of the rows, and not against a
	// compromised process -- and what a lost one costs.
	Keys []string `yaml:"keys"`
}

// ClientConfig is how a command reaches this deployment, when it is not this
// process.
//
// # Empty is local, and that is the default
//
// The opposite of what `oas` does, and the reason is the commands beside these:
// `roster init` and `roster key` have no remote form at all, since what they
// write is not served. A binary whose commands disagree about where they run is
// worse than one that is consistently the awkward way round.
//
// So a deployment that wants the wire says so, and one that does not gets a
// line in the log every time saying what it is doing.
type ClientConfig struct {
	// Addr is what to dial, and empty is this process. It is a gRPC target:
	// "dns:///roster.internal:8080".
	//
	// Which port to name is a decision with an answer that is not obvious; see
	// the note on `cmd/entity.go`'s `remote`. In short: `server.addr` is the
	// data plane and is walled, so what comes back is what the credential's
	// tenant holds -- not every tenant.
	Addr string `yaml:"addr"`

	// Insecure sends without TLS, for a deployment reached over a network only
	// it is on. It is written down rather than inferred from the address,
	// because inferring it is how a production address gets served in plaintext
	// by a default nobody read.
	Insecure bool `yaml:"insecure"`

	// Auth is how a call says who is making it.
	Auth ClientAuthConfig `yaml:"auth"`

	// Local is `--HAL`: read the database directly whatever the rest of this
	// says.
	//
	// Not in the file, and it is the one field here that is not. What it is for
	// is the moment somebody with a shell on the box wants to look at the rows
	// under a deployment that is configured to go over the wire -- and editing
	// the configuration to do that is a change that outlives the look.
	//
	// It is a flag alone, where `oas` requires a flag **and** a setting
	// allowing it. The difference is which way round the default is: there,
	// remote is the default and reading the database is the privileged path, so
	// it is locked twice. Here local is already what a file with no `client`
	// block does, and anybody who can pass this flag is already holding the
	// file the `db` block is in. A second lock would guard nothing.
	Local bool `yaml:"-"`
}

// ClientAuthConfig is the credential a command presents, and how.
//
// The scheme is a setting because roster serves more than one and which it
// serves depends on the rest of this file: with a control plane the data plane
// reads `Bearer` and checks an API key, and without one it reads `Plain` and
// believes whatever the caller writes. A command that could only send one of
// them would work against half the deployments this app supports.
type ClientAuthConfig struct {
	// Scheme is `bearer`, `plain`, or `none`, and it is the word that goes
	// before the credential in `authorization`.
	//
	//	bearer  an API key, checked against the control plane. What a
	//	        deployment serving anybody uses.
	//	plain   the caller says who it is and is believed, so the credential is
	//	        a slug: "@acme/admin". It is what this app serves with **no
	//	        control plane configured**, which is a sandbox and not something
	//	        to serve where anyone can reach it.
	//	none    send nothing. For a port that authenticates at the transport --
	//	        a client certificate -- or one that is open.
	//
	// Empty with a credential given is `bearer`, which is the production
	// scheme. Defaulted rather than refused because the wrong answer here is
	// loud: a scheme the server does not read comes back `Unauthenticated` on
	// the first call, and there is nothing to notice weeks later.
	//
	// A name that is none of the three is refused rather than ignored.
	Scheme string `yaml:"scheme"`

	// Credential is what goes after the scheme -- a key for `bearer`, a slug
	// for `plain`.
	//
	// CredentialFile is the same thing from a file, which is what a deployment
	// that mounts a secret has. Both may be set and the file wins, so that a
	// development default in a checked-in file is overridden by the mount
	// rather than silently competing with it.
	Credential     string `yaml:"credential"`
	CredentialFile string `yaml:"credential_file"`
}

// IsSet reports whether this says anything at all.
//
// It is what makes `client.auth` with no `client.addr` a refusal rather than a
// credential that is quietly never sent; see `cmd/entity.go`.
func (c ClientAuthConfig) IsSet() bool {
	return c.Scheme != "" || c.Credential != "" || c.CredentialFile != ""
}

// Provider is how a command says who it is, or nil when it says nothing.
//
// It is worked out here rather than where the connection is made, so that a
// configuration this cannot be built from is refused before anything is dialed
// and with the name of the field that is wrong.
func (c ClientAuthConfig) Provider() (auth.Provider, error) {
	v, err := c.value()
	if err != nil {
		return nil, err
	}

	scheme := strings.ToLower(c.Scheme)
	if scheme == "" && v != "" {
		scheme = "bearer"
	}

	switch scheme {
	case "", "none":
		if v != "" {
			return nil, fmt.Errorf("client.auth: a credential is given and the scheme is %q, which sends none", scheme)
		}

		return nil, nil

	case "bearer":
		if v == "" {
			return nil, fmt.Errorf("client.auth.scheme: bearer, and no credential to send")
		}

		return auth.BearerProvider(v), nil

	case "plain":
		if v == "" {
			return nil, fmt.Errorf("client.auth.scheme: plain, and nobody to say this call is from")
		}

		// Believed by whoever reads it, which is a deployment with no control
		// plane. Nothing here can tell whether that is what is at the other
		// end, so this is not refused -- but it is the one scheme worth
		// noticing in a file somebody reviews.
		return auth.PlainProvider(v), nil

	default:
		return nil, fmt.Errorf("client.auth.scheme: %q is not one of bearer, plain, none", c.Scheme)
	}
}

// value is the credential, from whichever of the two said one.
//
// The file wins, and a file that is named and not there is an error rather than
// an empty credential: a deployment that mounted a secret and got the mount
// path wrong must not fall through to calling as nobody, which reads as a
// permission problem three layers away.
func (c ClientAuthConfig) value() (string, error) {
	if c.CredentialFile == "" {
		return c.Credential, nil
	}

	b, err := os.ReadFile(c.CredentialFile)
	if err != nil {
		return "", fmt.Errorf("client.auth.credential_file: %w", err)
	}

	return strings.TrimSpace(string(b)), nil
}

// ControlConfig is the second roster: the one holding keys rather than people.
//
// # Why a database of its own
//
// A key must not live in the tables it protects. Separate, there is no query
// from the data plane to the keys at all, so a fault in the wall cannot reach
// one -- which is worth more than the migration it costs.
//
// # Why in this process
//
// Because the auth interceptor asks it on **every** request. A control plane
// somewhere else would need a credential to reach, and that credential would
// need checking somewhere, which is the same question one hop further out. Here
// the innermost lookup is a Go call against a server with no wall on it, and
// the recursion ends there.
type ControlConfig struct {
	Db config.DbConfig `yaml:"db"`

	// Where **this plane's** changes are published, which is its own question
	// and not the data plane's.
	//
	// It was `memory`, written into the code rather than read from anywhere,
	// and that made the console the one screen a second replica silently broke:
	// a key issued on one process would never reach an operator watching on
	// another, on a stream that stayed open and looked healthy.
	//
	// Separate from `watch` above for the reason the databases are separate.
	// A control plane publishing into the data plane's broker would make a key
	// changing look like a person changing, to every client watching -- so they
	// cannot be one setting, and the one that was implicit is the one nobody
	// could have found.
	//
	// Empty takes the data plane's **broker name**, and nothing else about it:
	// a deployment that named one broker for its people almost always means the
	// same kind for its keys, and having to write `memory` twice is a way of
	// getting it right once and wrong later.
	Watch config.WatchConfig `yaml:"watch"`

	// Where the control plane answers, and **empty is nowhere**.
	//
	// The rows are reachable in this process whatever this says -- that is what
	// the auth interceptor asks on every request. What an address adds is a way
	// for a console to manage them: which services exist, what keys they hold,
	// who the operators are.
	//
	// Nothing is opened unless it is written down, and it should be written
	// down as an interface a console can reach and nothing else can. A port
	// that is not open is a control nothing has to get right, which is the same
	// argument `AllowPprof` makes about a listener that is.
	//
	// Inlined, so this reads `control.addr` and `control.http` the way `admin`
	// reads `admin.addr` and `admin.http`. It was `control.server.addr` for one
	// commit, which put the two listeners' settings at different depths for no
	// reason anybody could have named.
	//
	// Its own settings rather than the data plane's, because they answer
	// different callers about different rows: a limit tuned for a product app's
	// traffic is not one for a console, and one `http` block cannot open two
	// ports.
	config.ServerConfig `yaml:",inline"`
}

// Serves reports whether this deployment checks who is calling.
func (c ControlConfig) Serves() bool { return c.Db.Driver != "" }

// Answers reports whether the control plane is reachable over the network.
func (c ControlConfig) Answers() bool { return c.Serves() && c.Addr != "" }

// Cmd is this app's own command line: what payday supplies, plus whatever the
// app has of its own.
//
// `config`, `config env` and `version` are payday's -- they are the commands
// that run against a **deployment** rather than against a checkout, and every
// one of them needs something only the app can hand over. `config env` is the
// clearest: listing the variables a deployment can set means walking this
// struct, and the struct is the app's.
//
// `serve` is not among them and will not be. It is the one command whose body
// is the stack -- which layers, in which order, with the wall on which server
// -- and that is the most important thing a reader of an app can see.
//
// The configuration is read on the **root**, so it has happened whichever
// subcommand runs -- `config` prints what came out, `serve` listens on what it
// says. A command that loaded it for itself would be one more place for the
// order to be wrong.
func Cmd(c *Config) *xli.Command {
	return &xli.Command{
		Name:  Name,
		Brief: "roster",

		Flags: flg.Flags{
			pdcmd.ConfigFlag(),

			// Named after the one `oas` has, which is named after the computer
			// that would not open the pod bay doors. What it does is skip the
			// wire: whatever `client.addr` says, the entity commands open the
			// database in `db` and read it directly.
			//
			// A switch on the root rather than on each command, because it is
			// about where this invocation runs and not about what it asks for.
			&flg.Switch{Name: "HAL", Brief: "read the database directly, whatever client.addr says"},
		},

		Commands: append([]*xli.Command{
			pdcmd.NewCmdVersion(),
			pdcmd.NewCmdConfig(Loader, c),
			NewCmdInit(c),
			NewCmdKey(c),
			NewCmdTrail(c),
			NewCmdForget(c),
			NewCmdRestore(c),
			NewCmdServe(c),
		}, NewCmdEntities(c)...),

		Handler: xli.Chain(pdcmd.Load(Loader, c), hal(c), xli.RequireSubcommand()),
	}
}

// hal is `--HAL`, read on the way down.
//
// A handler and not something the connector asks for itself, because a
// connector is handed a context and not the command -- and the flag is on the
// root, several commands above whichever one is running. This is the same seam
// `pdcmd.Load` uses to put the configuration where a leaf can find it.
func hal(c *Config) xli.Handler {
	// `xli.On(mode.Run)` and not `OnRun`, which is exact: a root with a
	// subcommand under it runs as `Run|Pass`, so the exact form never fires
	// there -- and this is only ever on a root. `pdcmd.Load` is gated the same
	// way, for the same reason.
	return xli.On(mode.Run, func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
		if v, ok := flg.Find[bool](cmd, "HAL"); ok && v {
			c.Client.Local = true
		}

		return next(ctx)
	})
}

// watch is which broker this plane publishes to, falling back to the kind the
// data plane named.
//
// The **name** and not the whole block: an outbox is about durability of one
// database's writes and the two planes have two databases, so inheriting it
// would turn one decision into two rows in a table nobody asked for.
//
// It is the one field that falls back, and it took a rewrite to be: this used
// to answer with a fresh `WatchConfig` carrying the data plane's broker
// whenever `control.watch.broker` was empty -- which threw away everything else
// the control block had said. A deployment that wrote `control.watch.outbox:
// true` and left the broker to be inherited, exactly as the field above invites
// it to, got no recorder and no drain: the setting was loaded, listed by
// `roster config env`, and dropped on the floor, so a crash between the commit
// and the publish still lost the key change it had paid a row per write to
// keep. Nothing said so, and nothing could have -- both arrangements build a
// working server.
//
// Filling the one field in rather than choosing between two blocks is also what
// keeps the next field added to `WatchConfig` from having to be remembered
// here.
func (c ControlConfig) watch(data config.WatchConfig) config.WatchConfig {
	if c.Watch.Broker == "" {
		c.Watch.Broker = data.Broker
	}

	return c.Watch
}
