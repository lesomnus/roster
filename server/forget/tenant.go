package forget

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/connection"
	"github.com/lesomnus/roster/internal/ent/consent"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/groupexposure"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/host"
	"github.com/lesomnus/roster/internal/ent/hostproof"
	"github.com/lesomnus/roster/internal/ent/maildomain"
	"github.com/lesomnus/roster/internal/ent/nomination"
	"github.com/lesomnus/roster/internal/ent/role"
	"github.com/lesomnus/roster/internal/ent/site"
	"github.com/lesomnus/roster/internal/ent/sitemembership"
	"github.com/lesomnus/roster/internal/ent/team"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	"github.com/lesomnus/roster/internal/ent/tenant"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// A tenant leaving, which is a person leaving one level up (#98).
//
// `TenantService.Close` shuts everybody out and destroys nothing, and
// `Restore` undoes it while `tenant.restore_within` lasts -- both verbs on the
// entity, in `server/core`. What is here is what comes after, and it is here
// for this package's reason: it destroys, so it writes through ent past every
// server, and it is reached from a shell and a sweep and never over the wire.
//
//	DestroyTenant  every person forgotten, every other row of the tenant gone,
//	               the tenant row blank -- unless an operator holds it
//	FreeAlias      the name, once `tenant.alias_hold` after that has passed
//	Hold, Lift     the operator's hold, which also answers to the trail
//
// # What stays
//
// The tenant row, and the blank `Holder` rows, because the trail names them by
// identifier and twelve foreign keys reach a holder. A forgotten person is a
// pseudonym reaching nothing; a destroyed tenant is the same, and for a while
// it keeps its alias so that nobody else can be that name to its former people
// (`Tenant.date_destroyed`).
//
// And the trail, which keeps its events and follows its own windows: what a
// destroyed tenant's people did is erased of its contents as each person is
// forgotten, and the rest ages out like everybody's. A legal claim on it is a
// hold, and a hold stops all of this.

// ErrHeld is a tenant an operator holds, which nothing destroys until the hold
// is lifted.
var ErrHeld = errors.New("held")

// TenantResult is what destroying one tenant did.
type TenantResult struct {
	// People is how many of its people were forgotten now.
	People int

	// Rows is how many of its other rows were removed.
	Rows int

	// Held is the trail rows about its people a legal hold elsewhere kept.
	Held trail.Held

	// Owed is what of its people the archive's half is still owed for: see
	// [Result.Owed].
	Owed int
}

func (r TenantResult) String() string {
	v := fmt.Sprintf("%d person(s) forgotten, %d other row(s) removed", r.People, r.Rows)
	if r.Held.Any() {
		v += fmt.Sprintf(", %d trail row(s) kept by a legal hold", r.Held.Rows)
	}
	if r.Owed > 0 {
		v += fmt.Sprintf(", %d owed to the archive", r.Owed)
	}

	return v
}

// DestroyTenant takes a closed tenant out.
//
// It refuses a tenant that is open -- closing is the step that shuts people
// out, and destroying a tenant somebody is signed in to would be taking it
// from under them -- and one an operator holds, with [ErrHeld].
//
// # In an order that leaves less, and that a restore cannot get between
//
// The first write is `date_destroyed`, so that from then `Restore` refuses: a
// tenant half destroyed is not one that can come back as it was. Then its
// people, each forgotten as [Forget] forgets anybody; then its other rows,
// children before what they hang off; then the row itself, blank. Every step
// is idempotent and none is undone by an interruption, so running this again
// finishes it -- which is what the sweep does with a tenant it finds stamped
// and not finished.
//
// # And a hold that arrives while it runs stops it
//
// The checks above are a read, and this writes for as long as a tenant has
// people -- one person at a time, through the trail and the archive. So the
// stamp is written on the condition the read found -- held by nobody, closed,
// not yet destroyed -- and is no write at all if a hold was placed or a
// restore made in between; and the hold is asked again before each person.
// What a hold placed half way through stops is the rest of the people: those
// forgotten before it stay forgotten, and lifting it lets the sweep finish
// ([Unfinished] finds a destroyed tenant by the people it still has). Once
// they are all gone, what is left is a few statements about rows that name
// nobody, and they finish with the run.
func DestroyTenant(ctx context.Context, db *ent.Client, t pdid.Id, p trail.Policy, cs audit.Contents) (TenantResult, error) {
	var out TenantResult

	v, err := db.Tenant.Get(ctx, t.Uuid())
	if err != nil {
		return out, err
	}
	if err := destroyable(v); err != nil {
		return out, err
	}

	if v.DateDestroyed == nil {
		n, err := db.Tenant.Update().
			Where(
				tenant.IdEQ(t.Uuid()),
				tenant.DateHeldIsNil(),
				tenant.DateClosedNotNil(),
				tenant.DateDestroyedIsNil(),
			).
			SetDateDestroyed(time.Now()).
			SetDateUpdated(time.Now()).
			Save(ctx)
		if err != nil {
			return out, err
		}
		if n == 0 {
			// Something moved since the read. Whatever it was is the answer:
			// held, restored, or destroyed by another run, which this one then
			// finishes beside.
			if v, err = db.Tenant.Get(ctx, t.Uuid()); err != nil {
				return out, err
			}
			if err := destroyable(v); err != nil {
				return out, err
			}
		}
	}

	// Its people, including those erased and not yet forgotten. Those
	// forgotten already have no alias, which is the row's own answer to
	// whether there is anything left of them.
	who, err := db.Holder.Query().
		Where(holder.TenantId(t.Uuid()), holder.AliasNEQ("")).
		Ids(ctx)
	if err != nil {
		return out, err
	}
	for _, h := range who {
		if err := notHeld(ctx, db, t); err != nil {
			return out, err
		}
		res, err := Forget(ctx, db, pdid.Id(h), p, cs)
		if err != nil {
			return out, fmt.Errorf("holder %s: %w", pdid.Id(h), err)
		}

		out.People++
		out.Owed += res.Owed
		out.Held.Rows += res.Held.Rows
		out.Held.By = append(out.Held.By, res.Held.By...)
		out.Held.Unanswered = append(out.Held.Unanswered, res.Held.Unanswered...)
	}

	// Its other rows, and then the row itself, in one transaction that puts
	// each on the trail as the act it is (payday#108, #115): the rows it
	// removed with a digest that says they went, so a check of the rows does
	// not find them things the trail says are rows, and the tenant's row as a
	// destruction leaves it.
	tx, err := db.Tx(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	removed := []pdid.Id{}
	for _, of := range tenanted() {
		ids, err := of.ids(ctx, tx.Client(), t.Uuid())
		if err != nil {
			return out, fmt.Errorf("%s: %w", of.name, err)
		}
		for _, id := range ids {
			removed = append(removed, pdid.Id(id))
		}

		n, err := of.remove(ctx, tx.Client(), t.Uuid())
		if err != nil {
			return out, fmt.Errorf("%s: %w", of.name, err)
		}

		out.Rows += n
	}
	if _, err := pd.RecordErased(ctx, tx.Client(), cs, DestroyedTenant, removed); err != nil {
		return out, err
	}

	// The row stays and stops saying anything about whose it was -- except
	// its alias, which is held; see `Tenant.date_destroyed`. Written past the
	// servers, so put on the trail as the act it is: a check of the rows finds
	// the row a destruction left as it is, and the trail says that the
	// deployment destroyed it, and when.
	if err := tx.Tenant.UpdateOneId(t.Uuid()).
		SetName("").
		SetDesc("").
		SetLabels(map[string]string{}).
		SetConfig(&app.TenantConfig{}).
		SetDateUpdated(time.Now()).
		Exec(ctx); err != nil {
		return out, err
	}
	if _, err := pd.Record(ctx, tx.Client(), cs, DestroyedTenant, []pdid.Id{t}); err != nil {
		return out, err
	}

	return out, tx.Commit()
}

// destroyable is whether a tenant as it was read may be destroyed: closed, and
// held by nobody.
func destroyable(v *ent.Tenant) error {
	switch {
	case v.DateHeld != nil:
		return fmt.Errorf("%w since %s: %s", ErrHeld, v.DateHeld.Format(time.RFC3339), v.Hold)
	case v.DateClosed == nil:
		return fmt.Errorf("@%s is open; `roster tenant close` it first, which is what shuts its people out", v.Alias)
	}

	return nil
}

// notHeld asks again, between the steps of [DestroyTenant], whether an operator
// has placed a hold since it began.
func notHeld(ctx context.Context, db *ent.Client, t pdid.Id) error {
	v, err := db.Tenant.Get(ctx, t.Uuid())
	if err != nil {
		return err
	}
	if v.DateHeld != nil {
		return fmt.Errorf("%w since %s: %s", ErrHeld, v.DateHeld.Format(time.RFC3339), v.Hold)
	}

	return nil
}

// The acts this package writes past the servers, as the trail names them.
const (
	DestroyedTenant = "roster.tenant.destroy"
	ForgottenHolder = "roster.holder.forget"
	RestoredHolder  = "roster.holder.restore"
)

// tenanted is a tenant's rows besides its people, children first: each `remove`
// takes what hangs off the next one's rows before that one goes, since the
// foreign keys between them are `NO ACTION`.
//
// Written out rather than read off the schema, for [subjects]' reason: which
// rows are a tenant's is not something a foreign key says. What keeps it whole
// is `TestEveryTenantedTableGoesWithItsTenant`, which holds it to the schema:
// a table added with a key to a tenant, and not added here, fails there.
func tenanted() []subject {
	of := func(name string, ids func(context.Context, *ent.Client, uuid.UUID) ([]uuid.UUID, error), remove func(context.Context, *ent.Client, uuid.UUID) (int, error)) subject {
		return subject{name: name, ids: ids, remove: remove}
	}

	return []subject{
		// What a role, a group or a site reaches. A person's own bindings went
		// with them; these are the ones written to a group or held in a site.
		of("binding", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Binding.Query().Where(binding.Or(
				binding.HasRoleWith(role.TenantId(k)),
				binding.HasGroupWith(group.TenantId(k)),
				binding.HasSiteWith(site.TenantId(k)),
			)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Binding.Delete().Where(binding.Or(
				binding.HasRoleWith(role.TenantId(k)),
				binding.HasGroupWith(group.TenantId(k)),
				binding.HasSiteWith(site.TenantId(k)),
			)).Exec(ctx)
		}),
		of("team membership", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.TeamMembership.Query().Where(teammembership.HasTeamWith(team.TenantId(k))).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.TeamMembership.Delete().Where(teammembership.HasTeamWith(team.TenantId(k))).Exec(ctx)
		}),
		of("team", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Team.Query().Where(team.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Team.Delete().Where(team.TenantId(k)).Exec(ctx)
		}),
		of("group membership", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.GroupMembership.Query().Where(groupmembership.HasGroupWith(group.TenantId(k))).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.GroupMembership.Delete().Where(groupmembership.HasGroupWith(group.TenantId(k))).Exec(ctx)
		}),
		of("group exposure", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.GroupExposure.Query().Where(groupexposure.HasGroupWith(group.TenantId(k))).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.GroupExposure.Delete().Where(groupexposure.HasGroupWith(group.TenantId(k))).Exec(ctx)
		}),
		of("group", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Group.Query().Where(group.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Group.Delete().Where(group.TenantId(k)).Exec(ctx)
		}),
		of("site membership", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.SiteMembership.Query().Where(sitemembership.HasSiteWith(site.TenantId(k))).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.SiteMembership.Delete().Where(sitemembership.HasSiteWith(site.TenantId(k))).Exec(ctx)
		}),
		of("role", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Role.Query().Where(role.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Role.Delete().Where(role.TenantId(k)).Exec(ctx)
		}),
		of("site", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Site.Query().Where(site.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Site.Delete().Where(site.TenantId(k)).Exec(ctx)
		}),
		// The ways the tenant was reached from outside: who answered as which
		// app here, its directories, and its names. A hostname needs proof to
		// be taken again (#42), so it is freed at once and not held.
		of("nomination", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Nomination.Query().Where(nomination.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Nomination.Delete().Where(nomination.TenantId(k)).Exec(ctx)
		}),
		of("connection", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Connection.Query().Where(connection.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Connection.Delete().Where(connection.TenantId(k)).Exec(ctx)
		}),
		of("mail domain", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.MailDomain.Query().Where(maildomain.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.MailDomain.Delete().Where(maildomain.TenantId(k)).Exec(ctx)
		}),
		of("host proof", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.HostProof.Query().Where(hostproof.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.HostProof.Delete().Where(hostproof.TenantId(k)).Exec(ctx)
		}),
		of("host", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Host.Query().Where(host.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Host.Delete().Where(host.TenantId(k)).Exec(ctx)
		}),
		// What its people consented to, which [Forget] leaves: a consent is kept
		// past its account as proof, for `consent.keep` ([SweepConsents]). Not
		// past its tenant -- a tenant destroyed is one whose people are all gone
		// and whose apps answer nobody, so there is nobody left to show it to.
		of("consent", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Consent.Query().Where(consent.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Consent.Delete().Where(consent.TenantId(k)).Exec(ctx)
		}),
	}
}

// Gone is what a destroyed tenant's alias becomes once its hold is over:
// a name that is a valid alias -- so nothing that reads one has a special case
// -- that no person would choose, and that is this tenant's alone.
func Gone(t pdid.Id) string { return "gone-" + hex.EncodeToString(t.Bytes()) }

// FreeAlias gives a destroyed tenant's alias back, now. Through `at` -- the
// server the wall was never installed on -- rather than ent, so that the trail
// records it: the sweep doing it when the hold is over, and an operator doing
// it early on purpose (`roster forget --reuse`), are both acts somebody may
// later need to have been told about.
func FreeAlias(ctx context.Context, at app.Server, t pdid.Id) error {
	_, err := at.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Alias:            z.Ptr(Gone(t)),
		DateUpdatedForce: z.Ptr(true),
	}.Build())

	return err
}

// DueTenants is every closed tenant whose grace has run out and that nobody
// holds, and every one whose destruction began and did not finish -- which
// [DestroyTenant] finishes.
func DueTenants(ctx context.Context, db *ent.Client, before time.Time) ([]pdid.Id, error) {
	due, err := db.Tenant.Query().
		Where(
			tenant.DateClosedNotNil(),
			tenant.DateClosedLT(before),
			tenant.DateDestroyedIsNil(),
			tenant.DateHeldIsNil(),
		).
		Ids(ctx)
	if err != nil {
		return nil, err
	}

	unfinished, err := Unfinished(ctx, db)
	if err != nil {
		return nil, err
	}

	out := make([]pdid.Id, 0, len(due)+len(unfinished))
	for _, v := range due {
		out = append(out, pdid.Id(v))
	}

	return append(out, unfinished...), nil
}

// Unfinished is every tenant stamped destroyed with somebody of it not yet
// forgotten: a destruction that stopped part way, whatever the window.
func Unfinished(ctx context.Context, db *ent.Client) ([]pdid.Id, error) {
	vs, err := db.Holder.Query().
		Where(
			holder.AliasNEQ(""),
			holder.HasTenantWith(tenant.DateDestroyedNotNil(), tenant.DateHeldIsNil()),
		).
		Select(holder.FieldTenantId).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := []pdid.Id{}
	seen := map[uuid.UUID]bool{}
	for _, v := range vs {
		if seen[v.TenantId] {
			continue
		}
		seen[v.TenantId] = true
		out = append(out, pdid.Id(v.TenantId))
	}

	return out, nil
}

// DueAliases is every destroyed tenant whose alias has been held long enough.
func DueAliases(ctx context.Context, db *ent.Client, before time.Time) ([]pdid.Id, error) {
	vs, err := db.Tenant.Query().
		Where(
			tenant.DateDestroyedNotNil(),
			tenant.DateDestroyedLT(before),
			tenant.Not(tenant.AliasHasPrefix("gone-")),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]pdid.Id, 0, len(vs))
	for _, v := range vs {
		if id := pdid.Id(v.Id); v.Alias != Gone(id) {
			out = append(out, id)
		}
	}

	return out, nil
}

// SweepTenants is what the clocks of a tenant leaving do on their own: destroy
// the closed tenants whose grace has run out -- when the deployment named one
// -- and free the aliases whose hold is over. Daily, for [Sweep]'s reason.
//
// `at` is the server the wall was never installed on, for what is recorded;
// see [FreeAlias].
func SweepTenants(db *ent.Client, at app.Server, within, hold time.Duration, p trail.Policy, cs audit.Contents, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		now := time.Now()

		var due []pdid.Id
		var err error
		if within > 0 {
			due, err = DueTenants(ctx, db, now.Add(-within))
		} else {
			// No window: destroying is an operator's act, and only a
			// destruction they began and that stopped is finished here.
			due, err = Unfinished(ctx, db)
		}
		if err != nil {
			log.From(ctx).WarnContext(ctx, "forget: which tenants are due", "err", err)
		}
		for _, t := range due {
			res, err := DestroyTenant(ctx, db, t, p, cs)
			if err != nil {
				log.From(ctx).WarnContext(ctx, "forget: a tenant", "tenant", t.String(), "err", err)

				continue
			}

			log.From(ctx).InfoContext(ctx, "forget: a tenant",
				"tenant", t.String(), "people", res.People, "rows", res.Rows, "held", res.Held.Rows, "owed", res.Owed)
		}

		free, err := DueAliases(ctx, db, now.Add(-hold))
		if err != nil {
			log.From(ctx).WarnContext(ctx, "forget: which aliases are free", "err", err)

			return nil
		}
		for _, t := range free {
			if err := FreeAlias(ctx, at, t); err != nil {
				log.From(ctx).WarnContext(ctx, "forget: an alias", "tenant", t.String(), "err", err)

				continue
			}

			log.From(ctx).InfoContext(ctx, "forget: an alias held after its tenant is free", "tenant", t.String())
		}

		return nil
	})
}
