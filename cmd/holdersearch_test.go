package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestSearchFindsPeopleByWhoTheyAre is `Holder.Search`: the question a
// directory answers and `List` cannot. A fragment against alias, name and
// display name; a department and an employee number exactly; walled, so a
// caller in one customer finds nobody in another; and paged on `List`'s own
// cursor, so reading through answers everybody once.
func TestSearchFindsPeopleByWhoTheyAre(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	person := func(tenant pdid.Id, alias, display, department, no string) pdid.Id {
		who := b.holder(t, ctx, tenant, alias)
		ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
		v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		_, err = b.Ungated.Holder().Update(ctx, app.HolderUpdateRequest_builder{
			Ref:         ref,
			DateUpdated: v.GetDateUpdated(),
			Profile:     app.Profile_builder{DisplayName: display, Department: department, EmployeeNo: no}.Build(),
		}.Build())
		x.NoError(err)
		return who
	}
	erin := person(b.Contoso, "erin", "Erin Kim", "platform", "1001")
	person(b.Contoso, "sam", "Sam Park", "platform", "1002")
	person(b.Contoso, "kim-taylor", "Taylor", "sales", "2001")
	person(b.Fabrikam, "kim", "Kim Lee", "platform", "1001")

	as := b.as(ctx, erin, b.Contoso)
	search := func(req *app.HolderSearchRequest) []string {
		res, err := b.Walled.Holder().Search(as, req)
		x.NoError(err)
		out := []string{}
		for _, v := range res.GetItems() {
			out = append(out, v.GetAlias())
		}
		return out
	}

	x.ElementsMatch([]string{"erin", "kim-taylor"}, search(app.HolderSearchRequest_builder{Q: ptr("KIM")}.Build()),
		"a fragment against alias and display name, whatever the case -- and not fabrikam's kim")
	x.ElementsMatch([]string{"erin", "sam"}, search(app.HolderSearchRequest_builder{Department: ptr("Platform")}.Build()))
	x.ElementsMatch([]string{"erin"}, search(app.HolderSearchRequest_builder{EmployeeNo: ptr("1001")}.Build()),
		"fabrikam's 1001 came through the wall")
	x.ElementsMatch([]string{"sam"}, search(app.HolderSearchRequest_builder{Q: ptr("park"), Department: ptr("platform")}.Build()))
	x.Empty(search(app.HolderSearchRequest_builder{Q: ptr("nobody")}.Build()))

	_, err := b.Walled.Holder().Search(as, app.HolderSearchRequest_builder{}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a search for nothing is a List")

	t.Run("paged on List's cursor, everybody once", func(t *testing.T) {
		x := require.New(t)
		seen := []string{}
		after := ""
		for range 10 {
			res, err := b.Walled.Holder().Search(as, app.HolderSearchRequest_builder{Q: ptr("a"), Size: 1, After: after}.Build())
			x.NoError(err)
			for _, v := range res.GetItems() {
				seen = append(seen, v.GetAlias())
			}
			if res.GetNext() == "" {
				break
			}
			after = res.GetNext()
		}
		// Everybody in contoso whose alias, name or display name has an "a":
		// sam, kim-taylor (Taylor) -- and erin's "Erin Kim" does not.
		x.ElementsMatch([]string{"sam", "kim-taylor"}, seen)
	})
}
