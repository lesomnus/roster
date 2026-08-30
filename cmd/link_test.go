package cmd_test

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

const (
	link   = "/roster.VouchService/Link"
	redeem = "/roster.VouchService/Redeem"
)

// TestALinkIsAWayInThatSomebodyElseDelivers is item 3, and D19's line applied
// one more time.
//
// *A single-use opaque nonce roster mints and roster checks, delivered by
// somebody else.* Who checks it? Only roster: it resolves nowhere else and
// revoking it is a delete. Sending it is not roster's, and separating the two
// is what makes the air-gapped case work at all — with no mail the somebody
// else is a person, and what they hand over is a password from `Vouch.Reset`.
func TestALinkIsAWayInThatSomebodyElseDelivers(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, link, redeem, listHolders)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)

	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, b.Token)

	made, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.NotEmpty(made.GetToken())

	t.Run("it signs somebody in", func(t *testing.T) {
		x := require.New(t)

		res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
			Token:   made.GetToken(),
			Methods: []string{listHolders},
		}.Build())
		x.NoError(err)
		x.True(res.GetVerified().GetOk())
		x.NotEmpty(res.GetToken())

		_, err = app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, b.Token, res.GetToken()), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
	})

	// Spending is an erase, and *used* is *not there* -- one mechanism, the
	// same one a continuation uses.
	t.Run("and once only", func(t *testing.T) {
		x := require.New(t)

		again, err := c.Redeem(as, app.VouchRedeemRequest_builder{
			Token:   made.GetToken(),
			Methods: []string{listHolders},
		}.Build())
		x.NoError(err)
		x.False(again.GetVerified().GetOk(), "a link was spent twice")
		x.Empty(again.GetToken())
	})

	t.Run("and every other way of being wrong is one answer", func(t *testing.T) {
		x := require.New(t)

		// Written directly, because `Link` will not mint one that has already
		// expired -- a caller may ask for **less** than the default and not for
		// more, and a time in the past is not less, it is nonsense. What is
		// being tested here is the read's refusal.
		token, sum, err := staleLink()
		x.NoError(err)

		_, err = b.Ungated.Link().Add(ctx, app.LinkAddRequest_builder{
			Holder:      app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			Secret:      sum,
			Issuer:      issuerOf(t, ctx, b),
			DateExpires: timestamppb.New(time.Now().Add(-time.Minute)),
		}.Build())
		x.NoError(err)

		for _, tc := range []struct{ desc, token string }{
			{"never here", vouch.PrefixLink + "nothing-at-all"},
			{"not one of ours", "not-even-prefixed"},
			{"expired", token},
		} {
			res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
				Token:   tc.token,
				Methods: []string{listHolders},
			}.Build())
			x.NoError(err, tc.desc)
			x.False(res.GetVerified().GetOk(), tc.desc)
		}
	})

	// One app must not spend what another was issued, which is the condition
	// every short-lived thing here carries.
	t.Run("and another app does not spend it", func(t *testing.T) {
		x := require.New(t)

		mine, err := c.Link(as, app.VouchLinkRequest_builder{
			Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		theirs := keyed(t, ctx, b, "another-app", []string{link, redeem})

		res, err := c.Redeem(bearing(ctx, theirs), app.VouchRedeemRequest_builder{
			Token:   mine.GetToken(),
			Methods: []string{redeem},
		}.Build())
		x.NoError(err)
		x.False(res.GetVerified().GetOk(), "one app spent what another was issued")
	})
}

// TestAskingForALinkSaysNothingAboutWhoIsHere is the property that is easiest
// to lose and hardest to notice.
//
// A form that asks for an address and is filled in by strangers is the one
// place an account-existence oracle is most useful to whoever is looking for
// one. So a request for nobody answers exactly as a request for somebody does,
// with a token that resolves to nothing.
func TestAskingForALinkSaysNothingAboutWhoIsHere(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, link, redeem)
	ctx := t.Context()

	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, b.Token)

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)

	real, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Tenant: "contoso", Address: "someone@contoso.example"}.Build(),
	}.Build())
	x.NoError(err)

	stranger, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Tenant: "contoso", Address: "nobody@contoso.example"}.Build(),
	}.Build())
	x.NoError(err)

	x.NotEmpty(stranger.GetToken(), "a form answered 'nobody here' to whoever typed into it")
	x.Len(stranger.GetToken(), len(real.GetToken()))

	// And the one for nobody resolves to nobody, which is where the difference
	// is allowed to show.
	res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
		Token:   stranger.GetToken(),
		Methods: []string{redeem},
	}.Build())
	x.NoError(err)
	x.False(res.GetVerified().GetOk())

	// Nothing was written for them either, so the table is not a list of every
	// address anybody has ever typed.
	n, err := b.Ent.Link.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n)
}

// TestALinkDoesNotSkipASecondFactor.
//
// A link that let somebody past a second factor would be a way to turn a
// mailbox into an account, which is most of what a second factor is for.
func TestALinkDoesNotSkipASecondFactor(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, link, redeem, delegate)
	ctx := t.Context()

	v := b.keyed(t)
	seed := enrolled(t, ctx, b.Ungated.Credential(), v, b.Who)

	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, b.Token)

	made, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
		Token:   made.GetToken(),
		Methods: []string{delegate},
	}.Build())
	x.NoError(err)

	x.False(res.GetVerified().GetOk(), "a link signed somebody in past their second factor")
	x.Empty(res.GetToken())
	x.Equal([]string{"link"}, res.GetVerified().GetSatisfied())
	x.NotEmpty(res.GetVerified().GetContinuation())

	// And it finishes exactly as a password would have.
	done, err := c.Delegate(as, app.VouchDelegateRequest_builder{
		Continuation: res.GetVerified().GetContinuation(),
		Kind:         vouch.KindTotp,
		Secret:       []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
		Methods:      []string{delegate},
	}.Build())
	x.NoError(err)
	x.True(done.GetVerified().GetOk())
	x.NotEmpty(done.GetToken())
}

// TestAResetVoidsWhatCameBeforeIt is what D26 left for the work that has the
// reset in it.
//
// *A password reset that leaves old sessions alive is not a reset.* D26 would
// not couple it to `Set`, because somebody changing their own password would
// then sign themselves out of everything with nothing having said so. This is
// the other act — somebody else giving them a new one — and it is where
// recovery from a takeover happens, so the sessions the takeover opened go with
// it.
func TestAResetVoidsWhatCameBeforeIt(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate, listHolders)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)
	b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())

	held := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)

	list := func() error {
		_, err := app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, b.Token, held), app.HolderListRequest_builder{}.Build())

		return err
	}
	x.NoError(list(), "the control")

	time.Sleep(2 * time.Millisecond)

	_, err := b.vouchedLocal().Reset(ctx, app.VouchResetRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	x.Equal(codes.Unauthenticated, status.Code(list()),
		"a reset left the credentials the takeover was using")
}

// staleLink is a token and its verifier, made the way `Vouch.Link` makes one.
//
// The mint refuses an expiry in the past, so a row that has already expired has
// to be written directly -- which is what a link that sat in a mailbox for a
// day is, and what the read has to refuse.
func staleLink() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}

	token := vouch.PrefixLink + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))

	return token, sum[:], nil
}
