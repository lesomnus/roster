package cmd_test

import (
	"context"
	"encoding/base32"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// enrolled gives somebody a password and a confirmed second factor, and answers
// with the seed.
func enrolled(t *testing.T, ctx context.Context, cred app.CredentialServiceServer, v *vouch.Server, who pdid.Id) []byte {
	t.Helper()
	x := require.New(t)

	_, err := cred.Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	res, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:  app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)

	seed := seedOf(t, res.GetSeed())

	// Confirmed: a factor that has never had a code verified against it is a
	// QR somebody may have mis-scanned, and it is deliberately not offered.
	//
	// With the **previous** step's code, which the drift window accepts and
	// which leaves the current one unspent -- so a sign-in a moment later is
	// not refused as a replay of this. That is the replay rule working, and a
	// test that confirmed with the current code would be a test fighting it.
	got, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30-1)),
	}.Build())
	x.NoError(err)
	x.True(got.GetOk())

	return seed
}

// TestASecondFactorIsAnAttemptRosterHoldsAndABrowserDoesNot is D21.
//
// The question that found it: **an app showing a second form has to remember
// who passed the first one.** An app developer wants to know who somebody is
// and does not want to be in the sign-in business at all, so making them carry
// that is handing them the one part of the process they were trying to avoid.
//
// So *which browser* is mid-sign-in stays the app's, and *what has been proved
// about this person* is roster's -- carried in an opaque string the app passes
// back, with no cookie set here and no browser in sight.
func TestASecondFactorIsAnAttemptRosterHoldsAndABrowserDoesNot(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate, "/roster.VouchService/Continue", listHolders)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)

	v := b.keyed(t)
	seed := enrolled(t, ctx, b.Ungated.Credential(), v, b.Who)

	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, b.Token)

	first, err := c.Delegate(as, app.VouchDelegateRequest_builder{
		Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret:  []byte("correct horse battery staple"),
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)

	t.Run("the first factor is not a sign-in", func(t *testing.T) {
		x := require.New(t)

		f := first.GetVerified()
		x.False(f.GetOk(), "one factor answered as though it were the whole sign-in")
		x.Empty(first.GetToken(), "a half-proved identity was handed a credential")

		x.Equal([]string{"password"}, f.GetSatisfied())
		x.NotEmpty(f.GetContinuation())
		x.Len(f.GetAvailable(), 1)
		x.Equal(vouch.KindTotp, f.GetAvailable()[0].GetKind())

		// Who it is, so that an app can say "welcome back" over the second
		// form. What is not here is how many steps there are: `two is enough`
		// is sufficiency, and D20 leaves that to the caller.
		x.Equal(b.Who.Bytes(), f.GetHolder())
	})

	t.Run("and the second one finishes it", func(t *testing.T) {
		x := require.New(t)

		done, err := c.Delegate(as, app.VouchDelegateRequest_builder{
			Continuation: first.GetVerified().GetContinuation(),
			Kind:         vouch.KindTotp,
			Secret:       []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
			Methods:      []string{listHolders},
		}.Build())
		x.NoError(err)

		x.True(done.GetVerified().GetOk())
		x.NotEmpty(done.GetToken(), "both factors passed and nothing was minted")
		x.Empty(done.GetVerified().GetContinuation(), "the attempt is still open")

		_, err = app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, b.Token, done.GetToken()), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
	})

	// Single use, and there is one mechanism for it: spending is an erase, and
	// *used* is *not there*.
	t.Run("and the continuation is spent", func(t *testing.T) {
		x := require.New(t)

		again, err := c.Delegate(as, app.VouchDelegateRequest_builder{
			Continuation: first.GetVerified().GetContinuation(),
			Kind:         vouch.KindTotp,
			Secret:       []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
			Methods:      []string{listHolders},
		}.Build())
		x.NoError(err)
		x.Empty(again.GetToken(), "a spent continuation was spent again")
	})
}

// TestOneFactorPaysNothingForTwo.
//
// A continuation is minted only when there is more to prove, so a deployment
// with one factor gets byte for byte the answer it got before and pays no row
// for a handle nobody would spend. It is also what keeps an app that has never
// heard of second factors gating on `ok` -- which is only ever set when there
// is nothing left, so ignoring the new fields fails **closed**.
func TestOneFactorPaysNothingForTwo(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate)
	ctx := t.Context()

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	res, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token),
		app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret:  []byte("correct horse battery staple"),
			Methods: []string{delegate},
		}.Build())
	x.NoError(err)

	x.True(res.GetVerified().GetOk())
	x.NotEmpty(res.GetToken())
	x.Empty(res.GetVerified().GetContinuation())
	x.Empty(res.GetVerified().GetAvailable())

	n, err := b.Ent.Continuation.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "a row was written for an attempt nobody would continue")
}

// TestTheCountIsOneCountAcrossTheSteps is D21's fourth condition, and the
// obvious reading of it is wrong.
//
// *One count across `Begin` and `Continue`, or the second factor is an unmetered
// guessing surface reached by passing the first.* `Credential` is unique on
// (holder, kind, name), so a password row and a TOTP row carry two independent
// counters -- ten wrong codes would close the second factor and leave the first
// untouched, and a fresh first factor costs nothing because a **successful**
// verify is never counted.
//
// So a failed second step counts against the row the first step used.
func TestTheCountIsOneCountAcrossTheSteps(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.keyedLocal(t)
	enrolled(t, ctx, b.Ungated.Credential(), v, b.ContosoUser)

	// A frame, because a continuation is bound to whoever asked for one.
	as := frame.Into(ctx, frame.New(b.ContosoUser, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))

	wrong := func() *app.VouchVerifyResponse {
		t.Helper()

		first, err := v.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		require.NoError(t, err)
		require.NotEmpty(t, first.GetContinuation())

		res, err := v.Continue(as, app.VouchContinueRequest_builder{
			Continuation: first.GetContinuation(),
			Kind:         vouch.KindTotp,
			Secret:       []byte("000000"),
		}.Build())
		require.NoError(t, err)

		return res.GetVerified()
	}

	for range vouch.MaxFailures - 1 {
		x.False(wrong().GetOk())
	}

	// The one that closes it, and what it closes is the **password**.
	x.False(wrong().GetOk())

	shut, err := v.Verify(as, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	x.NotNil(shut.GetLockedUntil(),
		"guessing the second factor did not cost the first anything")
}

// TestAnAttemptBelongsToWhoeverOpenedIt is D21's third condition, on the object
// it was written about.
func TestAnAttemptBelongsToWhoeverOpenedIt(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate, "/roster.VouchService/Continue")
	ctx := t.Context()

	v := b.keyed(t)
	seed := enrolled(t, ctx, b.Ungated.Credential(), v, b.Who)

	c := app.NewVouchServiceClient(b.Conn)

	first, err := c.Delegate(bearing(ctx, b.Token), app.VouchDelegateRequest_builder{
		Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret:  []byte("correct horse battery staple"),
		Methods: []string{delegate},
	}.Build())
	x.NoError(err)

	theirs := keyed(t, ctx, b, "another-app", []string{delegate, "/roster.VouchService/Continue"})

	res, err := c.Continue(bearing(ctx, theirs), app.VouchContinueRequest_builder{
		Continuation: first.GetVerified().GetContinuation(),
		Kind:         vouch.KindTotp,
		Secret:       []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.False(res.GetVerified().GetOk(),
		"one app picked up an authentication another one started")

	// And naming both ways is a caller that has not decided.
	_, err = c.Delegate(bearing(ctx, b.Token), app.VouchDelegateRequest_builder{
		Who:          app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Continuation: first.GetVerified().GetContinuation(),
		Methods:      []string{delegate},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// seedOf is the base32 an enrolment answered with, as bytes.
func seedOf(t *testing.T, v string) []byte {
	t.Helper()

	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(v)
	require.NoError(t, err)

	return b
}

// keyed is the service with a keyring, on a keyed harness.
func (b *keyedBuilt) keyed(t *testing.T) *vouch.Server {
	t.Helper()

	// **This deployment's** keyring and not a fresh one. Two keyrings in one
	// process is a deployment that cannot read what it wrote, and finding that
	// out took a morning: the row was right, the key name was right, and the
	// bytes were not.
	return vouch.New(b.Ungated, b.Ungated, vouch.WithKeys(b.Keyring))
}

// keyedLocal is the same on the plain harness.
func (b *built) keyedLocal(t *testing.T) *vouch.Server {
	t.Helper()

	return vouch.New(b.Ungated, b.Ungated, vouch.WithKeys(newKeyring(t)))
}

func newKeyring(t *testing.T) vouch.Keyring {
	t.Helper()

	k, err := vouch.NewKeyring([]string{"one:" + base64.StdEncoding.EncodeToString(freshKey(t))})
	require.NoError(t, err)

	return k
}

// vouchedLocal is the plain service on a keyed harness.
func (b *keyedBuilt) vouchedLocal() *vouch.Server { return vouch.New(b.Ungated, b.Ungated) }
