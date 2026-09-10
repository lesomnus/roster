package cli

import (
	"context"
	"fmt"

	"github.com/lesomnus/xli"
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
func NewCmdResources(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "resources",
		Brief: "the rows a file declares: a customer, its names, its directories",

		Commands: xli.Commands{newCmdResourcesApply(c)},
	}
}

func newCmdResourcesApply(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "apply",
		Brief: "write what the files declared; --dry-run says what it would do",

		Flags: flg.Flags{
			&flg.Strings{Name: "file", Brief: "a file to read; the `resources:` block otherwise"},
			&flg.Switch{Name: "dry-run", Brief: "say what would change and write nothing"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			paths := c.Resources
			if vs, _ := flg.Find[[]string](cl, "file"); len(vs) > 0 {
				paths = vs
			}
			if len(paths) == 0 {
				return fmt.Errorf("resources (--file): no files declared, and none given")
			}

			rs, err := cmd.ReadResources(paths)
			if err != nil {
				return err
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			dry, _ := flg.Find[bool](cl, "dry-run")
			if err := ready(ctx, s, c.Db); err != nil {
				return err
			}

			v, err := cmd.ApplyResources(ctx, s, rs, dry)
			if err != nil {
				return err
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
			if dry {
				fmt.Fprintln(cl.Root().Writer, "\n(dry run; nothing was written)")
			}

			return nil
		}),
	}
}
