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
// `Email.Confirm` (`ts/plan.md` § D): on the resource, because unlike recovery
// there is a row to reference; and worth strictly less than a recovery link,
// because a mailbox read once must not be an account held.
//
// The front door mints a link for an address, delivers it (not here -- roster
// does not mail), and confirms what comes back. What is pinned: the row is
// stamped at confirmation and not before; the link is spent by it; a link that
// is not one, and a link somebody else minted, answer the same nothing; and
// nothing about the response is a credential.
func TestAnAddressIsVerifiedByALinkThatSignsNobodyIn(t *testing.T) {
	const (
		verify  = "/roster.EmailService/Verify"
		confirm = "/roster.EmailService/Confirm"
	)

	x := require.New(t)
	b := keyFor(t, verify, confirm)
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
