package session

import (
	"context"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/session"
)

// Swept is how often expired sessions are collected.
//
// The same shape `keys.Swept` has and for the same reason: the **read** decides
// whether somebody is signed in -- `authsession.Handler` asks `Session.Dead`
// on every request and answers `ErrNoSession` -- so this is not a deadline and
// not a security control. It is the other half of that sentence. Without it the
// table is one row per sign-in since the deployment started, forever.
//
// It was missing, and `docs/OPERATING.md` said so out loud while the table was
// being written. This is that sentence closed.
//
// An hour, because nothing waits on it.
const Swept = time.Hour

// Sweep deletes sessions whose expiry has passed.
//
// # Why nothing else was ever going to
//
// payday's own `MemStore` collects on every write, and a table cannot: that
// store holds a map it is free to walk. What roster inherited from
// `authsession.Store` was the interface, which has `Put`, `Get` and `Del` and
// no pass over everything -- correctly, since a store over a hundred million
// rows should not be walked by whoever happens to be signing in.
//
// And `Del` is not a delete. It is the soft erase every entity here has, so
// even signing out leaves the row.
//
// # `date_expires` and nothing else
//
// Not `date_idle`, and the difference is worth writing down because it looks
// arbitrary.
//
// `Store.Put` resurrects on a miss: it looks for a live row with this secret,
// and creates one when it finds none. That is right for what it is for -- the
// idle clock being bumped -- and it means a row this sweep deletes can come
// back if a request carrying that cookie is in flight. A row past
// `date_expires` comes back **already expired**, so the next read refuses it
// and the next pass collects it; a row past `date_idle` would come back with a
// fresh half-window, which turns "collected" into "live" and is a thing not
// worth having to reason about.
//
// A revoked session is left until its own clock runs out, exactly as
// `keys.Sweep` leaves a revoked delegation, and for the reason written there.
// It blocks nothing while it waits: the unique index on the secret is partial
// -- `date_erased IS NULL` -- so an erased row is not in it.
//
// # Why it goes around the stack
//
// It takes the ent client, like the other two sweeps. There is nobody to narrow
// a sweep to, nothing to record about it, and no version to compare -- and
// going through the stack would produce a `Watch` event and an `Audit` row per
// collected row, which is the cost this exists to remove arriving in a
// different table.
func Sweep(db *ent.Client, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		n, err := Collect(ctx, db)
		if err != nil {
			// A pass that failed is a pass, and the next one is in an hour. A
			// database that blinked is not a reason to take the process down,
			// which is what answering with an error here would do -- `spin.Run`
			// reads one as giving up.
			log.From(ctx).WarnContext(ctx, "sweep: sessions", "err", err)

			return nil
		}
		if n > 0 {
			log.From(ctx).InfoContext(ctx, "sweep: sessions", "gone", n)
		}

		return nil
	})
}

// Collect is one pass of [Sweep], and answers with how many it removed.
func Collect(ctx context.Context, db *ent.Client) (int, error) {
	return db.Session.Delete().
		Where(session.DateExpiresLT(time.Now())).
		Exec(ctx)
}
