package cmd

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

	"github.com/lesomnus/roster/internal/ent"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	enttenant "github.com/lesomnus/roster/internal/ent/tenant"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
)

// NewCmdForget is `roster forget`: destroy what this deployment holds about
// somebody.
//
// # Why it is not `holder erase`
//
// Because they are different acts and the difference is the whole point.
// `roster holder erase` writes two columns and stops -- the row keeps the
// alias, the name and the profile, the addresses and identities keep theirs,
// and the trail holds a copy of all of it. It makes somebody unreachable and
// destroys nothing, which is right for *this person has left*.
//
// This is *destroy what you hold about them*, which is a different request with
// a legal clock on it. See `server/forget`.
//
// # And why it is a command
//
// The same boundary everything else here needs one for. It writes through ent,
// past every server: most of them refuse this, and the ones that would not
// would **record** it -- and a record of what was destroyed is the thing being
// destroyed. What guards it is a shell on the box, which is a credential
// nothing can steal over the wire.
func NewCmdForget(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "forget",
		Brief: "destroy what is held about somebody, or everybody whose grace has run out",

		Args: arg.Args{
			&pdcmd.ArgRef{Name: "REF", Optional: true, Brief: "who, as @tenant/alias or an identifier; everybody due by default"},
		},

		Flags: flg.Flags{
			&flg.Switch{Name: "dry-run", Brief: "say who, and destroy nothing"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			s, err := Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			dry, _ := flg.Find[bool](cmd, "dry-run")

			ref, named := arg.Get[pdcmd.Ref](cmd, "REF")
			if named {
				if err := ref.Expect(pd.HolderDomain); err != nil {
					return err
				}

				who, err := whoIs(ctx, s.Ent, ref)
				if err != nil {
					return err
				}
				if dry {
					fmt.Fprintf(os.Stdout, "%s\n", who)

					return nil
				}

				// No grace. They asked, and the clock a regulator counts is
				// already running -- a window inside it would be risk bought
				// with nothing.
				return forgotten(ctx, s.Ent, who, c.Holder.Archive(*c))
			}

			// Nobody named, so everybody the policy is done waiting for. Which
			// is what somebody putting this in cron means, and it is the same
			// shape `trail prune` has for the same reason.
			after := c.Holder.ForgetAfter
			if after <= 0 {
				return errors.New("this deployment destroys nobody on a clock, so there is nobody due; " +
					"see `holder.forget_after` in the configuration, or name who")
			}

			vs, err := forget.Due(ctx, s.Ent, time.Now().Add(-after))
			if err != nil {
				return err
			}
			if len(vs) == 0 {
				fmt.Fprintf(os.Stderr, "nobody has been erased for longer than %s\n", after)

				return nil
			}

			for _, who := range vs {
				if dry {
					fmt.Fprintf(os.Stdout, "%s\n", who)

					continue
				}
				if err := forgotten(ctx, s.Ent, who, c.Holder.Archive(*c)); err != nil {
					return err
				}
			}

			return nil
		}),
	}
}

// NewCmdRestore is `roster restore`: undo an erase while there is still one to
// undo.
//
// It is what makes `holder.forget_after` a **grace** rather than a delay. The
// window exists for a mistaken deletion, a compromised account and a billing
// dispute, and every one of those is a reason that needs the mistake to be
// reversible.
func NewCmdRestore(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "restore",
		Brief: "bring back somebody who was erased and has not been forgotten",

		Args: arg.Args{
			&pdcmd.ArgRef{Name: "REF", Brief: "who, as @tenant/alias or an identifier"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			ref, ok := arg.Get[pdcmd.Ref](cmd, "REF")
			if !ok {
				return errors.New("REF: who to bring back")
			}
			if err := ref.Expect(pd.HolderDomain); err != nil {
				return err
			}

			s, err := Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			who, err := whoIs(ctx, s.Ent, ref)
			if err != nil {
				return err
			}
			if err := forget.Restore(ctx, s.Ent, who); err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "%s is back\n", who)

			return nil
		}),
	}
}

func forgotten(ctx context.Context, db *ent.Client, who pdid.Id, archive string) error {
	res, err := forget.Forget(ctx, db, who, archive)
	if err != nil {
		return fmt.Errorf("%s: %w", who, err)
	}

	fmt.Fprintf(os.Stderr, "%s forgotten: %s\n", who, res)

	return nil
}

// whoIs resolves a reference **including the erased**, which is the whole
// difficulty and the reason this does not go through a server.
//
// Every read a server makes is narrowed to the rows still there -- `HolderPick`
// composes that into every reference -- and everybody this command is about has
// been erased. Asking a server for a leaver by alias is asking for nobody, and
// correctly.
func whoIs(ctx context.Context, db *ent.Client, ref pdcmd.Ref) (pdid.Id, error) {
	if !ref.Id.IsZero() {
		return ref.Id, nil
	}
	if ref.Alias == "" {
		return pdid.Nil, errors.New("REF: an alias or an identifier")
	}

	q := db.Holder.Query().Where(entholder.AliasEQ(ref.Alias))
	if ref.Tenant != "" {
		q = q.Where(entholder.HasTenantWith(enttenant.AliasEQ(ref.Tenant)))
	}

	vs, err := q.Limit(2).All(ctx)
	if err != nil {
		return pdid.Nil, err
	}
	switch len(vs) {
	case 0:
		return pdid.Nil, fmt.Errorf("no holder is called %q", ref.Alias)

	case 1:
		return pdid.Id(vs[0].Id), nil

	default:
		// An alias is unique **within a tenant and among the living**, so two
		// answers is either two tenants or a name reused after a leaver. Both
		// are things to be told about rather than to have chosen between.
		return pdid.Nil, fmt.Errorf("more than one holder is called %q; name the tenant, or use an identifier", ref.Alias)
	}
}
