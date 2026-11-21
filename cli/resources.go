package cli

import (
	"context"
	"fmt"
	"os"

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

			v, err := cmd.ApplyResources(ctx, s, rs, dry)
			if err != nil {
				return err
			}

			// What `serve` logs, said here because nothing installs a logger
			// on a command: a file that declares nothing is no row, so it is
			// said beside them and not among them.
			for _, f := range v.Nothing {
				fmt.Fprintf(os.Stderr, "roster: resources: %s declares nothing.\n", f)
			}

			// Said one per line and not counted, because what somebody running
			// this wants is **which**. The counts are what a log line is for.
			for _, w := range v.Added {
				fmt.Fprintf(cl.Root().Writer, "+ %s\n", w)
			}
			for _, w := range v.Changed {
				fmt.Fprintf(cl.Root().Writer, "~ %s\n", w)
			}
			for _, w := range v.Same {
				fmt.Fprintf(cl.Root().Writer, "  %s\n", w)
			}
			// What the files declare of a tenant that is closed or destroyed,
			// which is not written and not compared (`cmd.ApplyResources`).
			// Said here because nothing installs a logger on a command, and
			// the warning `serve` logs for it would reach nobody.
			for _, w := range v.Left {
				fmt.Fprintf(cl.Root().Writer, "! %s\n", w)
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
