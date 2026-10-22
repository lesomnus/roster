package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAPersonChangesTheirOwnPassword is the self-service password change as the
// line has it (CLAUDE.md, *no self-only twin of a verb*): no verb of its own.
// A person calls `Credential.Set` with their **own** reference -- the same verb
// an operator calls about somebody else -- and the layer asks them for the
// password they hold before writing the one they chose.
//
// It proves the whole reopening at once: `CredentialService` is served for
// `Set` while its `Get` -- the verifier read D13 closed it for -- stays shut;
// the change is gated by reauth, not by which credential presents it; the new
// password is the one that works afterwards; and a wrong `current` costs what a
// wrong sign-in costs, so it cannot be guessed at any faster.
func TestAPersonChangesTheirOwnPassword(t *testing.T) {
	const set = "/roster.CredentialService/Set"

	x := require.New(t)
	b := keyFor(t, set)
	ctx := t.Context()

	const old, next = "correct horse battery staple", "a whole new set of words entirely"

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()

	// Alice has a password, set the operator way: no frame, so nobody's own row
	// and nothing to prove.
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    own,
		Secret: []byte(old),
	}.Build())
	x.NoError(err)

	// A role that names Set, and her own key holding it. Both are gates: the
	// key's list is checked by `auth`, the holder's role by the policy. What
	// neither says is *whose row* -- that is the layer's, below.
	permits(t, ctx, b, b.Contoso, b.Who, "self", set)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{set}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	t.Run("her own row, without the current password, is refused", func(t *testing.T) {
		x := require.New(t)

		// The whole point: a credential that merely acts as her -- this key,
		// were it lifted from a build log -- can name her row and still not
		// replace what she signs in with.
		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:    own,
			Secret: []byte(next),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("the wrong current password is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:     own,
			Current: []byte("not it"),
			Secret:  []byte(next),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("the right current password changes it", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:     own,
			Current: []byte(old),
			Secret:  []byte(next),
		}.Build())
		x.NoError(err)

		// The new one is what verifies now, and the old one is not.
		v := vouch.New(b.Ungated, b.Ungated)
		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(), Secret: []byte(next),
		}.Build())
		x.NoError(err)
		x.True(res.GetOk(), "the new password does not work")

		res, err = v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(), Secret: []byte(old),
		}.Build())
		x.NoError(err)
		x.False(res.GetOk(), "the old password still works")
	})

	t.Run("somebody else's row is not this method at all", func(t *testing.T) {
		x := require.New(t)

		// It was: a role naming `Set` reached anybody no wider than the caller,
		// held there by `mayReach`. That rule is the wrong shape for this one
		// write -- it protects an administrator from a junior and does nothing
		// for an ordinary person, who is narrower than almost everybody -- and
		// a password is the most persistent thing there is to write on a row.
		//
		// So there is one door for it now, and the refusal says which. That
		// `Credential.Issue` works is
		// `TestASecretIsResetForTheAddressThatNamesSomebody` and
		// `TestAResetVoidsWhatCameBeforeIt`; a refusal is only right if the
		// thing it points at is open.
		mate, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "mate",
		}.Build())
		x.NoError(err)
		theirs := app.HolderRef_builder{Id: mate.GetId()}.Build()

		_, err = cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:    theirs,
			Secret: []byte("for mate"),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a walled caller set somebody else's password")
		x.Contains(status.Convert(err).Message(), "Issue",
			"the refusal did not say where that write went")

		// And the deployment's own work is untouched: no frame, nobody to
		// refuse. `roster init` and `roster vouch set` are this.
		_, err = b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    theirs,
			Secret: []byte("for mate"),
		}.Build())
		x.NoError(err, "the unwalled server refused the deployment's own write")
	})

	t.Run("the reopened service still never answers a verifier", func(t *testing.T) {
		x := require.New(t)

		// The read D13 closed the whole service for is still closed by method,
		// even to a key that may call Set on the same service.
		_, err := cl.Get(bearing(ctx, hers), app.CredentialGetRequest_builder{
			Ref: app.CredentialRef_builder{
				Kind: app.CredentialRefByKind_builder{
					Holder: own,
					Kind:   ptr("password"),
				}.Build(),
			}.Build(),
		}.Build())
		x.Error(err, "CredentialService.Get answered over the wire")
		x.NotEqual(codes.OK, status.Code(err))
	})

	t.Run("guessing the current password locks the account like guessing at a sign-in", func(t *testing.T) {
		x := require.New(t)

		// `ChangeMine` compared without counting, so a lifted delegation could
		// guess at leisure. Now each wrong `current` is a wrong sign-in, and
		// after enough of them even the right one is refused until the lock
		// lifts.
		for range vouch.MaxFailures {
			_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
				Ref:     own,
				Current: []byte("still not it"),
				Secret:  []byte("whatever comes next"),
			}.Build())
			x.Equal(codes.PermissionDenied, status.Code(err))
		}

		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:     own,
			Current: []byte(next),
			Secret:  []byte("whatever comes next"),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "the right password got through a lockout")
		x.Contains(status.Convert(err).Message(), "locked")
	})
}

// TestAFirstPasswordOfYourOwnTakesAFreshSignIn is the case with no password to
// prove, and what it asks for instead.
//
// Somebody who arrived through a provider has no password, so the reauth the
// change above rests on has nothing to compare. It was allowed with nothing at
// all, and that let a bearer that merely acts as them -- a session left open, a
// key somebody else minted for them -- set a password they never chose, which
// unlike the bearer does not expire and which they are not told of. So it asks
// for a sign-in a few minutes old, by the credential the call came with, the
// way deleting an account does (`keys.Authenticated`).
func TestAFirstPasswordOfYourOwnTakesAFreshSignIn(t *testing.T) {
	const set = "/roster.CredentialService/Set"

	x := require.New(t)
	b := keyFor(t, set)
	ctx := t.Context()

	const first, next = "correct horse battery staple", "a whole new set of words entirely"

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	cl := app.NewCredentialServiceClient(b.Conn)
	sets := func(as context.Context, current, secret string) error {
		_, err := cl.Set(as, app.CredentialSetRequest_builder{
			Ref:     own,
			Current: []byte(current),
			Secret:  []byte(secret),
		}.Build())

		return err
	}

	// No operator write first: this is somebody a provider vouched for and who
	// has never had a password.
	permits(t, ctx, b, b.Contoso, b.Who, "self", set)
	stale := acting(ctx, b.Token, signedInAt(t, ctx, b, b.Who, []string{set}, time.Now().Add(-2*core.FirstPasswordWithin)))

	t.Run("not from a key with no sign-in of theirs behind it", func(t *testing.T) {
		x := require.New(t)

		k := mintFor(t, ctx, b, b.Who, "laptop", []string{set}, time.Time{})
		err := sets(bearing(ctx, k), "", first)
		x.Equal(codes.FailedPrecondition, status.Code(err), "a key nobody signed in for gave its holder a password")
	})

	t.Run("nor from a sign-in that is not minutes old", func(t *testing.T) {
		x := require.New(t)

		err := sets(stale, "", first)
		x.Equal(codes.FailedPrecondition, status.Code(err), "a session open all afternoon minted a password")
		x.Contains(status.Convert(err).Message(), "sign in again")
	})

	// Neither wrote anything: there is still no password to sign in with.
	v := vouch.New(b.Ungated, b.Ungated)
	res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(), Secret: []byte(first),
	}.Build())
	x.NoError(err)
	x.False(res.GetOk(), "a refused first password signs in")

	t.Run("but from one that is", func(t *testing.T) {
		x := require.New(t)

		fresh := acting(ctx, b.Token, signedInAt(t, ctx, b, b.Who, []string{set}, time.Now()))
		x.NoError(sets(fresh, "", first), "a sign-in a moment ago could not give its holder a password")

		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(), Secret: []byte(first),
		}.Build())
		x.NoError(err)
		x.True(res.GetOk(), "the first password does not sign in")
	})

	// And the door closes behind it: there is something to prove now, so the
	// next change asks for it -- and a change that proves it asks for nothing
	// else, however long ago the caller signed in.
	t.Run("and the next change asks for the one held, from any sign-in", func(t *testing.T) {
		x := require.New(t)

		err := sets(stale, "", next)
		x.Equal(codes.PermissionDenied, status.Code(err), "the second password was replaced with nothing proved")
		x.NoError(sets(stale, first, next))
	})

	// A `current` sent when there is none is ignored rather than refused: a
	// page that sent one has guessed about a row it cannot read, which is not
	// a thing to fail a call over.
	t.Run("a current nobody holds is ignored, not refused", func(t *testing.T) {
		x := require.New(t)

		cx := t.Context()
		c := keyFor(t, set)
		permits(t, cx, c, c.Contoso, c.Who, "self", set)
		theirs := acting(cx, c.Token, signedInAt(t, cx, c, c.Who, []string{set}, time.Now()))

		_, err := app.NewCredentialServiceClient(c.Conn).Set(theirs,
			app.CredentialSetRequest_builder{
				Ref:     app.HolderRef_builder{Id: c.Who.Bytes()}.Build(),
				Current: []byte("nothing is stored under this"),
				Secret:  []byte(first),
			}.Build())
		x.NoError(err)
	})
}

// TestALinkSignInSetsAFirstPasswordAndLeaves is the road for somebody whose
// way in cannot say when it asked them: a recovery link, spent, is a sign-in
// made now (`Vouch.Redeem`), so the person it signs in may give themselves a
// first password, or delete their account, as they could a moment after
// typing one.
func TestALinkSignInSetsAFirstPasswordAndLeaves(t *testing.T) {
	const set = "/roster.CredentialService/Set"

	x := require.New(t)
	b := keyFor(t, link, redeem, set, leaveMe, getMe)
	ctx := t.Context()
	offersLeave(t, ctx, b.Server, b.Contoso, true)
	permits(t, ctx, b, b.Contoso, b.Who, "self", set)

	c := app.NewVouchServiceClient(b.Conn)
	made, err := c.Link(bearing(ctx, b.Token), app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	spent, err := c.Redeem(bearing(ctx, b.Token), app.VouchRedeemRequest_builder{
		Token:   made.GetToken(),
		Methods: []string{set, leaveMe, getMe},
	}.Build())
	x.NoError(err)
	x.True(spent.GetVerified().GetOk())
	as := acting(ctx, b.Token, spent.GetToken())

	_, err = app.NewCredentialServiceClient(b.Conn).Set(as, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err, "a link spent a moment ago could not give its holder a password")

	_, err = app.NewMeServiceClient(b.Conn).Leave(as, app.MeLeaveRequest_builder{Confirm: "someone"}.Build())
	x.NoError(err, "a link spent a moment ago could not delete its holder's account")
	erased, _ := erasedAt(t, ctx, b.Server, b.Who)
	x.NotNil(erased)
}
