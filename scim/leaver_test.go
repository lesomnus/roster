package scim_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// TestADirectorysWordThatSomebodyLeftIsWrittenFirst: a `PUT` of a whole User,
// or one `PATCH`, that says `active: false` beside something roster refuses --
// an address the form refuses, a display name too long, a `userName` somebody
// else is called -- is answered with that refusal, and the person is suspended
// all the same.
//
// They were not. What the directory calls somebody was written first, then
// their profile, then `active`, and the first refusal was the answer: `400`,
// `400`, `400`, `409`, and in each the person who had left went on signing in,
// every cycle the directory sent it again.
//
// Of the two halves a request with no transaction across its writes can be
// left with, the one that keeps a leaver signed in is the wrong one. So the
// suspension goes first, and nothing is rolled back. What lets somebody back
// in is the other way about: `active: true` is written after the rest, so a
// request that is refused lifts nothing.
func TestADirectorysWordThatSomebodyLeftIsWrittenFirst(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	patch := func(ops ...map[string]any) map[string]any {
		return map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": ops}
	}
	off := map[string]any{"op": "replace", "path": "active", "value": false}

	// Somebody else the directory already calls by a name.
	taken := erin()
	taken["userName"], taken["externalId"] = "taken.name@corp.contoso.example", opsOid
	taken["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "taken@contoso.example"}}
	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", taken)
	x.Equal(http.StatusCreated, code, "%v", got)

	for _, tc := range []struct {
		what     string
		name     string
		oid      string
		method   string
		body     func(name string) any
		status   int
		scimType string
	}{
		{
			what: "a whole User, and an address the form refuses", name: "leaver-a", oid: erinOid, method: http.MethodPut,
			body: func(name string) any {
				b := erin()
				b["userName"], b["externalId"], b["active"] = name+"@contoso.example", erinOid, false
				b["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "zoë@contoso.example"}}

				return b
			},
			status: http.StatusBadRequest, scimType: "invalidValue",
		},
		{
			what: "a PATCH, and a work address the form refuses", name: "leaver-b", oid: fredOid, method: http.MethodPatch,
			body: func(string) any {
				return patch(off, map[string]any{"op": "replace", "path": `emails[type eq "work"].value`, "value": "zoë@contoso.example"})
			},
			status: http.StatusBadRequest, scimType: "invalidValue",
		},
		{
			what: "a PATCH, and a display name of 300 letters", name: "leaver-c", oid: ginaOid, method: http.MethodPatch,
			body: func(string) any {
				return patch(off, map[string]any{"op": "replace", "path": "displayName", "value": strings.Repeat("a", 300)})
			},
			status: http.StatusBadRequest, scimType: "invalidValue",
		},
		{
			what: "a PATCH, and a userName somebody else is called", name: "leaver-d", oid: guid(t), method: http.MethodPatch,
			body: func(string) any {
				return patch(off, map[string]any{"op": "replace", "path": "userName", "value": "taken.name@corp.contoso.example"})
			},
			status: http.StatusConflict, scimType: "uniqueness",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			id := d.someone(t, tc.name, tc.oid)

			code, got, _ := d.call(t, d.key, tc.method, scim.Base+"/Users/"+id, tc.body(tc.name))
			x.Equal(tc.status, code, "%v", got)
			x.Equal(tc.scimType, got["scimType"], "%v", got)

			h := d.holder(t, id)
			x.NotNil(h.GetDateDisabled(), "suspended, though the rest was refused")
			x.Equal("inactive", h.GetDirectory(), "and by the directory")
		})
	}

	t.Run("and back, beside a display name refused", func(t *testing.T) {
		x := require.New(t)
		id := d.someone(t, "leaver-e", guid(t))
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, patch(off))
		x.Equal(http.StatusOK, code, "%v", got)

		code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, patch(
			map[string]any{"op": "replace", "path": "active", "value": true},
			map[string]any{"op": "replace", "path": "displayName", "value": strings.Repeat("a", 300)},
		))
		x.Equal(http.StatusBadRequest, code, "%v", got)
		x.NotNil(d.holder(t, id).GetDateDisabled(), "what lets somebody back in is not written ahead of a refusal")
	})
}
