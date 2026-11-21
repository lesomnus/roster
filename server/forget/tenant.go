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
	"github.com/protobuf-orm/ent/dialect/sql"

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
	"github.com/lesomnus/roster/internal/ent/predicate"
	"github.com/lesomnus/roster/internal/ent/role"
	"github.com/lesomnus/roster/internal/ent/site"
	"github.com/lesomnus/roster/internal/ent/sitemembership"
	"github.com/lesomnus/roster/internal/ent/team"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	"github.com/lesomnus/roster/internal/ent/tenant"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/expire"
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
// identifier and seventeen foreign keys reach a holder. A forgotten person is a
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
// restore made in between; and the hold is asked again of each person, in the
// transaction that forgets them.
// What a hold placed half way through stops is the rest of the people: those
// forgotten before it stay forgotten, and lifting it lets the sweep finish
// ([Unfinished] finds a destroyed tenant by what is left of it). Once they
// are all gone, what is left is a few statements about rows that name nobody,
// in one transaction; and if that one does not commit, the tenant is found
// the same way and the next pass makes it again.
//
// Each person goes on the stream through `q` as [Forget] puts them there: a
// closed tenant's people were heard shut out when it closed, and this is them
// heard erased.
func DestroyTenant(ctx context.Context, db *ent.Client, t pdid.Id, p trail.Policy, cs audit.Contents, q bare.Recorder) (TenantResult, error) {
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
		res, err := forget(ctx, db, pdid.Id(h), p, cs, q, func(_ *ent.Holder, in *ent.Tenant) error { return unheld(in) })
		if errors.Is(err, ErrHeld) {
			return out, err
		}
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
	// Under the tenant, said by the context: a line that says a row went has
	// no row to read a tenant from (`expire.Under`).
	if _, err := pd.RecordErased(expire.Under(ctx, t), tx.Client(), cs, DestroyedTenant, removed); err != nil {
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
	if err := unheld(v); err != nil {
		return err
	}
	if v.DateClosed == nil {
		return fmt.Errorf("@%s is open; `roster tenant close` it first, which is what shuts its people out", v.Alias)
	}

	return nil
}

// unheld is [ErrHeld] for a tenant an operator holds, saying since when and
// what for. It is what [DestroyTenant] asks again of each of a tenant's
// people, in the transaction that forgets them ([forget]): a hold placed
// since it began is found there, and not at a read a moment before it.
func unheld(v *ent.Tenant) error {
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
//
// A kind whose rows say which tenant they are of in a column of their own is
// written with `own`, naming the table and the column: that is what
// [Unfinished] finds a destruction that stopped by. The others say it through
// one of those -- a membership through its team, a binding through its role
// -- and are left only while that row is.
func tenanted() []subject {
	of := func(name string, ids func(context.Context, *ent.Client, uuid.UUID) ([]uuid.UUID, error), remove func(context.Context, *ent.Client, uuid.UUID) (int, error)) subject {
		return subject{name: name, ids: ids, remove: remove}
	}
	own := func(table, column, name string, ids func(context.Context, *ent.Client, uuid.UUID) ([]uuid.UUID, error), remove func(context.Context, *ent.Client, uuid.UUID) (int, error)) subject {
		return subject{name: name, ids: ids, remove: remove, table: table, tenant: column}
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
		own(team.Table, team.FieldTenantId, "team", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
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
		own(group.Table, group.FieldTenantId, "group", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Group.Query().Where(group.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Group.Delete().Where(group.TenantId(k)).Exec(ctx)
		}),
		of("site membership", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.SiteMembership.Query().Where(sitemembership.HasSiteWith(site.TenantId(k))).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.SiteMembership.Delete().Where(sitemembership.HasSiteWith(site.TenantId(k))).Exec(ctx)
		}),
		own(role.Table, role.FieldTenantId, "role", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Role.Query().Where(role.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Role.Delete().Where(role.TenantId(k)).Exec(ctx)
		}),
		own(site.Table, site.FieldTenantId, "site", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Site.Query().Where(site.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Site.Delete().Where(site.TenantId(k)).Exec(ctx)
		}),
		// The ways the tenant was reached from outside: who answered as which
		// app here, its directories, and its names. A hostname needs proof to
		// be taken again (#42), so it is freed at once and not held.
		own(nomination.Table, nomination.FieldTenantId, "nomination", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Nomination.Query().Where(nomination.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Nomination.Delete().Where(nomination.TenantId(k)).Exec(ctx)
		}),
		own(connection.Table, connection.FieldTenantId, "connection", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Connection.Query().Where(connection.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Connection.Delete().Where(connection.TenantId(k)).Exec(ctx)
		}),
		own(maildomain.Table, maildomain.FieldTenantId, "mail domain", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.MailDomain.Query().Where(maildomain.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.MailDomain.Delete().Where(maildomain.TenantId(k)).Exec(ctx)
		}),
		own(hostproof.Table, hostproof.FieldTenantId, "host proof", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.HostProof.Query().Where(hostproof.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.HostProof.Delete().Where(hostproof.TenantId(k)).Exec(ctx)
		}),
		own(host.Table, host.FieldTenantId, "host", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
			return db.Host.Query().Where(host.TenantId(k)).Ids(ctx)
		}, func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
			return db.Host.Delete().Where(host.TenantId(k)).Exec(ctx)
		}),
		// What its people consented to, which [Forget] leaves: a consent is kept
		// past its account as proof, for `consent.keep` ([SweepConsents]). Not
		// past its tenant -- a tenant destroyed is one whose people are all gone
		// and whose apps answer nobody, so there is nobody left to show it to.
		own(consent.Table, consent.FieldTenantId, "consent", func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
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
//
// # Not of a tenant an operator holds
//
// With [ErrHeld], whoever asks. An operator who asked by hand was refused
// already: `roster forget --reuse` destroys first, and [DestroyTenant] refuses
// a held tenant. The clock asked nothing, so under a hold placed on a
// destroyed tenant the name was given back by the sweep that an operator was
// refused giving back (#280). The question is here, where the name goes, so
// that the two cannot be asked different things again; and [DueAliases]
// leaves a held tenant out, so that a pass does not come here to be refused
// every day the hold lasts.
func FreeAlias(ctx context.Context, at app.Server, t pdid.Id) error {
	v, err := at.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Select: app.TenantSelect_builder{DateHeld: z.Ptr(true), Hold: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	if held := v.GetDateHeld(); held != nil {
		return fmt.Errorf("%w since %s: %s", ErrHeld, held.AsTime().Format(time.RFC3339), v.GetHold())
	}

	_, err = at.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
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

// Unfinished is every tenant stamped destroyed, and held by nobody, with
// something of it left: a destruction that stopped part way, whatever the
// window.
//
// # By what is left of it, and not by its people alone
//
// It found one by the people it still had. The people are forgotten first,
// each in a transaction of their own, and everything else is one transaction
// after the last of them -- so a destruction that stopped *there* had nobody
// left to be found by, and nothing finished it: the tenant stayed stamped,
// with its name, its roles and its hosts, and its hostname went on answering
// for it (#279). A database that is busy once is enough.
//
// So it is asked of everything [DestroyTenant] takes: somebody not yet
// forgotten, a row of a kind that goes with a tenant ([tenanted]), or what
// its own row says. Its labels and settings are not asked: they are blanked
// by the write that blanks the name, in the transaction that removes the
// rows, so they are left only where one of those is.
//
// One read, which a tenant that is finished is not in. A pass does not walk
// every tenant a deployment ever destroyed, which is [Due]'s reason for
// leaving out the forgotten.
//
// What is written into a tenant after it was destroyed is found by this too,
// and the next pass takes it: a destroyed tenant holds nothing.
func Unfinished(ctx context.Context, db *ent.Client) ([]pdid.Id, error) {
	vs, err := db.Tenant.Query().
		Where(tenant.DateDestroyedNotNil(), tenant.DateHeldIsNil(), left()).
		Ids(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]pdid.Id, len(vs))
	for i, v := range vs {
		out[i] = pdid.Id(v)
	}

	return out, nil
}

// left is a tenant something is left of, for [Unfinished].
//
// Subqueries on each table's own column and not edge predicates: a `Tenant`
// has no edge to what is in it.
func left() predicate.Tenant {
	return func(s *sql.Selector) {
		id := s.C(tenant.FieldId)
		has := func(table, column string, where ...func(*sql.SelectTable) *sql.Predicate) *sql.Predicate {
			t := sql.Table(table)
			q := sql.Select(t.C(column)).From(t).Where(sql.ColumnsEQ(t.C(column), id))
			for _, w := range where {
				q.Where(w(t))
			}

			return sql.Exists(q)
		}

		// What the last write of a destruction blanks.
		found := []*sql.Predicate{
			sql.NEQ(s.C(tenant.FieldName), ""),
			sql.NEQ(s.C(tenant.FieldDesc), ""),
		}

		// Somebody not yet forgotten: a forgotten row stays, with no alias.
		found = append(found, has(holder.Table, holder.FieldTenantId, func(t *sql.SelectTable) *sql.Predicate {
			return sql.NEQ(t.C(holder.FieldAlias), "")
		}))

		for _, of := range tenanted() {
			if of.table != "" {
				found = append(found, has(of.table, of.tenant))
			}
		}

		s.Where(sql.Or(found...))
	}
}

// DueAliases is every destroyed tenant whose alias has been held long enough,
// and that no operator holds: see [FreeAlias].
//
// One already freed is told by the exact name freeing writes ([Gone]), which
// is that tenant's alone. It was told by the prefix, and an alias is anybody's
// to choose: a tenant somebody named `gone-fishing` was never due, and kept
// the name for good (#279). The comparison is made here and not by the
// database, which cannot spell a row's identifier the way [Gone] does; so a
// pass reads the identifier and the alias of every tenant destroyed that long
// ago, and nothing else of them.
func DueAliases(ctx context.Context, db *ent.Client, before time.Time) ([]pdid.Id, error) {
	vs, err := db.Tenant.Query().
		Where(
			tenant.DateDestroyedNotNil(),
			tenant.DateDestroyedLT(before),
			tenant.DateHeldIsNil(),
		).
		Select(tenant.FieldAlias).
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
// see [FreeAlias]. `q` is what each person forgotten is put on the stream
// through; see [Forget].
func SweepTenants(db *ent.Client, at app.Server, within, hold time.Duration, p trail.Policy, cs audit.Contents, q bare.Recorder, every time.Duration) spin.Func {
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
			res, err := DestroyTenant(ctx, db, t, p, cs, q)
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
