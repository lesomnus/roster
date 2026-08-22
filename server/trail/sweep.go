package trail

import (
	"context"
	"errors"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/internal/ent"
)

// Swept is how often the policy is applied when nothing says otherwise.
//
// Daily, and it is the one sweep in this app whose period is not about the
// thing it collects. A continuation lives for minutes and a link for a quarter
// of an hour, so those sweeps run on the clock of what they are chasing. A
// trail row is not stale and never becomes stale -- it is **old**, on a scale of
// months -- so what this period decides is only how far past the window a row
// may sit, and a day is invisible against ninety of them.
const Swept = 24 * time.Hour

// Policy is what a deployment keeps, and it is two clocks rather than one.
//
// [Policy.Retain] is how long a row stays in the database: an operational
// choice, about what the console can show and what a query costs. [Policy.Destroy]
// is how long the record exists at all, which is the obligation, and it is
// normally much the longer of the two. Between them is [Policy.Archive], which
// is where a row lives out the difference.
//
// Both are **empty by default**, and empty is forever. That is the only honest
// default for a trail: a deployment upgrading into a version that had opinions
// about how long its evidence lasts would discover them by not having the
// evidence.
type Policy struct {
	// Retain is how long a row stays in the database. Empty is forever, and is
	// what a deployment that has not thought about it gets.
	Retain time.Duration

	// Archive is the directory rows are written to on their way out. Empty
	// keeps no copy, which is refused unless [Policy.Discard] says so.
	Archive string

	// Discard is a deployment saying it means to keep nothing.
	//
	// Its own field rather than an empty [Policy.Archive], because those are
	// two different states that look alike: *I have not configured where* and
	// *I do not want one*. A blank field defaulting to destruction is the
	// configuration mistake that is discovered by an auditor.
	Discard bool

	// Destroy is how long an archive file is kept. Empty is forever, and a
	// deployment with an obligation to destroy is the one that sets it.
	Destroy time.Duration

	// Every is how often the two are applied. Empty is [Swept].
	Every time.Duration
}

// On answers whether there is anything to do.
func (p Policy) On() bool { return p.Retain > 0 || p.Destroy > 0 }

// Valid refuses a policy that would destroy something nobody asked it to.
//
// Read at startup rather than at the first sweep, which is what makes it worth
// having: a deployment that has named a window and no archive learns about it
// while somebody is watching the process come up, and not a day later when the
// first pass has already run.
func (p Policy) Valid() error {
	if p.Retain > 0 && p.Archive == "" && !p.Discard {
		return errors.New("audit.retain names a window and audit.archive names nowhere to put what leaves it; " +
			"set audit.archive, or audit.discard: true to say the rows are meant to go")
	}
	if p.Destroy > 0 && p.Archive == "" {
		return errors.New("audit.destroy is how long the archive is kept and audit.archive names none")
	}
	if p.Destroy > 0 && p.Retain > 0 && p.Destroy < p.Retain {
		return errors.New("audit.destroy is shorter than audit.retain, so a row would be destroyed before it left the database")
	}

	return nil
}

func (p Policy) every() time.Duration {
	if p.Every <= 0 {
		return Swept
	}

	return p.Every
}

// Sweep applies the policy on a clock.
//
// It is the other half of the sentence `vouch.Sweep` states about itself and it
// is the half that is different: an expired continuation is refused the moment
// it is presented, so that sweep is only about space. This one is the
// **mechanism**. Nothing else applies a retention window, so a deployment whose
// sweep has been failing for a month is a deployment that has been keeping
// records it said it would not -- which is why every pass that fails says so
// rather than being counted.
func Sweep(db *ent.Client, p Policy) spin.Func {
	return spin.Every(p.every(), func(ctx context.Context) error {
		if p.Retain > 0 {
			before := time.Now().Add(-p.Retain)

			n, err := p.leave(ctx, db, before)
			if err != nil {
				log.From(ctx).WarnContext(ctx, "sweep: the trail's retention window", "err", err, "before", before)
			} else if n > 0 {
				log.From(ctx).InfoContext(ctx, "sweep: the trail's retention window",
					"moved", n, "before", before, "kept", p.Archive != "")
			}
		}
		if p.Destroy > 0 {
			before := time.Now().Add(-p.Destroy)

			vs, err := Purge(ctx, p.Archive, before)
			if err != nil {
				log.From(ctx).WarnContext(ctx, "sweep: the trail's archive", "err", err, "before", before)
			} else if len(vs) > 0 {
				log.From(ctx).InfoContext(ctx, "sweep: the trail's archive", "destroyed", len(vs), "before", before)
			}
		}

		return nil
	})
}

// leave is one pass of the window, whichever of the two acts this deployment
// asked for.
func (p Policy) leave(ctx context.Context, db *ent.Client, before time.Time) (int, error) {
	if p.Archive == "" {
		return Collect(ctx, db, before)
	}

	return Archive(ctx, db, before, p.Archive)
}
