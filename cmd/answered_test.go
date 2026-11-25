package cmd_test

import (
	"encoding/json"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestWhatAReadDidNotSelectIsAbsent: a field a `Get` or a `List` did not
// select is not in the answer, a stamp as much as any.
//
// `date_created` and `date_updated` not selected were answered as year one,
// `0001-01-01T00:00:00Z`, and `cli.md` told a page's author to read year one
// as *not selected*. `date_updated` is the version a write sends back, and
// year one sent back was refused as a version that moved. Right is absent,
// which payday answers now for every entity (#531).
func TestWhatAReadDidNotSelectIsAbsent(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	admin := b.holder(t, ctx, b.Contoso, "unselected-admin")
	b.mayAnything(admin, b.Contoso)
	as := b.asNobody(ctx, admin, b.Contoso)
	who := b.holder(t, ctx, b.Contoso, "unselected")

	got, err := b.Walled.Holder().Get(as, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Select: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("unselected", got.GetAlias())
	x.Equal(who.Bytes(), got.GetId(), "the identifier is always answered")
	x.False(got.HasDateUpdated(), "date_updated not selected is answered as %v", got.GetDateUpdated())
	x.False(got.HasDateCreated(), "date_created not selected is answered as %v", got.GetDateCreated())

	got, err = b.Walled.Holder().Get(as, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Select: app.HolderSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.True(got.HasDateUpdated(), "the version selected is answered")
	x.Empty(got.GetAlias())

	ls, err := b.Walled.Holder().List(as, app.HolderListRequest_builder{
		Select: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.NotEmpty(ls.GetItems())
	for _, v := range ls.GetItems() {
		x.NotEmpty(v.GetAlias())
		x.False(v.HasDateUpdated(), "%s: a listed row answers a stamp it did not select", v.GetAlias())
	}
}

// TestAListTakesASelect: a `List` fills the rows each listed row points at, as
// a `Get` does, each read by its own rule.
//
// A `List` took no `select`, and a listed row carried each row it points at as
// its identifier alone, so a list of the people of a site with their names was
// the list and then a call for every person (#531).
func TestAListTakesASelect(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	north := b.site(t, ctx, b.Contoso, "north")
	south := b.site(t, ctx, b.Contoso, "south")
	nina := b.holder(t, ctx, b.Contoso, "nina")
	sam := b.holder(t, ctx, b.Contoso, "sam")
	b.joins(t, ctx, nina, north)
	b.joins(t, ctx, sam, south)

	admin := b.holder(t, ctx, b.Contoso, "select-admin")
	b.mayAnything(admin, b.Contoso)

	people := app.SiteMembershipSelect_builder{
		Holder: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
		Site:   app.SiteSelect_builder{Alias: z.Ptr(true)}.Build(),
	}.Build()
	ls, err := b.Walled.SiteMembership().List(b.asNobody(ctx, admin, b.Contoso), app.SiteMembershipListRequest_builder{
		Filters: []*app.SiteMembershipFilter{app.SiteMembershipFilter_builder{
			Site: app.SiteRef_builder{Id: north.Bytes()}.Build(),
		}.Build()},
		Select: people,
	}.Build())
	x.NoError(err)
	x.Len(ls.GetItems(), 1)
	x.Equal("nina", ls.GetItems()[0].GetHolder().GetAlias(), "the person a place names, in the one call")
	x.Equal("north", ls.GetItems()[0].GetSite().GetAlias())

	// Somebody who holds everything in north alone lists north's people, and
	// south's not at all, with a select as without.
	everything, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: north.Bytes()}.Build(),
		Alias:   "north-everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	nora := b.holder(t, ctx, b.Contoso, "nora")
	b.binds(t, nora, mustId(t, everything.GetId()), &north)

	ls, err = b.Walled.SiteMembership().List(b.asNobody(ctx, nora, b.Contoso), app.SiteMembershipListRequest_builder{Select: people}.Build())
	x.NoError(err)
	for _, v := range ls.GetItems() {
		x.NotEqual("sam", v.GetHolder().GetAlias(), "a person of another site, read through a list's select")
		x.NotEqual("south", v.GetSite().GetAlias())
	}
	x.NotEmpty(ls.GetItems())

}

// TestAnEmptyListIsAList: `-o json` of a list with nothing in it prints its
// `items`, empty.
//
// It printed `{}`, which a script reading `.items` read as null where every
// other answer had an array (#531).
func TestAnEmptyListIsAList(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	out, _, err := boxRun(t, at(b.c, "admin"), "", "site", "list", "-o", "json")
	x.NoError(err)

	var got map[string]any
	x.NoError(json.Unmarshal([]byte(out), &got), "%s", out)
	items, ok := got["items"]
	x.True(ok, "an empty list printed %s", out)
	x.Equal([]any{}, items)
}
