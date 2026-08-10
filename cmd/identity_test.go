package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

// TestOnePersonHasSeveralIdentities is why Identity is a row.
//
// The same human arrives through the company's Entra tenant and through GitHub,
// and both have to land on one Holder. A column would make the second route a
// second person, with a second history and a second set of permissions.
func TestOnePersonHasSeveralIdentities(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.identity(t, ctx, b.AcmeUser, "entra", "8f14e45f-ea1e-4f0e-9a1b-2c3d4e5f6a7b")
	b.identity(t, ctx, b.AcmeUser, "github", "1074321")

	vs, err := b.Walled.Identity().List(b.as(ctx, b.AcmeUser, b.Acme), app.IdentityListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2)
}

// TestTwoPeopleCannotBeOneSubject is the account-takeover shape, refused.
//
// If two Holders could claim the same subject at the same provider, whoever
// logs in next becomes whichever row was found first -- and which one that is
// depends on an index.
func TestTwoPeopleCannotBeOneSubject(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other := b.holder(t, ctx, b.Acme, "somebody-else")

	b.identity(t, ctx, b.AcmeUser, "github", "1074321")

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: other.Bytes()}.Build(),
		Provider: "github",
		Subject:  "1074321",
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err))
}

// TestTheSameSubjectAtAnotherProviderIsAnotherIdentity, because the pair is the
// key and not either half of it. Two providers can and do use the same
// numbering.
func TestTheSameSubjectAtAnotherProviderIsAnotherIdentity(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.identity(t, ctx, b.AcmeUser, "github", "1074321")

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Provider: "gitlab",
		Subject:  "1074321",
	}.Build())
	x.NoError(err)
}

// TestAnUnlinkedSubjectComesFree is the question soft erasure raises here.
//
// Unlinking GitHub and linking it again is an ordinary thing for a person to
// do. If the erased row goes on holding the pair, the second link is refused
// with a conflict against a row nobody can see -- which is a support ticket
// with no resolution.
//
// payday's answer is that a unique constraint on a softly-erased entity becomes
// a partial index, so the pair is free again. This is here because that is a
// property of the generator rather than of anything written in this repository,
// and roster is the first app to lean on it for something other than an alias.
func TestAnUnlinkedSubjectComesFree(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.identity(t, ctx, b.AcmeUser, "github", "1074321")

	_, err := b.Ungated.Identity().Erase(ctx, v.Ref())
	x.NoError(err)

	_, err = b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Provider: "github",
		Subject:  "1074321",
	}.Build())
	x.NoError(err, "an unlinked identity still holds its subject")
}

// TestTheWallReachesThroughTheHolder is `tenanted: {via: "holder.tenant"}`.
//
// An Identity has no tenant edge of its own -- it belongs to whoever it names,
// and they belong to a tenant. The wall walks that path, and this asserts it
// walks it rather than leaving the rows open.
func TestTheWallReachesThroughTheHolder(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v := b.identity(t, ctx, b.AcmeUser, "entra", "8f14e45f")

	hooliUser := b.holder(t, ctx, b.Hooli, "theirs")

	_, err := b.Walled.Identity().Get(
		b.as(ctx, hooliUser, b.Hooli),
		app.IdentityGetRequest_builder{Ref: v.Ref()}.Build())

	// NotFound and not PermissionDenied: a row outside the wall is a row the
	// query did not match, and that it exists is itself not to be said.
	x.Equal(codes.NotFound, status.Code(err))
}

// TestASubjectThatLooksLikeAnAddressIsRefused is the mistake with an
// unrecoverable ending.
//
// A provider's subject has to be what that provider treats as immutable, and
// what gets written by mistake is the username or the email -- both are in the
// same claims and both read like a name. Nothing goes wrong at link time. It
// goes wrong months later when the address is reassigned to a new joiner, who
// signs in and is served as the person who left.
func TestASubjectThatLooksLikeAnAddressIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Provider: "entra",
		Subject:  "someone@acme.example",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))

	// And it says which field, so a console can put the message under the box
	// rather than at the top of the page.
	vs := pderr.Violations(err)
	x.Len(vs, 1)
	x.Equal("subject", vs[0].Field)
}

// TestASecondAccountAtOneProviderIsRefused.
//
// The pair being unique stops two people sharing a subject. This is the other
// direction: one person accumulating two accounts at one provider, which is
// what a link that found the wrong row looks like from here. A person has one
// account at each provider; if they have two, one belongs to somebody else.
func TestASecondAccountAtOneProviderIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.identity(t, ctx, b.AcmeUser, "github", "1074321")

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Provider: "github",
		Subject:  "2222222",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "already has a github identity")
}

// TestTheRulesApplyWithoutTheWallToo. `Ungated` is not a way around what this
// app means; it is a way around the wall.
func TestTheRulesApplyWithoutTheWallToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Provider: "ldap",
		Subject:  "",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}
