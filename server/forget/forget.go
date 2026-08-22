// Package forget is what "destroy this person" means here, and it is one act
// with two triggers.
//
// # Why it is not the erase
//
// `Holder.Erase` writes two columns -- `date_erased` and the version beside it
// -- and stops. Nothing cascades, deliberately, and nothing is destroyed: the
// row keeps the alias, the name and the profile; the addresses and the external
// identities keep theirs; and the trail holds a copy of all of it, including
// the copy the erase itself wrote.
//
// So an erase makes somebody **unreachable** and destroys nothing. That is the
// right shape for *this person has left* and the wrong one for *destroy what
// you hold about them*, which is a different request with a legal clock on it.
//
// # The two triggers, which are one act
//
// A **request**: somebody exercises a right, and there is no grace -- they
// asked, and the clock a regulator counts is already running.
//
// An **account closing**: `Holder.Erase`, and then a window. The window is not
// legal, it is operational -- a mistaken deletion, a compromised account
// deleting things, a billing dispute -- which is why it is short, and why the
// thing that makes it worth having is that it can be **undone** while it lasts.
// [Restore] is that, and without it the window is a delay rather than a grace.
//
// Both end here, and what differs is only when.
//
// # What it destroys, and what it keeps
//
// Everything that exists to say *this person reaches here, signs in there,
// holds this* is **removed**: their addresses, their external identities, their
// verifiers, their keys, their sessions and attempts, and the rows that say
// what they may do. Those have no meaning without the person, and a destroyed
// person holding permissions is a row waiting to be a surprise.
//
// The `Holder` row itself **stays, blank**. Its identifier is referenced --
// `Audit.actor_id` is it, and twelve foreign keys point at it -- and what makes
// it personal data is that it resolves to a name. Emptied of the columns that
// name somebody, it is a stable pseudonym reaching nothing, which is exactly
// what a trail wants: *the same someone did these fourteen things*, with no way
// to say who.
//
// And the trail keeps its events and loses its contents, in the database and in
// the archive both. `payday/trail` says why at length.
package forget

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/apikey"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/continuation"
	"github.com/lesomnus/roster/internal/ent/credential"
	"github.com/lesomnus/roster/internal/ent/delegation"
	"github.com/lesomnus/roster/internal/ent/email"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/identity"
	"github.com/lesomnus/roster/internal/ent/link"
	"github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/internal/ent/sitemembership"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	"github.com/lesomnus/roster/server/pd"
)

// Result is what one destruction did, for a log and for somebody watching.
type Result struct {
	// Rows is how many rows of the person's own were removed.
	Rows int

	// Trail is how many trail rows lost their contents.
	Trail int

	// Archived is how many archived rows lost theirs.
	Archived int
}

func (r Result) String() string {
	return fmt.Sprintf("%d row(s), %d trail row(s), %d archived", r.Rows, r.Trail, r.Archived)
}

// Forget destroys what this deployment holds about somebody.
//
// It erases them first if nothing had, so that the immediate path is one call:
// a person asking to be destroyed is also a person leaving, and requiring two
// commands in an order is requiring somebody to get an order right under a
// deadline.
//
// It is **not** a transaction across the archive, and cannot be -- a file and a
// database do not commit together. The order is chosen so that an interruption
// leaves less rather than more: the person's own rows go first, then the trail,
// then the archive. Every step is idempotent, so running it again finishes it.
func Forget(ctx context.Context, db *ent.Client, who pdid.Id, archive string) (Result, error) {
	var out Result

	v, err := db.Holder.Get(ctx, who.Uuid())
	if err != nil {
		return out, err
	}

	// Every identifier whose trail rows are about this person. Not the holder
	// alone: `Audit.value` for a write to an `Email` row holds the address, and
	// that row's `object_id` is the email's. A pass that named only the holder
	// would blank the record of the person and leave every address they ever
	// had in the trail beside it.
	objects := []pdid.Id{who}

	for _, of := range subjects() {
		ids, err := of.ids(ctx, db, who.Uuid())
		if err != nil {
			return out, fmt.Errorf("%s: %w", of.name, err)
		}
		for _, id := range ids {
			objects = append(objects, pdid.Id(id))
		}

		n, err := of.remove(ctx, db, who.Uuid())
		if err != nil {
			return out, fmt.Errorf("%s: %w", of.name, err)
		}

		out.Rows += n
	}

	// The row itself stays and stops naming anybody. Through ent, like every
	// other write here: the servers refuse most of this, and the ones that do
	// not would record it -- and a record of *what was destroyed* is the thing
	// being destroyed.
	u := db.Holder.UpdateOneID(who.Uuid()).
		SetAlias("").
		SetName("").
		SetDesc("").
		SetLabels(map[string]string{}).
		ClearProfile().
		ClearData().
		SetIdpSubject("").
		SetDateUpdated(time.Now())
	if v.DateErased == nil {
		// Asked for by somebody who had not left first. Leaving is implied by
		// the request, and it is the state everything else already assumes.
		u = u.SetDateErased(time.Now())
	}
	if err := u.Exec(ctx); err != nil {
		return out, err
	}

	out.Rows++

	n, err := pd.ForgetInTrail(ctx, db, objects)
	if err != nil {
		return out, err
	}

	out.Trail = n

	if archive != "" {
		vs := make([]string, len(objects))
		for i, k := range objects {
			vs[i] = base64.StdEncoding.EncodeToString(k.Bytes())
		}

		k, err := trail.Forget(archive, vs)
		if err != nil {
			return out, err
		}

		out.Archived = k
	}

	return out, nil
}

// Restore undoes an erase, while there is still an erase to undo.
//
// The grace period is worth having only because of this. Without it, *thirty
// days and then destruction* is a delay, and the reasons the window exists at
// all -- a mistake, a compromised account, a dispute -- are reasons that need
// the mistake to be reversible.
//
// It refuses somebody already destroyed, and what answers that is the row
// itself: a forgotten holder has no alias, because that is the first thing
// [Forget] takes. Not a column, because a column would be a second answer that
// a row written before it existed gets wrong -- and there is nothing this could
// read that is more honest than *there is no name here any more*.
func Restore(ctx context.Context, db *ent.Client, who pdid.Id) error {
	v, err := db.Holder.Get(ctx, who.Uuid())
	if err != nil {
		return err
	}
	if v.DateErased == nil {
		return fmt.Errorf("%s has not been erased, so there is nothing to bring back", who)
	}
	if v.Alias == "" {
		return fmt.Errorf("%s has been forgotten; what is left is an identifier, and it names nobody", who)
	}

	return db.Holder.UpdateOneID(who.Uuid()).
		ClearDateErased().
		SetDateUpdated(time.Now()).
		Exec(ctx)
}

// Due is everybody whose grace has run out.
//
// Those already destroyed are left out by their empty alias rather than by a
// column saying so. [Forget] is idempotent, so including them would be correct
// and would mean walking the whole history of a deployment's leavers on every
// pass.
func Due(ctx context.Context, db *ent.Client, before time.Time) ([]pdid.Id, error) {
	vs, err := db.Holder.Query().
		Where(
			holder.DateErasedNotNil(),
			holder.DateErasedLT(before),
			holder.AliasNEQ(""),
		).
		IDs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]pdid.Id, len(vs))
	for i, v := range vs {
		out[i] = pdid.Id(v)
	}

	return out, nil
}

// subject is one kind of row that belongs to a person.
//
// Written out rather than derived from the schema, because *which of an app's
// entities are a person's* is not a thing a schema states and not a thing to
// guess: an edge to `Holder` is carried by rows that are about somebody and by
// rows that merely mention them, and destroying the second kind would be
// destroying somebody else's record.
type subject struct {
	name   string
	ids    func(context.Context, *ent.Client, uuid.UUID) ([]uuid.UUID, error)
	remove func(context.Context, *ent.Client, uuid.UUID) (int, error)
}

func subjects() []subject {
	return []subject{
		// The ways in. Each of these exists to say *this person reaches here* or
		// *signs in there*, and each is worthless without them.
		{
			name: "email",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Email.Query().Where(email.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Email.Delete().Where(email.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "identity",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Identity.Query().Where(identity.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Identity.Delete().Where(identity.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "credential",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Credential.Query().Where(credential.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Credential.Delete().Where(credential.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		// The credentials issued to them, and the half-finished sign-ins. These
		// expire anyway; taking them now is the difference between *cannot sign
		// in* and *whatever was already issued still works*.
		{
			name: "api key",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.ApiKey.Query().Where(apikey.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.ApiKey.Delete().Where(apikey.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "session",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Session.Query().Where(session.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Session.Delete().Where(session.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "delegation",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Delegation.Query().Where(delegation.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Delegation.Delete().Where(delegation.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "continuation",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Continuation.Query().Where(continuation.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Continuation.Delete().Where(continuation.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "link",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Link.Query().Where(link.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Link.Delete().Where(link.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		// And what they may do. A destroyed person holding permissions is a row
		// waiting to be a surprise -- and one of the twelve foreign keys into
		// `holder` is `SET NULL`, so leaving these would leave a binding pointing
		// at nobody rather than at somebody who is gone.
		{
			name: "binding",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Binding.Query().Where(binding.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Binding.Delete().Where(binding.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "site membership",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.SiteMembership.Query().Where(sitemembership.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.SiteMembership.Delete().Where(sitemembership.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "team membership",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.TeamMembership.Query().Where(teammembership.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.TeamMembership.Delete().Where(teammembership.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
		{
			name: "group membership",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.GroupMembership.Query().Where(groupmembership.HasHolderWith(holder.IDEQ(k))).IDs(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.GroupMembership.Delete().Where(groupmembership.HasHolderWith(holder.IDEQ(k))).Exec(ctx)
			},
		},
	}
}

// Swept is how often the grace is checked when nothing says otherwise.
//
// Daily, for `trail.Swept`'s reason: what this period decides is only how far
// past the window somebody may sit, and a day is invisible against thirty of
// them.
const Swept = 24 * time.Hour

// Sweep destroys everybody whose grace has run out, on a clock.
//
// It is the same **kind** of loop the trail's retention is and not the kind the
// expiry sweeps are: nothing else applies this window, so an outage of it is a
// deployment holding what it told somebody it would destroy. Every pass that
// fails says so rather than being counted.
//
// A pass that fails part way has still destroyed whoever it reached, because
// each person is their own act -- there is no batch here to be half of.
func Sweep(db *ent.Client, after time.Duration, archive string, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		before := time.Now().Add(-after)

		vs, err := Due(ctx, db, before)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "forget: who is due", "err", err, "before", before)

			return nil
		}
		if len(vs) == 0 {
			return nil
		}

		for _, who := range vs {
			res, err := Forget(ctx, db, who, archive)
			if err != nil {
				log.From(ctx).WarnContext(ctx, "forget", "holder", who.String(), "err", err)

				continue
			}

			log.From(ctx).InfoContext(ctx, "forget",
				"holder", who.String(), "rows", res.Rows, "trail", res.Trail, "archived", res.Archived)
		}

		return nil
	})
}
