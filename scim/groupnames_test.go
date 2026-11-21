package scim_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// aliasOf is a group's alias as roster has it, by its id at the endpoint.
func (d *deployment) aliasOf(t *testing.T, group string) string {
	t.Helper()
	k, err := pdid.Parse(group)
	require.NoError(t, err)
	v, err := d.s.Ungated.Group().Get(t.Context(), rstr.GroupGetRequest_builder{
		Ref: rstr.GroupRef_builder{Id: k.Bytes()}.Build(), Select: rstr.GroupSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	require.NoError(t, err)

	return v.GetAlias()
}

// TestTwoOfADirectorysGroupsWhoseNamesMakeOneAliasAreTwoGroups: a group's
// alias is made from its name by the rule written for an address -- it stops
// at an `@`, drops the digits it begins with, and folds punctuation into a
// hyphen -- so two names a directory keeps apart may make one alias. The
// second is made under that alias and a suffix, as a person whose alias is
// taken is.
//
// It was refused: `R/D` beside `R&D`, `Sales@US` beside `Sales@EU`, `2025
// Interns` beside `2024 Interns` were each `409 uniqueness`, *Group already
// exists*, on every cycle, with no group here called that.
func TestTwoOfADirectorysGroupsWhoseNamesMakeOneAliasAreTwoGroups(t *testing.T) {
	d := stand(t)

	for _, tc := range []struct{ first, second, alias string }{
		{"R&D", "R/D", "r-d"},
		{"Sales@EU", "Sales@US", "sales"},
		{"2024 Interns", "2025 Interns", "interns"},
	} {
		t.Run(tc.second, func(t *testing.T) {
			x := require.New(t)

			ids := []string{}
			for _, name := range []string{tc.first, tc.second} {
				code, got := d.group(t, http.MethodPost, "", map[string]any{"displayName": name, "externalId": guid(t)})
				x.Equal(http.StatusCreated, code, "%v", got)
				ids = append(ids, got["id"].(string))

				code, got = d.group(t, http.MethodGet, "?excludedAttributes=members&filter="+url.QueryEscape(`displayName eq "`+name+`"`), nil)
				x.Equal(http.StatusOK, code, "%v", got)
				x.EqualValues(1, got["totalResults"])
				x.Equal(ids[len(ids)-1], got["Resources"].([]any)[0].(map[string]any)["id"])
			}
			x.NotEqual(ids[0], ids[1], "two groups")

			x.Equal(tc.alias, d.aliasOf(t, ids[0]), "the first keeps the alias its name makes")
			second := d.aliasOf(t, ids[1])
			x.True(strings.HasPrefix(second, tc.alias+"-"), "and the second's begins with it: %s", second)
		})
	}
}

// TestAGroupIsNotRenamedToWhatAnotherIsCalled: a rename asks what a create
// asks, of the same groups, and is refused the same way.
//
// It was `204`, after which a lookup by that name found two groups, though a
// create under it was refused: a directory that finds two where it looked
// for one cannot say which it means.
func TestAGroupIsNotRenamedToWhatAnotherIsCalled(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	code, got := d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops Team", "externalId": opsOid})
	x.Equal(http.StatusCreated, code, "%v", got)
	code, got = d.group(t, http.MethodPost, "", map[string]any{"displayName": "Platform", "externalId": guid(t)})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)

	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(map[string]any{"op": "replace", "path": "displayName", "value": "ops team"}))
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])
	x.Equal("a group here is already called that", got["detail"])

	code, got = d.group(t, http.MethodPut, "/"+id, map[string]any{"displayName": "Ops Team"})
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])

	code, got = d.group(t, http.MethodGet, "?excludedAttributes=members&filter="+url.QueryEscape(`displayName eq "Ops Team"`), nil)
	x.Equal(http.StatusOK, code, "%v", got)
	x.EqualValues(1, got["totalResults"], "one group is called that")

	// Renamed to what it is called already, it is nothing, and to what
	// nobody is called, renamed.
	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(map[string]any{"op": "replace", "path": "displayName", "value": "Platform"}))
	x.Equal(http.StatusNoContent, code, "%v", got)
	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(map[string]any{"op": "replace", "path": "displayName", "value": "Platform Team"}))
	x.Equal(http.StatusNoContent, code, "%v", got)
}
