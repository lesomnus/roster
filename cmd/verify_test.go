package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAnAddressIsVerifiedByALinkThatSignsNobodyIn is `Email.Verify` and
// `Email.Confirm` is on the resource, because unlike recovery
// there is a row to reference; and worth strictly less than a recovery link,
// because a mailbox read once must not be an account held.
//
// The front door mints a link for an address, delivers it (not here -- roster
// does not mail), and confirms what comes back. What is pinned: the row is
// stamped at confirmation and not before; the link is spent by it; a link that
// is not one, and a link somebody else minted, answer the same nothing; and
// nothing about the response is a credential.
//
// And both directions of the discriminator, because for a while only one of
// them was written. The key here holds recovery's two methods as well as this
// pair -- which is the account app's key, where one issuer mints both kinds --
// so the `email` edge is the only thing that can tell them apart.
func TestAnAddressIsVerifiedByALinkThatSignsNobodyIn(t *testing.T) {
	const (
		verify      = "/roster.EmailService/Verify"
		confirm     = "/roster.EmailService/Confirm"
		redeem      = "/roster.VouchService/Redeem"
		listHolders = "/roster.HolderService/List"
	)

	x := require.New(t)
	b := keyFor(t, verify, confirm, redeem, listHolders)
	ctx := t.Context()

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: own, Address: "someone@contoso.example"}.Build())
	x.NoError(err)
	x.Nil(e.GetDateVerified())
	ref := app.EmailRef_builder{Id: e.GetId()}.Build()

	cl := app.NewEmailServiceClient(b.Conn)
	as := bearing(ctx, b.Token)

	v, err := cl.Verify(as, app.EmailVerifyRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.True(strings.HasPrefix(v.GetToken(), vouch.PrefixLink), "a verify link does not look like a link")

	t.Run("nothing is checked until the link comes back", func(t *testing.T) {
		x := require.New(t)

		row, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		x.Nil(row.GetDateVerified(), "minting a link checked the address")
	})

	t.Run("a link that is not one answers nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Confirm(as, app.EmailConfirmRequest_builder{Token: "rl_not-one"}.Build())
		x.Equal(codes.NotFound, status.Code(err))
		_, err = cl.Confirm(as, app.EmailConfirmRequest_builder{Token: "rd_a-delegation-instead"}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("the link stamps the address, once", func(t *testing.T) {
		x := require.New(t)

		res, err := cl.Confirm(as, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
		x.NoError(err)
		x.NotNil(res.GetEmail().GetDateVerified(), "confirmed and not stamped")
		x.NotContains(res.String(), "rd_", "a verification minted a delegation")

		row, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		x.NotNil(row.GetDateVerified())

		// Spent: a second confirmation of the same link is nothing.
		_, err = cl.Confirm(as, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a link was confirmed twice")
	})

	t.Run("and a verification link is not a way in", func(t *testing.T) {
		x := require.New(t)

		// A fresh one, since the link above was spent. Handed to the door that
		// mints, by the caller that minted it -- so the issuer check passes and
		// the `email` edge is what has to refuse.
		w, err := cl.Verify(as, app.EmailVerifyRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		res, err := app.NewVouchServiceClient(b.Conn).Redeem(as, app.VouchRedeemRequest_builder{
			Token:   w.GetToken(),
			Methods: []string{listHolders},
		}.Build())
		x.NoError(err)
		x.False(res.GetVerified().GetOk(), "a verification link signed somebody in")
		x.Empty(res.GetToken(), "a verification link minted a delegation")

		// Still there to be confirmed: refusing to spend it is not spending it.
		done, err := cl.Confirm(as, app.EmailConfirmRequest_builder{Token: w.GetToken()}.Build())
		x.NoError(err)
		x.NotNil(done.GetEmail().GetDateVerified())
	})

	t.Run("and a recovery link is not a verification", func(t *testing.T) {
		x := require.New(t)

		// Minted by `Vouch.Link` for the person, naming no address: worth more
		// than this, and so refused here -- a caller holding one learns nothing.
		link, err := vouch.New(b.Ungated, b.Ungated).Link(as, app.VouchLinkRequest_builder{
			Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		}.Build())
		if err == nil && link.GetToken() != "" {
			_, err = cl.Confirm(as, app.EmailConfirmRequest_builder{Token: link.GetToken()}.Build())
			x.Equal(codes.NotFound, status.Code(err), "a recovery link verified an address")
		}
	})
}
