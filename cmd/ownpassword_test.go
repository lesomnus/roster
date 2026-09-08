package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
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
		// `Vouch.Reset` works is `TestASecretIsResetForTheAddressThatNamesSomebody`
		// and `TestAResetVoidsWhatCameBeforeIt`; a refusal is only right if the
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
		x.Contains(status.Convert(err).Message(), "Vouch.Reset",
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

// TestAFirstPasswordOfYourOwnAsksForNothing is the case with nothing to prove,
// and the trade it is.
//
// Somebody who arrived through a provider has no password, so the reauth the
// change above rests on has nothing to compare. That was refused, and the
// refusal was right on its own terms: a bearer that merely acts as them can now
// set a password they never chose, and unlike the bearer it does not expire and
// they are not told.
//
// What changed is the alternative. The refusal pointed at an operator or the
// recovery flow, and recovery needs mail, which most deployments never
// configure -- so in the common case it pointed at nothing. `server/core` has
// the argument at length; this asks that both halves are true: the first one
// goes in with nothing, and the second still cannot be replaced without the
// first.
func TestAFirstPasswordOfYourOwnAsksForNothing(t *testing.T) {
	const set = "/roster.CredentialService/Set"

	x := require.New(t)
	b := keyFor(t, set)
	ctx := t.Context()

	const first, next = "correct horse battery staple", "a whole new set of words entirely"

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()

	// No operator write first: this is somebody a provider vouched for and who
	// has never had a password.
	permits(t, ctx, b, b.Contoso, b.Who, "self", set)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{set}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
		Ref:    own,
		Secret: []byte(first),
	}.Build())
	x.NoError(err, "somebody with no password could not give themselves one")

	// It is the password now, which is the half that says the write landed
	// rather than being accepted and dropped.
	v := vouch.New(b.Ungated, b.Ungated)
	res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(), Secret: []byte(first),
	}.Build())
	x.NoError(err)
	x.True(res.GetOk(), "the first password does not sign in")

	// And the door closes behind it: there is something to prove now, so the
	// next change asks for it.
	_, err = cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
		Ref:    own,
		Secret: []byte(next),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"the second password was replaced with nothing proved")

	// A `current` sent when there is none is ignored rather than refused: a
	// page that sent one has guessed about a row it cannot read, which is not
	// a thing to fail a call over.
	t.Run("a current nobody holds is ignored, not refused", func(t *testing.T) {
		x := require.New(t)

		cx := t.Context()
		c := keyFor(t, set)
		permits(t, cx, c, c.Contoso, c.Who, "self", set)
		theirs := mintFor(t, cx, c, c.Who, "laptop", []string{set}, time.Time{})

		_, err := app.NewCredentialServiceClient(c.Conn).Set(bearing(cx, theirs),
			app.CredentialSetRequest_builder{
				Ref:     app.HolderRef_builder{Id: c.Who.Bytes()}.Build(),
				Current: []byte("nothing is stored under this"),
				Secret:  []byte(first),
			}.Build())
		x.NoError(err)
	})
}
