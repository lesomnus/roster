package prove

import (
	"context"
	"time"

	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/hostproof"
	"github.com/lesomnus/roster/server/expire"
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
// reason: a sweep is not a caller. There is nobody to narrow it to and no
// version to compare. What it keeps is a line of the trail per collected claim
// saying it went (`expire.Rows`, payday#115), which is what tells it apart from
// one somebody removed in the database.
//
// # What it does not collect
//
// A claim that was **spent**. `Host.Add` erases it on the way through, and an
// erased row is out of reach the moment that lands -- so what is left is a row
// taking space until its own clock runs out, and one pass rather than two is
// worth more than the space.
func Sweep(db *ent.Client, cs audit.Contents, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		n, err := Collect(ctx, db, cs)
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
func Collect(ctx context.Context, db *ent.Client, cs audit.Contents) (int, error) {
	now := time.Now()

	return expire.Rows(ctx, db, cs, Expired,
		func(ctx context.Context, db *ent.Client) ([]uuid.UUID, error) {
			return db.HostProof.Query().Where(hostproof.DateExpiresLT(now)).Limit(audit.RowsPage).Ids(ctx)
		},
		func(ctx context.Context, db *ent.Client, ids []uuid.UUID) (int, error) {
			return db.HostProof.Delete().Where(hostproof.IdIn(ids...)).Exec(ctx)
		},
	)
}

// Expired is the act a claim the sweep collected is on the trail as.
const Expired = "roster.prove.expire"
