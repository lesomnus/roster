package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
)

// TestAnEraseDestroysNothing, which is the finding this whole thing is for.
//
// `Holder.Erase` writes `date_erased` and the version beside it and stops.
// Nothing cascades, and nothing is destroyed: the row keeps the alias and the
// name, the addresses and the external identities keep theirs, and the trail
// holds a copy of all of it -- including the copy the erase itself wrote, since
// `Audit.value` is the row as the event left it.
//
// So "we deleted them" was not true of anything roster did, and this is the
// test that says so out loud rather than leaving it to be rediscovered.
func TestAnEraseDestroysNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "erin")
	b.identity(t, ctx, who, "github", "gh-erin")
	b.addressOf(t, ctx, who, "erin@acme.example")

	_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)

	v, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	x.NotNil(v.DateErased, "the erase did not happen, so this proves nothing")
	x.Equal("erin", v.Alias, "an erase used to keep the alias and this is what said so")

	n, err := b.Ent.Email.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(n, "the address went with the erase")

	n, err = b.Ent.Identity.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(n, "the external identity went with the erase")

	held := 0
	vs, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	for _, u := range vs {
		if len(u.Value) > 0 {
			held++
		}
	}
	x.NotZero(held, "the trail kept nothing, so this proves nothing")
}

// TestForgettingSomebodyKeepsTheEventAndLosesTheContents.
//
// The act an erase is not. What has to be true afterwards is two things at once
// and they pull against each other: nothing left says who this was, and the
// record that things happened is intact. A version that destroyed the trail
// rows would be a version that let somebody erase the evidence of what was done
// to them by asking to be forgotten.
func TestForgettingSomebodyKeepsTheEventAndLosesTheContents(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "erin")
	id := b.identity(t, ctx, who, "github", "gh-erin")
	b.addressOf(t, ctx, who, "erin@acme.example")

	// Somebody else, whose everything must not move.
	other := b.holder(t, ctx, b.Acme, "bystander")
	b.addressOf(t, ctx, other, "bystander@acme.example")

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	res, err := forget.Forget(ctx, b.Ent, who, "")
	x.NoError(err)
	x.NotZero(res.Rows)
	x.NotZero(res.Trail)

	t.Run("their rows are gone", func(t *testing.T) {
		x := require.New(t)

		n, err := b.Ent.Identity.Query().Count(ctx)
		x.NoError(err)
		x.Zero(n, "an external identity survived")

		n, err = b.Ent.Email.Query().Where().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "their address survived, or somebody else's went with it")
	})

	t.Run("and the row that is left names nobody", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ent.Holder.Get(ctx, who.Uuid())
		x.NoError(err)
		x.Empty(v.Alias)
		x.Empty(v.Name)
		x.Empty(v.IdpSubject)
		x.Nil(v.Profile)
		x.NotNil(v.DateErased)

		// The identifier stays, and that is the point of keeping the row: it is
		// `Audit.actor_id`, and what makes it personal data is that it
		// resolves. Emptied, it is a pseudonym reaching nothing.
		x.Equal(who.Uuid(), v.ID)
	})

	t.Run("the trail keeps every event", func(t *testing.T) {
		x := require.New(t)

		now, err := b.Ent.Audit.Query().Count(ctx)
		x.NoError(err)
		x.Equal(was, now, "the record of what happened was destroyed with the contents")
	})

	t.Run("and loses what they said", func(t *testing.T) {
		x := require.New(t)

		// Their holder row **and** the rows about their identity and address,
		// because `Audit.value` for a write to an `Email` is the address and
		// that row's object is the email's identifier, not the person's.
		for _, k := range []pdid.Id{who, mustId(t, id.GetId())} {
			vs, err := b.Ent.Audit.Query().Where(entaudit.ObjectIDEQ(k.Uuid())).All(ctx)
			x.NoError(err)
			x.NotEmpty(vs, "%s has no trail, so this proves nothing", k)

			for _, u := range vs {
				x.Empty(u.Value, "a write about them still says what it wrote")
				x.Empty(u.Patch, "a document about them survived")
				x.NotEmpty(u.Action, "the event went with the contents")
				x.False(u.DateCreated.IsZero())
			}
		}
	})

	t.Run("and nobody else moved", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ent.Holder.Get(ctx, other.Uuid())
		x.NoError(err)
		x.Equal("bystander", v.Alias)

		vs, err := b.Ent.Audit.Query().Where(entaudit.ObjectIDEQ(other.Uuid())).All(ctx)
		x.NoError(err)
		x.NotEmpty(vs)

		held := 0
		for _, u := range vs {
			if len(u.Value) > 0 {
				held++
			}
		}
		x.NotZero(held, "somebody else's record was blanked")
	})
}

// TestTheGraceCanBeUndoneUntilItIsNot.
//
// A window that cannot be reversed is a delay, not a grace. The reasons the
// window exists -- a mistaken deletion, a compromised account deleting things,
// a dispute -- are all reasons that need the mistake to be undoable, and roster
// had no way to undo an erase at all: `HolderPatchRequest` carries no
// `date_erased`, on purpose, since a caller who could write it could un-erase
// anybody.
func TestTheGraceCanBeUndoneUntilItIsNot(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "erin")

	x.ErrorContains(forget.Restore(ctx, b.Ent, who), "has not been erased")

	_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)

	// Due, and then not, which is the whole of what the window is.
	vs, err := forget.Due(ctx, b.Ent, time.Now().Add(time.Hour))
	x.NoError(err)
	x.Contains(vs, who)

	vs, err = forget.Due(ctx, b.Ent, time.Now().Add(-time.Hour))
	x.NoError(err)
	x.NotContains(vs, who, "somebody inside their grace was due")

	x.NoError(forget.Restore(ctx, b.Ent, who))

	v, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	x.Nil(v.DateErased)
	x.Equal("erin", v.Alias)

	t.Run("and not after they are forgotten", func(t *testing.T) {
		x := require.New(t)

		_, err := forget.Forget(ctx, b.Ent, who, "")
		x.NoError(err)

		x.ErrorContains(forget.Restore(ctx, b.Ent, who), "has been forgotten")

		// And they are not due again, which is what keeps a sweep from walking
		// every leaver a deployment has ever had.
		vs, err := forget.Due(ctx, b.Ent, time.Now().Add(time.Hour))
		x.NoError(err)
		x.NotContains(vs, who)
	})
}

// TestForgettingReachesTheArchiveToo.
//
// A mechanism that stopped at the database would destroy the copy an operator
// can see and leave the copy on the disk beside it. That is not a gap; it is an
// answer that is wrong in the direction that matters.
func TestForgettingReachesTheArchiveToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "erin")
	b.addressOf(t, ctx, who, "erin@acme.example")

	dir := t.TempDir()

	// Everything into the archive first, so that the database holds none of it
	// and the only copy left is the file.
	n, err := trail.Archive(ctx, pd.TrailStore(b.Ent), trail.Kinds{}, time.Now().Add(time.Hour), dir)
	x.NoError(err)
	x.NotZero(n)

	files, err := trail.Files(dir)
	x.NoError(err)

	held := 0
	x.NoError(pd.ReadTrail(files, func(v *app.Audit) error {
		if string(v.GetObjectId()) == string(who.Bytes()) && len(v.GetValue()) > 0 {
			held++
		}

		return nil
	}))
	x.NotZero(held, "the archive holds nothing about them, so this proves nothing")

	res, err := forget.Forget(ctx, b.Ent, who, dir)
	x.NoError(err)
	x.NotZero(res.Archived, "the archive was not reached")

	left := 0
	events := 0
	x.NoError(pd.ReadTrail(files, func(v *app.Audit) error {
		if string(v.GetObjectId()) != string(who.Bytes()) {
			return nil
		}

		events++
		if len(v.GetValue()) > 0 || len(v.GetPatch()) > 0 {
			left++
		}

		return nil
	}))
	x.Zero(left, "an archived row still holds what it said about them")
	x.NotZero(events, "the archived events went as well as their contents")
}
