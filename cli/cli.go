// Package cli is this app's command line, and what only a process does.
//
// `cl` is the wiring both entry points share -- `Build` is there, and the
// sandbox in `wasm/` calls it to stand up the same server the process stands
// up. This package is everything on the other side of that: parsing arguments,
// the database engines a process opens, and what to do about the shape of a
// database that was already there.
//
// # Why a package and not a build tag
//
// Because the linker follows imports. A blank-imported driver or a migration
// engine named in `cl` is linked into the sandbox whether the page can use it
// or not, and neither is small. Measured on this app, moving them here took
// `GOOS=js GOARCH=wasm go build ./wasm` from 104.7 MB to 69.0 MB -- 11.8 MB
// rather than 19.2 MB gzipped, which is what a browser actually downloads --
// and the page opens `sqlite3-wasm` and never migrates, so none of it was ever
// reachable there.
//
// A `//go:build !js` on each would also have done it, and is worse: a tag
// refuses to compile what would compile, and says nothing about why. What is
// true is that a process and a page need different things, and a package
// boundary is how Go says that. Nothing here is excluded from a build; the
// sandbox simply does not import it.
//
// The rule that follows: **nothing in `cl` may name a driver, `entmigrate` or
// `entschema`.** `pd doctor` does not check it and no test can; what checks it
// is the size of `ts/public/app.wasm`.
package cli

import (
	"context"
	"errors"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/mode"

	"github.com/lesomnus/payday/pdcmd"

	"github.com/lesomnus/roster/cmd"
)

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
// order to be wrong. What `c` holds when the loader is made is the defaults:
// every load starts from it, which is what lets a test hand over a `Config`
// filled in for it.
//
// The commands handed `l` are the ones with a flag that says a setting.
// `cfg.Bind` ties the flag to the field, and the load applies it over the file
// and the environment when it is given -- so the block a handler copies
// already holds it, and `--help` prints the variable beside it. `Bind` takes a
// field of this `c` and no other, which is why both are handed over.
func Cmd(c *cmd.Config) *xli.Command {
	l := cfg.New(cmd.Name, c,
		// `ROSTER_ACCOUNT_TENANT_KEY_<ALIAS>` and the one beside it are read by
		// the consumers themselves (`keysOf`), not by the loader, and are not
		// typos. Anything else beginning `ROSTER_` that no field answers to is
		// reported, which is what a typo looks like. The Login App holds one
		// key, an ordinary field the loader reads
		// (`ROSTER_LOGIN_DEPLOYMENT_KEY`).
		cfg.Reads("ACCOUNT_TENANT_KEY_", "LDAP_TENANT_KEY_"),
	)

	// `version` needs no configuration, and is what somebody runs to ask a
	// deployment whose configuration is wrong what build it is.
	version := pdcmd.NewCmdVersion()

	// payday's words for `--short` are *the version and nothing else*, and
	// what it prints is the commit the binary was built from (#284). Said
	// here, where roster mounts it, until payday says it.
	if v, ok := version.Flags.Get("short").(*flg.Switch); ok {
		v.Brief = "the commit it was built from and nothing else: twelve characters, a + for uncommitted changes, empty where the build has none"
	}
	stamped(version)

	return &xli.Command{
		Name:  cmd.Name,
		Brief: "roster",

		Flags: flg.Flags{
			cfg.ConfigFlag(),

			// Named after the one `oas` has, which is named after the computer
			// that would not open the pod bay doors. What it does is skip the
			// wire: whatever `client.addr` says, the entity commands open the
			// database in `db` and read it directly.
			//
			// A switch on the root rather than on each command, because it is
			// about where this invocation runs and not about what it asks for.
			&flg.Switch{Name: "HAL", Brief: "read the database directly, whatever client.addr says"},

			// Who is at this shell, for the access record (#221). On the root
			// for `--HAL`'s reason: it is about the invocation, and every
			// command that reads the database directly needs it said once.
			// And a flag only, never the file or the environment, which are
			// everybody's who opens a shell here; see `cmd.ClientConfig.As`.
			//
			// Looked up wherever it is given ([built]), and required and kept
			// by the commands that show somebody's rows or write somebody's
			// way in ([saidWho]); the help says which, since a name a command
			// only checks is not one the record says (#284).
			&flg.String{Name: "as", Brief: "who is at this shell: a holder of the control plane that is not stopped, by alias; looked up by every command here; required, where there is a control plane, and named on the access record by the entity commands, key list|add|revoke, control key add, vouch reset|set|unlock and their control forms, control vouch enrol|prove, restore, and scim adopt"},
		},

		Commands: append([]*xli.Command{
			version,
			cfg.NewCmdConfig(l),
			NewCmdInit(c),
			NewCmdKey(c),
			NewCmdControl(c),
			NewCmdIssue(c),
			NewCmdVouch(c),
			NewCmdSignIn(c),
			NewCmdTrail(l, c),
			NewCmdForget(c),
			NewCmdRestore(c),
			NewCmdHold(c),
			NewCmdServe(c),
			NewCmdAccount(l, c),
			NewCmdLdap(l, c),
			NewCmdScim(l, c),
			NewCmdLogin(l, c),
			NewCmdResources(l, c),
			NewCmdApp(c),
		}, NewCmdEntities(c)...),

		Handler: xli.Chain(cfg.Load(l, version), hal(c), xli.RequireSubcommand()),
	}
}

// built is this deployment as a command at a shell stands it up: the same
// `cmd.Build`, with every write it makes queued in the outbox for the process
// that serves to publish ([cmd.Config.Queued], #214).
//
// A command is a process of its own that ends once it has answered, so what it
// would publish itself reaches nobody -- the broker it would publish to is its
// own -- and an app following the stream heard of `roster holder disable` only
// when it next asked. Every command but `serve` builds through this, and
// `TestEveryCommandBuildsWhatItWritesQueued` holds them to it: one that called
// `cmd.Build` itself would be back to writing rows the stream never carries.
//
// A configuration that names no database is refused here in roster's words: a
// file with only `client:` in it is what a customer's person is shown, and
// payday's sentence for it is about a driver nobody wrote.
//
// # And a name given with `--as` is looked up here, whatever the command
//
// `--as` was read by the entity commands, `key list`, `scim adopt` and `trail
// rows --accept`, and by nothing else: `roster --as nobody vouch reset …`
// printed a password, and the name was never looked up (#284). Somebody who
// gave it believes the record says it, which is the reason the wire refuses
// it ([connector]). So wherever it is given to a command that opens the
// deployment, it is looked up as the entity commands look it up -- nobody,
// somebody stopped and somebody erased are refused -- before anything is
// read or written. Which commands go on to keep the name is theirs to say
// ([keptBy]); the others check it and keep nothing.
func built(ctx context.Context, c cmd.Config) (*cmd.Server, error) {
	if c.Db.Driver == "" {
		return nil, errors.New("db.driver: this command reads the deployment's database, and this configuration " +
			"names none. Run it where the deployment's file is; the commands that ask a running roster instead " +
			"are me, front, sync, issue and the entity commands, with client.addr")
	}

	// At the box, which is where the line falls between a write a hook is
	// asked about and one that is the deployment's own: a command here has no
	// caller (`cmd/hook.go`).
	s, err := cmd.Build(ctx, c.Queued().AtTheBox())
	if err != nil {
		return nil, err
	}
	if _, err := namedAt(ctx, &c, s); err != nil {
		s.Close()

		return nil, err
	}

	return s, nil
}

// hal is `--HAL` and `--as`, read on the way down.
//
// A handler and not something the connector asks for itself, because a
// connector is handed a context and not the command -- and the flags are on the
// root, several commands above whichever one is running. This is the same seam
// `cfg.Load` uses to put the configuration where a leaf can find it.
func hal(c *cmd.Config) xli.Handler {
	// `xli.On(mode.Run)` and not `OnRun`, which is exact: a root with a
	// subcommand under it runs as `Run|Pass`, so the exact form never fires
	// there -- and this is only ever on a root. `cfg.Load` is gated the same
	// way, for the same reason.
	return xli.On(mode.Run, func(ctx context.Context, cl *xli.Command, next xli.Next) error {
		// By hand and not `cfg.Bind`: `Client.Local` is `yaml:"-"`, outside
		// what the loader reads, so there is no field of its for it to bind to.
		if v, ok := flg.Find[bool](cl, "HAL"); ok && v {
			c.Client.Local = true
		}
		if v, ok := flg.Find[string](cl, "as"); ok && v != "" {
			c.Client.As = v
		}

		return next(ctx)
	})
}
