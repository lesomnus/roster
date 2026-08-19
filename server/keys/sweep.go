package keys

import (
	"context"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/delegation"
)

// Swept is how often expired delegations are collected.
//
// Not a deadline and not a security control -- an expired delegation is refused
// the moment it is presented, by [findDelegation], because *a sweep that is the
// mechanism is a sweep whose outage is a security incident*. This is the other
// half of that sentence: the read decides, and this keeps the table from being
// one row per sign-in since the deployment started.
//
// An hour, because nothing waits on it.
const Swept = time.Hour

// Sweep deletes delegations whose expiry has passed.
//
// # Why it deletes rather than erases
//
// `Delegation` erases softly, so that a trail naming a row still finds
// something. That is the right shape for revoking one and the wrong shape for
// collecting it: a soft erase leaves the row, and the whole reason this exists
// is that the table grows by one row per sign-in forever. So this is a hard
// delete, and what it costs is that a trail entry naming a delegation that has
// since expired resolves to nothing -- which `Tenant` already pays for the same
// reason, and which the audit row itself does not depend on: it records what
// happened, not what the row still says.
//
// # Why it goes around the stack
//
// It takes the ent client rather than an `app.Server`, and that is the one
// place in this package that does. A sweep is not a caller: there is nobody to
// narrow it to, nothing to record about it in a trail, and no version to
// compare -- and going through the stack would produce a `Watch` event and an
// `Audit` row per collected row, which is the cost this is trying to remove
// arriving in a different table.
//
// # What it does not do
//
// It does not collect a **revoked** delegation until that one expires too. The
// row is out of reach the moment it is erased -- `<Entity>Pick` narrows to the
// live rows -- so what is left is a row taking up space until its own clock
// runs out, and one pass rather than two is worth more than the space.
func Sweep(db *ent.Client, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		n, err := Collect(ctx, db)
		if err != nil {
			// A pass that failed is a pass, and the next one is in an hour: a
			// database that blinked is not a reason to take the process down,
			// which is what answering with an error here would do. The table
			// being an hour larger than it should be is the whole cost.
			log.From(ctx).WarnContext(ctx, "sweep: delegations", "err", err)

			return nil
		}
		if n > 0 {
			log.From(ctx).InfoContext(ctx, "sweep: delegations", "gone", n)
		}

		return nil
	})
}

// Collect is one pass of [Sweep], and answers with how many it removed.
//
// Separate so that a test can run one rather than start a loop, and so that a
// deployment with an opinion about when this happens -- a cron, a maintenance
// window -- has something to call.
func Collect(ctx context.Context, db *ent.Client) (int, error) {
	return db.Delegation.Delete().
		Where(delegation.DateExpiresLT(time.Now())).
		Exec(ctx)
}
