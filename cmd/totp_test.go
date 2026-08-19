package cmd_test

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// keyed2fa is the service with a keyring, which is what a deployment that holds
// second factors has and no other deployment does.
func (b *built) keyed2fa(t *testing.T) *vouch.Server {
	t.Helper()
	x := require.New(t)

	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	x.NoError(err)

	k, err := vouch.NewKeyring([]string{"one:" + base64.StdEncoding.EncodeToString(raw)})
	x.NoError(err)

	return vouch.New(b.Ungated, b.Ungated, vouch.WithKeys(k))
}

// TestASecondFactorIsEnrolledOnceAndReadBackNever.
//
// The first secret roster holds that it has to be able to **read back**.
// Everything else here is a verifier: a copy of the database is a copy of
// things nobody can use. A seed is not that -- computing the code somebody is
// about to type means holding it -- so what leaves is a base32 string exactly
// once, and what stays is wrapped.
func TestASecondFactorIsEnrolledOnceAndReadBackNever(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyed2fa(t)

	res, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Issuer: "roster",
	}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetSeed())
	x.Contains(res.GetUri(), "otpauth://totp/")

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(res.GetSeed())
	x.NoError(err)

	t.Run("and the row is not the seed", func(t *testing.T) {
		x := require.New(t)

		row, err := b.Ent.Credential.Query().Only(ctx)
		x.NoError(err)
		x.Equal(vouch.KindTotp, row.Kind)
		x.NotContains(string(row.Secret), res.GetSeed())
		x.NotContains(string(row.Secret), string(seed))
		x.Zero(row.LastStep, "an enrolled factor counts as proved before anybody proved it")
	})

	t.Run("and the code it makes verifies", func(t *testing.T) {
		x := require.New(t)

		got, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Kind:   vouch.KindTotp,
			Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
		}.Build())
		x.NoError(err)
		x.True(got.GetOk())
	})

	// D20 puts replay in roster's half: *a TOTP step that has been used must not
	// work twice, and the only place that can be recorded is the row.* Without
	// it a code read over somebody's shoulder is good for the rest of its
	// thirty seconds.
	t.Run("and the same code does not verify twice", func(t *testing.T) {
		x := require.New(t)

		code := []byte(vouch.CodeAt(seed, time.Now().Unix()/30))

		again, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Kind:   vouch.KindTotp,
			Secret: code,
		}.Build())
		x.NoError(err)
		x.False(again.GetOk(), "a spent code worked twice")

		row, err := b.Ent.Credential.Query().Only(ctx)
		x.NoError(err)
		x.Positive(row.LastStep, "the spent step was not recorded")
	})

	// A password and a second factor are two rows now, and the index is on
	// (holder, kind, name) rather than (holder, kind) -- so a person may hold
	// both, and may hold two of one kind when the kind is one where that is the
	// standard advice.
	t.Run("and a password lives beside it", func(t *testing.T) {
		x := require.New(t)

		b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

		got, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		x.NoError(err)
		x.True(got.GetOk())

		n, err := b.Ent.Credential.Query().Count(ctx)
		x.NoError(err)
		x.Equal(2, n)
	})

	t.Run("and two of a kind are told apart by name", func(t *testing.T) {
		x := require.New(t)

		_, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
			Who:  app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Kind: vouch.KindTotp,
			Name: "the spare phone",
		}.Build())
		x.NoError(err, "a second authenticator was refused, which is WebAuthn's whole recovery story")

		_, err = v.Enrol(ctx, app.VouchEnrolRequest_builder{
			Who:  app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Kind: vouch.KindTotp,
			Name: "the spare phone",
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err), "one name twice")
	})
}

// TestADeploymentWithNoKeyHoldsNoSecondFactor.
//
// Refused rather than stored in the clear, and refused as `Unimplemented`
// rather than as a `no`: a deployment answering "wrong code" to every attempt
// is one where nobody can tell a misconfiguration from a mistake, and the
// person it happens to is the one who cannot sign in.
func TestADeploymentWithNoKeyHoldsNoSecondFactor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.vouched()

	_, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:  app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))

	_, err = v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte("123456"),
	}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))

	// And a password is unaffected, so this is about the kind rather than about
	// the service.
	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")
	x.True(b.verifies(t, ctx, b.AcmeUser, "correct horse battery staple").GetOk())
}

// TestAKindNobodyChecksIsRefusedBeforeAnybodyIsRead.
//
// It is a fact about the deployment rather than about the person, so it must
// not depend on whether they exist -- otherwise the refusal is a way to ask.
func TestAKindNobodyChecksIsRefusedBeforeAnybodyIsRead(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyed2fa(t)

	for _, who := range []*app.VouchWho{
		app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		app.VouchWho_builder{Tenant: "acme", Alias: "nobody-at-all"}.Build(),
	} {
		_, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    who,
			Kind:   "webauthn",
			Secret: []byte("whatever"),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	}
}
