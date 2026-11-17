package cmd_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// What somebody erased still holds, and what meets it.
//
// An erase stamps the holder alone. Their alias comes free at once; their
// addresses and their accounts at providers stay theirs, because somebody
// erased can be restored (`roster restore`) until they are forgotten, and a
// restore gives back who they were. That is decided, and stays.
//
// What was wrong is that the rows built on that did not know it. Each read the
// person through one of the rows that outlived them and asked something of a
// holder who was not there, and what came back was not a refusal but a fault:
// `Unknown` from the claim a front door hands over, and `NotFound: Holder not
// found` -- about a person the caller never named -- from a claim of their
// unproved address and from an administrator's erase of their account.

// TestAClaimOfSomebodyErasedReachesNobody: `VouchService/Accept` refuses a
// claim that reaches somebody erased, and said so in its comment, by a check
// that never fired. The identity's holder is an edge, an edge is loaded
// through the rows still there, and so somebody erased comes back as a bare
// reference: the `date_erased` the check read was a column of a row that was
// not loaded. What stopped them was the next thing to need their tenant, as an
// identifier with no bytes: `Unknown: not a payday identifier`, which a front
// door can only answer as its own failure.
//
// It is the reading #331 gave a key and #347 a link: a holder that did not
// come back is gone. And the answer is the one a claim that reaches nobody
// gets, so that an app in front learns nothing from which it was.
func TestAClaimOfSomebodyErasedReachesNobody(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, accept, listPeople)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Server, b.Contoso, "arrives-through-entra")
	mustIdentity(t, ctx, b.Server, who, "entra", "entra-subject-1")

	c := app.NewVouchServiceClient(b.Conn)
	claim := func(subject string) error {
		_, err := c.Accept(bearing(ctx, b.Token), app.VouchAcceptRequest_builder{
			Claim:   app.VouchClaim_builder{Tenant: b.Contoso.Bytes(), Provider: "entra", Subject: subject}.Build(),
			Methods: []string{listPeople},
		}.Build())

		return err
	}
	x.NoError(claim("entra-subject-1"), "the control: while they are here, the claim reaches them")

	nobody := claim("nobody-here")
	x.Equal(codes.NotFound, status.Code(nobody))

	gone, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)
	x.True(gone.GetErased())

	err = claim("entra-subject-1")
	x.Equal(codes.NotFound, status.Code(err), "a claim of somebody erased was answered as a fault: %v", err)
	x.Equal(nobody.Error(), err.Error(), "and is told apart from a claim nobody has")

	// Brought back, they are who they were, by the account they had.
	x.NoError(forget.Restore(ctx, b.Ent, who, audit.Contents{}, nil))
	x.NoError(claim("entra-subject-1"))
}

// TestAnAddressSomebodyErasedNeverProvedIsClaimedLikeAnyOther: an address
// nobody proved keeps nobody out (`TestAnAddressNobodyProvedNamesNobody`), and
// the road to one that another row holds is a claim: a link mailed to it,
// which whoever reads the mailbox spends. A claim asks that the claimant is of
// the address's own tenant, and read that tenant off the row's **holder** --
// who, erased, is not there. So the claim was `NotFound: Holder not found`,
// and an address somebody typed and never proved was held against its owner
// by a person who had left, for as long as nobody forgot them.
//
// The address's tenant is the row's own. A proved address still does not
// change hands: that one is theirs to come back to.
func TestAnAddressSomebodyErasedNeverProvedIsClaimedLikeAnyOther(t *testing.T) {
	const typed, theirs = "ceo@contoso.example", "gone@contoso.example"

	x := require.New(t)
	b, ctx := build(t)
	door := b.as(ctx, b.holder(t, ctx, b.Contoso, "front-door"), b.Contoso)

	gone := b.holder(t, ctx, b.Contoso, "gone")
	ref := app.HolderRef_builder{Id: gone.Bytes()}.Build()
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: typed}.Build())
	x.NoError(err)
	proved, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: theirs}.Build())
	x.NoError(err)
	_, err = b.Ungated.Email().Patch(ctx, app.EmailPatchRequest_builder{
		Ref: app.EmailRef_builder{Id: proved.GetId()}.Build(), DateVerified: timestamppb.Now(), DateUpdated: proved.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Holder().Erase(ctx, ref)
	x.NoError(err)

	ceo := b.holder(t, ctx, b.Contoso, "ceo")
	at := func(address string) *app.EmailRef {
		return app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(address)}.Build()}.Build()
	}

	v, err := b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{
		Ref: at(typed), Holder: app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
	}.Build())
	x.NoError(err, "an address nobody proved was kept from its owner by somebody erased")
	x.True(strings.HasPrefix(v.GetToken(), "rl_"))

	done, err := b.Walled.Email().Confirm(door, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
	x.NoError(err)
	x.Equal(ceo.Bytes(), done.GetEmail().GetHolder().GetId(), "the address did not change hands")
	x.NotNil(done.GetEmail().GetDateVerified())

	// The one they proved is theirs until they are forgotten, or an
	// administrator erases the row.
	_, err = b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{
		Ref: at(theirs), Holder: app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "a proved address changed hands: %v", err)

	// And a claimant of another tenant is still refused, by the tenant the
	// row itself says.
	stranger := b.holder(t, ctx, b.Fabrikam, "stranger")
	other, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "another").Bytes()}.Build(), Address: "another@contoso.example",
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Email().Verify(b.as(ctx, b.holder(t, ctx, b.Contoso, "front-door"), b.Contoso), app.EmailVerifyRequest_builder{
		Ref: app.EmailRef_builder{Id: other.GetId()}.Build(), Holder: app.HolderRef_builder{Id: stranger.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a claimant of another tenant: %v", err)
}

// TestAnAdministratorLetsGoOfWhatSomebodyErasedHolds: what somebody erased
// holds is theirs until they are forgotten, and until then an administrator
// may decide it is not: the address goes to a new joiner, the account at the
// directory is somebody else's now. Their address and their number could be
// erased, and their account at a provider could not. `IdentityService/Erase`
// counts what its person would be left to sign in by, and asked that of a
// holder who is not there: `NotFound: Holder not found`.
//
// It is nobody's last way in: they are erased, and nothing signs them in. So
// it is erased by whoever could have erased it while they were there -- the
// rule about whose way in is taken away reads what they hold all the same --
// and what it held is then free for somebody else.
//
// The rows are found as an administrator finds anything of the tenant's: a
// list filtered to the person answers what outlived them, though naming them
// answers nobody.
func TestAnAdministratorLetsGoOfWhatSomebodyErasedHolds(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.connected(t, ctx, b.Contoso, "entra")

	admin := b.holder(t, ctx, b.Contoso, "admin")
	b.mayAnything(admin, b.Contoso)
	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, admin)

	// Somebody who held something, with one of each, erased.
	gone := b.holder(t, ctx, b.Contoso, "gone")
	ref := app.HolderRef_builder{Id: gone.Bytes()}.Build()
	b.mayCall(t, ctx, gone, "reads-people", app.HolderService_List_FullMethodName)
	b.identity(t, ctx, gone, "entra", "gone-at-entra")
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: "gone@contoso.example"}.Build())
	x.NoError(err)
	_, err = b.Ungated.Phone().Add(ctx, app.PhoneAddRequest_builder{Holder: ref, Number: "+82 2 555 0100", Kind: "work"}.Build())
	x.NoError(err)
	_, err = b.Ungated.Holder().Erase(ctx, ref)
	x.NoError(err)

	holders := app.NewHolderServiceClient(conn)
	ids := app.NewIdentityServiceClient(conn)
	emails := app.NewEmailServiceClient(conn)
	phones := app.NewPhoneServiceClient(conn)

	// Naming them answers nobody; a list filtered to them answers what they
	// still hold, which is how it is found.
	_, err = holders.Get(wire, app.HolderGetRequest_builder{Ref: ref}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	accounts, err := ids.List(wire, app.IdentityListRequest_builder{
		Filters: []*app.IdentityFilter{app.IdentityFilter_builder{Holder: ref}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(accounts.GetItems(), 1)
	x.Equal("gone-at-entra", accounts.GetItems()[0].GetSubject())
	addresses, err := emails.List(wire, app.EmailListRequest_builder{
		Filters: []*app.EmailFilter{app.EmailFilter_builder{Holder: ref}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(addresses.GetItems(), 1)
	numbers, err := phones.List(wire, app.PhoneListRequest_builder{
		Filters: []*app.PhoneFilter{app.PhoneFilter_builder{Holder: ref}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(numbers.GetItems(), 1)

	account := app.IdentityRef_builder{Id: accounts.GetItems()[0].GetId()}.Build()

	// Not by somebody who could not have taken it while they were there: the
	// desk holds the erase, and not what they held.
	desk := b.holder(t, ctx, b.Contoso, "desk")
	as := b.mayCall(t, ctx, desk, "takes-ways-in-away", app.IdentityService_Erase_FullMethodName)
	_, err = b.Walled.Identity().Erase(as, account)
	x.Equal(codes.PermissionDenied, status.Code(err), "the account of somebody who held more than the caller: %v", err)

	// By an administrator, each of the three.
	i, err := ids.Erase(wire, account)
	x.NoError(err, "an administrator could not let go of an erased person's account")
	x.True(i.GetErased())
	e, err := emails.Erase(wire, app.EmailRef_builder{Id: addresses.GetItems()[0].GetId()}.Build())
	x.NoError(err)
	x.True(e.GetErased())
	p, err := phones.Erase(wire, app.PhoneRef_builder{Id: numbers.GetItems()[0].GetId()}.Build())
	x.NoError(err)
	x.True(p.GetErased())

	// And what they held is somebody else's to have.
	next := b.holder(t, ctx, b.Contoso, "joiner")
	_, err = ids.Add(wire, app.IdentityAddRequest_builder{
		Holder: app.HolderRef_builder{Id: next.Bytes()}.Build(), Provider: "entra", Subject: "gone-at-entra",
	}.Build())
	x.NoError(err)
	_, err = emails.Add(wire, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: next.Bytes()}.Build(), Address: "gone@contoso.example",
	}.Build())
	x.NoError(err)

	// Somebody who is there is held to the rule as before: their only way in
	// is not taken away.
	only := b.identity(t, ctx, b.holder(t, ctx, b.Contoso, "stays"), "entra", "stays-at-entra")
	_, err = ids.Erase(wire, app.IdentityRef_builder{Id: only.GetId()}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "somebody's last way in: %v", err)
}
