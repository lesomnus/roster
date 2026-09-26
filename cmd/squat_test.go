package cmd_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// An address nobody proved does not name anybody, and does not keep anybody out.
//
// #48, and it is #42's sentence said about the other medium: **a name that
// resolves somebody may only be taken by proof.** F7 made an address resolve a
// person -- `Email` is unique on `(tenant, address)` and `vouch.byAddress` is what
// a front door calls with what somebody typed -- and nothing on that path read
// `date_verified`, so the field was a decoration on the one road it exists for.
//
// # What that cost, exactly
//
// Not a takeover. The first test below walks it: Alice writes an address she does
// not own onto **her own** row, which `mayWriteAWayIn` passes and is right to --
// `mayReach` passes for the caller's own row, which is what lets anybody add their
// own address. A recovery link asked for at that address is then mailed to that
// mailbox and mints a delegation for **Alice's** row, so the CEO reading it is
// signed in as Alice. That costs Alice.
//
// What it cost the CEO is the thing: the unique index meant `Email.Add` answered
// `AlreadyExists`, so they could not write the row, could not reach `Email.Verify`,
// and had no road to their own address at all -- a name the rightful holder is
// refused, with a refusal that says only that somebody has it. That is
// `host.proto` § *Why a hostname is not checked* one entity along.
//
// # And the two halves of the answer
//
// They have to be both, and either alone is worse than useless:
//
//   - **`byAddress` reads the stamp**, so an unproved row resolves nobody. Alone,
//     the squat becomes a permanent denial: the address resolves nobody and the
//     rightful holder still cannot write it.
//   - **A proof takes an unproved address** (`EmailVerifyRequest.holder`), so the
//     rightful holder has a road. Alone, the squatter still answers for the address
//     until somebody notices.
func TestAnAddressNobodyProvedNamesNobody(t *testing.T) {
	const theirs = "ceo@contoso.example"

	x := require.New(t)
	b, ctx := build(t)

	// The front door, which is what mints and spends a link: roster does not
	// deliver, so every call below is an app's rather than a person's.
	door := b.as(ctx, b.holder(t, ctx, b.Contoso, "front-door"), b.Contoso)

	// The squatter, and the address is not hers. Written onto her own row, which
	// is the write no rule refuses and none should.
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.sets(t, ctx, alice, "the one alice chose")

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err, "writing an address onto your own row is refused, which is not this rule's business")

	// And it names nobody, which is the half that removes the harm. Her own
	// password at an address she wrote down herself opens nothing.
	res := b.verifiesAt(t, ctx, "contoso", theirs, "the one alice chose")
	x.False(res.GetOk(), "an address nothing checked signed somebody in")
	x.Empty(res.GetHolder())

	// The rightful holder still cannot write the row -- the index is unchanged and
	// deliberately so, because it is what makes an address name **one** person.
	ceo := b.holder(t, ctx, b.Contoso, "ceo")
	b.sets(t, ctx, ceo, "the one the ceo chose")

	_, err = b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err),
		"two rows in one tenant hold one address, which is the rule F7 closed")

	// So the road is a claim: a link minted for the address on somebody else's
	// unproved row, naming whose claim it is. The token goes to the caller, which
	// is a front door, which mails it to the address -- so whoever reads that
	// mailbox is who ends up holding it.
	v, err := b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Holder: app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.True(strings.HasPrefix(v.GetToken(), "rl_"))

	// Spending it moves the address: Alice's row is gone and the CEO's is proved.
	done, err := b.Walled.Email().Confirm(door, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
	x.NoError(err)
	x.Equal(ceo.Bytes(), done.GetEmail().GetHolder().GetId(), "the address did not change hands")
	x.NotNil(done.GetEmail().GetDateVerified(), "it changed hands and nobody had proved it")
	x.Equal(theirs, done.GetEmail().GetAddress())

	// And now it names them, which is the whole point.
	res = b.verifiesAt(t, ctx, "contoso", theirs, "the one the ceo chose")
	x.True(res.GetOk(), "a proved address did not sign its holder in")
	x.Equal(ceo.Bytes(), res.GetHolder())

	// Alice's password at it opens nothing, and her row is not merely unproved --
	// it is gone, because two rows cannot hold one address.
	res = b.verifiesAt(t, ctx, "contoso", theirs, "the one alice chose")
	x.False(res.GetOk(), "the squatter still answers for the address")
}

// TestAProvedAddressDoesNotChangeHands, which is where this and #42 differ and do
// so on purpose.
//
// #42 let a hostname move on the latest proof because a DNS name's incumbent
// cannot be relied on to let go: a domain changes hands and the old holder is
// gone. An address lives inside one tenant, whose operator can erase a row -- so
// the reassignment case `email.proto` names (*organisations reassign a leaver's
// address to a new joiner*) has an owner-driven answer, and letting a proved
// address move on a second proof would turn one compromised mailbox into an
// account somebody else holds.
func TestAProvedAddressDoesNotChangeHands(t *testing.T) {
	const theirs = "someone@contoso.example"

	x := require.New(t)
	b, ctx := build(t)

	proves(t, ctx, b.Server, b.ContosoUser, theirs)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	door := b.as(ctx, b.holder(t, ctx, b.Contoso, "front-door"), b.Contoso)

	// Refused at the mint, which is where there is somebody to tell: a page that
	// drew *claim this address* over a confirmed one should stop drawing it.
	_, err := b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Contains(status.Convert(err).Message(), "does not change hands")
}

// TestAClaimIsCheckedAgainWhenItIsSpent, because fifteen minutes pass in between.
//
// `vouch.LinkFor` is the window, and what the row said when the link was minted is
// not what it says when somebody clicks it. Whoever proved the address in that
// window keeps it, and the claim is the thing that loses.
func TestAClaimIsCheckedAgainWhenItIsSpent(t *testing.T) {
	const theirs = "ceo@contoso.example"

	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err)

	alice := b.holder(t, ctx, b.Contoso, "alice")
	door := b.as(ctx, b.holder(t, ctx, b.Contoso, "front-door"), b.Contoso)

	// A claim, minted while the row was unproved.
	v, err := b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// And the incumbent proves it in the meantime, which is the ordinary road:
	// their own link, on their own row.
	own, err := b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{
		Ref: app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = b.Walled.Email().Confirm(door, app.EmailConfirmRequest_builder{Token: own.GetToken()}.Build())
	x.NoError(err)

	// So the claim loses, and is told why rather than answered `NotFound`: the
	// caller is a front door holding a link it was right to mint.
	_, err = b.Walled.Email().Confirm(door, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Contains(status.Convert(err).Message(), "does not change hands")

	// And the incumbent still holds it.
	res := b.verifiesAt(t, ctx, "contoso", theirs, "")
	x.False(res.GetOk())

	row, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{
		Ref: app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Select: app.EmailSelect_builder{
			DateVerified: z.Ptr(true),
			Holder:       app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(b.ContosoUser.Bytes(), row.GetHolder().GetId())
	x.NotNil(row.GetDateVerified())
}

// TestNobodyClaimsAnAddressOntoAnAccountWiderThanTheirOwn.
//
// The rule `Email.Add` already meets, arriving at the other road to the same row:
// a claim writes a way into an account, so it is held to `mayWriteAWayIn` on the
// **claimant**. What is deliberately not asked is `mayReach` on the row's holder --
// that guards a row somebody proved, and this is one nobody did.
func TestNobodyClaimsAnAddressOntoAnAccountWiderThanTheirOwn(t *testing.T) {
	const theirs = "ceo@contoso.example"

	x := require.New(t)
	b, ctx := build(t)

	// The administrator, who holds everything, and the squatter's row.
	admin := b.holder(t, ctx, b.Contoso, "admin")
	b.mayAnything(admin, b.Contoso)

	alice := b.holder(t, ctx, b.Contoso, "alice")
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err)

	// A person who holds nothing, claiming the address onto the administrator.
	// Which is `mayWriteAWayIn`'s own example, one door along: the mailbox would
	// be theirs and the account would be his.
	nobody := b.holder(t, ctx, b.Contoso, "nobody")

	_, err = b.Walled.Email().Verify(b.asNobody(ctx, nobody, b.Contoso), app.EmailVerifyRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Holder: app.HolderRef_builder{Id: admin.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))

	// Their own is fine, which is the case the rule passes for and has to.
	_, err = b.Walled.Email().Verify(b.asNobody(ctx, nobody, b.Contoso), app.EmailVerifyRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Holder: app.HolderRef_builder{Id: nobody.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// TestAClaimDoesNotCrossATenant, which the wall decides and nothing here has to.
//
// Said anyway, because it is the one property that makes this write narrow: an
// address crosses rows and never an organisation, so a consultant who is a person
// at two customers under one address keeps both (D3).
func TestAClaimDoesNotCrossATenant(t *testing.T) {
	const theirs = "someone@contoso.example"

	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err)

	stranger := b.holder(t, ctx, b.Fabrikam, "someone")

	// Through the wall as somebody at the other customer: the row is not there.
	_, err = b.Walled.Email().Verify(b.as(ctx, stranger, b.Fabrikam), app.EmailVerifyRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Holder: app.HolderRef_builder{Id: stranger.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestADirectoryTakesAnAddressNobodyProved, which is the second road to the same
// move and is deliberately the narrower one.
//
// `Email.Attest` has the address and the holder in one request, so it never needed
// a row to exist -- and it used to answer `NotFound` for an address that was
// somebody else's, which is what made a squatted address unreachable even to a
// directory that vouched for it.
//
// Only on `verified`, and only over an unproved row. A provider's word is its own
// word where a link is a round trip to a mailbox, so this road may take less.
func TestADirectoryTakesAnAddressNobodyProved(t *testing.T) {
	const theirs = "ceo@contoso.example"

	x := require.New(t)
	b, ctx := build(t)

	alice := b.holder(t, ctx, b.Contoso, "alice")
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err)

	ceo := b.holder(t, ctx, b.Contoso, "ceo")
	who := b.identity(t, ctx, ceo, "entra", "e1b2c3")

	attest := func(verified bool) (*app.Email, error) {
		return b.Walled.Email().Attest(b.as(ctx, ceo, b.Contoso), app.EmailAttestRequest_builder{
			Holder:    app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
			Address:   z.Ptr(theirs),
			VouchedBy: app.IdentityRef_builder{Id: who.GetId()}.Build(),
			Verified:  z.Ptr(verified),
		}.Build())
	}

	t.Run("and a directory that does not say takes nothing", func(t *testing.T) {
		x := require.New(t)

		// The common case, which `docs/login.md` names: Microsoft's endpoint often
		// sends no `email_verified` at all. Nothing to write down is nothing to
		// move, and the answer is the one this road always gave.
		_, err := attest(false)
		x.Equal(codes.NotFound, status.Code(err))

		row, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{
			Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
			Select: app.EmailSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(alice.Bytes(), row.GetHolder().GetId(), "an unverified attestation moved an address")
	})

	t.Run("and one that does takes it", func(t *testing.T) {
		x := require.New(t)

		v, err := attest(true)
		x.NoError(err)
		x.Equal(ceo.Bytes(), v.GetHolder().GetId())
		x.NotNil(v.GetDateVerified())
		x.Equal(who.GetId(), v.GetVouchedBy().GetId(), "the row does not say whose word it was")
	})

	t.Run("and not one that is already proved", func(t *testing.T) {
		x := require.New(t)

		// The CEO holds it proved now, so a second person's directory cannot take
		// it -- the same bound the link road has, on the road where it matters
		// more.
		other := b.holder(t, ctx, b.Contoso, "other")
		theirId := b.identity(t, ctx, other, "entra", "f4g5h6")

		_, err := b.Walled.Email().Attest(b.as(ctx, other, b.Contoso), app.EmailAttestRequest_builder{
			Holder:    app.HolderRef_builder{Id: other.Bytes()}.Build(),
			Address:   z.Ptr(theirs),
			VouchedBy: app.IdentityRef_builder{Id: theirId.GetId()}.Build(),
			Verified:  z.Ptr(true),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})
}

// TestTheOrdinaryVerificationIsUnchanged, which is the case that has to keep
// working or none of the above is worth having.
//
// A person proving an address already on their row: no `holder` in the request,
// `mayReach` on the row's holder as before, and what `Confirm` does is stamp the
// row rather than move anything.
func TestTheOrdinaryVerificationIsUnchanged(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)

	door := b.as(ctx, b.holder(t, ctx, b.Contoso, "front-door"), b.Contoso)

	ref := app.EmailRef_builder{Id: e.GetId()}.Build()
	v, err := b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{Ref: ref}.Build())
	x.NoError(err)

	done, err := b.Walled.Email().Confirm(door, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
	x.NoError(err)
	x.Equal(e.GetId(), done.GetEmail().GetId(), "the row was replaced rather than stamped")
	x.Equal(b.ContosoUser.Bytes(), done.GetEmail().GetHolder().GetId())
	x.NotNil(done.GetEmail().GetDateVerified())
}
