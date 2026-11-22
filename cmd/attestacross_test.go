package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// TestAnAddressIsVouchedForByItsOwnPersonsIdentity: whose word an attested
// address is, is an identity of the person the address is for -- read as the
// caller reads, so one of another tenant is nobody's.
//
// # What it was
//
// `Attest` took `vouched_by` and wrote it as the address's edge, asked nothing.
// Named by identifier, or by `(tenant, provider, subject)`, it could be an
// identity of **another tenant**: a caller attested an address onto their own
// row under somebody else's customer's identity, and then read their own row
// with a `select` through the edge -- which is loaded with no wall -- and was
// answered that identity, its holder with their profile, and their tenant with
// what an operator had written on it. Rows `Get` answers `404` for (#274).
//
// And the verb was an oracle twice. A subject the other tenant had was a
// write, and one it had not was `Identity not found`. And an address attested
// onto another tenant's **holder**, by identifier, was refused by the reach
// rule in the words that say what they hold, where somebody who is nobody was
// `Holder not found`: the rule was asked before anything was read through the
// wall.
func TestAnAddressIsVouchedForByItsOwnPersonsIdentity(t *testing.T) {
	b, ctx := build(t)

	// Contoso's, who attests; and Fabrikam's, whose identity is not theirs to
	// name -- somebody who holds something, so that the reach rule has
	// something to say of them.
	mine := b.holder(t, ctx, b.Contoso, "mine")
	own := b.identity(t, ctx, mine, "entra", "mine-at-entra")

	other := b.holder(t, ctx, b.Fabrikam, "other")
	theirs := b.identity(t, ctx, other, "github", "gh-4242")
	b.as(ctx, other, b.Fabrikam)

	// Who attests holds the two methods and no more: what the other holds is
	// then more than theirs, and the reach rule has it to say.
	as := b.mayCall(t, ctx, mine, "attests", app.EmailService_Attest_FullMethodName, app.EmailService_Get_FullMethodName)
	attest := func(holder pdid.Id, address string, by *app.IdentityRef) (*app.Email, error) {
		return b.Walled.Email().Attest(as, app.EmailAttestRequest_builder{
			Holder:    app.HolderRef_builder{Id: holder.Bytes()}.Build(),
			Address:   address,
			VouchedBy: by,
		}.Build())
	}
	nobodys := app.IdentityRef_builder{
		Subject: app.IdentityRefBySubject_builder{
			TenantId: b.Fabrikam.Bytes(),
			Provider: z.Ptr("github"),
			Subject:  z.Ptr("nobody-at-github"),
		}.Build(),
	}.Build()

	t.Run("another tenant's identity is nobody's, by either name", func(t *testing.T) {
		x := require.New(t)

		_, missing := attest(mine, "a@contoso.example", nobodys)
		x.Equal(codes.NotFound, status.Code(missing), "%v", missing)

		for name, by := range map[string]*app.IdentityRef{
			"by identifier": app.IdentityRef_builder{Id: theirs.GetId()}.Build(),
			"by subject": app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					TenantId: b.Fabrikam.Bytes(),
					Provider: z.Ptr("github"),
					Subject:  z.Ptr("gh-4242"),
				}.Build(),
			}.Build(),
		} {
			_, err := attest(mine, "a@contoso.example", by)
			x.Equal(codes.NotFound, status.Code(err), "%s: %v", name, err)

			// The same answer as for a subject they do not have, word for
			// word: which is what does not say whether they have it.
			x.Equal(status.Convert(missing).Message(), status.Convert(err).Message(), name)
		}

		// And nothing was written to read through.
		vs, err := b.Ungated.Email().List(ctx, app.EmailListRequest_builder{
			Filters: []*app.EmailFilter{app.EmailFilter_builder{
				Holder: app.HolderRef_builder{Id: mine.Bytes()}.Build(),
			}.Build()},
		}.Build())
		x.NoError(err)
		x.Empty(vs.GetItems(), "an address was written under an identity the caller cannot see")
	})

	t.Run("nor somebody else's of the same tenant", func(t *testing.T) {
		x := require.New(t)

		colleague := b.holder(t, ctx, b.Contoso, "colleague")
		hers := b.identity(t, ctx, colleague, "entra", "colleague-at-entra")

		_, err := attest(mine, "b@contoso.example", app.IdentityRef_builder{Id: hers.GetId()}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.ErrorContains(err, "vouched_by")
	})

	t.Run("their own is, as a sign-in says it", func(t *testing.T) {
		x := require.New(t)

		v, err := attest(mine, "c@contoso.example", app.IdentityRef_builder{Id: own.GetId()}.Build())
		x.NoError(err)

		got, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{
			Ref:    app.EmailRef_builder{Id: v.GetId()}.Build(),
			Select: app.EmailSelect_builder{VouchedBy: app.IdentitySelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(own.GetId(), got.GetVouchedBy().GetId())
	})

	// The reach rule, asked of somebody the wall would not have shown: it
	// says what they hold, which is theirs and their tenant's to know.
	t.Run("another tenant's person is nobody, and not somebody who holds more", func(t *testing.T) {
		x := require.New(t)

		nobody := pdid.New(pd.HolderDomain)
		_, missing := attest(nobody, "d@contoso.example", app.IdentityRef_builder{Id: own.GetId()}.Build())
		x.Equal(codes.NotFound, status.Code(missing), "%v", missing)

		_, err := attest(other, "d@contoso.example", app.IdentityRef_builder{Id: own.GetId()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
		x.Equal(status.Convert(missing).Message(), status.Convert(err).Message())
	})
}
