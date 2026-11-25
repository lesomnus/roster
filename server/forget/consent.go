package forget

import (
	"context"
	"fmt"
	"time"

	"uuid"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/consent"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/predicate"
	"github.com/lesomnus/roster/internal/ent/tenant"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/expire"
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
	return db.Consent.Query().Where(dueConsent(before)).Ids(ctx)
}

// dueConsent is what makes a consent due: its account erased before `before`,
// in a tenant nobody holds. Asked when a pass reads which are, and again of
// each where it is destroyed ([DestroyConsents]).
func dueConsent(before time.Time) predicate.Consent {
	return consent.HasHolderWith(
		holder.DateErasedNotNil(),
		holder.DateErasedLT(before),
		holder.HasTenantWith(tenant.DateHeldIsNil()),
	)
}

// DestroyConsents removes those of `ids` that are still due, and puts each on
// the trail as the act it is in the same transaction: written past the
// servers, which refuse it, so a check of the rows does not find them things
// the trail says are rows (`pd.RecordErased`, payday#115). What that writes is
// which rows and when, each under the tenant the consent was of
// (`expire.Gone`).
//
// # Those still due, asked where they are destroyed
//
// `ids` is what a pass read, and a moment that has passed. It removed every
// one of them, so the consent of somebody restored since was destroyed with
// the rest, and so was one of a tenant an operator had held since (#280) --
// the two things the comment at the top of this file says keep a consent. So
// what is removed is asked again inside the transaction, in the statement
// that removes it; and if that takes fewer than were read as due a statement
// before, somebody moved between the two, and the transaction is left
// unmade for the next pass rather than putting on the trail a row that is
// still there.
func DestroyConsents(ctx context.Context, db *ent.Client, ids []uuid.UUID, before time.Time, cs audit.Contents) (int, error) {
	// Through ent, past the servers, on purpose, and the deployment's own
	// whoever runs it -- a person's window running out, an operator at the
	// box: so where a hook is installed it is asked nothing, and payday's
	// guard is told so (`cmd/hook.go`).
	ctx = bare.TrustAdmission(ctx)

	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Those still due, and whose each is, read while it is there: a line that
	// says a row went has no row to read a tenant from.
	was, err := tx.Consent.Query().Where(consent.IdIn(ids...), dueConsent(before)).All(ctx)
	if err != nil || len(was) == 0 {
		return 0, err
	}
	still := make([]uuid.UUID, len(was))
	gone := make([]expire.Row, len(was))
	for i, v := range was {
		still[i] = v.Id
		gone[i] = expire.Row{Id: v.Id, Tenant: v.TenantId}
	}

	n, err := tx.Consent.Delete().Where(consent.IdIn(still...), dueConsent(before)).Exec(ctx)
	if err != nil {
		return 0, err
	}
	if n != len(still) {
		return 0, fmt.Errorf("%d of %d consents stopped being due while they were being destroyed -- an account restored, a tenant held -- "+
			"and none of them was; the next pass reads again", len(still)-n, len(still))
	}

	if err := expire.Gone(ctx, tx.Client(), cs, DestroyedConsent, gone); err != nil {
		return 0, err
	}

	return n, tx.Commit()
}

// consentsAtOnce is how many consents one transaction destroys, so that a
// first pass over years of departures is many short transactions and not one
// long one.
const consentsAtOnce = 500

// PassConsents destroys every consent kept past `before`, a transaction's
// worth at a time, and answers how many: what a tick of [SweepConsents] does,
// and what `roster forget` with no name does about consents. Each is asked
// again where it is destroyed ([DestroyConsents]).
func PassConsents(ctx context.Context, db *ent.Client, before time.Time, cs audit.Contents) (int, error) {
	ids, err := DueConsents(ctx, db, before)
	if err != nil {
		return 0, err
	}

	destroyed := 0
	for len(ids) > 0 {
		k := min(len(ids), consentsAtOnce)
		n, err := DestroyConsents(ctx, db, ids[:k], before, cs)
		if err != nil {
			return destroyed, err
		}
		destroyed += n
		ids = ids[k:]
	}

	return destroyed, nil
}

// SweepConsents destroys, on a clock, every consent kept `keep` past its
// account's erase. A pass that fails says so and the next one carries on: what
// it reached is gone, and what it did not is still due.
func SweepConsents(db *ent.Client, keep time.Duration, cs audit.Contents, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		before := time.Now().Add(-keep)

		destroyed, err := PassConsents(ctx, db, before, cs)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "consent: destroy", "err", err, "destroyed", destroyed, "before", before)
		}
		if destroyed > 0 {
			log.From(ctx).InfoContext(ctx, "consent: destroyed", "rows", destroyed, "before", before)
		}

		return nil
	})
}
