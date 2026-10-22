package forget

import (
	"context"
	"time"

	"uuid"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/consent"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/tenant"
	"github.com/lesomnus/roster/server/pd"
)

// What a consent is, after its account.
//
// It is the one kind of row of somebody's that [Forget] leaves: a consent is
// what shows afterwards that they agreed, so it is kept past them -- the
// holder stays, blank, for it to point at -- for `consent.keep` after the
// erase, and then destroyed here. A restore inside that window makes them
// somebody again, and their consents are theirs as they were; a tenant an
// operator holds keeps them as it keeps everything else; a tenant destroyed
// takes them with it ([DestroyTenant]).

// DestroyedConsent is the act a consent destroyed is on the trail as.
const DestroyedConsent = "roster.consent.destroy"

// DueConsents is every consent whose account was erased before `before`, of a
// tenant nobody holds.
func DueConsents(ctx context.Context, db *ent.Client, before time.Time) ([]uuid.UUID, error) {
	return db.Consent.Query().
		Where(consent.HasHolderWith(
			holder.DateErasedNotNil(),
			holder.DateErasedLT(before),
			holder.HasTenantWith(tenant.DateHeldIsNil()),
		)).
		Ids(ctx)
}

// DestroyConsents removes consents, and puts each on the trail as the act it is
// in the same transaction: written past the servers, which refuse it, so a
// check of the rows does not find them things the trail says are rows
// (`pd.RecordErased`, payday#115). What that writes is which rows and when.
func DestroyConsents(ctx context.Context, db *ent.Client, ids []uuid.UUID, cs audit.Contents) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	n, err := tx.Consent.Delete().Where(consent.IdIn(ids...)).Exec(ctx)
	if err != nil {
		return 0, err
	}
	gone := make([]pdid.Id, len(ids))
	for i, v := range ids {
		gone[i] = pdid.Id(v)
	}
	if _, err := pd.RecordErased(ctx, tx.Client(), cs, DestroyedConsent, gone); err != nil {
		return 0, err
	}

	return n, tx.Commit()
}

// consentsAtOnce is how many consents one transaction destroys, so that a
// first pass over years of departures is many short transactions and not one
// long one.
const consentsAtOnce = 500

// SweepConsents destroys, on a clock, every consent kept `keep` past its
// account's erase. A pass that fails says so and the next one carries on: what
// it reached is gone, and what it did not is still due.
func SweepConsents(db *ent.Client, keep time.Duration, cs audit.Contents, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		before := time.Now().Add(-keep)

		ids, err := DueConsents(ctx, db, before)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "consent: which are due", "err", err, "before", before)

			return nil
		}

		destroyed := 0
		for len(ids) > 0 {
			k := min(len(ids), consentsAtOnce)
			n, err := DestroyConsents(ctx, db, ids[:k], cs)
			if err != nil {
				log.From(ctx).WarnContext(ctx, "consent: destroy", "err", err, "destroyed", destroyed)

				return nil
			}
			destroyed += n
			ids = ids[k:]
		}
		if destroyed > 0 {
			log.From(ctx).InfoContext(ctx, "consent: destroyed", "rows", destroyed, "before", before)
		}

		return nil
	})
}
