package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	entcredential "github.com/lesomnus/roster/internal/ent/credential"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAPersonEnrolsTheirOwnSecondFactor is the second-factor half of the same
// self-service screen `TestAPersonChangesTheirOwnPassword` is the password half
// of, and it has the same shape: no verb of its own. A person calls
// `Credential.Enrol` with their **own** reference, the operator's verb, and the
// layer's `mayReach` lets it through because the target is the caller.
//
// A role naming `Enrol` reaches anybody no wider than the caller -- RBAC as it
// is -- and the app that serves a person is what passes only their reference.
// What is proved here is the verb: the code the enrolment answered with
// verifies for the row it was made for.
func TestAPersonEnrolsTheirOwnSecondFactor(t *testing.T) {
	const enrol = "/roster.CredentialService/Enrol"

	x := require.New(t)
	b := keyFor(t, enrol)
	ctx := t.Context()

	// A role that names Enrol, and her own key holding it -- the two gates every
	// write meets: the key's list, and the holder's role.
	permits(t, ctx, b, b.Contoso, b.Who, "self", enrol)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{enrol}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	res, err := cl.Enrol(bearing(ctx, hers), app.CredentialEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
	}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetSeed())
	x.Contains(res.GetUri(), "otpauth://totp/")

	// It landed on **her** account: the code it made verifies for the row the
	// reference named, which is hers.
	seed := seedOf(t, res.GetSeed())
	got, err := b.keyed(t).Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.Equal([]string{vouch.KindTotp}, got.GetSatisfied(), "the factor a person enrolled on themselves does not verify for them")

	row, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.Equal(vouch.KindTotp, row.Kind)
	x.Equal(b.Who.Uuid(), row.HolderId, "the factor landed on somebody other than the caller")
}

// TestTakingOnesOwnSecondFactorOffTakesAFreshSignIn: somebody takes a second
// factor off their own account only from a sign-in a few minutes old.
//
// # What it was
//
// It asked that the caller be the person and nothing more. Changing one's own
// password asks for the one held, and giving oneself a first one asks for a
// sign-in a few minutes old, *so that a credential which merely acts as
// somebody cannot turn temporary access into a way in of its own choosing*
// (`credential_svc.ext.proto`). Taking a second factor off is the mirror of
// that: what is left is the password alone, for whoever has it. A key minted
// last year that named `Erase`, or a page left open since the morning, did it
// (#275; the owner's decision, 2026-11-15).
//
// The rule a first password is under, read off the credential the call came
// with: no field, and nothing for a page to send. Since roster's own sign-in
// asks the second factor, a fresh one is somebody who has just answered it.
// Somebody **else's** factor is the reach rule's, as it was -- a lost phone is
// taken off by whoever administers the person.
func TestTakingOnesOwnSecondFactorOffTakesAFreshSignIn(t *testing.T) {
	const erase = "/roster.CredentialService/Erase"

	b := keyFor(t, erase)
	ctx := t.Context()

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	permits(t, ctx, b, b.Contoso, b.Who, "self", erase)
	cl := app.NewCredentialServiceClient(b.Conn)

	_, err := b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Holder: own, Kind: vouch.KindTotp, Name: "phone"}.Build())
	require.NoError(t, err)

	methods := []string{erase}
	key := bearing(ctx, mintFor(t, ctx, b, b.Who, "laptop", methods, time.Time{}))
	stale := acting(ctx, b.Token, signedInAt(t, ctx, b, b.Who, methods, time.Now().Add(-2*core.FirstPasswordWithin)))
	fresh := acting(ctx, b.Token, signedInAt(t, ctx, b, b.Who, methods, time.Now()))

	phone := app.CredentialRef_builder{
		Kind: app.CredentialRefByKind_builder{Holder: own, Kind: ptr(vouch.KindTotp), Name: ptr("phone")}.Build(),
	}.Build()
	factors := func(t *testing.T) int {
		t.Helper()

		n, err := b.Ent.Credential.Query().Where(entcredential.KindEQ(vouch.KindTotp), entcredential.DateErasedIsNil()).Count(ctx)
		require.NoError(t, err)

		return n
	}

	t.Run("not by a key of theirs, nor by a sign-in that is not minutes old", func(t *testing.T) {
		x := require.New(t)

		for name, as := range map[string]context.Context{"a key": key, "a stale sign-in": stale} {
			_, err := cl.Erase(as, phone)
			x.Equal(codes.FailedPrecondition, status.Code(err), "%s: %v", name, err)
		}
		x.Equal(1, factors(t), "a second factor was taken off by a credential that proved nothing just now")

		_, err := cl.Erase(stale, phone)
		x.Contains(status.Convert(err).Message(), "sign in again")
	})

	t.Run("by one that is", func(t *testing.T) {
		x := require.New(t)

		v, err := cl.Erase(fresh, phone)
		x.NoError(err)
		x.True(v.GetErased())
		x.Zero(factors(t))
	})

	t.Run("and somebody else's is the reach rule's, as it was", func(t *testing.T) {
		x := require.New(t)

		// Somebody who holds no more than she does, and has lost their phone.
		other := addHolder(t, ctx, b.Server, b.Contoso, "lost-their-phone")
		theirs := app.HolderRef_builder{Id: other.Bytes()}.Build()
		_, err := b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Holder: theirs, Kind: vouch.KindTotp, Name: "phone"}.Build())
		x.NoError(err)

		v, err := cl.Erase(key, app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{Holder: theirs, Kind: ptr(vouch.KindTotp), Name: ptr("phone")}.Build(),
		}.Build())
		x.NoError(err, "whoever administers somebody could not take a lost phone off them")
		x.True(v.GetErased())
	})
}
