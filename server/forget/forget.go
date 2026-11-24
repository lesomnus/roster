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
// person holding permissions is a row waiting to be a surprise. So is a link
// that names an address of theirs, whoever it is for: what it would prove is
// gone, and [subjects] says what it was read as once it named nothing.
//
// The `Holder` row itself **stays, blank**. Its identifier is referenced --
// `Audit.actor_id` is it, and twenty-one foreign keys point at it -- and what makes
// it personal data is that it resolves to a name. Emptied of the columns that
// name somebody -- every one the schema declares personal, their picture and
// what their directory called them among them -- it is a stable pseudonym
// reaching nothing, which is exactly what a trail wants: *the same someone did
// these fourteen things*, with no way to say who.
//
// And the trail keeps its events and loses its contents, in the database and in
// the archive both. `payday/trail` says why at length.
package forget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/payday/audit"
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
	"github.com/lesomnus/roster/internal/ent/nomination"
	"github.com/lesomnus/roster/internal/ent/phone"
	"github.com/lesomnus/roster/internal/ent/predicate"
	"github.com/lesomnus/roster/internal/ent/relation"
	"github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/internal/ent/sitemembership"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	"github.com/lesomnus/roster/internal/ent/tenant"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/expire"
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

	// Held is the trail rows about them a legal hold kept as they were. What
	// a hold keeps stays owed, and the first pass after the hold lifts erases
	// it ([SweepOwed] says whose pass): nobody has to run this again.
	Held trail.Held

	// Owed is how many of what was forgotten the archive's half is still
	// owed for (payday#115): what a hold keeps part of, or all of it when
	// paying after the transaction failed. The next pass pays it.
	Owed int

	// Under is the hold their tenant was under as they were forgotten, and
	// nil when nobody held it: a forget by name is made under one ([Hold]),
	// and whoever made it is told what the hold kept and what it did not.
	Under *Holding
}

// Holding is an operator's hold on a tenant, as somebody who meets one is
// told of it: whose, since when, and what for.
type Holding struct {
	Tenant pdid.Id
	Alias  string
	Since  time.Time
	Why    string
}

func (r Result) String() string {
	v := fmt.Sprintf("%d row(s), %d trail row(s), %d archived", r.Rows, r.Trail, r.Archived)
	if r.Held.Any() {
		v += fmt.Sprintf(", %d kept by a legal hold", r.Held.Rows)
	}
	if r.Owed > 0 {
		v += fmt.Sprintf(", %d owed to the archive", r.Owed)
	}

	return v
}

// Forget destroys what this deployment holds about somebody.
//
// It erases them first if nothing had, so that the immediate path is one call:
// a person asking to be destroyed is also a person leaving, and requiring two
// commands in an order is requiring somebody to get an order right under a
// deadline.
//
// # And the trail's erasure is one with it
//
// The person's own rows, the holder blanked and the trail's rows about them
// in the database go in one transaction: the trail's erasure is asked of the
// transaction's store (`trail.Policy.Forget`), which blanks those rows there
// and owes the archive's half there (payday#115), and the rows' seals with it
// (payday#144) -- one row of payday's `Owed` per identifier, which a rollback
// takes with the rest. It takes no turn with a pass in the transaction: what a
// pass could write back is what is owed. The archive takes no part in a
// transaction, so its half is paid once the transaction has committed,
// naming what was forgotten (`trail.Policy.Pay`); a payment
// that fails, or a process that stops before it, leaves the debt to the
// next pass: the trail's, which pays what is owed before it moves anything,
// or in a deployment whose trail has none, the loop that does only that
// ([SweepOwed]). What is still owed is [Result.Owed]. A commit that never happened forgot
// nothing anywhere, and one that did owes the rest, so there is nothing left
// to lose in between, and nothing an operator has to run again.
//
// The trail is the policy's to erase because a row of it a legal hold is on
// stays as it was: that is in [Result.Held], and the debt stays with it until
// the hold lifts, when the pass after pays the rest.
//
// # And what it removes is on the trail as the act it is
//
// Every row of theirs it takes is written past the servers, and a check of the
// rows finds a thing the trail says is a row and is not -- what a row somebody
// removed in the database looks like -- unless the removal is recorded. So the
// rows go, and the holder is blanked, in one transaction that also puts each
// on the trail as [ForgottenHolder] (payday#115): the ones it removed with a
// digest that says they went (`pd.RecordErased`), and the holder with one of
// the row as it is left (`pd.Record`). What that writes is which rows and when,
// and nothing of what they held. `cs` is what the recorder is handed.
//
// # And on the stream, as the erasure it is
//
// The same transaction puts the person in the outbox through `q` (#214), so an
// app following the stream hears that they are gone -- which is news about
// somebody nobody had erased, the person who asked to be destroyed, and the
// erasure said again about anybody else. A write past the servers is published
// by nothing else: what publishes a write is the call that made it, and this is
// none. Nil is a deployment that publishes nowhere; see [heard].
//
// # By name, and of the person as they are when it destroys
//
// This is the forget somebody asked for: `roster forget @tenant/who`. It is
// made of anybody, erased or not, in a tenant held or not ([Hold] says what a
// hold keeps of it). What it decides on is the row as it is inside the
// transaction that destroys ([forget]), with one refusal: somebody it found
// erased who has been restored since. A restore and a forget of one person at
// once were each made on what they had read, and left somebody who was
// neither -- not erased, with no name and no rows (#280). Whichever of the
// two comes second is told what the first did: a restore that they are
// forgotten ([Restore]), and this that they are back. It is not made on what
// was true a moment ago, because it is the one of the two that cannot be
// taken back; asked again, of the person as they are, it is made.
func Forget(ctx context.Context, db *ent.Client, who pdid.Id, p trail.Policy, cs audit.Contents, q bare.Recorder) (Result, error) {
	was, err := db.Holder.Get(ctx, who.Uuid())
	if err != nil {
		return Result{}, err
	}

	return forget(ctx, db, who, p, cs, q, func(v *ent.Holder, _ *ent.Tenant) error {
		if was.DateErased != nil && v.DateErased == nil {
			return fmt.Errorf("%s was restored while this was being decided, and is left as they are: forgetting somebody is not taken back, "+
				"so it is not made on what was true a moment ago. Ask again if they are still to be forgotten", who)
		}

		return nil
	})
}

// ErrNotDue is somebody a pass came to who is no longer due: restored, or of
// a tenant an operator holds, since the pass read who was. They are left as
// they are, and it is not a failure.
var ErrNotDue = errors.New("no longer due")

// due is what a forget made on the clock asks again: that the person is what
// [Due] found them to be. Erased, and since before `before` -- somebody
// restored and erased again has a grace that began again; not forgotten
// already, by another pass or by name; and of a tenant nobody holds.
func due(before time.Time) asked {
	return func(v *ent.Holder, in *ent.Tenant) error {
		switch {
		case v.DateErased == nil:
			return fmt.Errorf("%w: restored since the pass read who was", ErrNotDue)
		case !v.DateErased.Before(before):
			return fmt.Errorf("%w: restored and erased again since the pass read who was, and their grace runs from then", ErrNotDue)
		case v.Alias == "":
			return fmt.Errorf("%w: forgotten already", ErrNotDue)
		case in.DateHeld != nil:
			return fmt.Errorf("%w: their tenant is held since %s", ErrNotDue, in.DateHeld.Format(time.RFC3339))
		}

		return nil
	}
}

// asked is what a forget asks again of somebody once their row is its own:
// the row and their tenant's, as they are inside the transaction that
// destroys. Nil goes on, and anything else leaves the person as they are.
type asked func(v *ent.Holder, in *ent.Tenant) error

// forget is [Forget] on the condition `still`.
//
// # It asks again, inside the transaction that destroys
//
// A pass read who was due once and forgot each; a tenant's destruction asked
// its hold before each person and not in the write; and nothing was asked of
// the person at all. So somebody restored after a pass began was forgotten by
// it, having been told they were back, and a hold placed after it began
// stopped nobody of it (#280). What a forget is made on is asked where it
// cannot have moved by the time the write lands: of the person's row once the
// transaction has it for itself ([mine]), and of their tenant's read after
// that.
func forget(ctx context.Context, db *ent.Client, who pdid.Id, p trail.Policy, cs audit.Contents, q bare.Recorder, still asked) (Result, error) {
	var out Result

	tx, err := db.Tx(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	v, in, err := mine(ctx, tx.Client(), who)
	if err != nil {
		return out, err
	}
	if err := still(v, in); err != nil {
		return out, err
	}
	if in.DateHeld != nil {
		out.Under = &Holding{Tenant: pdid.Id(in.Id), Alias: in.Alias, Since: *in.DateHeld, Why: in.Hold}
	}

	// Every identifier whose trail rows are about this person. Not the holder
	// alone: `Audit.value` for a write to an `Email` row holds the address, and
	// that row's `object_id` is the email's. A pass that named only the holder
	// would blank the record of the person and leave every address they ever
	// had in the trail beside it.
	objects := []pdid.Id{who}
	removed := []pdid.Id{}

	for _, of := range subjects() {
		ids, err := of.ids(ctx, tx.Client(), who.Uuid())
		if err != nil {
			return out, fmt.Errorf("%s: %w", of.name, err)
		}
		for _, id := range ids {
			removed = append(removed, pdid.Id(id))
		}

		n, err := of.remove(ctx, tx.Client(), who.Uuid())
		if err != nil {
			return out, fmt.Errorf("%s: %w", of.name, err)
		}

		out.Rows += n
	}
	objects = append(objects, removed...)

	// The row itself stays and stops naming anybody. Through ent, like every
	// other write here: the servers refuse most of this, and the ones that do
	// not would record it -- and a record of *what was destroyed* is the thing
	// being destroyed.
	//
	// Every column the schema declares personal, in the one update: the six a
	// holder had when this was written, and their picture and what their
	// directory called them, which came later and stayed on the row until
	// somebody read one (#279). `TestSomebodyForgottenIsABlankRow` holds the
	// list to the schema, so the next column declared personal is cleared here
	// or fails there.
	u := tx.Holder.UpdateOneId(who.Uuid()).
		SetAlias("").
		SetName("").
		SetDesc("").
		SetLabels(map[string]string{}).
		ClearProfile().
		ClearData().
		ClearPortrait().
		ClearDirectorySubject().
		ClearDirectoryAddress().
		ClearDirectoryName().
		SetDateUpdated(time.Now())
	if v.DateErased == nil {
		// Asked for by somebody who had not left first. Leaving is implied by
		// the request, and it is the state everything else already assumes.
		// Decided on the row as this transaction holds it, and not on a read
		// before it: a restore in between is somebody live, who is erased here
		// as anybody live is.
		u = u.SetDateErased(time.Now())
	}
	if err := u.Exec(ctx); err != nil {
		return out, err
	}

	out.Rows++

	// Under their tenant, said by the context: a line that says a row went has
	// no row to read a tenant from (`expire.Under`).
	if _, err := pd.RecordErased(expire.Under(ctx, pdid.Id(v.TenantId)), tx.Client(), cs, ForgottenHolder, removed); err != nil {
		return out, err
	}
	if _, err := pd.Record(ctx, tx.Client(), cs, ForgottenHolder, []pdid.Id{who}); err != nil {
		return out, err
	}
	if err := heard(ctx, q, tx.Client(), ForgottenHolder, app.HolderService_Erase_FullMethodName, who); err != nil {
		return out, err
	}

	// The trail's, in the same transaction: its rows in the database
	// blanked, and the archive's half owed.
	got, err := p.Forget(ctx, pd.TrailStore(tx.Client()), objects)
	if err != nil {
		return out, fmt.Errorf("trail: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.Trail = got.Rows
	out.Held = got.Held
	out.Owed = got.Owed

	// And the archive's half, paid now that it is owed: this erasure's alone,
	// so that what it answers is theirs. A payment that fails has destroyed
	// nothing less -- the debt is written, and the next pass pays it.
	paid, err := p.Pay(ctx, pd.TrailStore(db), objects...)
	out.Archived = paid.Archived
	if err != nil {
		log.From(ctx).WarnContext(ctx, "forget: the archive's half is owed, for the next pass to pay", "holder", who.String(), "err", err)

		return out, nil
	}

	// What paying answers is both halves of it, the database's reached again.
	out.Held = paid.Held
	out.Owed = paid.Owed

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
//
// Written past the servers, as [Forget] is, and put on the trail as the act it
// is in the same transaction (payday#108): the row is live again, and a check
// of the rows finds it as the restore left it. And on the stream through `q`,
// in the same transaction, as [Forget] puts itself there (#214): an app that
// marked them as leaving hears them back when they are, rather than at the
// next thing that happens to them.
//
// # It gives back the account, and not the sign-ins
//
// An erase stops everything issued to somebody, and clearing it started all of
// it again: a delegation, a recovery link inside its quarter hour, a session
// at a console, each as it was before an operator erased the account -- which
// is as often because somebody else was in it as by mistake. Nothing could be
// done about them first, since somebody erased is read by no server, and
// `Invalidate` of them is `NotFound`. Somebody who left on their own had no
// such thing to come back to: `MeService/Leave` signs out everywhere before it
// erases (#279).
//
// So a restore stamps `date_invalidated`, the moment everything issued before
// it is void, as `Invalidate` does and in the write that brings them back.
// Now, and taken from nobody, for `Invalidate`'s reason. What comes back is
// the rows: the name, the ways in, what they may do. Not the roles, the
// groups, the bindings and the keys that were of them as an app (`Role.app`,
// `Group.app`, `Binding.app`, `ApiKey.app`, whoever's key): those were erased
// with them, as their own act, and stay erased. A key of their own comes back
// too --
// it is not ended by signing somebody out everywhere, on purpose
// (`holder.ext.proto`, `date_invalidated`) -- and is an operator's to revoke,
// which they can while its holder is erased.
//
// # And it says which name is taken
//
// An alias is one person's among the living of a tenant, so an erase frees it
// at once and somebody new may have it by the time this runs. The index
// refused the write, and what an operator read was the store's own sentence
// about a constraint. [taken] says it in words, before anything is written.
//
// # Written on the condition it was decided on
//
// The questions above are a read, and the write cleared `date_erased` with no
// condition. A forget that committed between the two was undone by half:
// somebody not erased, with no name and no rows, whom this had just called
// back (#280). So the write carries what was decided on -- erased, and not
// forgotten -- and is no write at all when either has moved, as a tenant's
// restore is (`server/core/close.go`). Then the row is read again and the
// answer is the one it would have been given a moment later: forgotten, or
// restored already.
//
// # And not into a tenant that is destroyed
//
// A tenant's destruction forgets its people one at a time, and a hold placed
// while it runs stops it with some of them still there. One of those who had
// been erased was restored: back, in a tenant stamped destroyed, where no
// door lets anybody in and nothing is written (`core.living`), to be
// forgotten with the rest the moment the hold lifts (#280). An account is
// somebody of a tenant, and there is none for them to be of. Refused in
// words ([standing]), and part of the write's condition.
func Restore(ctx context.Context, db *ent.Client, who pdid.Id, cs audit.Contents, q bare.Recorder) error {
	v, err := db.Holder.Get(ctx, who.Uuid())
	if err != nil {
		return err
	}
	if err := restorable(who, v); err != nil {
		return err
	}
	if err := standing(ctx, db, who, v); err != nil {
		return err
	}
	if err := taken(ctx, db, v); err != nil {
		return err
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	n, err := tx.Holder.Update().
		Where(
			holder.IdEQ(who.Uuid()), holder.DateErasedNotNil(), holder.AliasNEQ(""),
			holder.HasTenantWith(tenant.DateDestroyedIsNil()),
		).
		ClearDateErased().
		SetDateInvalidated(now).
		SetDateUpdated(now).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// Somebody took a name of theirs between the question and the
			// write. Asked again outside the transaction, which this ends.
			tx.Rollback()
			if said := taken(ctx, db, v); said != nil {
				return said
			}
		}

		return err
	}
	if n == 0 {
		// Forgotten, or restored by somebody else, between the question and
		// the write. Asked again outside the transaction, which this ends.
		tx.Rollback()
		if v, err = db.Holder.Get(ctx, who.Uuid()); err != nil {
			return err
		}
		if err := restorable(who, v); err != nil {
			return err
		}
		if err := standing(ctx, db, who, v); err != nil {
			return err
		}

		return fmt.Errorf("%s changed while this was being decided, and was not brought back; ask again", who)
	}
	if _, err := pd.Record(ctx, tx.Client(), cs, RestoredHolder, []pdid.Id{who}); err != nil {
		return err
	}
	if err := heard(ctx, q, tx.Client(), RestoredHolder, app.HolderService_Patch_FullMethodName, who); err != nil {
		return err
	}

	return tx.Commit()
}

// restorable is whether somebody as they were read can be brought back:
// erased, and not forgotten. What answers the second is the row itself; see
// [Restore].
func restorable(who pdid.Id, v *ent.Holder) error {
	switch {
	case v.DateErased == nil:
		return fmt.Errorf("%s has not been erased, so there is nothing to bring back", who)
	case v.Alias == "":
		return fmt.Errorf("%s has been forgotten; what is left is an identifier, and it names nobody", who)
	}

	return nil
}

// standing refuses a restore of somebody whose tenant is destroyed; see
// [Restore].
func standing(ctx context.Context, db *ent.Client, who pdid.Id, v *ent.Holder) error {
	in, err := db.Tenant.Get(ctx, v.TenantId)
	if err != nil {
		return err
	}
	if in.DateDestroyed != nil {
		return fmt.Errorf("%s is of a tenant that was destroyed on %s, and nobody is brought back into a tenant that is gone: "+
			"what is left of them there is forgotten with it. A customer that comes back is a new tenant, and its people are new people in it",
			who, in.DateDestroyed.Format(time.RFC3339))
	}

	return nil
}

// mine takes somebody's row for the transaction `tx`, and answers it and
// their tenant's as they are then.
//
// # Why first, and why this lock
//
// What a forget decides on, and what it removes, is read after this. So an
// act that was already writing about the person has committed by then and is
// seen, and one that comes after waits and finds them forgotten.
//
// On PostgreSQL the row is taken `FOR UPDATE`. That is the lock the write
// `server/core` makes on a person's row waits for (`only.go`), and it is also
// the one a foreign key waits for: an insert of a row that names somebody
// holds their row against exactly this until it commits. So a write that has
// already named them is waited for and then removed with everything else of
// theirs; and a write that names them afterwards waits for the forget, is
// made -- the row is still there, blank, so the key is satisfied -- and is
// then asked whether whoever it names was forgotten ([Nobody]). A plain
// update of the row would not do: it waits for other updates and not for a
// key's lock, and a binding written at the same moment would be read by
// neither.
//
// SQLite has one writer at a time, and a transaction is that writer from its
// first statement (`_txlock=immediate`, which `config.DbConfig.Open` asks
// for). There is no row to lock and nothing to wait for.
//
// The tenant is read after the row is held, in a statement of its own: a
// hold placed while this waited for the row is one it finds.
func mine(ctx context.Context, tx *ent.Client, who pdid.Id) (*ent.Holder, *ent.Tenant, error) {
	q := tx.Holder.Query().Where(holder.IdEQ(who.Uuid()))
	q.Modify(func(s *sql.Selector) {
		if tx.Dialect() != dialect.SQLite {
			s.ForUpdate()
		}
	})

	v, err := q.Only(ctx)
	if err != nil {
		return nil, nil, err
	}
	in, err := tx.Tenant.Get(ctx, v.TenantId)
	if err != nil {
		return nil, nil, err
	}

	return v, in, nil
}

// taken is the refusal a restore answers when a name of somebody erased is
// somebody else's now, and nil when every name of theirs is free.
//
// The indexes that keep a name one person's cover the living of a tenant, so
// each of them is free from the erase: the alias, and the three things a
// directory calls somebody. The alias is the one an operator can do something
// about with a command, and the sentence gives the command as the binary
// takes it. Somebody erased is renamed by nothing -- no server reads them --
// so it is whoever has the name now that makes way.
//
// # It names people by identifier, and says no name
//
// It said the alias: *... was @contoso/erin*, to whoever had asked by an
// identifier. A restore is one of the commands that take no `--as` and leave
// no row of the access record, on the ground that they print no field
// anybody's row declares personal (`docs/operating.md`, *What it reads and
// writes of anybody is on the access record*), and an alias is declared so
// (#280). So the sentence says who has
// the name and not what it is, and the commands it gives name them by
// identifier. The first of those is a read with `--as`, which is where the
// name is read by somebody the record names.
func taken(ctx context.Context, db *ent.Client, v *ent.Holder) error {
	who := pdid.Id(v.Id)
	living := func(p predicate.Holder) (pdid.Id, bool, error) {
		id, err := db.Holder.Query().
			Where(holder.TenantId(v.TenantId), holder.DateErasedIsNil(), holder.IdNEQ(v.Id), p).
			FirstId(ctx)
		if ent.IsNotFound(err) {
			return pdid.Nil, false, nil
		}

		return pdid.Id(id), err == nil, err
	}

	has, ok, err := living(holder.AliasEQ(v.Alias))
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("the alias %[1]s had is %[2]s's now: an erase frees an alias at once, and it is one person's in a tenant. "+
			"Somebody erased is renamed by nothing, so whoever has the name makes way. Read the version a rename takes with "+
			"`roster --as <alias> holder get -o json %[2]s`, rename them with "+
			"`roster --as <alias> holder realias %[2]s '{\"alias\":\"<another>\",\"date_updated\":\"<its dateUpdated>\"}'`, and restore %[1]s again",
			who, has)
	}

	for _, d := range []struct {
		column string
		said   *string
		is     func(string) predicate.Holder
	}{
		{holder.FieldDirectoryName, v.DirectoryName, holder.DirectoryNameEQ},
		{holder.FieldDirectorySubject, v.DirectorySubject, holder.DirectorySubjectEQ},
		{holder.FieldDirectoryAddress, v.DirectoryAddress, holder.DirectoryAddressEQ},
	} {
		if d.said == nil {
			continue
		}

		has, ok, err := living(d.is(*d.said))
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("what their directory called %[1]s (`%[2]s`) is what it calls %[3]s now, and it calls one person of a tenant that. "+
				"Which of the two the directory means is an operator's to say: erase the other with "+
				"`roster --as <alias> holder erase %[3]s` and restore %[1]s again, or leave %[1]s erased",
				who, d.column, has)
		}
	}

	return nil
}

// heard puts a person's row on the stream apps follow, as the act `act` names:
// queued through `q` on `db`, the transaction that wrote it, for the serving
// process's drain to publish once that has committed -- and gone with it if it
// does not.
//
// `by` is the kind of write it was, named as the generated server that would
// have made it is: a stream knows whose row an event is about by the service
// that wrote it, and reads the row again to say what it is now, so the name is
// the entity and what happened, and the row is the news. `act` is what was
// asked for, as the trail has it.
//
// Nil queues nothing, and is a deployment that publishes nowhere
// (`watch.broker: none`): a row there is a row nothing drains.
func heard(ctx context.Context, q bare.Recorder, db *ent.Client, act, by string, who pdid.Id) error {
	if q == nil {
		return nil
	}

	return q.Record(ctx, bare.Server{Store: bare.Store{Db: db}}, bare.Change{Method: act, By: by, Key: who.Uuid()})
}

// Due is everybody whose grace has run out.
//
// Those already destroyed are left out by their empty alias rather than by a
// column saying so. [Forget] is idempotent, so including them would be correct
// and would mean walking the whole history of a deployment's leavers on every
// pass.
//
// And so is everybody of a tenant an operator holds: a hold means *keep this
// as it is*, and a clock running out under it is not somebody asking. They are
// forgotten on the first pass after it is lifted. See [Hold].
func Due(ctx context.Context, db *ent.Client, before time.Time) ([]pdid.Id, error) {
	vs, err := db.Holder.Query().
		Where(
			holder.DateErasedNotNil(),
			holder.DateErasedLT(before),
			holder.AliasNEQ(""),
			holder.HasTenantWith(tenant.DateHeldIsNil()),
		).
		Ids(ctx)
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

	// table and tenant are a tenant's kind's table and the column of it that
	// says which tenant a row is of, where its rows say so themselves: see
	// [tenanted]. Empty for a person's.
	table, tenant string
}

func subjects() []subject {
	return []subject{
		// The links, before the addresses, and by both of a link's edges: the
		// ones that act for this person, and the ones that name an address of
		// theirs whoever they are for.
		//
		// A link that proves an address is told from one that signs somebody
		// in by the address it names (`vouch.Server.link`,
		// `email_svc.ext.proto`), and `link_email_email` is `ON DELETE SET
		// NULL`. So a claim of one of their addresses -- somebody else's link,
		// in a mailbox for a quarter of an hour -- named nothing once the
		// address below was removed, and read as a way in for whoever claimed
		// it: `VouchService/Redeem` answered a delegation for a token it had
		// refused a moment before (#279). What a link proves is gone with the
		// address, so the link goes with it, and first, while the edge still
		// says which links those are.
		{
			name: "link",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Link.Query().Where(linked(k)).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Link.Delete().Where(linked(k)).Exec(ctx)
			},
		},
		// The ways in. Each of these exists to say *this person reaches here* or
		// *signs in there*, and each is worthless without them.
		{
			name: "email",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Email.Query().Where(email.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Email.Delete().Where(email.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		// Where they are reached, which is as much theirs: a number is
		// personal, and the trail's copy of one goes with it.
		{
			name: "phone",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Phone.Query().Where(phone.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Phone.Delete().Where(phone.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		// What they are to somebody, and what somebody is to them. Both ends:
		// a relation to a person who is gone says something about nobody, and
		// who they were to anybody is theirs as much as who anybody was to
		// them.
		{
			name: "relation",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Relation.Query().Where(relation.Or(
					relation.HasHolderWith(holder.IdEQ(k)), relation.HasOtherWith(holder.IdEQ(k)),
				)).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Relation.Delete().Where(relation.Or(
					relation.HasHolderWith(holder.IdEQ(k)), relation.HasOtherWith(holder.IdEQ(k)),
				)).Exec(ctx)
			},
		},
		{
			name: "identity",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Identity.Query().Where(identity.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Identity.Delete().Where(identity.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		{
			name: "credential",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Credential.Query().Where(credential.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Credential.Delete().Where(credential.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		// The credentials issued to them, and the half-finished sign-ins. These
		// expire anyway; taking them now is the difference between *cannot sign
		// in* and *whatever was already issued still works*.
		{
			name: "api key",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.ApiKey.Query().Where(apikey.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.ApiKey.Delete().Where(apikey.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		{
			name: "session",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Session.Query().Where(session.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Session.Delete().Where(session.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		{
			name: "delegation",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Delegation.Query().Where(delegation.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Delegation.Delete().Where(delegation.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		{
			name: "continuation",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Continuation.Query().Where(continuation.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Continuation.Delete().Where(continuation.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		// And what they may do. A destroyed person holding permissions is a row
		// waiting to be a surprise.
		//
		// And there is an asymmetry underneath worth knowing about, which
		// nothing else states: of the twenty-one foreign keys into `holder`,
		// sixteen are `NO ACTION`, and `binding_holder_holder`,
		// `binding_holder_app`, `role_holder_app`, `group_holder_app` and
		// `apikey_holder_app` are `SET NULL` -- see
		// `internal/ent/migrate/schema.go`. Nothing depends on it today,
		// because an erase is an UPDATE and no `ON DELETE` rule fires. It
		// would matter the day somebody builds a **hard** erase of a person:
		// sixteen children would block it, `Binding` would quietly keep a row
		// whose holder is NULL, and a role, a group, a binding or a key of the
		// app they were would become one of roster's own or the tenant's
		// (`Role.app`, `Group.app`, `Binding.app` -- a grant across the tenant
		// -- and `ApiKey.app`, a key for that app that would be a credential
		// at roster). Removing the bindings here is what makes the first
		// irrelevant either way; the rest is the day's to settle.
		{
			name: "binding",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Binding.Query().Where(binding.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Binding.Delete().Where(binding.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		// Who an app is answered as in this tenant, when the answer is them.
		// A nomination of a forgotten holder is a deployment key still acting
		// as a blank pseudonym -- with nothing bound to it once the row above is
		// gone, so it would do nothing, and that is the problem: the app is
		// refused everything there and nothing says why. Gone, the key is
		// refused at the nomination, which is the refusal that names the cause.
		{
			name: "nomination",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.Nomination.Query().Where(nomination.HasActsAsWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.Nomination.Delete().Where(nomination.HasActsAsWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		{
			name: "site membership",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.SiteMembership.Query().Where(sitemembership.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.SiteMembership.Delete().Where(sitemembership.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		{
			name: "team membership",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.TeamMembership.Query().Where(teammembership.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.TeamMembership.Delete().Where(teammembership.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
		{
			name: "group membership",
			ids: func(ctx context.Context, db *ent.Client, k uuid.UUID) ([]uuid.UUID, error) {
				return db.GroupMembership.Query().Where(groupmembership.HasHolderWith(holder.IdEQ(k))).Ids(ctx)
			},
			remove: func(ctx context.Context, db *ent.Client, k uuid.UUID) (int, error) {
				return db.GroupMembership.Delete().Where(groupmembership.HasHolderWith(holder.IdEQ(k))).Exec(ctx)
			},
		},
	}
}

// linked is the links that go with a person: the ones that are theirs, and
// the ones that name an address of theirs, which are somebody else's when the
// address was claimed. See [subjects].
func linked(k uuid.UUID) predicate.Link {
	return link.Or(
		link.HasHolderWith(holder.IdEQ(k)),
		link.HasEmailWith(email.HasHolderWith(holder.IdEQ(k))),
	)
}

// Swept is how often the grace is checked when nothing says otherwise.
//
// Daily, for `trail.Swept`'s reason: what this period decides is only how far
// past the window somebody may sit, and a day is invisible against thirty of
// them.
const Swept = 24 * time.Hour

// Lapsed is what a pass did about one person it found due.
type Lapsed struct {
	Who pdid.Id

	// Result is what forgetting them did, when they were forgotten.
	Result

	// Err is why they were not: [ErrNotDue] for somebody restored, or of a
	// tenant held, since the pass read who was due -- left as they are, and
	// no failure -- and anything else for a forget that failed, which the
	// next pass makes again.
	Err error
}

// Pass forgets everybody whose grace ran out before `before`, and answers
// what it did about each: what a tick of [Sweep] does, and what `roster
// forget` with no name does about people.
//
// It reads who is due once, and that list is a moment that has passed by the
// time it comes to each of them. So each is asked again, in the transaction
// that destroys them, whether they are still what [Due] found ([due]): a
// restore made while the pass ran is somebody the pass leaves, and so is
// everybody of a tenant held while it ran.
//
// A person that fails does not stop it. Each is their own act, and what one
// of them could not have done to them says nothing of the next.
func Pass(ctx context.Context, db *ent.Client, before time.Time, p trail.Policy, cs audit.Contents, q bare.Recorder) ([]Lapsed, error) {
	vs, err := Due(ctx, db, before)
	if err != nil {
		return nil, err
	}

	out := make([]Lapsed, 0, len(vs))
	for _, who := range vs {
		res, err := forget(ctx, db, who, p, cs, q, due(before))
		out = append(out, Lapsed{Who: who, Result: res, Err: err})
	}

	return out, nil
}

// Sweep destroys everybody whose grace has run out, on a clock.
//
// It is the same **kind** of loop the trail's retention is and not the kind the
// expiry sweeps are: nothing else applies this window, so an outage of it is a
// deployment holding what it told somebody it would destroy. Every pass that
// fails says so rather than being counted.
//
// A pass that fails part way has still destroyed whoever it reached, because
// each person is their own act -- there is no batch here to be half of. Each
// is put on the stream through `q`, as [Forget] says. What a pass is, and
// what it asks again of each person, is [Pass].
func Sweep(db *ent.Client, after time.Duration, p trail.Policy, cs audit.Contents, q bare.Recorder, every time.Duration) spin.Func {
	if every <= 0 {
		every = Swept
	}

	return spin.Every(every, func(ctx context.Context) error {
		before := time.Now().Add(-after)

		vs, err := Pass(ctx, db, before, p, cs, q)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "forget: who is due", "err", err, "before", before)

			return nil
		}
		for _, v := range vs {
			switch {
			case errors.Is(v.Err, ErrNotDue):
				log.From(ctx).InfoContext(ctx, "forget: left, having been due when the pass began", "holder", v.Who.String(), "why", v.Err)

				continue

			case v.Err != nil:
				log.From(ctx).WarnContext(ctx, "forget", "holder", v.Who.String(), "err", v.Err)

				continue
			}

			log.From(ctx).InfoContext(ctx, "forget",
				"holder", v.Who.String(), "rows", v.Rows, "trail", v.Trail, "archived", v.Archived,
				"held", v.Held.Rows, "owed", v.Owed)
		}

		return nil
	})
}
