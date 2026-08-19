package vouch

import (
	"context"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/continuation"
)

// Swept is how often expired attempts are collected.
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
func Sweep(db *ent.Client, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		n, err := Collect(ctx, db)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "sweep: continuations", "err", err)

			return nil
		}
		if n > 0 {
			log.From(ctx).InfoContext(ctx, "sweep: continuations", "gone", n)
		}

		return nil
	})
}

// Collect is one pass of [Sweep], and answers with how many it removed.
func Collect(ctx context.Context, db *ent.Client) (int, error) {
	return db.Continuation.Delete().
		Where(continuation.DateExpiresLT(time.Now())).
		Exec(ctx)
}
