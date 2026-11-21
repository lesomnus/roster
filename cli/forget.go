package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/grpcx"
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

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) (err error) {
			s, err := built(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// What forgetting logs is said, on stderr, and what it logged
			// that it could not do fails the command ([aloud], #280). The
			// archive's half of an erasure is paid after its transaction, and
			// a payment that fails is logged and left owed: with no logger
			// that was said to nobody, and the command answered exit 0 beside
			// an archive that still held what its rows said of the person.
			ctx, told := aloud(ctx)
			defer func() {
				if err == nil {
					err = told.failed("forgetting")
				}
			}()

			p, err := c.Holder.Trail(ctx, *c)
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
				return forgotten(ctx, s, who, p, cs)
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
// ran out (`holder.forget_after`), the consents kept past theirs
// (`consent.keep`), the closed tenants whose grace did
// (`tenant.restore_within`) and any whose destruction began and stopped, and
// the aliases whose hold is over (`tenant.alias_hold`, which has a default).
//
// And, first, what forgets already made still owe the trail ([owed]): a hold
// that lifted is something a clock was waiting for too.
//
// # It is what `serve`'s loops do, all of them
//
// It was less, in two ways (#280). It stopped at the first person or tenant
// that failed, where each loop logs the failure and carries on -- so one
// person who could not be forgotten kept every tenant and every alias after
// them from being looked at, for as long as they could not. And it destroyed
// no consents, which only `serve`'s loop did: a deployment that ran this from
// cron kept them past `consent.keep` for good. So each part is the function
// its loop calls (`forget.Pass`, `forget.PassConsents`), a failure is said
// where it happens and does not stop the rest, and what it answers at the end
// is who failed: somebody running it from cron hears of a failure by the exit,
// which is the only thing cron reads.
func due(ctx context.Context, s *cmd.Server, c *cmd.Config, p trail.Policy, dry bool) error {
	now := time.Now()

	cs, err := c.Audit.Contents()
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	var failed []string
	fail := func(what string, err error) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		failed = append(failed, what)
	}

	if err := owed(ctx, s, p, dry); err != nil {
		fail("what the trail is owed was not all paid", err)
	}

	if after := c.Holder.ForgetAfter; after > 0 {
		before := now.Add(-after)

		var vs []forget.Lapsed
		if dry {
			due, err := forget.Due(ctx, s.Ent, before)
			if err != nil {
				fail("who is due was not read", err)
			}
			for _, who := range due {
				fmt.Fprintf(os.Stdout, "%s\n", who)
				vs = append(vs, forget.Lapsed{Who: who})
			}
		} else if vs, err = forget.Pass(ctx, s.Ent, before, p, cs, s.Queue); err != nil {
			// The pass the loop makes (`forget.Pass`): each person asked
			// again, in the transaction that destroys them, whether they are
			// still due.
			fail("who is due was not read", err)
		}
		if len(vs) == 0 && len(failed) == 0 {
			fmt.Fprintf(os.Stderr, "nobody has been erased for longer than %s\n", after)
		}
		for _, v := range vs {
			switch {
			case dry:

			case errors.Is(v.Err, forget.ErrNotDue):
				// Restored, or their tenant held, while this ran. Left, and
				// said: they were due when it began.
				fmt.Fprintf(os.Stderr, "%s is left as they are: %v\n", v.Who, v.Err)

			case v.Err != nil:
				fail(fmt.Sprintf("%s was not forgotten", v.Who), v.Err)

			default:
				reported(v.Who, v.Result)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "nobody is destroyed on a clock: `holder.forget_after` names no window\n")
	}

	// What they consented to, kept past them as proof for `consent.keep` and
	// then destroyed. Nothing where no window is named: kept for good.
	if keep := c.Consent.Keep; keep > 0 {
		before := now.Add(-keep)

		if dry {
			ids, err := forget.DueConsents(ctx, s.Ent, before)
			if err != nil {
				fail("which consents are due was not read", err)
			}
			if len(ids) > 0 {
				fmt.Fprintf(os.Stderr, "%d consent(s) kept for longer than %s past their account\n", len(ids), keep)
			}
		} else {
			n, err := forget.PassConsents(ctx, s.Ent, before, cs)
			if n > 0 {
				fmt.Fprintf(os.Stderr, "%d consent(s) destroyed, kept for longer than %s past their account\n", n, keep)
			}
			if err != nil {
				fail("the consents kept past their account were not all destroyed", err)
			}
		}
	}

	var ts []pdid.Id
	if within := c.Tenant.RestoreWithin; within > 0 {
		ts, err = forget.DueTenants(ctx, s.Ent, now.Add(-within))
	} else {
		ts, err = forget.Unfinished(ctx, s.Ent)
	}
	if err != nil {
		fail("which tenants are due was not read", err)
	}
	for _, t := range ts {
		if dry {
			fmt.Fprintf(os.Stdout, "%s\n", t)

			continue
		}
		if err := destroyed(ctx, s, t, p, cs, false); err != nil {
			fail(fmt.Sprintf("%s was not destroyed", t), err)
		}
	}

	free, err := forget.DueAliases(ctx, s.Ent, now.Add(-c.Tenant.Hold()))
	if err != nil {
		fail("which aliases are free was not read", err)
	}
	for _, t := range free {
		if dry {
			fmt.Fprintf(os.Stdout, "%s: alias\n", t)

			continue
		}
		if err := forget.FreeAlias(grpcx.AsMethod(ctx, aliasFreed), s.Ungated, t); err != nil {
			fail(fmt.Sprintf("%s: its alias was not freed", t), err)

			continue
		}
		fmt.Fprintf(os.Stderr, "%s: its alias is free\n", t)
	}

	if len(failed) > 0 {
		return fmt.Errorf("the rest was done, and %d thing(s) failed: %s. Each is said above with why, and is still due: the next run takes it",
			len(failed), strings.Join(failed, "; "))
	}

	return nil
}

// owed pays what the trail owes of forgets already made, and says what it
// paid; with `dry` it says what is owed and pays nothing.
//
// A forget pays its own debt as soon as it has committed, so what is owed
// when this runs is what that could not pay: above all the lines a legal hold
// kept, which wait for the hold to lift. `serve` pays them on its clock --
// the trail's pass where a retention window is named, and a loop that does
// only this where none is (`forget.SweepOwed`) -- and this is the same
// payment for whoever runs the command instead, or does not want to wait a
// day for it. It printed nothing about them and paid nothing, and nothing
// else an operator could run paid either (#280).
//
// Before the people, as a pass of the trail pays before it moves anything.
// Somebody forgotten below pays their own; what a hold keeps of them is said
// with them.
func owed(ctx context.Context, s *cmd.Server, p trail.Policy, dry bool) error {
	owing, err := p.Owing(ctx, pd.TrailStore(s.Ent))
	if err != nil {
		return err
	}
	if owing.Objects == 0 {
		return nil
	}
	if dry {
		fmt.Fprintf(os.Stderr, "%d erasure(s) owed to the trail by forgets already made, the oldest since %s; `roster trail owed` says what a hold keeps of them\n",
			owing.Objects, owing.Oldest.UTC().Format(time.RFC3339))

		return nil
	}

	paid, err := forget.PayOwed(ctx, s.Ent, p)
	fmt.Fprintf(os.Stderr, "%d erasure(s) owed to the trail by forgets already made were paid: %d trail row(s), %d archived\n",
		paid.Objects, paid.Rows, paid.Archived)
	if paid.Owed > 0 {
		fmt.Fprintf(os.Stderr, "%d more are still owed: %d trail row(s) kept by a legal hold of %v, until it lifts\n",
			paid.Owed, paid.Held.Rows, append(paid.Held.By, paid.Held.Unanswered...))
	}
	if err != nil {
		return fmt.Errorf("the erasures owed to the trail: %w", err)
	}

	return nil
}

// destroyed is one tenant destroyed, and said: what went, what a hold
// elsewhere kept, and its alias -- freed now with `reuse`, which the trail
// records, and otherwise held.
func destroyed(ctx context.Context, s *cmd.Server, t pdid.Id, p trail.Policy, cs audit.Contents, reuse bool) error {
	res, err := forget.DestroyTenant(ctx, s.Ent, t, p, cs, s.Queue)
	if err != nil {
		return fmt.Errorf("%s: %w", t, err)
	}

	fmt.Fprintf(os.Stderr, "%s destroyed: %s\n", t, res)
	if res.Held.Any() {
		// What a hold keeps stays owed (payday#115), as for a person.
		fmt.Fprintf(os.Stderr, "%s: %d trail row(s) kept by a legal hold of %v; they are erased at the first pass after it is lifted, `serve`'s or `roster forget`\n",
			t, res.Held.Rows, append(res.Held.By, res.Held.Unanswered...))
	}

	if reuse {
		if err := forget.FreeAlias(grpcx.AsMethod(ctx, aliasFreed), s.Ungated, t); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		fmt.Fprintf(os.Stderr, "%s: its alias is free now, ahead of its hold, and the trail says so\n", t)
	}

	return nil
}

// aliasFreed is what a tenant's alias freed is called on the trail. It is a
// patch of the tenant, and was filed under the generated method that wrote
// it, `/roster.TenantService/Patch`, as any change of a tenant is (#284). It
// has no RPC, so it is named as `roster.tenant.destroy` beside it is.
const aliasFreed = "roster.alias.free"

// tenantOrHolder is what a REF names where it may be either: a tenant, by
// `@alias`, `@alias#tenant` or its identifier, or a person, by
// `@tenant/alias` or theirs -- answering the identifier and whether it is a
// tenant's.
//
// `@acme` alone is how a person called acme in any tenant has always been
// written here, and is now also how the tenant acme is. When both exist this
// refuses rather than choose: destroying the wrong one of those is not a
// mistake anybody can take back.
//
// An identifier is looked up too, erased or not, and one that names nothing
// here is refused in a sentence: `forget --dry-run` printed it back and ended
// with status 0 whoever had it, and `forget` and `restore` said `ent: holder
// not found` -- which is all they said of an operator's identifier, whose row
// is the control plane's and not this database's (#284).
func tenantOrHolder(ctx context.Context, db *ent.Client, ref pdcmd.Ref) (pdid.Id, bool, error) {
	switch {
	case !ref.Id.IsZero():
		isTenant := ref.Id.Domain() == pd.TenantDomain

		var there bool
		var err error
		if isTenant {
			there, err = db.Tenant.Query().Where(enttenant.IdEQ(ref.Id.Uuid())).Exist(ctx)
		} else {
			there, err = db.Holder.Query().Where(entholder.IdEQ(ref.Id.Uuid())).Exist(ctx)
		}
		switch {
		case err != nil:
			return pdid.Nil, false, err
		case !there && isTenant:
			return pdid.Nil, false, fmt.Errorf("%s: no tenant of the customers' database has that identifier", ref.Id)
		case !there:
			return pdid.Nil, false, fmt.Errorf("%s: nobody of the customers' database has that identifier; "+
				"an operator is the control plane's, which this does not read", ref.Id)
		}

		return ref.Id, isTenant, nil

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
//
// What comes back is the account and not how they were signed in, and a
// restore of somebody whose name was taken while they were gone says who has
// it: both are `forget.Restore`'s, and said there.
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

			s, err := built(ctx, *c)
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
				//
				// As the verb it is. A write is filed on the trail under the
				// method gRPC says is being served, and a call made in
				// process is served as none, so this one was filed under the
				// generated method that wrote it: `Apply`, where the close
				// before it says `Close` (#279). The context says which
				// method this is, as a call over the wire would have.
				as := grpcx.AsMethod(ctx, rstr.TenantService_Restore_FullMethodName)
				if _, err := s.Ungated.Tenant().Restore(as, rstr.TenantRestoreRequest_builder{
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
			// Their standing written back, which is a call at the shell by
			// whoever `--as` named, on the access record ([keptBy], #284),
			// as `holder erase` of them was. It has no RPC, and is filed
			// under the name the trail gives it.
			by, err := namedAt(ctx, c, s)
			if err != nil {
				return err
			}
			if err := keptBy(ctx, s, by, forget.RestoredHolder, func(ctx context.Context) error {
				return restoredOne(ctx, s, who, cs)
			}); err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "%s is back\n", who)

			return nil
		}),
	}
}

// restoredOne brings back one person erased, and says the call wrote their
// row: `forget.Restore` writes past every server, so the recorder a server's
// write tells is not told, and a call at the shell that made it would be no
// row of the access record at all. Filed under their tenant, as a write of
// their row through a server is.
func restoredOne(ctx context.Context, s *cmd.Server, who pdid.Id, cs audit.Contents) error {
	v, err := s.Ent.Holder.Get(ctx, who.Uuid())
	if err != nil {
		return err
	}
	if err := forget.Restore(ctx, s.Ent, who, cs, s.Queue); err != nil {
		return err
	}
	if call, ok := access.Of(ctx); ok {
		call.Wrote(access.Subject{Id: who, Tenant: pdid.Id(v.TenantId)})
	}

	return nil
}

func forgotten(ctx context.Context, s *cmd.Server, who pdid.Id, p trail.Policy, cs audit.Contents) error {
	res, err := forget.Forget(ctx, s.Ent, who, p, cs, s.Queue)
	if err != nil {
		// Once: a forget that lost a race to a restore says who already.
		if strings.HasPrefix(err.Error(), who.String()) {
			return err
		}

		return fmt.Errorf("%s: %w", who, err)
	}

	reported(who, res)

	return nil
}

// reported is one person forgotten, as the command says it.
//
// # And under a hold, what the hold is
//
// A hold on a tenant does not stop somebody of it being forgotten by name:
// their rows are destroyed like anybody's, and what the hold keeps is the
// trail's lines about them (`server/forget/hold.go`). The command that did
// that printed a count of trail rows, under a help text that said nothing of
// a held tenant is destroyed (#280). It says what happened, each time: whose
// hold and what for, that the rows went all the same, and which lines are
// kept and until when. No flag asks for it and none turns the forget into a
// refusal: the request has a clock on it that a hold does not stop.
func reported(who pdid.Id, res forget.Result) {
	fmt.Fprintf(os.Stderr, "%s forgotten: %s\n", who, res)
	if u := res.Under; u != nil {
		fmt.Fprintf(os.Stderr, "%s: their tenant @%s is held since %s: %s. A hold does not stop somebody being forgotten by name: "+
			"%d row(s) of theirs are destroyed and their own is blank. What the hold keeps is the trail's lines about them and about what was theirs, "+
			"%d of them, as they were until it is lifted; the first pass after that erases them, `serve`'s or `roster forget`\n",
			who, u.Alias, u.Since.UTC().Format(time.RFC3339), u.Why, res.Rows, res.Held.Rows)

		return
	}
	if res.Held.Any() {
		// Not an error: what a hold keeps is not this command's to destroy.
		// It stays owed (payday#115), and is paid at the first pass after
		// the hold is lifted: `serve`'s, in every deployment, or this
		// command's with no name ([owed]).
		fmt.Fprintf(os.Stderr, "%s: %d trail row(s) kept by a legal hold of %v; they are erased at the first pass after it is lifted, `serve`'s or `roster forget`\n",
			who, res.Held.Rows, append(res.Held.By, res.Held.Unanswered...))
	}
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

	vs, err := q.WithTenant().Limit(many + 1).All(ctx)
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
		//
		// Told about with who they are. It said to use an identifier and gave
		// none, so somebody restoring a leaver whose name had been taken since
		// -- which is one tenant, named already -- was sent to find an
		// identifier nothing here would show them (#279).
		said := make([]string, 0, many)
		for _, v := range vs[:min(len(vs), many)] {
			one := pdid.Id(v.Id).String()
			if v.Edges.Tenant != nil {
				one += " of @" + v.Edges.Tenant.Alias
			}
			if v.DateErased != nil {
				one += ", erased " + v.DateErased.UTC().Format(time.RFC3339)
			}
			said = append(said, one)
		}
		if len(vs) > many {
			said = append(said, "and more")
		}

		return pdid.Nil, fmt.Errorf("more than one holder is called %q: %s. Name the tenant where that tells them apart, or use an identifier",
			ref.Alias, strings.Join(said, "; "))
	}
}

// many is how many holders of one alias [whoIs] names before it says there
// are more.
const many = 8
