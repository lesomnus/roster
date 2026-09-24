package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"

	"github.com/lesomnus/payday/pdcmd"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	"github.com/lesomnus/roster/server/keys"
)

// NewCmdControl is `roster control`: the commands that write to the control
// plane, which is a database of its own (`control.db`) holding this
// deployment's own holders: its operators, and whatever calls in.
//
// # A subcommand, not a flag
//
// It was two flags, spelled two ways: `roster key add --service NAME` and
// `roster vouch reset|set|unlock --control`. Each turned a command about a
// customer into one writing to a different database, and each could be left
// off. `--service` was the worse of the two, because *service* is a word a
// customer uses too: to somebody running a tenant, `key add --service ci-bot`
// reads as a key for their own machine, and what it did was make a caller of
// the whole deployment -- a holder in the control plane with an `rk_` walled by
// no tenant. The path now says which database before anything else does.
//
// What stays outside is what answers across both: `roster key list` and
// `roster key revoke`, because an identifier does not say which plane it is on
// and an operator stopping a leaked key should not have to know; and `init`,
// `serve` and the migration, which are about both.
//
// # Local only
//
// Every command here opens `control.db` directly, whatever `client.addr` says.
// `client.addr` names the data plane's port, and a command reaching the control
// plane's over the wire would need a deployment key to present -- a second
// credential in the file, for the one plane whose whole point is holding them.
// `roster issue` is the wire form, for the two mints that have one.
func NewCmdControl(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "control",
		Brief: "the control plane: this deployment's own holders and their keys",

		Commands: append(xli.Commands{
			newCmdControlKey(c),
			newCmdControlVouch(c),
		}, newCmdControlEntities(c)...),

		Handler: xli.RequireSubcommand(),
	}
}

func newCmdControlKey(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "key",
		Brief: "mint a key for one of this deployment's own holders",

		Commands: xli.Commands{
			newCmdControlKeyAdd(c),
		},

		Handler: xli.RequireSubcommand(),
	}
}

// newCmdControlKeyAdd mints an `rk_` for one of this deployment's own callers
// -- the Login App, a product backend, a job reading the trail -- and prints it
// once.
//
// # Naming a holder makes it
//
// Unlike one of a customer's, which `roster key add` only looks up. A caller of
// this deployment's own is not a row somebody sets up on purpose beforehand:
// this is the moment it becomes one, and the control plane has one tenant so
// the name is the whole of who. [cmd.HolderNamed] is that decision.
func newCmdControlKeyAdd(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "add",
		Brief: "mint a key for a holder of this deployment, made if new, and print it once",

		Args: arg.Args{
			&arg.String{Name: "HOLDER", Brief: "which holder, by alias; made if there is none"},
		},

		Flags: mintFlags(),

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			alias, _ := arg.Get[string](cl, "HOLDER")

			m, err := mintingOf(cl)
			if err != nil {
				return err
			}

			s, err := controlled(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			// The control database may be new: this is often the first thing
			// that writes to it.
			if err := entmigrate.NewSchema(s.Control.Drv).Create(ctx); err != nil {
				return err
			}

			who, err := cmd.HolderNamed(ctx, s.Control, alias)
			if err != nil {
				return err
			}

			return m.mint(ctx, s.Control.Ungated, who, keys.PrefixDeployment, "@"+alias)
		}),
	}
}

// newCmdControlVouch is `roster vouch`'s three local commands, on the control
// plane: an operator's password, reset or set, and their account unlocked.
//
// `reset @admin` here is how a deployment of one operator who lost their
// password gets back in (#16); `whom` says why it looks the person up rather
// than letting `Credential.Issue` make one.
func newCmdControlVouch(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "vouch",
		Brief: "an operator's way in: reset it, set it, unlock it",

		Commands: xli.Commands{
			newCmdVouchReset(c, true),
			newCmdVouchSet(c, true),
			newCmdVouchUnlock(c, true),
		},

		Handler: xli.RequireSubcommand(),
	}
}

// newCmdControlEntities is the entity tree on the control plane: `holder ls`
// for who is registered there, `api-key ls` for what they hold,
// `holder disable` for stopping one.
//
// There was no way to see any of it from a shell. The admin console was the only
// view of the control plane's rows, which made a deployment run from a
// terminal one whose operators nobody could list.
//
// # Without `tenant add`
//
// The control plane has **one** tenant, and that is load-bearing:
// [cmd.HolderNamed] takes the first tenant it lists, and `Credential.Issue`
// names an operator by alias alone because an alias names one person only
// where there is one tenant. A second tenant would make both answer about
// whichever row a query happened to return first -- so the command that could
// make one is not here. The tenant itself is made by whatever writes first.
func newCmdControlEntities(c *cmd.Config) xli.Commands {
	t := pdcmd.NewIn(controlConnector{c}, "roster")
	overlays(t)

	if err := t.Drop("tenant/add"); err != nil {
		// A path the schema no longer has, which is a mistake in this file
		// rather than something a deployment can cause.
		panic(err)
	}

	return t.Commands()
}

// controlConnector is the control plane's `Ungated`, on a pipe with no address
// -- [local], one database over.
type controlConnector struct{ c *cmd.Config }

func (v controlConnector) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	// Said out loud, for [local]'s reason.
	fmt.Fprintln(os.Stderr, "roster: reading this deployment's control database directly")

	s, err := controlled(ctx, v.c)
	if err != nil {
		return nil, nil, err
	}

	return piped(s, s.Control.Ungated)
}

// controlled is the deployment, refused if it has no control plane to write to.
func controlled(ctx context.Context, c *cmd.Config) (*cmd.Server, error) {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return nil, err
	}
	if s.Control == nil {
		s.Close()
		return nil, errors.New("this deployment has no control plane; see `control` in the configuration")
	}

	return s, nil
}
