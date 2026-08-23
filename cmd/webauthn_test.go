package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

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
// two RPCs an app actually calls.
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
	// has nothing that can start a sign-in.
	_, err := v.Set(as, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	res, err := v.Enrol(as, app.VouchEnrolRequest_builder{
		Who:         app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Kind:        vouch.KindWebAuthn,
		Name:        "the yubikey in the drawer",
		Attestation: a.Register(t, vouchtest.Challenge(t)),
	}.Build())
	x.NoError(err)

	// Nothing to answer with, which is the shape rather than an omission: the
	// private half never left the authenticator.
	x.Empty(res.GetSeed())
	x.Empty(res.GetUri())

	t.Run("and an assertion is the second step", func(t *testing.T) {
		x := require.New(t)

		c := vouchtest.Challenge(t)

		got, err := v.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Kind:   vouch.KindWebAuthn,
			Name:   "the yubikey in the drawer",
			Secret: a.Assert(t, c),
		}.Build())
		x.NoError(err)

		// Not `ok`, because a key does not begin a sign-in: what is left is the
		// password, which is what `vouch.Begins` answering no means in
		// practice.
		x.False(got.GetOk())
		x.Contains(kindsOf(got.GetAvailable()), "password")
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

		c := vouchtest.Challenge(t)
		once := a.Assert(t, c)

		// Named, because this key was enrolled with one -- `Enrol` invites a
		// name and `Verify` takes one for exactly that reason.
		first, err := v.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Kind:   vouch.KindWebAuthn,
			Name:   "the yubikey in the drawer",
			Secret: once,
		}.Build())
		x.NoError(err)
		x.False(first.GetOk())
		x.NotEmpty(first.GetContinuation(), "the first assertion was not accepted at all")

		again, err := v.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Kind:   vouch.KindWebAuthn,
			Name:   "the yubikey in the drawer",
			Secret: once,
		}.Build())
		x.NoError(err)
		x.Empty(again.GetContinuation(), "a captured assertion worked a second time")
	})
}
