package cmd_test

import (
	"encoding/base32"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/ent/credential"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAFactorWithANameCanBeConfirmed.
//
// `Enrol` invites a name -- *"the phone", "the yubikey in the drawer"* -- and
// `operate.go` promises what happens next: *an unconfirmed factor still
// **verifies**, and that is how it gets confirmed.* For a named one there was
// no call that could do it.
//
// `Verify` resolves a credential by kind with no name, and an unset name in a
// `CredentialRefByKind` is `name = ""` rather than "any", so it matched only
// the unnamed row. `Continue` takes a name and needs a continuation, and no
// continuation is minted when nothing is left to prove -- `factors` skips
// unconfirmed TOTP by design, which is right and leaves the first named factor
// with nothing that reaches it.
//
// So the second factor a person just scanned did not exist for any call they
// could make, and the deployment that thought it had 2FA had none. It went
// unnoticed because every test enrolled the *unnamed* one first.
func TestAFactorWithANameCanBeConfirmed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyed2fa(t)
	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	res, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:  app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind: vouch.KindTotp,
		Name: "the phone",
	}.Build())
	x.NoError(err)

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(res.GetSeed())
	x.NoError(err)

	// The call the person makes with the app open in front of them.
	got, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Name:   "the phone",
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.True(got.GetOk(), "the factor they had just enrolled did not verify")

	// And it counts as confirmed now, which is what `available` reads.
	row, err := b.Ent.Credential.Query().Where(credential.NameEQ("the phone")).Only(ctx)
	x.NoError(err)
	x.Positive(row.LastStep, "it verified without being confirmed")
}

// TestAConfirmedNamedFactorIsThenAsked, which is the whole point of confirming
// it: an unconfirmed row is left out of `available`, so a factor that can
// never confirm is a deployment that silently has one factor.
func TestAConfirmedNamedFactorIsThenAsked(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyed2fa(t)
	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	res, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:  app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind: vouch.KindTotp,
		Name: "the phone",
	}.Build())
	x.NoError(err)

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(res.GetSeed())
	x.NoError(err)

	// As somebody, because a continuation is minted for whoever asked and a
	// frameless caller is answered the way this always answered.
	asApp := b.as(ctx, b.AcmeUser, b.Acme)

	password := func() *app.VouchVerifyResponse {
		t.Helper()

		got, err := v.Verify(asApp, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		x.NoError(err)

		return got
	}

	// Before it is confirmed, a password is the whole of signing in.
	got := password()
	x.True(got.GetOk())
	x.Empty(got.GetAvailable())

	// One code against it, with the **previous** step so the current one stays
	// unspent -- `enrolled` in `twostep_test.go` says why.
	//
	// The answer is half a sign-in rather than a yes, and that is right: what
	// has been proved is the second factor, and the password has not been. The
	// call is worth making anyway, and this is the one thing it is for -- the
	// step is written whatever the answer, so the factor is now confirmed.
	got, err = v.Verify(asApp, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Name:   "the phone",
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30-1)),
	}.Build())
	x.NoError(err)
	x.Equal([]string{vouch.KindTotp}, got.GetSatisfied(), "the code was not accepted")

	row, err := b.Ent.Credential.Query().Where(credential.NameEQ("the phone")).Only(ctx)
	x.NoError(err)
	x.Positive(row.LastStep, "the factor is still unconfirmed, so nothing will ever ask for it")

	// And now a password alone is not a sign-in.
	got = password()
	x.False(got.GetOk(), "a password alone signed somebody in who holds a second factor")
	x.NotEmpty(got.GetContinuation())

	names := []string{}
	for _, f := range got.GetAvailable() {
		names = append(names, f.GetName())
	}
	x.Equal([]string{"the phone"}, names)

	// And the second form finishes it, by name.
	step, err := v.Continue(asApp, app.VouchContinueRequest_builder{
		Continuation: got.GetContinuation(),
		Kind:         vouch.KindTotp,
		Name:         "the phone",
		Secret:       []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.True(step.GetVerified().GetOk())
}

// TestAnUnnamedFactorIsStillTheOnlyOne, so that what changed above is the name
// and not the lookup: a request naming nothing must still match the row named
// nothing, rather than any row of the kind.
func TestAnUnnamedFactorIsStillTheOnlyOne(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyed2fa(t)

	res, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:  app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind: vouch.KindTotp,
		Name: "the phone",
	}.Build())
	x.NoError(err)

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(res.GetSeed())
	x.NoError(err)

	// Naming nothing does not stand for "whichever one you have".
	got, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.False(got.GetOk(), "a code verified against a factor the request did not name")
}
