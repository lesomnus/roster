package cmd_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
)

// TestHowSomebodySignsInIsAllOfIt: `HolderService/SignsIn` answers every
// account, every credential and every key somebody has.
//
// # What it was
//
// It read each through `List`, once, with no size and no cursor: the first
// page, which is twenty. So for somebody with twenty-five keys it answered
// twenty, and nothing in the answer said there were more -- and it is what an
// administrator reads to end the keys of an account, or a front door to learn
// whether somebody already signs in through a provider (#279). `MeService/Get`
// answered the same person all twenty-five.
//
// The message has no cursor, so the whole of it is the answer: every page is
// read.
func TestHowSomebodySignsInIsAllOfIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)

	const many = 25
	bot := b.holder(t, ctx, b.Contoso, "bot")
	whose := app.HolderRef_builder{Id: bot.Bytes()}.Build()
	for i := range many {
		_, sum, err := keys.Mint(keys.PrefixTenant)
		x.NoError(err)
		_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder: whose, Alias: fmt.Sprintf("key-%02d", i), Secret: sum, Methods: []string{app.MeService_Get_FullMethodName},
		}.Build())
		x.NoError(err)

		// An account at a provider of its own: a second at one provider is
		// refused as a link that went wrong.
		b.identity(t, ctx, bot, fmt.Sprintf("idp-%02d", i), fmt.Sprintf("subject-%02d", i))

		// And a security key, which is the kind somebody has several of.
		_, err = b.Ungated.Credential().Add(ctx, app.CredentialAddRequest_builder{
			Holder: whose, Kind: "webauthn", Name: fmt.Sprintf("key-%02d", i), Secret: []byte("a-key-shaped-thing"),
		}.Build())
		x.NoError(err)
	}

	got, err := app.NewHolderServiceClient(conn).SignsIn(admin, app.HolderSignsInRequest_builder{Ref: whose}.Build())
	x.NoError(err)
	x.Len(got.GetKeys(), many, "the first page of somebody's keys was answered as all of them")
	x.Len(got.GetIdentities(), many)
	x.Len(got.GetCredentials(), many)

	// In the order they were made, as every list is.
	for i := range many {
		x.Equal(fmt.Sprintf("key-%02d", i), got.GetKeys()[i].GetAlias())
		x.Equal(fmt.Sprintf("idp-%02d", i), got.GetIdentities()[i].GetProvider())
		x.Equal(fmt.Sprintf("key-%02d", i), got.GetCredentials()[i].GetName())
	}

	// And what the person reads of themselves is the same answer.
	theirs, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, bot), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Len(theirs.GetKeys(), many)
	for i := range many {
		x.Equal(got.GetKeys()[i].GetId(), theirs.GetKeys()[i].GetId())
		x.Equal(got.GetIdentities()[i].GetId(), theirs.GetIdentities()[i].GetId())
		x.Equal(got.GetCredentials()[i].GetName(), theirs.GetCredentials()[i].GetName())
	}
}

// TestWhereSomebodyReachesIsSaidOnceAndInOrder: the sites `MeService/Get` and
// `HolderService/Reaches` answer are each said once, in the order of their
// identifiers -- which is the order they were made in.
//
// # What it was
//
// A site was appended for each binding and each team that names it, as the
// rows came: somebody bound twice in Seoul, once in Busan, and on two of
// Seoul's teams was answered `[seoul seoul seoul busan seoul]`. A page that
// drew a row a site drew Seoul four times, and one that compared two answers
// compared two orders (#279).
//
// And how they sign in -- their accounts, credentials and keys -- was in the
// order the store gave, where their teams, groups and addresses each have one
// (`server/me`). They are in the order they were made, which is the order
// `SignsIn` answers an administrator in: two shapes saying one thing.
func TestWhereSomebodyReachesIsSaidOnceAndInOrder(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	kim := b.holder(t, ctx, b.Contoso, "kim")

	// Busan first, so that the order they are said in is not the order the
	// grants were made in.
	b.boundIn(t, ctx, kim, busan, "busan-reader", app.TeamService_Get_FullMethodName)
	b.boundIn(t, ctx, kim, seoul, "seoul-reader", app.TeamService_Get_FullMethodName)
	b.boundIn(t, ctx, kim, seoul, "seoul-lister", app.TeamService_List_FullMethodName)
	for _, alias := range []string{"day-shift", "night-shift"} {
		_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: kim.Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: b.team(t, ctx, seoul, alias).Bytes()}.Build(),
		}.Build())
		x.NoError(err)
	}

	want := [][]byte{seoul.Bytes(), busan.Bytes()}
	slices.SortFunc(want, bytes.Compare)
	x.Equal(seoul.Bytes(), want[0], "an identifier begins with when it was made")

	me, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, kim), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(want, me.GetSites(), "a site was said once for each grant that names it")

	reaches, err := app.NewHolderServiceClient(conn).Reaches(admin, app.HolderReachesRequest_builder{
		Ref: app.HolderRef_builder{Id: kim.Bytes()}.Build(), Roster: ptrTo(true),
	}.Build())
	x.NoError(err)
	x.Equal(want, reaches.GetSites())

	t.Run("and how they sign in is in the order it was made", func(t *testing.T) {
		x := require.New(t)
		park := b.holder(t, ctx, b.Contoso, "park")

		// Written so that the order of the rows is not the order they are
		// dated in: the newest first.
		const three = 3
		at := time.Now().Add(-time.Hour)
		for i := three - 1; i >= 0; i-- {
			when := at.Add(time.Duration(i) * time.Minute)

			_, sum, err := keys.Mint(keys.PrefixTenant)
			x.NoError(err)
			x.NoError(b.Ent.ApiKey.Create().
				SetId(pdid.New(pd.ApiKeyDomain).Uuid()).SetHolderId(park.Uuid()).
				SetAlias(fmt.Sprintf("key-%d", i)).SetDesc("").SetSecret(sum).
				SetDateCreated(when).SetDateUpdated(when).Exec(bare.TrustAdmission(ctx)))
		}

		me, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, park), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Len(me.GetKeys(), three)
		for i, k := range me.GetKeys() {
			x.Equal(fmt.Sprintf("key-%d", i), k.GetAlias())
		}
	})
}

// TestASearchSaysWhenItsPageWasTheLast: `HolderService/Search` answers an
// empty `next` with the last of what matches.
//
// # What it was
//
// It stopped at the row that filled the page and answered that row as where
// to carry on from, whether or not anything after it matched. So ten matches
// asked for ten at a time were a full page and a cursor, and the page after
// was empty: `next` is said to be *empty when this was the last of them*, and
// a caller that drew a "more" button by it drew one that showed nothing
// (#279). It reads on until it finds one more, or the end.
func TestASearchSaysWhenItsPageWasTheLast(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)
	holders := app.NewHolderServiceClient(conn)
	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	person := func(alias, department string) {
		_, err := holders.Add(admin, app.HolderAddRequest_builder{
			Tenant: contoso, Alias: alias, Profile: app.Profile_builder{Department: department}.Build(),
		}.Build())
		x.NoError(err)
	}
	// Ten of sales, with others between and after them.
	for i := range 10 {
		person(fmt.Sprintf("seller-%02d", i), "Sales")
		person(fmt.Sprintf("engineer-%02d", i), "Engineering")
	}

	search := func(size int32, after string) *app.HolderSearchResponse {
		res, err := holders.Search(admin, app.HolderSearchRequest_builder{Q: ptr("seller"), Size: size, After: after}.Build())
		x.NoError(err)

		return res
	}

	page := search(10, "")
	x.Len(page.GetItems(), 10)
	x.Empty(page.GetNext(), "a page that was the last of them said there is more")

	// With one more than a page, the first says so and the second is the end.
	person("seller-10", "Sales")
	page = search(10, "")
	x.Len(page.GetItems(), 10)
	x.NotEmpty(page.GetNext())
	page = search(10, page.GetNext())
	x.Len(page.GetItems(), 1)
	x.Empty(page.GetNext())

	// And read a few at a time, each is answered once.
	seen := map[string]int{}
	after := ""
	for range 10 {
		page = search(4, after)
		for _, v := range page.GetItems() {
			seen[v.GetAlias()]++
		}
		if after = page.GetNext(); after == "" {
			break
		}
	}
	x.Len(seen, 11)
	for alias, n := range seen {
		x.Equal(1, n, alias)
	}

	// A department is the whole of it, whatever the case.
	for _, department := range []string{"sales", "SALES"} {
		res, err := holders.Search(admin, app.HolderSearchRequest_builder{Department: ptr(department), Size: 100}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 11, department)
	}
	res, err := holders.Search(admin, app.HolderSearchRequest_builder{Department: ptr("Sale")}.Build())
	x.NoError(err)
	x.Empty(res.GetItems())
}

// TestAKeyReadsItsOwnNominationsEitherWayRound: a deployment key's `List` of
// its own nominations is read in the order it asks for.
//
// The layer writes the filter for a key (`coreNomination.List`), and built the
// request again to do it, out of the filters, the size and the cursor: what
// else the caller said was dropped, and `reverse` is something else a caller
// says. A key that asked for its newest nominations first was answered its
// oldest (#279).
func TestAKeyReadsItsOwnNominationsEitherWayRound(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, "/roster.NominationService/*")
	ctx := t.Context()

	fabrikam := add(t, ctx, b.Server, "fabrikam")
	nominates(t, b.Server, b.Contoso, b.Service, b.Who)
	nominates(t, b.Server, fabrikam, b.Service, addHolder(t, ctx, b.Server, fabrikam, "front"))

	nominations := app.NewNominationServiceClient(b.Conn)
	wide := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+b.Token))
	tenants := func(reverse bool) [][]byte {
		vs, err := nominations.List(wide, app.NominationListRequest_builder{Reverse: reverse}.Build())
		x.NoError(err)

		out := [][]byte{}
		for _, v := range vs.GetItems() {
			out = append(out, v.GetTenant().GetId())
		}

		return out
	}

	x.Equal([][]byte{b.Contoso.Bytes(), fabrikam.Bytes()}, tenants(false))
	x.Equal([][]byte{fabrikam.Bytes(), b.Contoso.Bytes()}, tenants(true), "a key that asked for the newest first was answered the oldest")
}
