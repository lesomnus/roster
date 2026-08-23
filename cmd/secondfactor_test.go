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

// TestASeedIsNotAWayIn.
//
// The rule that refuses to take away somebody's last way in counted every
// credential a person had, and a TOTP seed is a credential. So a person with
// one provider and one seed could have the provider unlinked: the count said
// one was left, and what was left was six digits nobody may be asked for until
// they have already said who they are.
//
// It is the same shape as the race `lastwayin_test.go` is about and the
// opposite half of it. There the number was computed from a state that changed
// underneath it; here the number was right and the **question** was wrong.
//
// Written from the state rather than from the call: what must not be true at
// the end is *this person cannot sign in*, and a count that answers otherwise
// is a count of the wrong things.
func TestASeedIsNotAWayIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")
	id := b.identity(t, ctx, who, "github", "gh-erin")

	v := b.keyed2fa(t)

	res, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:  app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)

	seed := seedOf(t, res.GetSeed())

	_, err = b.Ungated.Identity().Erase(ctx, app.IdentityRef_builder{Id: id.GetId()}.Build())
	x.ErrorContains(err, "the only way they can sign in",
		"a seed was counted as though somebody could sign in with it")

	t.Run("and a confirmed one is not either", func(t *testing.T) {
		x := require.New(t)

		// Confirmed with the previous step's code, which the drift window
		// accepts. Asked because a reader will: a seed nobody has proved is
		// left out of what a person is offered, so the question is whether the
		// count changes once it is proved. It does not -- what a seed is is
		// settled by its kind and not by whether anybody has typed one.
		got, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Kind:   vouch.KindTotp,
			Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30-1)),
		}.Build())
		x.NoError(err)
		x.True(got.GetOk())

		_, err = b.Ungated.Identity().Erase(ctx, app.IdentityRef_builder{Id: id.GetId()}.Build())
		x.ErrorContains(err, "the only way they can sign in")
	})

	t.Run("and a password is", func(t *testing.T) {
		x := require.New(t)

		// The control, and the reason this is not simply *nothing may be
		// erased*: a way in that can begin a sign-in still counts, so the same
		// erase goes through the moment there is one.
		_, err := v.Set(ctx, app.VouchSetRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		x.NoError(err)

		_, err = b.Ungated.Identity().Erase(ctx, app.IdentityRef_builder{Id: id.GetId()}.Build())
		x.NoError(err)
	})
}

// TestASeedAloneSignsNobodyIn is the other side of the same sentence, and it is
// the one that was a hole rather than a miscount.
//
// `Verify` takes a kind and checks it, and `answer` sets `ok` when there is
// nothing left to prove. For somebody whose only credential is a seed there
// never was anything left -- so a six-digit code inside a thirty-second window
// was a whole sign-in, and the account was one `Enrol` old.
//
// What makes it easy to miss is that the shape is the one that keeps everything
// else failing closed: `ok` is set from the **absence** of work outstanding, so
// the emptier an account is the more finished its sign-in looks.
func TestASeedAloneSignsNobodyIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")

	v := b.keyed2fa(t)

	res, err := v.Enrol(ctx, app.VouchEnrolRequest_builder{
		Who:  app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)

	seed := seedOf(t, res.GetSeed())

	// Framed, which is every caller a deployment has: a key or a certificate
	// carries an actor, and it is what an attempt is issued to.
	as := b.as(ctx, who, b.Contoso)

	got, err := v.Verify(as, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err),
		"a second factor finished a sign-in it could not have started")
	x.ErrorContains(err, "nothing for it to be second to")
	x.False(got.GetOk())

	t.Run("and with a first one it is the second step", func(t *testing.T) {
		x := require.New(t)

		// The control. The refusal above is about there being nothing to be
		// second to, and not about the order the two are proved in -- so the
		// same call against the same seed answers with an attempt the moment
		// the person has something that could have begun one.
		_, err := v.Set(ctx, app.VouchSetRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		x.NoError(err)

		got, err := v.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Kind:   vouch.KindTotp,
			Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
		}.Build())
		x.NoError(err)
		x.False(got.GetOk())
		x.NotEmpty(got.GetContinuation())
		x.Len(got.GetAvailable(), 1)
		x.Equal(vouch.KindPassword, got.GetAvailable()[0].GetKind())
	})
}
