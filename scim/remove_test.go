package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// TestARemoveOfMembersTakesOutWhoItNames: a `remove` of `members` takes out
// the people it names, and everybody only where it names no list at all --
// no `value`, or a `null` -- which is what RFC 7644 §3.5.2.2 says. A member
// is named by `value`, and an element of a list that names nobody that way
// refuses the request before anything is written.
//
// An element with no `value` was dropped, and a `remove` whose list came out
// empty was read as everybody. So a `remove` with an empty list emptied a
// group of three, `204`, and so did one whose member was named by `$ref`
// alone, by `display` alone or by an empty `value`; and a `replace` whose one
// member was named by `$ref` alone emptied it too.
func TestARemoveOfMembersTakesOutWhoItNames(t *testing.T) {
	d := stand(t)
	a, b, c := d.someone(t, "ann", guid(t)), d.someone(t, "bob", guid(t)), d.someone(t, "cat", guid(t))
	ref := scim.Base + "/Users/" + a
	everybody := []string{a, b, c}

	// group is a group of the three, made afresh for each case.
	group := func(t *testing.T) string {
		t.Helper()
		code, got := d.group(t, http.MethodPost, "", map[string]any{
			"displayName": "G " + guid(t), "externalId": guid(t), "members": list(a, b, c),
		})
		require.Equal(t, http.StatusCreated, code, "%v", got)

		return got["id"].(string)
	}

	for _, tc := range []struct {
		what   string
		op     string
		status int
		left   []string
	}{
		{"a remove of an empty list takes nobody out", `{"op":"remove","path":"members","value":[]}`, http.StatusNoContent, everybody},
		{"a remove of null is everybody", `{"op":"remove","path":"members","value":null}`, http.StatusNoContent, []string{}},
		{"a remove with no value is everybody", `{"op":"remove","path":"members"}`, http.StatusNoContent, []string{}},
		{"a remove names who it takes out", `{"op":"remove","path":"members","value":[{"value":"` + a + `"}]}`, http.StatusNoContent, []string{b, c}},
		{"a member named by $ref alone is refused", `{"op":"Remove","path":"members","value":[{"$ref":"` + ref + `"}]}`, http.StatusBadRequest, everybody},
		{"a member named by display alone is refused", `{"op":"Remove","path":"members","value":[{"display":"ann"}]}`, http.StatusBadRequest, everybody},
		{"a member whose value is empty is refused", `{"op":"remove","path":"members","value":[{"value":""}]}`, http.StatusBadRequest, everybody},
		{"and beside one that is named", `{"op":"remove","path":"members","value":[{"value":"` + b + `"},{"display":"ann"}]}`, http.StatusBadRequest, everybody},
		{"a replace whose member is named by $ref alone is refused", `{"op":"replace","path":"members","value":[{"$ref":"` + ref + `"}]}`, http.StatusBadRequest, everybody},
		{"an add whose member is named by $ref alone is refused", `{"op":"add","path":"members","value":[{"$ref":"` + ref + `"}]}`, http.StatusBadRequest, everybody},
		{"a replace with an empty list empties it", `{"op":"replace","path":"members","value":[]}`, http.StatusNoContent, []string{}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			id := group(t)

			code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Groups/"+id, `{"Operations":[`+tc.op+`]}`)
			x.Equal(tc.status, code, "%v", got)
			if code == http.StatusBadRequest {
				x.Equal("invalidValue", got["scimType"])
			}
			x.ElementsMatch(tc.left, d.answered(t, id))
		})
	}

	t.Run("in a whole Group", func(t *testing.T) {
		x := require.New(t)
		code, got := d.group(t, http.MethodPost, "", map[string]any{
			"displayName": "Named by $ref", "members": []map[string]any{{"value": a}, {"$ref": ref}},
		})
		x.Equal(http.StatusBadRequest, code, "%v", got)
		x.Equal("invalidValue", got["scimType"])
		code, got = d.group(t, http.MethodGet, "?filter=displayName%20eq%20%22Named%20by%20%24ref%22", nil)
		x.Equal(http.StatusOK, code, "%v", got)
		x.EqualValues(0, got["totalResults"], "and no group is made")

		id := group(t)
		code, got = d.group(t, http.MethodPut, "/"+id, map[string]any{
			"displayName": "Ops", "members": []map[string]any{{"value": a}, {"display": "bob"}},
		})
		x.Equal(http.StatusBadRequest, code, "%v", got)
		x.Equal("invalidValue", got["scimType"])
		x.ElementsMatch(everybody, d.answered(t, id))
	})
}
