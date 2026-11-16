package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/internal/ent/credential"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
	"github.com/lesomnus/roster/server/vouch/vouchtest"
)

// kindsOf is the kinds a person still has to prove, as a list to look in.
func kindsOf(vs []*app.VouchFactor) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.GetKind()
	}

	return out
}

// TestAKeyIsEnrolledAndThenSignsSomebodyIn is the whole ceremony, through the
// two Rpcs an app actually calls.
//
// The parts above check the arithmetic; this checks that the row written by one
// call is the row read by the other, and that the counter survives the trip
// through the database -- which is the state D20 says verification is roster's
// for.
func TestAKeyIsEnrolledAndThenSignsSomebodyIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyed2fa(t)
	a := vouchtest.New(t)

	// Framed, which is every caller a deployment has. Without one `Verify`
	// takes the branch that cannot mint a continuation and answers `ok` to a
	// first factor -- `init` and the sandbox -- and a two-step assertion would
	// have nothing to be the first step of.
	as := b.as(ctx, b.ContosoUser, b.Contoso)

	// A password, because a security key is a **second** factor here and
	// `vouch.Begins` answers no for it: somebody whose only credential is a key
	// has nothing that can start a sign-in. Set the operator way -- no frame --
	// since a person's own first password takes a sign-in a few minutes old,
	// which this call has no credential to say.
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	res, err := b.Ungated.Credential().Enrol(as, app.CredentialEnrolRequest_builder{
		Ref:         app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Kind:        vouch.KindWebAuthn,
		Name:        "the yubikey in the drawer",
		Attestation: a.Register(t, vouchtest.Challenge(t)),
	}.Build())
	x.NoError(err)

	// Nothing to answer with, which is the shape rather than an omission: the
	// private half never left the authenticator.
	x.Empty(res.GetSeed())
	x.Empty(res.GetUri())

	// The first step, and what it answers about the key: which one to ask the
	// browser for, and what it is to sign.
	begin := func(t *testing.T) (attempt, id, challenge string) {
		t.Helper()

		got, err := v.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		require.NoError(t, err)
		require.False(t, got.GetOk())

		for _, f := range got.GetAvailable() {
			if f.GetKind() == vouch.KindWebAuthn {
				id, challenge = f.GetCredentialId(), f.GetChallenge()
			}
		}

		return got.GetContinuation(), id, challenge
	}
	answer := func(t *testing.T, attempt string, assertion []byte) *app.VouchVerifyResponse {
		t.Helper()

		got, err := v.Continue(as, app.VouchContinueRequest_builder{
			Continuation: attempt,
			Kind:         vouch.KindWebAuthn,
			Name:         "the yubikey in the drawer",
			Secret:       assertion,
		}.Build())
		require.NoError(t, err)

		return got.GetVerified()
	}

	t.Run("and the second form is told which key to ask for, and what it signs", func(t *testing.T) {
		x := require.New(t)

		// The credential identifier is what a page passes to
		// `navigator.credentials.get` -- without it a non-resident key cannot
		// be used at all -- and the challenge is this attempt's, and no other's.
		attempt, id, challenge := begin(t)
		x.NotEmpty(attempt)
		x.NotEmpty(id, "the key was offered without the identifier a browser needs")
		x.NotEmpty(challenge, "the key was offered with nothing for it to sign")

		_, _, another := begin(t)
		x.NotEqual(challenge, another, "two attempts are to sign the same thing")
	})

	var spent []byte

	t.Run("and an assertion over it is the second step", func(t *testing.T) {
		x := require.New(t)

		attempt, _, challenge := begin(t)
		spent = a.Assert(t, challenge)

		got := answer(t, attempt, spent)
		x.True(got.GetOk(), "a password and the key it was second to did not finish a sign-in")
	})

	t.Run("and the counter it consumed is in the row", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ent.Credential.Query().
			Where(credential.KindEQ(vouch.KindWebAuthn)).
			Only(ctx)
		x.NoError(err)
		x.Equal(int64(1), v.LastStep, "the counter did not survive the write")
	})

	t.Run("and a replay of that assertion is refused", func(t *testing.T) {
		x := require.New(t)

		attempt, _, _ := begin(t)
		x.False(answer(t, attempt, spent).GetOk(), "a captured assertion worked a second time")
	})

	t.Run("and a key answers an attempt, or nothing", func(t *testing.T) {
		x := require.New(t)

		// Presented first, there is no attempt for it to have signed -- and a
		// challenge its presenter chose is one a captured assertion carries
		// with it.
		_, err := v.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Kind:   vouch.KindWebAuthn,
			Name:   "the yubikey in the drawer",
			Secret: a.Assert(t, vouchtest.Challenge(t)),
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	})
}

// TestASecurityKeySignsTheAttempt: what a security key signs at a second step
// is the attempt's own challenge, so an assertion is good for the attempt it
// was made for and no other.
//
// # What it was
//
// The challenge arrived **beside** the assertion, in the same envelope, from
// whoever presented it -- with the relying party and the origins, as the three
// things roster has no browser to know. Nothing issued one, remembered one or
// spent one, and in the front door roster ships the browser made it up. So
// one assertion, once seen, was a second factor for as long as its key
// reported no counter: the same bytes were `ok` every time. A passkey kept by
// a platform reports none, always (#275).
//
// The attempt is the challenge now. A continuation is roster's, single-use and
// minutes long, so what a key is to sign is derived from it and answered with
// it, and whatever an envelope says of the challenge is not read.
func TestASecurityKeySignsTheAttempt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyed2fa(t)
	as := b.as(ctx, b.ContosoUser, b.Contoso)
	who := app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build()

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	// A key that reports no counter, which is the one a counter cannot guard.
	a := vouchtest.New(t)
	_, err = b.Ungated.Credential().Enrol(as, app.CredentialEnrolRequest_builder{
		Ref:         app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Kind:        vouch.KindWebAuthn,
		Attestation: a.Register(t, vouchtest.Challenge(t)),
	}.Build())
	x.NoError(err)

	begin := func() (string, string) {
		got, err := v.Verify(as, app.VouchVerifyRequest_builder{Who: who, Secret: []byte("correct horse battery staple")}.Build())
		require.NoError(t, err)
		require.Len(t, got.GetAvailable(), 1)

		return got.GetContinuation(), got.GetAvailable()[0].GetChallenge()
	}
	answer := func(attempt string, assertion []byte) bool {
		got, err := v.Continue(as, app.VouchContinueRequest_builder{
			Continuation: attempt, Kind: vouch.KindWebAuthn, Secret: assertion,
		}.Build())
		require.NoError(t, err)

		return got.GetVerified().GetOk()
	}

	attempt, challenge := begin()
	once := a.AssertAt(t, challenge, 0)
	x.True(answer(attempt, once))

	t.Run("an assertion is not a second factor twice", func(t *testing.T) {
		x := require.New(t)

		// The same bytes, on an attempt opened by the same right password.
		again, _ := begin()
		x.False(answer(again, once), "one assertion finished two sign-ins")
	})

	t.Run("nor is one over a challenge its presenter chose", func(t *testing.T) {
		x := require.New(t)

		// Signed, by the right key, over a challenge the envelope names -- which
		// was the whole of what was checked.
		attempt, _ := begin()
		x.False(answer(attempt, a.AssertAt(t, vouchtest.Challenge(t), 0)))
	})

	t.Run("nor one made for another attempt", func(t *testing.T) {
		x := require.New(t)

		_, other := begin()
		attempt, _ := begin()
		x.False(answer(attempt, a.AssertAt(t, other, 0)))
	})

	t.Run("and one made for this attempt still is", func(t *testing.T) {
		x := require.New(t)

		attempt, challenge := begin()
		x.True(answer(attempt, a.AssertAt(t, challenge, 0)))
	})
}
