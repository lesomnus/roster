package cmd_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/flob"

	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
)

// TestAnEraseDestroysNothing, which is the finding this whole thing is for.
//
// `Holder.Erase` writes `date_erased` and the version beside it and stops.
// Nothing cascades, and nothing is destroyed: the row keeps the alias and the
// name, and the addresses and the external identities keep theirs.
//
// So "we deleted them" was not true of anything roster did, and this is the
// test that says so out loud rather than leaving it to be rediscovered.
//
// What changed is the trail. It used to hold a copy of all of it -- including
// the copy the erase itself wrote, since `Audit.value` was the row as the event
// left it. The alias, the address and the identity are personal now
// (payday#92), so the trail holds that each was set and never what to, even
// where it keeps rows: the rows are the only copy, which is what makes
// forgetting somebody a matter of the rows.
func TestAnEraseDestroysNothing(t *testing.T) {
	x := require.New(t)
	// At the content level that keeps the row (payday#92): what is under
	// test is what the trail holds of one.
	b, ctx := build(t, keepingRows)

	who := b.holder(t, ctx, b.Contoso, "erin")
	b.identity(t, ctx, who, "github", "gh-erin")
	b.addressOf(t, ctx, who, "erin@contoso.example")

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
		for _, c := range [][]byte{u.Patch, u.Value} {
			x.NotContains(string(c), "erin@contoso.example", "the trail holds a copy of the address")
			x.NotContains(string(c), "gh-erin", "the trail holds a copy of the identity")
			x.NotContains(string(c), "erin", "the trail holds a copy of the alias")
		}
		if len(u.Value) > 0 {
			held++
		}
	}
	x.NotZero(held, "the trail kept no rows, so the checks above never looked at one")
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
	// At the content level that keeps the row (payday#92): what is under
	// test is what the trail holds of one.
	b, ctx := build(t, keepingRows)

	who := b.holder(t, ctx, b.Contoso, "erin")
	id := b.identity(t, ctx, who, "github", "gh-erin")
	b.addressOf(t, ctx, who, "erin@contoso.example")

	// Somebody else, whose everything must not move.
	other := b.holder(t, ctx, b.Contoso, "bystander")
	b.addressOf(t, ctx, other, "bystander@contoso.example")

	// And an app's key answered as each of them in this tenant.
	for _, h := range []pdid.Id{who, other} {
		_, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			BorrowerId: pdid.New(pd.HolderDomain).Bytes(),
			ActsAs:     app.HolderRef_builder{Id: h.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
	}

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	res, err := forget.Forget(ctx, b.Ent, who, trail.Policy{}, audit.Contents{})
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

		n, err = b.Ent.Nomination.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "an app is still answered as them, or the bystander's nomination went too")
	})

	t.Run("and the row that is left names nobody", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ent.Holder.Get(ctx, who.Uuid())
		x.NoError(err)
		x.Empty(v.Alias)
		x.Empty(v.Name)
		x.Nil(v.Profile)
		x.NotNil(v.DateErased)

		// The identifier stays, and that is the point of keeping the row: it is
		// `Audit.actor_id`, and what makes it personal data is that it
		// resolves. Emptied, it is a pseudonym reaching nothing.
		x.Equal(who.Uuid(), v.Id)
	})

	t.Run("the trail keeps every event", func(t *testing.T) {
		x := require.New(t)

		// And one more for each row it removed or blanked: the act that
		// forgot them, on the trail as what it is (payday#115).
		now, err := b.Ent.Audit.Query().Count(ctx)
		x.NoError(err)
		x.Equal(was+res.Rows, now, "the record of what happened was destroyed with the contents, or what forgot them is not on it")
	})

	t.Run("and loses what they said", func(t *testing.T) {
		x := require.New(t)

		// Their holder row **and** the rows about their identity and address,
		// because `Audit.value` for a write to an `Email` is the address and
		// that row's object is the email's identifier, not the person's.
		for _, k := range []pdid.Id{who, mustId(t, id.GetId())} {
			vs, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(k.Uuid())).All(ctx)
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

		vs, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(other.Uuid())).All(ctx)
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

	who := b.holder(t, ctx, b.Contoso, "erin")

	x.ErrorContains(forget.Restore(ctx, b.Ent, who, audit.Contents{}), "has not been erased")

	_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)

	// Due, and then not, which is the whole of what the window is.
	vs, err := forget.Due(ctx, b.Ent, time.Now().Add(time.Hour))
	x.NoError(err)
	x.Contains(vs, who)

	vs, err = forget.Due(ctx, b.Ent, time.Now().Add(-time.Hour))
	x.NoError(err)
	x.NotContains(vs, who, "somebody inside their grace was due")

	x.NoError(forget.Restore(ctx, b.Ent, who, audit.Contents{}))

	v, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	x.Nil(v.DateErased)
	x.Equal("erin", v.Alias)

	t.Run("and not after they are forgotten", func(t *testing.T) {
		x := require.New(t)

		_, err := forget.Forget(ctx, b.Ent, who, trail.Policy{}, audit.Contents{})
		x.NoError(err)

		x.ErrorContains(forget.Restore(ctx, b.Ent, who, audit.Contents{}), "has been forgotten")

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
	// At the content level that keeps the row (payday#92): what is under
	// test is what the trail holds of one.
	b, ctx := build(t, keepingRows)

	who := b.holder(t, ctx, b.Contoso, "erin")
	b.addressOf(t, ctx, who, "erin@contoso.example")

	archive := flob.NewOsStores(t.TempDir())
	p := trail.Policy{Archive: archive, Signer: pdtest.Signer(t)}

	// Everything into the archive first, so that the database holds none of it
	// and the only copy left is the archive's.
	n, err := trail.Archive(ctx, pd.TrailStore(b.Ent), p, trail.Kinds{}, time.Now().Add(time.Hour))
	x.NoError(err)
	x.NotZero(n)

	held := 0
	x.NoError(trail.Read(ctx, archive, pd.TrailOf(func(v *app.Audit) error {
		if string(v.GetObjectId()) == string(who.Bytes()) && len(v.GetValue()) > 0 {
			held++
		}

		return nil
	})))
	x.NotZero(held, "the archive holds nothing about them, so this proves nothing")

	// A debt somebody else's erasure owes, which is theirs or the pass's to
	// pay: a forget pays its own (payday#115).
	other := pdid.New(pd.HolderDomain)
	x.NoError(pd.TrailStore(b.Ent).Owe(ctx, []pdid.Id{other}))

	res, err := forget.Forget(ctx, b.Ent, who, p, audit.Contents{})
	x.NoError(err)
	x.NotZero(res.Archived, "the archive was not reached")
	x.Zero(res.Owed, "an archive that was paid is said to be owed")
	owed, err := b.Ent.Owed.Query().All(ctx)
	x.NoError(err)
	x.Len(owed, 1, "their debt is still owed, or somebody else's was paid")
	x.Equal(other.Uuid(), owed[0].Object, "somebody else's debt was paid, and theirs is still owed")

	left := 0
	events := 0
	x.NoError(trail.Read(ctx, archive, pd.TrailOf(func(v *app.Audit) error {
		if string(v.GetObjectId()) != string(who.Bytes()) {
			return nil
		}

		events++
		if len(v.GetValue()) > 0 || len(v.GetPatch()) > 0 {
			left++
		}

		return nil
	})))
	x.Zero(left, "an archived row still holds what it said about them")
	x.NotZero(events, "the archived events went as well as their contents")
}

// TestForgettingIsOneTransactionWithTheTrail (payday#115).
//
// The person's rows, the holder blanked and the trail's rows about them in the
// database are one transaction, and the archive's half is owed in it. So a
// trail that cannot be erased takes the rest with it: nothing of theirs is
// removed or blanked, nothing is owed, and nothing on the trail says they were
// forgotten. A forget that committed first and erased the trail after left
// their rows gone and the trail saying who they were whenever the second step
// did not happen.
func TestForgettingIsOneTransactionWithTheTrail(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, keepingRows)

	who := b.holder(t, ctx, b.Contoso, "erin")
	b.addressOf(t, ctx, who, "erin@contoso.example")

	said := func() int {
		n, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(who.Uuid()), entaudit.ValueNEQ([]byte{})).Count(ctx)
		x.NoError(err)

		return n
	}
	x.NotZero(said(), "the trail says nothing about them, so this proves nothing")
	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	// The trail's turn refused, so its erasure fails inside the transaction.
	_, err = forget.Forget(ctx, b.Ent, who, trail.Policy{Lease: refusing{}}, audit.Contents{})
	x.ErrorIs(err, errRefused)

	v, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	x.Equal("erin", v.Alias, "a forget that failed blanked the holder")
	n, err := b.Ent.Email.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(n, "a forget that failed removed their address")
	x.NotZero(said(), "a forget that failed blanked the trail")
	now, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(was, now, "a forget that failed is on the trail")
	owed, err := b.Ent.Owed.Query().Count(ctx)
	x.NoError(err)
	x.Zero(owed, "a debt outlived the transaction that owed it")
}

// refusing is a lease that holds nothing: the trail's turn, refused.
type refusing struct{}

var errRefused = errors.New("refused")

func (refusing) Hold(context.Context, string) (context.Context, func(), error) {
	return nil, nil, errRefused
}

// TestAnArchiveNotPaidIsOwedToTheNextPass (payday#115).
//
// The archive takes no part in the transaction, so its half is paid after the
// commit -- and a payment that fails has destroyed nothing less. The person is
// forgotten, their debt is written, the forget says what is owed rather than
// failing, and the trail's next pass pays it.
func TestAnArchiveNotPaidIsOwedToTheNextPass(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, keepingRows)

	who := b.holder(t, ctx, b.Contoso, "erin")
	b.addressOf(t, ctx, who, "erin@contoso.example")

	archive := flob.NewOsStores(t.TempDir())
	p := trail.Policy{Archive: archive, Signer: pdtest.Signer(t)}
	_, err := trail.Archive(ctx, pd.TrailStore(b.Ent), p, trail.Kinds{}, time.Now().Add(time.Hour))
	x.NoError(err)
	archived := func() int {
		n := 0
		x.NoError(trail.Read(ctx, archive, pd.TrailOf(func(v *app.Audit) error {
			if string(v.GetObjectId()) == string(who.Bytes()) && (len(v.GetValue()) > 0 || len(v.GetPatch()) > 0) {
				n++
			}

			return nil
		})))

		return n
	}
	x.NotZero(archived(), "the archive holds nothing about them, so this proves nothing")

	// With no key the transaction's half needs none and a payment is refused
	// (`trail.ErrNoKey`): any payment that fails after the commit.
	keyless := p
	keyless.Signer = nil
	res, err := forget.Forget(ctx, b.Ent, who, keyless, audit.Contents{})
	x.NoError(err, "a payment that failed failed the forget, which had committed")
	x.Positive(res.Owed, "what is owed is not said")
	x.Zero(res.Archived)

	v, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	x.Empty(v.Alias, "the person was not forgotten")
	x.NotZero(archived(), "the archive was paid by a payment that failed, so this proves nothing")
	owed, err := b.Ent.Owed.Query().Count(ctx)
	x.NoError(err)
	x.Equal(res.Owed, owed, "what is owed is not what it said")

	p.Pass(ctx, pd.TrailStore(b.Ent))
	x.Zero(archived(), "the next pass did not pay what was owed")
	owed, err = b.Ent.Owed.Query().Count(ctx)
	x.NoError(err)
	x.Zero(owed, "a debt paid is still owed")
}
