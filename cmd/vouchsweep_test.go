package cmd_test

import (
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"

	entcontinuation "github.com/lesomnus/roster/internal/ent/continuation"
	entlink "github.com/lesomnus/roster/internal/ent/link"
	"github.com/lesomnus/roster/server/vouch"
)

// Somebody has to collect the attempts and the links, and `vouch.Collect` is
// what does -- untested until here, while `cmd/serve.go` runs it on a timer
// beside `keys.Sweep` and `session.Sweep`.
//
// The two failures it sits between are both quiet. Collecting nothing grows a
// table by one row per two-factor sign-in and per mail sent, forever, and is
// noticed as disk. Collecting one row too many is worse and shows up nowhere: a
// live continuation deleted mid-sign-in is one person refused at the second
// form, once, with `no()` -- the same answer a wrong code gets, because every
// unusable handle is deliberately one answer. There is no log line to find
// afterwards and no row left to look at.
//
// Which is why what these pin is the *predicate*, both halves of it, on both
// tables: `date_expires < now`, and nothing else.

// TestExpiredAttemptsAndLinksAreCollectedAndLiveOnesAreNot is one pass over
// both tables, and the two directions it has to get right.
//
// Collecting too little is a table that grows, which somebody eventually
// notices. Collecting too much is a live attempt or a live link taken out from
// under the person holding it -- a second form that says no to a correct code,
// or a recovery link that was sent and does not work, each once and with
// nothing anywhere saying why. So the live rows are asserted as hard as the
// expired ones, and the live link is asserted by **spending** it: a row that
// is still in the table and no longer redeemable would pass a row count.
func TestExpiredAttemptsAndLinksAreCollectedAndLiveOnesAreNot(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, link, redeem)
	ctx := t.Context()

	who := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	by := issuerOf(t, ctx, b)

	// Written through `Ungated` rather than minted, because neither RPC will
	// make an expired one: `Link` refuses an expiry that is not in the future
	// and a continuation's lifetime is a constant with no field to say
	// otherwise. What is under test is the collector, so the rows are stated
	// directly.
	live, deadC := sweepable(t), sweepable(t)
	for _, tc := range []struct {
		secret  []byte
		expires time.Time
	}{
		{live, time.Now().Add(time.Hour)},
		{deadC, time.Now().Add(-time.Minute)},
	} {
		_, err := b.Ungated.Continuation().Add(ctx, app.ContinuationAddRequest_builder{
			Holder:      who,
			Secret:      tc.secret,
			Issuer:      by,
			DateExpires: timestamppb.New(tc.expires),
		}.Build())
		x.NoError(err)
	}

	// A link that is still deliverable, minted the way one really is, so that
	// the row this claims survives is a row somebody could actually be holding
	// -- and is spendable afterwards, which is the half a count cannot state.
	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, b.Token)

	made, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	deadL := sweepable(t)
	_, err = b.Ungated.Link().Add(ctx, app.LinkAddRequest_builder{
		Holder:      who,
		Secret:      deadL,
		Issuer:      by,
		DateExpires: timestamppb.New(time.Now().Add(-time.Minute)),
	}.Build())
	x.NoError(err)

	// Two, and not one: both tables, one pass. They are swept together because
	// they are the same fact one shape apart, and a collector that quietly
	// stopped after the first table would leave the other growing with nothing
	// to say it had.
	n, err := vouch.Collect(ctx, b.Ent)
	x.NoError(err)
	x.Equal(2, n, "the expired pair was not collected, or a live row went with it")

	x.False(hasContinuation(t, b, deadC), "an expired attempt outlived the sweep")
	x.False(hasLink(t, b, deadL), "an expired link outlived the sweep")
	x.True(hasContinuation(t, b, live), "a live attempt was collected")

	// And the one nobody can see from a count: the live link still opens the
	// door. This is the whole failure being guarded against -- a predicate that
	// took one row too many refuses somebody exactly once, with the same answer
	// a forged token gets.
	res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
		Token:   made.GetToken(),
		Methods: []string{redeem},
	}.Build())
	x.NoError(err)
	x.True(res.GetVerified().GetOk(), "a live link was collected out from under the person holding it")

	// Idempotent, which is what lets every replica run it on its own timer
	// without any of them coordinating.
	n, err = vouch.Collect(ctx, b.Ent)
	x.NoError(err)
	x.Zero(n)
}

// TestASpentAttemptIsCollectedOnItsOwnClockAndNotBefore is what makes the
// predicate `date_expires` alone, on the table where spending is an erase.
//
// `spend` erases softly, so a continuation that has been used is out of reach
// and still present -- the same shape a signed-out session has, and the reason
// `Collect` says out loud that it takes spent rows *once they expire* rather
// than at once. That is one pass instead of two over rows that live five
// minutes, and it is only correct while the erase is what puts them out of
// reach. A predicate widened to `date_erased IS NOT NULL` would look like
// tidiness and would be a second mechanism deciding reachability, in a
// background job, where nothing on the read path is asking.
func TestASpentAttemptIsCollectedOnItsOwnClockAndNotBefore(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	secret := sweepable(t)
	v, err := b.Ungated.Continuation().Add(ctx, app.ContinuationAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret:      secret,
		Issuer:      issuerOf(t, ctx, b),
		DateExpires: timestamppb.New(time.Now().Add(time.Hour)),
	}.Build())
	x.NoError(err)

	spent, err := b.Ungated.Continuation().Erase(ctx,
		app.ContinuationRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)
	x.True(spent.GetErased())

	n, err := vouch.Collect(ctx, b.Ent)
	x.NoError(err)
	x.Zero(n, "a spent attempt was collected before its own clock ran out")
	x.True(hasContinuation(t, b, secret), "a spent attempt was collected before its own clock ran out")
}

// sweepable is a secret nothing else in the table has.
//
// The column is unique, so two rows written by hand need two of these; and
// they are what the assertions name the rows by, since an id read back is not
// what the sweep is narrowed on.
func sweepable(t *testing.T) []byte {
	t.Helper()

	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)

	sum := sha256.Sum256(b)

	return sum[:]
}

// hasContinuation and hasLink ask the table directly, because a row the sweep
// should have left is a row that is *there* -- and asking through the server
// would answer `no such continuation` for a row that is present and merely
// spent, which is the distinction the second test is about.
func hasContinuation(t *testing.T, b *keyedBuilt, secret []byte) bool {
	t.Helper()

	ok, err := b.Ent.Continuation.Query().
		Where(entcontinuation.SecretEQ(secret)).
		Exist(t.Context())
	require.NoError(t, err)

	return ok
}

func hasLink(t *testing.T, b *keyedBuilt, secret []byte) bool {
	t.Helper()

	ok, err := b.Ent.Link.Query().
		Where(entlink.SecretEQ(secret)).
		Exist(t.Context())
	require.NoError(t, err)

	return ok
}
