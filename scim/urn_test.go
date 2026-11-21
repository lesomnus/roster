package scim_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

const (
	coreUser  = "urn:ietf:params:scim:schemas:core:2.0:User"
	coreGroup = "urn:ietf:params:scim:schemas:core:2.0:Group"
)

// TestAPathBehindItsSchemasUrnIsTheAttributeItNames: RFC 7644 §3.10 lets a
// client write an attribute behind its schema's URN --
// `urn:ietf:params:scim:schemas:core:2.0:User:active` -- and that is the
// attribute it names: in a path, as a key of an operation with no path, and
// inside the URN itself as a key holding an object. A path behind a URN the
// endpoint keeps nothing of is what any such path is: not kept, and said.
//
// The long way fell through to *not kept here*: `replace` of `active` with
// `false` was `200`, the answer said `"active": true`, and nobody was
// suspended.
func TestAPathBehindItsSchemasUrnIsTheAttributeItNames(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	id := d.someone(t, "erin", erinOid)
	patch := func(ops ...map[string]any) map[string]any {
		t.Helper()
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, patchOf(ops...))
		x.Equal(http.StatusOK, code, "%v", got)

		return got
	}

	got := patch(map[string]any{"op": "replace", "path": coreUser + ":active", "value": false})
	x.Equal(false, got["active"])
	x.NotNil(d.holder(t, id).GetDateDisabled(), "suspended")

	got = patch(map[string]any{"op": "replace", "path": strings.ToUpper(coreUser) + ":displayName", "value": "Erin U."})
	x.Equal("Erin U.", got["displayName"], "a URN in another case is the same URN")

	got = patch(map[string]any{"op": "replace", "value": map[string]any{coreUser + ":displayName": "Erin V."}})
	x.Equal("Erin V.", got["displayName"], "a key of an operation with no path")

	got = patch(map[string]any{"op": "replace", "value": map[string]any{coreUser: map[string]any{"active": true, "displayName": "Erin W."}}})
	x.Equal(true, got["active"], "the URN as a key holding an object")
	x.Equal("Erin W.", got["displayName"])
	x.Nil(d.holder(t, id).GetDateDisabled())

	patch(map[string]any{"op": "remove", "path": coreUser + ":displayName"})
	x.Empty(d.holder(t, id).GetProfile().GetDisplayName(), "and a remove")

	d.log.lines()
	patch(map[string]any{"op": "replace", "path": "urn:example:scim:schemas:extension:acme:2.0:User:costCenter", "value": "4100"})
	x.True(slices.ContainsFunc(d.log.lines(), func(l string) bool { return strings.Contains(l, "scim: not kept here") }),
		"a URN nothing is kept of is not kept, and said")

	t.Run("a group", func(t *testing.T) {
		x := require.New(t)
		fred := d.someone(t, "fred", fredOid)
		code, got := d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops Team", "externalId": opsOid})
		x.Equal(http.StatusCreated, code, "%v", got)
		g := got["id"].(string)

		code, got = d.group(t, http.MethodPatch, "/"+g, patchOf(
			map[string]any{"op": "replace", "path": coreGroup + ":displayName", "value": "Operations"},
			map[string]any{"op": "add", "path": coreGroup + ":members", "value": list(id, fred)},
		))
		x.Equal(http.StatusNoContent, code, "%v", got)
		code, got = d.group(t, http.MethodGet, "/"+g, nil)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal("Operations", got["displayName"])
		x.ElementsMatch([]string{id, fred}, d.answered(t, g))

		code, got = d.group(t, http.MethodPatch, "/"+g, patchOf(
			map[string]any{"op": "remove", "path": coreGroup + `:members[value eq "` + fred + `"]`},
			map[string]any{"op": "replace", "value": map[string]any{coreGroup: map[string]any{"displayName": "Ops"}}},
		))
		x.Equal(http.StatusNoContent, code, "%v", got)
		code, got = d.group(t, http.MethodGet, "/"+g, nil)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal("Ops", got["displayName"])
		x.Equal([]string{id}, d.answered(t, g))

		code, got = d.group(t, http.MethodPatch, "/"+g, patchOf(map[string]any{"op": "remove", "path": coreGroup + ":members"}))
		x.Equal(http.StatusNoContent, code, "%v", got)
		x.Empty(d.answered(t, g))
	})
}
