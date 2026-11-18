package forget

import (
	"context"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/server/pd"
)

// What a forget owes, and who pays it.
//
// A forget blanks the trail's rows about somebody in its own transaction and
// owes the rest there: the archive's half, the rows' seals, and every row a
// legal hold kept as it was ([Forget]). It pays what it can as soon as it has
// committed. What is left is a row of payday's `owed` for each thing
// forgotten, and it is left in three cases: the payment failed, the process
// stopped before it, or a hold kept part of it -- which is the one that
// lasts, for as long as the hold does.
//
// # It was paid by the trail's pass, and most deployments have none
//
// `trail.Policy.Pass` pays every erasure owed before it moves anything, and
// that was the only thing that paid one after the forget itself. `serve`
// spins the trail's pass where the policy names a retention window, and
// keeping the trail for ever is the default. So a default deployment never
// paid: a tenant held, one of its people forgotten by name, the hold lifted,
// and the lines about their address and their key kept what they said for
// good, while the manual and the command both said that nobody runs anything
// again (#280). `roster trail prune` refused, there being no window to apply,
// and a second `roster forget` of the person paid the person's own line and
// not those of the rows that went with them, whose identifiers it no longer
// had.
//
// # So paying is a loop of its own, wherever there is no pass
//
// [SweepOwed] is what `serve` spins in a deployment whose trail has no pass.
// It calls `trail.Policy.Pay` with no names, which is the first thing a pass
// does and nothing else of one: it erases what a forget already decided to
// erase and is still owed, and applies no window to anything. A deployment
// that keeps its trail for ever goes on keeping it. And [PayOwed] is the same
// call for `roster forget` with no name, which is what an operator runs.

// PayOwed pays every erasure the trail owes that can be paid now, and answers
// what it reached. One a hold keeps part of stays owed, and is in what it
// answers ([trail.Paid]).
func PayOwed(ctx context.Context, db *ent.Client, p trail.Policy) (trail.Paid, error) {
	return p.Pay(ctx, pd.TrailStore(db))
}

// SweepOwed pays what the trail owes on a clock: the trail's own, `audit.every`
// or a day, since it stands where the trail's pass would.
//
// A pass that fails says so and the next one carries on, as [Sweep]'s does and
// for its reason: nothing else pays, so a loop that has been failing is a
// deployment keeping what it told somebody it had destroyed.
func SweepOwed(db *ent.Client, p trail.Policy) spin.Func {
	every := p.Every
	if every <= 0 {
		every = trail.Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		paid, err := PayOwed(ctx, db, p)
		switch {
		case err != nil:
			log.From(ctx).WarnContext(ctx, "forget: the erasures owed, and what is left of them is paid by the next pass", "err", err, "paid", paid.Objects)

		case paid.Objects > 0 || paid.Owed > 0:
			log.From(ctx).InfoContext(ctx, "forget: erasures owed, paid", "paid", paid.Objects, "rows", paid.Rows, "archived", paid.Archived,
				"held", paid.Owed, "by", paid.Held.By)
		}

		return nil
	})
}
