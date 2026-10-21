package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
)

// TestOnePersonHasSeveralAddresses, which is the first reason this is a row.
func TestOnePersonHasSeveralAddresses(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	for _, a := range []string{"someone@contoso.example", "someone@personal.example"} {
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: a,
		}.Build())
		x.NoError(err)
	}

	vs, err := b.Walled.Email().List(b.as(ctx, b.ContosoUser, b.Contoso), app.EmailListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2)
}

// TestTheSameAddressTwiceForOnePersonIsRefused. Nobody lists an address twice,
// and a duplicate is a bug in whatever wrote it rather than a fact.
func TestTheSameAddressTwiceForOnePersonIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	add := func() error {
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: "someone@contoso.example",
		}.Build())

		return err
	}

	x.NoError(add())
	x.Equal(codes.AlreadyExists, status.Code(add()))
}

// TestTwoPeopleMayShareAnAddress is the decision, asserted.
//
// A consultant is legitimately a person in two tenants under one address, and a
// deployment-wide constraint makes the second one an error with no resolution
// -- neither organisation can give the address up, and support cannot either.
//
// The cost is that nothing here can resolve anybody by address, which is
// exactly what the design already requires: an address is not a key. See
// `email.proto`'s index comments.
func TestTwoPeopleMayShareAnAddress(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	theirs := b.holder(t, ctx, b.Fabrikam, "consultant")

	for _, of := range []struct {
		holder []byte
	}{{b.ContosoUser.Bytes()}, {theirs.Bytes()}} {
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: of.holder}.Build(),
			Address: "consultant@example.com",
		}.Build())
		x.NoError(err)
	}
}

// TestVerificationIsATimeAndNotAFlag.
//
// "Verified" on its own is not the question asked afterwards. A check from
// three years ago is a different fact from one from this morning, and a boolean
// cannot tell them apart -- which matters when deciding whether an address may
// be trusted to link an account.
func TestVerificationIsATimeAndNotAFlag(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)
	x.False(v.HasDateVerified(), "an address arrives unverified")

	at := timestamppb.Now()
	u, err := b.Ungated.Email().Patch(ctx, app.EmailPatchRequest_builder{
		Ref:          v.Ref(),
		DateVerified: at,
		DateUpdated:  v.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	x.True(u.HasDateVerified())
}

// TestAnAddressCanNameTheIdentityThatVouchedForIt.
//
// An address that arrived in a provider's claims is only as good as that
// provider's check. Keeping which identity vouched is what lets a later
// decision -- may this address link an account -- be made on evidence rather
// than on a flag somebody set.
func TestAnAddressCanNameTheIdentityThatVouchedForIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	id := b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	v, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address:   "someone@users.noreply.github.com",
		VouchedBy: id.Ref(),
	}.Build())
	x.NoError(err)

	got, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{
		Ref: v.Ref(),
		Select: app.EmailSelect_builder{
			All:       ptr(true),
			VouchedBy: app.IdentitySelect_builder{All: ptr(true)}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("github", got.GetVouchedBy().GetProvider())
}

// TestAnAddressWithNoVoucherIsAllowed, since most arrive by somebody typing
// them in.
func TestAnAddressWithNoVoucherIsAllowed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "typed-in@contoso.example",
	}.Build())
	x.NoError(err)
	x.False(v.HasVouchedBy())
}

// TestAnAddressSaysWhoWroteIt, which is what tells a person's claim about their
// own mailbox from somebody else's word about them (`Email.entered_by`).
//
// The writer is the frame's actor and never the request's: a caller who could
// say who wrote an address could make their own claim read as an operator's
// invitation, which is the shape a sign-in matching by address was taken by.
func TestAnAddressSaysWhoWroteIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	got := func(v *app.Email) []byte {
		t.Helper()
		e, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{
			Ref:    v.Ref(),
			Select: app.EmailSelect_builder{EnteredBy: ptr(true)}.Build(),
		}.Build())
		x.NoError(err)

		return e.GetEnteredBy()
	}

	// Their own, typed in as themselves.
	own, err := b.Walled.Email().Add(b.as(ctx, b.ContosoUser, b.Contoso), app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "mine@contoso.example",
	}.Build())
	x.NoError(err)
	x.Equal(b.ContosoUser.Bytes(), got(own))

	// An administrator's, about somebody else.
	admin := b.holder(t, ctx, b.Contoso, "admin")
	theirs, err := b.Walled.Email().Add(b.as(ctx, admin, b.Contoso), app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "entered@contoso.example",
	}.Build())
	x.NoError(err)
	x.Equal(admin.Bytes(), got(theirs))

	// The deployment's own command, with nobody asking: nobody to name.
	x.Empty(got(func() *app.Email {
		v, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: "seeded@contoso.example",
		}.Build())
		x.NoError(err)

		return v
	}()))

	// And a request that says it itself is refused, whoever it names.
	_, err = b.Walled.Email().Add(b.as(ctx, b.ContosoUser, b.Contoso), app.EmailAddRequest_builder{
		Holder:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address:   "forged@contoso.example",
		EnteredBy: admin.Bytes(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
}

func ptr[T any](v T) *T { return &v }
