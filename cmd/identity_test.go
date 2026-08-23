package cmd_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

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

	b.identity(t, ctx, b.ContosoUser, "entra", "8f14e45f-ea1e-4f0e-9a1b-2c3d4e5f6a7b")
	b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	vs, err := b.Walled.Identity().List(b.as(ctx, b.ContosoUser, b.Contoso), app.IdentityListRequest_builder{}.Build())
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

	other := b.holder(t, ctx, b.Contoso, "somebody-else")

	b.identity(t, ctx, b.ContosoUser, "github", "1074321")

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

	b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
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

	v := b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	// With a password as well, because `server/core` refuses the removal of
	// somebody's **last** way in -- which is a different rule and has its own
	// test. This one is about what the subject does afterwards.
	b.sets(t, ctx, b.ContosoUser, "correct horse battery staple")

	_, err := b.Ungated.Identity().Erase(ctx, v.Ref())
	x.NoError(err)

	_, err = b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
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

	v := b.identity(t, ctx, b.ContosoUser, "entra", "8f14e45f")

	fabrikamUser := b.holder(t, ctx, b.Fabrikam, "theirs")

	_, err := b.Walled.Identity().Get(
		b.as(ctx, fabrikamUser, b.Fabrikam),
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
		Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Provider: "entra",
		Subject:  "someone@contoso.example",
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

	b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Provider: "github",
		Subject:  "2222222",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "already has a github identity")
}

// TestASecondAccountIsRefusedBehindAFullPage is the same rule, for somebody who
// is not among the first people a deployment ever had.
//
// The check read a page of the tenant's identities and sifted them here. A page
// is 20 rows ordered by `date_created` ascending, so what it read was the
// twenty **oldest** -- and whether somebody was checked at all depended on when
// they joined. This puts twenty identities in the tenant first, so the person
// being checked is off the end of that page.
//
// It is written with a number rather than "a lot" on purpose: 20 is
// `list.size` in `proto/app/identity.proto`, and if that line moves this test
// should be read again rather than quietly go on passing.
func TestASecondAccountIsRefusedBehindAFullPage(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Twenty, on twenty other people, all older than the one below. Different
	// holders because the rule under test is about one holder, and putting them
	// on this one would be twenty violations of it.
	for i := range 20 {
		who := b.holder(t, ctx, b.Contoso, fmt.Sprintf("early-%02d", i))
		b.identity(t, ctx, who, "github", fmt.Sprintf("90000%02d", i))
	}

	b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Provider: "github",
		Subject:  "2222222",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "already has a github identity")
}

// TestASecondAccountIsRefusedWhoeverTheRefNames, which is every way a holder
// can be named and not the one way.
//
// [app.HolderRef] is a oneof: an identifier, a slug, or the IdP subject the
// person is already known by. Enrolment uses the last two -- what an OIDC
// callback holds is a subject and a tenant, not a row identifier -- so a check
// that only understood identifiers was inert on exactly the path that creates
// identities.
//
// The ref is handed to the filter as it arrived rather than resolved here,
// which is what makes all three work without this test naming any of them
// twice.
func TestASecondAccountIsRefusedWhoeverTheRefNames(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
				Alias:  proto.String("someone"),
			}.Build(),
		}.Build(),
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
		Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Provider: "ldap",
		Subject:  "",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestAProfileIsStoredOnTheHolder, which is roster being the profile service.
//
// The design puts it outside the identity provider on purpose: a customer IdP's
// `/userinfo` has only what that customer put in it, and the schema has to be
// ours to change. Product apps hold the identifier and ask for this.
//
// It is also the message-field-in-a-column path, which is a thing payday could
// not do until recently -- `encoding/json` cannot see an opaque message, so
// every such value stored as `{}` with nothing failing anywhere.
func TestAProfileIsStoredOnTheHolder(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "ada",
		Profile: app.Profile_builder{
			DisplayName: "Ada Lovelace",
			Department:  "Analytical Engines",
			Locale:      "en-GB",
		}.Build(),
	}.Build())
	x.NoError(err)

	// Read back rather than trusting the answer: an Add echoes what it was
	// given, so a value that was never stored still looks right.
	got, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: v.Ref()}.Build())
	x.NoError(err)
	x.Equal("Ada Lovelace", got.GetProfile().GetDisplayName())
	x.Equal("Analytical Engines", got.GetProfile().GetDepartment())

	// Replaced whole, which is the shape it was chosen for.
	u, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:         v.Ref(),
		Profile:     app.Profile_builder{DisplayName: "Ada Byron"}.Build(),
		DateUpdated: got.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	x.Equal("Ada Byron", u.GetProfile().GetDisplayName())
	x.Empty(u.GetProfile().GetDepartment())
}

// TestAHolderWithNoProfileHasNone, so that "not set" and "set to nothing" are
// different rows.
func TestAHolderWithNoProfileHasNone(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "plain",
	}.Build())
	x.NoError(err)
	x.False(v.HasProfile())
}

// TestIdentitiesCanBeListedByHolder is the question anybody looking at a person
// asks: what do they sign in with.
//
// The pair that names one row is no use for it -- a `ref` names one identity,
// and what is wanted is every identity of somebody. So `list.by` carries
// `holder`.
func TestIdentitiesCanBeListedByHolder(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other := b.holder(t, ctx, b.Contoso, "somebody-else")

	b.identity(t, ctx, b.ContosoUser, "entra", "8f14e45f-ea1e-4f0e-9a1b-2c3d4e5f6a7b")
	b.identity(t, ctx, b.ContosoUser, "github", "1074321")
	b.identity(t, ctx, other, "github", "2200002")

	of := func(who pdid.Id) *app.IdentityListRequest {
		return app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{
					Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
				}.Build(),
			},
		}.Build()
	}

	as := b.as(ctx, b.ContosoUser, b.Contoso)

	vs, err := b.Walled.Identity().List(as, of(b.ContosoUser))
	x.NoError(err)

	got := []string{}
	for _, v := range vs.GetItems() {
		got = append(got, v.GetProvider())
	}
	x.ElementsMatch([]string{"entra", "github"}, got)

	// And it is a filter, not a way in. Somebody in fabrikam naming an contoso holder
	// is answered with nothing: the wall reaches this entity through
	// `holder.tenant` and runs first.
	fabrikamUser := b.holder(t, ctx, b.Fabrikam, "outsider")

	vs, err = b.Walled.Identity().List(b.as(ctx, fabrikamUser, b.Fabrikam), of(b.ContosoUser))
	x.NoError(err)
	x.Empty(vs.GetItems(), "a filter can only cut the scope down, never widen it")
}

// TestIdentitiesCanBeListedByTenant is what the stamp made possible, and it
// could not be asked for at all before it.
//
// A `list.by` filter reaches one hop and `holder.tenant` is two, so there was
// nothing here for "every identity in this tenant" to name -- the generator
// refuses `holder.tenant` by name. The column is one hop from anywhere.
func TestIdentitiesCanBeListedByTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	fabrikamUser := b.holder(t, ctx, b.Fabrikam, "outsider")

	b.identity(t, ctx, b.ContosoUser, "entra", "8f14e45f-ea1e-4f0e-9a1b-2c3d4e5f6a7b")
	b.identity(t, ctx, b.ContosoUser, "github", "1074321")
	b.identity(t, ctx, fabrikamUser, "github", "9900001")

	of := func(tenant pdid.Id) *app.IdentityListRequest {
		return app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{TenantId: tenant.Bytes()}.Build(),
			},
		}.Build()
	}

	// The deployment, from outside every tenant, asks who signs in to contoso and
	// with what.
	vs, err := b.Ungated.Identity().List(ctx, of(b.Contoso))
	x.NoError(err)

	got := []string{}
	for _, v := range vs.GetItems() {
		got = append(got, v.GetProvider())
	}
	x.ElementsMatch([]string{"entra", "github"}, got)

	// And it is a filter, not a way in: somebody in fabrikam naming contoso is
	// answered with nothing. The wall reads the same column and ran first.
	vs, err = b.Walled.Identity().List(b.as(ctx, fabrikamUser, b.Fabrikam), of(b.Contoso))
	x.NoError(err)
	x.Empty(vs.GetItems(), "a filter can only cut the scope down, never widen it")
}

// TestTheStampIsTheServersToWrite: what a caller puts in the column is
// overwritten, including through the server the wall was never installed on.
//
// It matters here more than on most entities. The wall reads this column now,
// so a caller who could write it could put somebody's sign-in behind another
// tenant's wall.
func TestTheStampIsTheServersToWrite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Provider: "github",
		Subject:  "7700007",
		TenantId: b.Fabrikam.Bytes(),
	}.Build())
	x.NoError(err)

	got, err := pdid.From(v.GetTenantId())
	x.NoError(err)
	x.Equal(b.Contoso, got, "what holder.tenant reaches, not what the caller wrote")
}

// TestNobodyRemovesTheirLastWayIn is item 8 of PLAN.md's list.
//
// Removing it locks somebody out of their own account, and no deployment would
// want that configured differently -- so it is a layer, the way D17 put the
// built-in team rules in one rather than in a policy. *A configurable invariant
// is one that every deployment configures identically until one of them gets it
// wrong.*
//
// What counts as a way in is an `Identity` **or** a `Credential`: they are the
// two things a Login App and `VouchService` between them can turn into a
// signed-in person, so the count is over both.
func TestNobodyRemovesTheirLastWayIn(t *testing.T) {
	b, ctx := build(t)

	t.Run("the only identity, and no password", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "one-way")
		v := b.identity(t, ctx, who, "github", "9001")

		_, err := b.Ungated.Identity().Erase(ctx, v.Ref())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	t.Run("and the same one once they have a password", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "two-ways")
		v := b.identity(t, ctx, who, "github", "9002")
		b.sets(t, ctx, who, "correct horse battery staple")

		_, err := b.Ungated.Identity().Erase(ctx, v.Ref())
		x.NoError(err)
	})

	t.Run("and one of two providers", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "two-providers")
		v := b.identity(t, ctx, who, "github", "9003")
		b.identity(t, ctx, who, "entra", "9004")

		_, err := b.Ungated.Identity().Erase(ctx, v.Ref())
		x.NoError(err)
	})

	// Erasing the person is a different act with a different name, and it is
	// not what this stops.
	t.Run("and erasing the person is not stopped", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "leaving")
		b.identity(t, ctx, who, "github", "9005")

		_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
		x.NoError(err)
	})

	// And erasing what is not there still succeeds, which is the generated
	// rule this must not have changed on its way past.
	t.Run("and erasing what is gone succeeds", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "already-gone")
		v := b.identity(t, ctx, who, "github", "9006")
		b.sets(t, ctx, who, "correct horse battery staple")

		_, err := b.Ungated.Identity().Erase(ctx, v.Ref())
		x.NoError(err)

		_, err = b.Ungated.Identity().Erase(ctx, v.Ref())
		x.NoError(err)
	})
}
