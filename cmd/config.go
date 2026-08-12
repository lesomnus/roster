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
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/pdcmd"

	"github.com/lesomnus/payday/config"

	// The one driver this app runs on. It is blank-imported here rather than
	// by payday so that an app does not carry an engine it never opens.
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

		Flags: flg.Flags{pdcmd.ConfigFlag()},

		Commands: []*xli.Command{
			pdcmd.NewCmdVersion(),
			pdcmd.NewCmdConfig(Loader, c),
			NewCmdInit(c),
			NewCmdKey(c),
			NewCmdServe(c),
		},

		Handler: xli.Chain(pdcmd.Load(Loader, c), xli.RequireSubcommand()),
	}
}
