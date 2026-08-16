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
	// -- the quickstart `docs/OPERATING.md` gives -- fail at
	// `unknown driver "pgx"`, which is a sentence about a name rather than
	// about a missing import and reads as a typo in the configuration.
	_ "github.com/lesomnus/payday/config/dbpgx"
	_ "github.com/lesomnus/payday/config/dbsqlite3"
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
