package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// TestADirectorySaysSomebodysTitle: the core User's `title` (RFC 7643
// §4.1.1) is `Profile.title`, taken in wherever a department is -- a create,
// a replace, each `PATCH` operation that sets or removes it -- and answered
// with the User.
//
// A title was among what the endpoint answered as accepted and wrote to the
// log, and kept nowhere: Entra maps `jobTitle` to it, and an app reading the
// person from roster had a department and no title (#415). It is the core
// schema's, not the enterprise extension's, so it is answered beside
// `displayName` and not inside the extension.
func TestADirectorySaysSomebodysTitle(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	body := erin()
	body["title"] = "Engineer"
	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	x.Equal("Engineer", got["title"], "the User made is answered with no title")
	x.Equal("Engineer", d.holder(t, id).GetProfile().GetTitle(), "a title made with somebody was not kept")
	x.Equal("R&D", d.holder(t, id).GetProfile().GetDepartment())

	t.Run("a replace", func(t *testing.T) {
		x := require.New(t)
		body := erin()
		body["title"] = "Staff Engineer"
		code, got, _ := d.call(t, d.key, http.MethodPut, scim.Base+"/Users/"+id, body)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal("Staff Engineer", got["title"])
		x.Equal("Staff Engineer", d.holder(t, id).GetProfile().GetTitle())
	})

	t.Run("a patch that sets it, in each dialect", func(t *testing.T) {
		for _, op := range []map[string]any{
			{"op": "Replace", "path": "title", "value": "Principal Engineer"},
			{"op": "add", "path": "urn:ietf:params:scim:schemas:core:2.0:User:title", "value": "Distinguished Engineer"},
			{"op": "replace", "value": map[string]any{"title": "Fellow"}},
		} {
			x := require.New(t)
			code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
				"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
				"Operations": []map[string]any{op},
			})
			x.Equal(http.StatusOK, code, "%v", got)
			want := op["value"]
			if v, ok := want.(map[string]any); ok {
				want = v["title"]
			}
			x.Equal(want, got["title"], "%v", op)
			x.Equal(want, d.holder(t, id).GetProfile().GetTitle(), "%v", op)
		}
	})

	t.Run("a patch that removes it", func(t *testing.T) {
		x := require.New(t)
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
			"Operations": []map[string]any{{"op": "remove", "path": "title"}},
		})
		x.Equal(http.StatusOK, code, "%v", got)
		x.NotContains(got, "title", "a User with no title is answered with one")
		x.Empty(d.holder(t, id).GetProfile().GetTitle())
		x.Equal("R&D", d.holder(t, id).GetProfile().GetDepartment(), "taking the title away took the department with it")
	})

	t.Run("and the schema says it is kept", func(t *testing.T) {
		x := require.New(t)
		code, got, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Schemas/urn:ietf:params:scim:schemas:core:2.0:User", nil)
		x.Equal(http.StatusOK, code, "%v", got)
		names := []string{}
		for _, a := range got["attributes"].([]any) {
			names = append(names, a.(map[string]any)["name"].(string))
		}
		x.Contains(names, "title")
	})
}
