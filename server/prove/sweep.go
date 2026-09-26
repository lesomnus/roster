package prove

import (
	"context"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/hostproof"
)

// Swept is how often abandoned claims are collected.
//
// Not a deadline and not the mechanism, which is the sentence `keys.Swept`
// already carries: an expired claim is refused the moment it is spent, in
// `server/core`, because *a sweep that is the mechanism is a sweep whose outage
// is a security incident*. This is the other half -- the read decides, and this
// keeps the table from being one row per name anybody ever thought about
// claiming.
//
// A day, and not the hour the other two use. Nothing waits on it either, and
// what this collects arrives at human speed: a tenant claims a handful of names
// in the life of a deployment, where a delegation is minted per sign-in.
const Swept = 24 * time.Hour

// Sweep deletes claims whose expiry has passed.
//
// A hard delete, for `keys.Sweep`'s reason: a soft erase leaves the row, and
// leaving the row is the thing this exists to stop. What it costs is that a
// trail entry naming a claim resolves to nothing afterwards -- which the trail
// does not depend on, since it records what happened rather than what the row
// still says.
//
// And it goes around the stack, on the ent client, for that function's other
// reason: a sweep is not a caller. There is nobody to narrow it to, no version
// to compare, and going through the stack would write an `Audit` row per
// collected row, which is the cost being removed arriving in another table.
//
// # What it does not collect
//
// A claim that was **spent**. `Host.Add` erases it on the way through, and an
// erased row is out of reach the moment that lands -- so what is left is a row
// taking space until its own clock runs out, and one pass rather than two is
// worth more than the space.
func Sweep(db *ent.Client, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		n, err := Collect(ctx, db)
		if err != nil {
			// A pass that failed is a pass, and the next one is tomorrow: a
			// database that blinked is not a reason to take the process down,
			// which is what answering with an error here would do.
			log.From(ctx).WarnContext(ctx, "sweep: host claims", "err", err)

			return nil
		}
		if n > 0 {
			log.From(ctx).InfoContext(ctx, "sweep: host claims", "gone", n)
		}

		return nil
	})
}

// Collect is one pass of [Sweep], and answers with how many it removed.
//
// Separate so that a test can run one rather than start a loop, and so that a
// deployment with an opinion about when this happens has something to call.
func Collect(ctx context.Context, db *ent.Client) (int, error) {
	return db.HostProof.Delete().
		Where(hostproof.DateExpiresLT(time.Now())).
		Exec(ctx)
}
