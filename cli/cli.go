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

	"github.com/lesomnus/xli"
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
// order to be wrong.
func Cmd(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  cmd.Name,
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
			pdcmd.NewCmdConfig(cmd.Loader, c),
			NewCmdInit(c),
			NewCmdKey(c),
			NewCmdIssue(c),
			NewCmdVouch(c),
			NewCmdTrail(c),
			NewCmdForget(c),
			NewCmdRestore(c),
			NewCmdServe(c),
			NewCmdAccount(c),
			NewCmdLdap(c),
			NewCmdLogin(c),
			NewCmdResources(c),
		}, NewCmdEntities(c)...),

		// `ROSTER_ACCOUNT_KEY_<ALIAS>` and the three beside it are read by the
		// consumers themselves (`keysOf`, `clientsOf`), not by the loader, and
		// are not typos. Anything else beginning `ROSTER_` that no field
		// answers to is reported, which is what a typo looks like -- and which
		// is how the Login App's two were found, by being reported.
		Handler: xli.Chain(pdcmd.Load(cmd.Loader, c,
			pdcmd.Reads("ACCOUNT_KEY_", "LDAP_KEY_", "LOGIN_KEY_", "LOGIN_CLIENT_"),
		), hal(c), xli.RequireSubcommand()),
	}
}

// hal is `--HAL`, read on the way down.
//
// A handler and not something the connector asks for itself, because a
// connector is handed a context and not the command -- and the flag is on the
// root, several commands above whichever one is running. This is the same seam
// `pdcmd.Load` uses to put the configuration where a leaf can find it.
func hal(c *cmd.Config) xli.Handler {
	// `xli.On(mode.Run)` and not `OnRun`, which is exact: a root with a
	// subcommand under it runs as `Run|Pass`, so the exact form never fires
	// there -- and this is only ever on a root. `pdcmd.Load` is gated the same
	// way, for the same reason.
	return xli.On(mode.Run, func(ctx context.Context, cl *xli.Command, next xli.Next) error {
		if v, ok := flg.Find[bool](cl, "HAL"); ok && v {
			c.Client.Local = true
		}

		return next(ctx)
	})
}
