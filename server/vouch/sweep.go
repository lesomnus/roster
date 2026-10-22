package vouch

import (
	"context"
	"time"

	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/continuation"
	"github.com/lesomnus/roster/internal/ent/link"
	"github.com/lesomnus/roster/server/expire"
)

// Swept is how often expired attempts and links are collected.
//
// Not a deadline and not a security control -- an expired continuation is
// refused the moment it is presented, because *a sweep that is the mechanism is
// a sweep whose outage is a security incident*. This is the other half of that
// sentence, and `keys.Sweep` is the same pair one table over.
//
// More often than a delegation's, because these live for minutes and a busy
// deployment writes one per two-factor sign-in.
const Swept = 10 * time.Minute

// Sweep deletes attempts whose expiry has passed.
//
// A hard delete for `keys.Sweep`'s reason: a soft erase leaves the row, and the
// row is the whole thing being collected. It costs a trail entry naming a
// continuation resolving to nothing, which is a fair price for a row that lived
// five minutes.
//
// It also collects **spent** ones once they expire. Spending is an erase, so a
// spent row is out of reach immediately and takes up space until its own clock
// runs out -- one pass rather than two is worth more than the space.
//
// Each one collected is on the trail as [Expired], for `expire.Rows`' reason.
func Sweep(db *ent.Client, cs audit.Contents, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		n, err := Collect(ctx, db, cs)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "sweep: attempts and links", "err", err)

			return nil
		}
		if n > 0 {
			log.From(ctx).InfoContext(ctx, "sweep: attempts and links", "gone", n)
		}

		return nil
	})
}

// Collect is one pass of [Sweep], and answers with how many it removed.
//
// Both tables, because they are the same fact one shape apart -- a row that
// exists for minutes between two requests -- and two loops on two clocks would
// be two things to notice had stopped.
func Collect(ctx context.Context, db *ent.Client, cs audit.Contents) (int, error) {
	now := time.Now()

	n, err := expire.Rows(ctx, db, cs, Expired,
		func(ctx context.Context, db *ent.Client) ([]uuid.UUID, error) {
			return db.Continuation.Query().Where(continuation.DateExpiresLT(now)).Limit(audit.RowsPage).Ids(ctx)
		},
		func(ctx context.Context, db *ent.Client, ids []uuid.UUID) (int, error) {
			return db.Continuation.Delete().Where(continuation.IdIn(ids...)).Exec(ctx)
		},
	)
	if err != nil {
		return n, err
	}

	m, err := expire.Rows(ctx, db, cs, Expired,
		func(ctx context.Context, db *ent.Client) ([]uuid.UUID, error) {
			return db.Link.Query().Where(link.DateExpiresLT(now)).Limit(audit.RowsPage).Ids(ctx)
		},
		func(ctx context.Context, db *ent.Client, ids []uuid.UUID) (int, error) {
			return db.Link.Delete().Where(link.IdIn(ids...)).Exec(ctx)
		},
	)

	return n + m, err
}

// Expired is the act an attempt or a link the sweep collected is on the trail
// as.
const Expired = "roster.vouch.expire"
