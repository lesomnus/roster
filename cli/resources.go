package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/roster/cmd"
)

// NewCmdResources is `roster resources`: what a file declared, and what
// applying it would do.
//
// `serve` applies these on every start, so this command is not how they get
// there -- it is how somebody **finds out first**. A declared file is edited
// and pushed and a pod restarts, and the moment between those is the one where
// a person wants to know that a `Connection`'s issuer is about to change.
//
// So `--dry-run` is the reason it exists and `apply` without it is the
// afterthought: the same function `serve` calls, run by hand, for a deployment
// that would rather not wait for a restart.
func NewCmdResources(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "resources",
		Brief: "the rows a file declares: a customer, its names, its directories",

		Commands: xli.Commands{newCmdResourcesApply(l, c)},

		Handler: xli.RequireSubcommand(),
	}
}

func newCmdResourcesApply(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "apply",
		Brief: "write what the files declared; --dry-run says what it would do",

		Flags: flg.Flags{
			// `resources:` itself, given at the shell: the files replace the
			// list rather than adding to it.
			cfg.Bind(l, &c.Resources, &flg.Strings{Name: "file", Brief: "a file to read; the `resources:` block otherwise"}),
			&flg.Switch{Name: "dry-run", Brief: "say what would change and write nothing"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			paths := c.Resources
			if len(paths) == 0 {
				return fmt.Errorf("resources (--file): no files declared, and none given")
			}

			rs, err := cmd.ReadResources(paths)
			if err != nil {
				return err
			}

			s, err := built(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// A dry run alters no table: it asks of both planes what `serve`
			// asks before it serves, without migrating either, and is refused
			// in those words where a table is missing. It migrated the data
			// plane where `db.migrate` was on and never looked at the control
			// plane, so on a deployment nothing had started it made one plane's
			// tables and then failed on the other's.
			dry, _ := flg.Find[bool](cl, "dry-run")
			if dry {
				asked := *c
				asked.Db.Migrate, asked.Control.Db.Migrate = false, false
				if err := Ready(ctx, s, asked); err != nil {
					return err
				}
			} else if err := ready(ctx, s, c.Db); err != nil {
				return err
			}

			// What was done before an error is printed before it. The command
			// returned before it printed, so a tenant made ahead of a row that
			// failed was made with no line saying so.
			v, err := cmd.ApplyResources(ctx, s, rs, dry)
			printApplied(cl.Root().Writer, v)
			if err != nil {
				return err
			}

			// What `serve` logs, said here because nothing installs a logger
			// on a command: a file that declares nothing is no row, so it is
			// said beside them and not among them.
			for _, f := range v.Nothing {
				fmt.Fprintf(os.Stderr, "roster: resources: %s declares nothing.\n", f)
			}

			if len(v.Left) > 0 {
				fmt.Fprintln(cl.Root().Writer, "\n(! is of a tenant that is closed or destroyed: left as it is. "+
					"Restore the tenant if it is closed, or take these out of the file)")
			}
			if dry {
				fmt.Fprintln(cl.Root().Writer, "\n(dry run; nothing was written)")
			}

			return nil
		}),
	}
}

// printApplied is one line a row, `<sign> <Kind> <name>`, and after a `~` the
// keys that change: `~ Connection @contoso/entra: issuer, scopes`. The line is
// this command's output, and a script may read it.
//
// Said one per line and not counted, because what somebody running this wants
// is **which**; the counts are what a log line is for. The keys, because a
// `~` alone did not say what this command is for: that a `Connection`'s issuer
// is about to change. The kind and the tenant, because a `Host` and a
// `MailDomain` of one name printed alike and said nothing of whose they were.
//
// By what was done, as it always was: what is added, then what is changed,
// then what is the same; and last, `!`, what the files declare of a tenant
// that is closed or destroyed, which is not written and not compared
// (`cmd.ApplyResources`) -- said here because nothing installs a logger on a
// command, and the warning `serve` logs for it would reach nobody.
func printApplied(w io.Writer, v cmd.Applied) {
	for _, was := range []struct{ was, sign string }{{"added", "+"}, {"changed", "~"}, {"same", " "}, {"left", "!"}} {
		for _, r := range v.Rows {
			if r.Was != was.was {
				continue
			}
			line := fmt.Sprintf("%s %s %s", was.sign, r.Kind, r.Name)
			if was.was == "changed" && len(r.Fields) > 0 {
				line += ": " + strings.Join(r.Fields, ", ")
			}
			fmt.Fprintln(w, line)
		}
	}
}
