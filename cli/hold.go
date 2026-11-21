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

	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	enttenant "github.com/lesomnus/roster/internal/ent/tenant"
	"github.com/lesomnus/roster/server/forget"
)

// NewCmdHold is `roster hold`: an operator's hold on a tenant, which stops the
// tenant being destroyed and keeps what the trail has of it (#98). What it
// does not stop -- somebody of the tenant forgotten by name, and the tenant's
// own grace running out -- is said in `server/forget/hold.go`, and by the
// commands where each happens.
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
		Brief: "hold a tenant -- a legal claim, a dispute -- so that it is not destroyed and the trail keeps what it has of it",

		Commands: xli.Commands{
			{
				Name:  "place",
				Brief: "hold a tenant: it is not destroyed, nobody of it is forgotten on a clock, and the trail keeps what it has of it, until the hold is lifted",
				Args:  tenantArg(),
				Flags: flg.Flags{
					&flg.String{Name: "why", Brief: "what it is for -- a case, a demand; required, and somebody will read it"},
				},
				Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
					why, _ := flg.Find[string](cl, "why")

					return onHeld(ctx, c, cl, func(s *cmd.Server, t pdid.Id) error {
						if err := forget.Hold(grpcx.AsMethod(ctx, holdPlaced), s.Ungated, t, why); err != nil {
							return err
						}
						fmt.Fprintf(os.Stderr, "%s is held: %s\n", t, why)

						return leaving(ctx, s, c, t)
					})
				}),
			},
			{
				Name:  "lift",
				Brief: "lift a tenant's hold; what it stopped carries on from there",
				Args:  tenantArg(),
				Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
					return onHeld(ctx, c, cl, func(s *cmd.Server, t pdid.Id) error {
						if err := forget.Lift(grpcx.AsMethod(ctx, holdLifted), s.Ungated, t); err != nil {
							return err
						}
						fmt.Fprintf(os.Stderr, "%s is no longer held\n", t)

						return nil
					})
				}),
			},
			{
				Name:    "ls",
				Aliases: []string{"list"},
				Brief:   "every tenant held, since when, and what for",
				Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
					s, err := built(ctx, *c)
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

// What a hold placed and a hold lifted are called on the trail. Each is a
// patch of the tenant, and was filed under the generated method that wrote it,
// `/roster.TenantService/Patch`, the same for both and for any change of a
// tenant's note (#284). They have no RPC, so they are named as
// `roster.holder.restore` and `roster.keys.use` are.
const (
	holdPlaced = "roster.hold.place"
	holdLifted = "roster.hold.lift"
)

// leaving says, of a tenant just held that is on its way out, what the hold
// does not do for it.
//
// # A closed tenant's grace runs on
//
// `tenant.restore_within` is counted from the close whatever holds the
// tenant (`core.coreTenant.Restore`), so a hold on a closed tenant keeps it
// from being destroyed and not from becoming unrestorable: past the window it
// can be neither restored nor destroyed, and the pass after the hold lifts
// destroys it. The schema's comment said so and the manual said *everything
// carries on from where it waited* (#280). This is said here because it is
// the last moment it is of use: while the grace lasts, an operator who means
// the tenant to come back restores it, and holds it open.
//
// With no window there is nothing to run out: a closed tenant is restored
// until somebody destroys it, and under a hold nobody does.
//
// # A destroyed one is not brought back
//
// A hold may be placed on a tenant already destroyed, for what the trail has
// of it. What else it keeps is said, so that nobody reads *held* as *kept*.
func leaving(ctx context.Context, s *cmd.Server, c *cmd.Config, t pdid.Id) error {
	v, err := s.Ent.Tenant.Get(ctx, t.Uuid())
	if err != nil {
		return err
	}

	switch within := c.Tenant.RestoreWithin; {
	case v.DateDestroyed != nil:
		fmt.Fprintf(os.Stderr, "%s was destroyed on %s, and a hold brings nothing back. It keeps what the trail has of the tenant, "+
			"its alias as it is, and a destruction that stopped part way where it stopped, until it is lifted\n",
			t, v.DateDestroyed.UTC().Format(time.RFC3339))

	case v.DateClosed == nil || within <= 0:

	case time.Now().Before(v.DateClosed.Add(within)):
		fmt.Fprintf(os.Stderr, "%[1]s is closed, and its grace runs on under a hold (tenant.restore_within): it can be restored until %[2]s and not after, "+
			"held or not. From then the hold is all that keeps it, and lifting it lets the next pass destroy the tenant. "+
			"If it is to come back, `roster restore %[1]s` before then\n",
			t, v.DateClosed.Add(within).UTC().Format(time.RFC3339))

	default:
		fmt.Fprintf(os.Stderr, "%s is closed, and its grace ran out at %s (tenant.restore_within): it can no longer be restored. "+
			"The hold is all that keeps it, and lifting it lets the next pass destroy the tenant\n",
			t, v.DateClosed.Add(within).UTC().Format(time.RFC3339))
	}

	return nil
}

// onHeld resolves the tenant a hold command names and runs `f` on it.
func onHeld(ctx context.Context, c *cmd.Config, cl *xli.Command, f func(s *cmd.Server, t pdid.Id) error) error {
	ref, ok := arg.Get[pdcmd.Ref](cl, "TENANT")
	if !ok {
		return errors.New("TENANT: which tenant")
	}

	s, err := built(ctx, *c)
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
