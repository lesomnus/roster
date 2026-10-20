package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"os"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	enttenant "github.com/lesomnus/roster/internal/ent/tenant"
	"github.com/lesomnus/roster/rstr"
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
func NewCmdForget(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "forget",
		Brief: "destroy what is held about somebody or about a closed tenant, or everything whose grace has run out",

		Args: arg.Args{
			&pdcmd.ArgRef{Name: "REF", Optional: true, Brief: "who, as @tenant/alias or an identifier, or a closed tenant as @tenant; everything due by default"},
		},

		Flags: flg.Flags{
			&flg.Switch{Name: "dry-run", Brief: "say who, and destroy nothing"},
			&flg.Switch{Name: "reuse", Brief: "a tenant's: free its alias now rather than after `tenant.alias_hold`"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			p, err := c.Holder.Trail(*c)
			if err != nil {
				return err
			}
			// What a hold keeps, it keeps from this as well. See `forget.Hold`.
			p = forget.Held(p, s.Ent)

			// What a destruction leaves is on the trail as the act it is.
			cs, err := c.Audit.Contents()
			if err != nil {
				return fmt.Errorf("audit: %w", err)
			}

			dry, _ := flg.Find[bool](cl, "dry-run")
			reuse, _ := flg.Find[bool](cl, "reuse")

			ref, named := arg.Get[pdcmd.Ref](cl, "REF")
			if named {
				t, isTenant, err := tenantOrHolder(ctx, s.Ent, ref)
				if err != nil {
					return err
				}
				if isTenant {
					if dry {
						fmt.Fprintf(os.Stdout, "%s\n", t)

						return nil
					}

					return destroyed(ctx, s, t, p, cs, reuse)
				}
				if reuse {
					return errors.New("--reuse frees a tenant's alias; a person's is free the moment they are forgotten")
				}

				who := t
				if dry {
					fmt.Fprintf(os.Stdout, "%s\n", who)

					return nil
				}

				// No grace. They asked, and the clock a regulator counts is
				// already running -- a window inside it would be risk bought
				// with nothing.
				return forgotten(ctx, s.Ent, who, p)
			}
			if reuse {
				return errors.New("--reuse frees one tenant's alias, so name the tenant")
			}

			// Nobody named, so everything a clock is done waiting for. Which is
			// what somebody putting this in cron means, and it is the same
			// shape `trail prune` has for the same reason.
			return due(ctx, s, c, p, dry)
		}),
	}
}

// due is everything a clock has finished waiting for: the people whose grace
// ran out (`holder.forget_after`), the closed tenants whose grace did
// (`tenant.restore_within`) and any whose destruction began and stopped, and
// the aliases whose hold is over (`tenant.alias_hold`, which has a default).
func due(ctx context.Context, s *cmd.Server, c *cmd.Config, p trail.Policy, dry bool) error {
	now := time.Now()

	cs, err := c.Audit.Contents()
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	if after := c.Holder.ForgetAfter; after > 0 {
		vs, err := forget.Due(ctx, s.Ent, now.Add(-after))
		if err != nil {
			return err
		}
		if len(vs) == 0 {
			fmt.Fprintf(os.Stderr, "nobody has been erased for longer than %s\n", after)
		}
		for _, who := range vs {
			if dry {
				fmt.Fprintf(os.Stdout, "%s\n", who)

				continue
			}
			if err := forgotten(ctx, s.Ent, who, p); err != nil {
				return err
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "nobody is destroyed on a clock: `holder.forget_after` names no window\n")
	}

	var ts []pdid.Id
	if within := c.Tenant.RestoreWithin; within > 0 {
		ts, err = forget.DueTenants(ctx, s.Ent, now.Add(-within))
	} else {
		ts, err = forget.Unfinished(ctx, s.Ent)
	}
	if err != nil {
		return err
	}
	for _, t := range ts {
		if dry {
			fmt.Fprintf(os.Stdout, "%s\n", t)

			continue
		}
		if err := destroyed(ctx, s, t, p, cs, false); err != nil {
			return err
		}
	}

	free, err := forget.DueAliases(ctx, s.Ent, now.Add(-c.Tenant.Hold()))
	if err != nil {
		return err
	}
	for _, t := range free {
		if dry {
			fmt.Fprintf(os.Stdout, "%s: alias\n", t)

			continue
		}
		if err := forget.FreeAlias(ctx, s.Ungated, t); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		fmt.Fprintf(os.Stderr, "%s: its alias is free\n", t)
	}

	return nil
}

// destroyed is one tenant destroyed, and said: what went, what a hold
// elsewhere kept, and its alias -- freed now with `reuse`, which the trail
// records, and otherwise held.
func destroyed(ctx context.Context, s *cmd.Server, t pdid.Id, p trail.Policy, cs audit.Contents, reuse bool) error {
	res, err := forget.DestroyTenant(ctx, s.Ent, t, p, cs)
	if err != nil {
		return fmt.Errorf("%s: %w", t, err)
	}

	fmt.Fprintf(os.Stderr, "%s destroyed: %s\n", t, res)
	if res.Held.Any() {
		fmt.Fprintf(os.Stderr, "%s: %d trail row(s) kept by a legal hold of %v; run this again when it is lifted\n",
			t, res.Held.Rows, append(res.Held.By, res.Held.Unanswered...))
	}

	if reuse {
		if err := forget.FreeAlias(ctx, s.Ungated, t); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		fmt.Fprintf(os.Stderr, "%s: its alias is free now, ahead of its hold, and the trail says so\n", t)
	}

	return nil
}

// tenantOrHolder is what a REF names where it may be either: a tenant, by
// `@alias`, `@alias#tenant` or its identifier, or a person, by
// `@tenant/alias` or theirs -- answering the identifier and whether it is a
// tenant's.
//
// `@acme` alone is how a person called acme in any tenant has always been
// written here, and is now also how the tenant acme is. When both exist this
// refuses rather than choose: destroying the wrong one of those is not a
// mistake anybody can take back.
func tenantOrHolder(ctx context.Context, db *ent.Client, ref pdcmd.Ref) (pdid.Id, bool, error) {
	switch {
	case !ref.Id.IsZero():
		return ref.Id, ref.Id.Domain() == pd.TenantDomain, nil

	case ref.Tenant != "" || ref.Domain == pd.HolderDomain:
		who, err := whoIs(ctx, db, ref)

		return who, false, err

	case ref.Domain == pd.TenantDomain:
		v, err := db.Tenant.Query().Where(enttenant.AliasEQ(ref.Alias)).Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return pdid.Nil, false, fmt.Errorf("no tenant is called %q", ref.Alias)
			}

			return pdid.Nil, false, err
		}

		return pdid.Id(v.Id), true, nil

	case ref.Domain != pdid.Unknown:
		return pdid.Nil, false, &pdid.DomainError{Want: pd.TenantDomain, Got: ref.Domain}
	}

	t, err := db.Tenant.Query().Where(enttenant.AliasEQ(ref.Alias)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return pdid.Nil, false, err
	}
	people, err := db.Holder.Query().Where(entholder.AliasEQ(ref.Alias)).Exist(ctx)
	if err != nil {
		return pdid.Nil, false, err
	}

	switch {
	case t != nil && people:
		return pdid.Nil, false, fmt.Errorf("@%[1]s is a tenant, and somebody is called %[1]s too: write @%[1]s#tenant, or @<tenant>/%[1]s", ref.Alias)
	case t != nil:
		return pdid.Id(t.Id), true, nil
	default:
		who, err := whoIs(ctx, db, ref)

		return who, false, err
	}
}

// NewCmdRestore is `roster restore`: undo an erase while there is still one to
// undo.
//
// It is what makes `holder.forget_after` a **grace** rather than a delay. The
// window exists for a mistaken deletion, a compromised account and a billing
// dispute, and every one of those is a reason that needs the mistake to be
// reversible.
func NewCmdRestore(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "restore",
		Brief: "bring back somebody erased and not forgotten, or a tenant closed and not destroyed",

		Args: arg.Args{
			&pdcmd.ArgRef{Name: "REF", Brief: "who, as @tenant/alias or an identifier, or a tenant as @tenant"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ref, ok := arg.Get[pdcmd.Ref](cl, "REF")
			if !ok {
				return errors.New("REF: who to bring back")
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			who, isTenant, err := tenantOrHolder(ctx, s.Ent, ref)
			if err != nil {
				return err
			}
			if isTenant {
				// Through the server the wall was never installed on, which
				// is the verb an operator over the wire reaches too: the same
				// grace answers both, and the trail records it.
				if _, err := s.Ungated.Tenant().Restore(ctx, rstr.TenantRestoreRequest_builder{
					Ref: rstr.TenantRef_builder{Id: who.Bytes()}.Build(),
				}.Build()); err != nil {
					return err
				}

				fmt.Fprintf(os.Stderr, "%s is open again\n", who)

				return nil
			}
			cs, err := c.Audit.Contents()
			if err != nil {
				return fmt.Errorf("audit: %w", err)
			}
			if err := forget.Restore(ctx, s.Ent, who, cs); err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "%s is back\n", who)

			return nil
		}),
	}
}

func forgotten(ctx context.Context, db *ent.Client, who pdid.Id, p trail.Policy) error {
	res, err := forget.Forget(ctx, db, who, p)
	if err != nil {
		return fmt.Errorf("%s: %w", who, err)
	}

	fmt.Fprintf(os.Stderr, "%s forgotten: %s\n", who, res)
	if res.Held.Any() {
		// Not an error: what a hold keeps is not this command's to destroy.
		// It is the list of what to run this again for.
		fmt.Fprintf(os.Stderr, "%s: %d trail row(s) kept by a legal hold of %v; run this again when it is lifted\n",
			who, res.Held.Rows, append(res.Held.By, res.Held.Unanswered...))
	}

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
