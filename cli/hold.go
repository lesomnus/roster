package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	enttenant "github.com/lesomnus/roster/internal/ent/tenant"
	"github.com/lesomnus/roster/server/forget"
)

// NewCmdHold is `roster hold`: an operator's hold on a tenant, which stops
// everything that would destroy it (#98).
//
// # Why it is a command and not an RPC
//
// Because a hold is the operator's, against the tenant: a tenant's own
// administrator lifting the hold on their own records is the act it exists to
// prevent, and every method a role can name is one a tenant's `everything`
// role holds. A shell on the box is the credential nothing over the wire has.
// It writes through the server the wall was never installed on, so placing
// and lifting are in the trail. See `server/forget/hold.go`.
func NewCmdHold(c *cmd.Config) *xli.Command {
	tenantArg := func() arg.Args {
		return arg.Args{&pdcmd.ArgRef{Name: "TENANT", Brief: "which, as @tenant or an identifier"}}
	}

	return &xli.Command{
		Name:  "hold",
		Brief: "hold a tenant as it is -- a legal claim, a dispute -- so that nothing destroys it",

		Commands: xli.Commands{
			{
				Name:  "place",
				Brief: "hold a tenant: nothing of it is destroyed until the hold is lifted",
				Args:  tenantArg(),
				Flags: flg.Flags{
					&flg.String{Name: "why", Brief: "what it is for -- a case, a demand; required, and somebody will read it"},
				},
				Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
					why, _ := flg.Find[string](cl, "why")

					return onHeld(ctx, c, cl, func(s *cmd.Server, t pdid.Id) error {
						if err := forget.Hold(ctx, s.Ungated, t, why); err != nil {
							return err
						}
						fmt.Fprintf(os.Stderr, "%s is held: %s\n", t, why)

						return nil
					})
				}),
			},
			{
				Name:  "lift",
				Brief: "lift a tenant's hold; what it stopped carries on from there",
				Args:  tenantArg(),
				Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
					return onHeld(ctx, c, cl, func(s *cmd.Server, t pdid.Id) error {
						if err := forget.Lift(ctx, s.Ungated, t); err != nil {
							return err
						}
						fmt.Fprintf(os.Stderr, "%s is no longer held\n", t)

						return nil
					})
				}),
			},
			{
				Name:  "ls",
				Brief: "every tenant held, since when, and what for",
				Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
					s, err := cmd.Build(ctx, *c)
					if err != nil {
						return err
					}
					defer s.Close()

					vs, err := s.Ent.Tenant.Query().Where(enttenant.DateHeldNotNil()).All(ctx)
					if err != nil {
						return err
					}
					for _, v := range vs {
						cl.Printf("@%s\t%s\t%s\t%s\n", v.Alias, pdid.Id(v.Id), v.DateHeld.Format(time.RFC3339), v.Hold)
					}

					return nil
				}),
			},
		},

		Handler: xli.RequireSubcommand(),
	}
}

// onHeld resolves the tenant a hold command names and runs `f` on it.
func onHeld(ctx context.Context, c *cmd.Config, cl *xli.Command, f func(s *cmd.Server, t pdid.Id) error) error {
	ref, ok := arg.Get[pdcmd.Ref](cl, "TENANT")
	if !ok {
		return errors.New("TENANT: which tenant")
	}

	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()

	t, isTenant, err := tenantOrHolder(ctx, s.Ent, ref)
	if err != nil {
		return err
	}
	if !isTenant {
		return fmt.Errorf("%s is a person, and a hold is on a tenant", t)
	}

	return f(s, t)
}
