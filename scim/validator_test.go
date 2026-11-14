package scim_test

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// What Microsoft's SCIM Validator asks of an endpoint, a test of its to a
// subtest here, in the order and by the requests its tutorial lists
// (learn.microsoft.com/entra/identity/app-provisioning/scim-validator-tutorial,
// read 2026-11-13).
//
// # Why it is written out here
//
// The validator is a page Microsoft hosts: an endpoint's address and a token
// go into a form, and a person reads the result. Nothing of it runs in CI. What
// it asks is published, though, request by request, so the same questions are
// asked here on every change -- and the page is for what its tutorial does not
// say, once, against a deployment.
//
// # What it sends
//
// By *Discover schema*, which is what it recommends where `/Schemas` is
// served: a value for every attribute the schema lists, made up, and so no two
// alike -- a `userName` that is not the address in `emails`. An endpoint that
// kept one where a directory says two reads back the wrong one, which is what
// this found the first time it ran: somebody made by a `userName` that was
// not their address was not found by it.
//
// So what the schema lists is held to what is sent here
// (`TestTheSchemaListsWhatIsKept`): an attribute added to one and not the
// other is an attribute the validator would ask about and this would not.
//
// And as the page was seen to say them, the first time it was run against
// this endpoint (`TestWhatMicrosoftsValidatorSent`, which replays that run):
// one number under every `type` a number may have, `"primary": "true"` in
// quotes, an address of no `type`, and a `manager` that is a word it made up
// and nobody's id.

// made is a string nobody else was given.
func made(t *testing.T) string {
	t.Helper()
	b := make([]byte, 5)
	_, err := rand.Read(b)
	require.NoError(t, err)

	return hex.EncodeToString(b)
}

// guid is an identifier in the shape a directory's is.
func guid(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// validatorUser is a User with a value for every attribute `/Schemas` lists,
// as the page writes one.
func validatorUser(t *testing.T) map[string]any {
	n := made(t)

	// One number, said once for each kind there is.
	numbers := []map[string]any{}
	for _, k := range []string{"work", "mobile", "fax", "home", "pager", "other"} {
		numbers = append(numbers, map[string]any{"type": k, "value": "55-501-" + n[:4]})
	}

	return map[string]any{
		"schemas":           []string{"urn:ietf:params:scim:schemas:core:2.0:User", enterprise},
		"userName":          "login-" + n + "@contoso.example",
		"externalId":        guid(t),
		"displayName":       "Display " + n,
		"preferredLanguage": "sr-Latn-ME",
		"active":            true,
		"emails":            []map[string]any{{"value": "mail-" + n + "@contoso.example", "primary": "true"}},
		"phoneNumbers":      numbers,
		enterprise:          map[string]any{"department": "Department " + n, "employeeNumber": n, "manager": "MANAGER" + n},
	}
}

func TestWhatMicrosoftsValidatorAsks(t *testing.T) {
	d := stand(t)

	post := func(t *testing.T, path string, body map[string]any) (int, map[string]any) {
		t.Helper()
		code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+path, body)

		return code, got
	}
	patch := func(t *testing.T, path string, ops ...map[string]any) int {
		t.Helper()
		if ops == nil {
			// None is a list of none, which is what the page sends.
			ops = []map[string]any{}
		}
		code, _, _ := d.call(t, d.key, http.MethodPatch, scim.Base+path, map[string]any{
			"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": ops,
		})

		return code
	}
	// find is the validator's read-back: the resource by its joining property.
	find := func(t *testing.T, kind, attr, value string) []any {
		t.Helper()
		code, got, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/"+kind+"?filter="+url.QueryEscape(attr+` eq "`+value+`"`), nil)
		require.Equal(t, http.StatusOK, code, "%v", got)

		return got["Resources"].([]any)
	}
	one := func(t *testing.T, kind, attr, value string) map[string]any {
		t.Helper()
		vs := find(t, kind, attr, value)
		require.Len(t, vs, 1, "%s by %s eq %q", kind, attr, value)

		return vs[0].(map[string]any)
	}
	// same is every value a User was sent with, read back.
	same := func(t *testing.T, sent, got map[string]any) {
		t.Helper()
		x := require.New(t)
		for _, k := range []string{"userName", "externalId", "displayName", "preferredLanguage", "active"} {
			x.Equal(sent[k], got[k], k)
		}
		x.Equal(sent["emails"].([]map[string]any)[0]["value"], got["emails"].([]any)[0].(map[string]any)["value"], "emails")

		numbers := sent["phoneNumbers"].([]map[string]any)
		x.Len(got["phoneNumbers"], len(numbers), "a number said for six kinds is six of theirs")
		for i, v := range got["phoneNumbers"].([]any) {
			x.Equal(numbers[i]["type"], v.(map[string]any)["type"], "phoneNumbers")
			x.Equal(numbers[i]["value"], v.(map[string]any)["value"], "phoneNumbers")
		}

		// A manager who is nobody here is not kept, and the rest is.
		ent := got[enterprise].(map[string]any)
		x.Equal(sent[enterprise].(map[string]any)["department"], ent["department"])
		x.Equal(sent[enterprise].(map[string]any)["employeeNumber"], ent["employeeNumber"])
		x.NotContains(ent, "manager")
	}

	t.Run("Create New User", func(t *testing.T) {
		x := require.New(t)
		u := validatorUser(t)
		code, got := post(t, "/Users", u)
		x.Equal(http.StatusCreated, code, "%v", got)
		x.NotEmpty(got["id"])
		same(t, u, one(t, "Users", "userName", u["userName"].(string)))

		// Its clean-up, sent where a hard delete is offered: gone to the
		// directory, which is what a delete is to it.
		code, _, _ = d.call(t, d.key, http.MethodDelete, scim.Base+"/Users/"+got["id"].(string), nil)
		x.Equal(http.StatusNoContent, code)
		x.Empty(find(t, "Users", "userName", u["userName"].(string)))
	})

	t.Run("Create Duplicate User", func(t *testing.T) {
		x := require.New(t)
		u := validatorUser(t)
		code, got := post(t, "/Users", u)
		x.Equal(http.StatusCreated, code, "%v", got)
		code, got = post(t, "/Users", u)
		x.Equal(http.StatusConflict, code, "%v", got)
	})

	t.Run("Add Attributes", func(t *testing.T) {
		x := require.New(t)
		u := validatorUser(t)
		code, got := post(t, "/Users", map[string]any{"schemas": u["schemas"], "userName": u["userName"], "externalId": u["externalId"]})
		x.Equal(http.StatusCreated, code, "%v", got)

		x.Equal(http.StatusOK, patch(t, "/Users/"+got["id"].(string),
			map[string]any{"op": "add", "path": "displayName", "value": u["displayName"]},
			map[string]any{"op": "add", "path": "preferredLanguage", "value": u["preferredLanguage"]},
			map[string]any{"op": "add", "path": enterprise + ":department", "value": "Added"},
			map[string]any{"op": "add", "path": `phoneNumbers[type eq "work"].value`, "value": "+1 555 0100"},
		))
		now := one(t, "Users", "userName", u["userName"].(string))
		x.Equal(u["displayName"], now["displayName"])
		x.Equal(u["preferredLanguage"], now["preferredLanguage"])
		x.Equal("Added", now[enterprise].(map[string]any)["department"])
		x.Equal("+1 555 0100", now["phoneNumbers"].([]any)[0].(map[string]any)["value"])
	})

	t.Run("Replace User Attributes", func(t *testing.T) {
		x := require.New(t)
		u := validatorUser(t)
		code, got := post(t, "/Users", u)
		x.Equal(http.StatusCreated, code, "%v", got)

		x.Equal(http.StatusOK, patch(t, "/Users/"+got["id"].(string),
			map[string]any{"op": "replace", "path": "displayName", "value": "Replaced"},
			map[string]any{"op": "replace", "path": enterprise + ":employeeNumber", "value": "9999"},
			map[string]any{"op": "replace", "path": `phoneNumbers[type eq "work"].value`, "value": "+1 555 0199"},
		))
		now := one(t, "Users", "userName", u["userName"].(string))
		x.Equal("Replaced", now["displayName"])
		x.Equal("9999", now[enterprise].(map[string]any)["employeeNumber"])
		x.Equal("+1 555 0199", now["phoneNumbers"].([]any)[0].(map[string]any)["value"])
		x.Equal(u["preferredLanguage"], now["preferredLanguage"], "what was not replaced is as it was")
	})

	t.Run("Update Joining Property", func(t *testing.T) {
		x := require.New(t)
		u := validatorUser(t)
		code, got := post(t, "/Users", u)
		x.Equal(http.StatusCreated, code, "%v", got)

		renamed := "renamed-" + made(t) + "@contoso.example"
		x.Equal(http.StatusOK, patch(t, "/Users/"+got["id"].(string),
			map[string]any{"op": "replace", "path": "userName", "value": renamed}))
		now := one(t, "Users", "userName", renamed)
		x.Equal(got["id"], now["id"])
		x.Equal(renamed, now["userName"])
	})

	t.Run("Update Active Attribute to False", func(t *testing.T) {
		x := require.New(t)
		u := validatorUser(t)
		code, got := post(t, "/Users", u)
		x.Equal(http.StatusCreated, code, "%v", got)

		x.Equal(http.StatusOK, patch(t, "/Users/"+got["id"].(string),
			map[string]any{"op": "replace", "path": "active", "value": false}))
		now := one(t, "Users", "userName", u["userName"].(string))
		x.Equal(false, now["active"], "somebody disabled is still read, and read as disabled")
	})

	group := func(t *testing.T) map[string]any {
		return map[string]any{
			"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Group"},
			"displayName": "Group " + made(t),
			"externalId":  guid(t),
		}
	}

	t.Run("Create New Group", func(t *testing.T) {
		x := require.New(t)
		g := group(t)
		code, got := post(t, "/Groups", g)
		x.Equal(http.StatusCreated, code, "%v", got)
		x.NotEmpty(got["id"])
		now := one(t, "Groups", "displayName", g["displayName"].(string))
		x.Equal(g["displayName"], now["displayName"])
		x.Equal(g["externalId"], now["externalId"])
	})

	t.Run("Create Duplicate Group", func(t *testing.T) {
		x := require.New(t)
		g := group(t)
		code, got := post(t, "/Groups", g)
		x.Equal(http.StatusCreated, code, "%v", got)
		code, got = post(t, "/Groups", g)
		x.Equal(http.StatusConflict, code, "%v", got)
	})

	t.Run("Update Group Attributes", func(t *testing.T) {
		x := require.New(t)
		g := group(t)
		code, got := post(t, "/Groups", g)
		x.Equal(http.StatusCreated, code, "%v", got)

		renamed := "Renamed " + made(t)
		x.Equal(http.StatusNoContent, patch(t, "/Groups/"+got["id"].(string),
			map[string]any{"op": "replace", "path": "displayName", "value": renamed}))
		now := one(t, "Groups", "displayName", renamed)
		x.Equal(got["id"], now["id"])
		x.Equal(renamed, now["displayName"])
	})

	// Not in its tutorial, and in its report: a group it deleted is gone by
	// every way it then asks, and one that was never here is no different.
	t.Run("Delete a Group", func(t *testing.T) {
		x := require.New(t)
		g := group(t)
		code, got := post(t, "/Groups", g)
		x.Equal(http.StatusCreated, code, "%v", got)
		at := scim.Base + "/Groups/" + got["id"].(string)

		code, _, _ = d.call(t, d.key, http.MethodDelete, at, nil)
		x.Equal(http.StatusNoContent, code)
		code, _, _ = d.call(t, d.key, http.MethodGet, at, nil)
		x.Equal(http.StatusNotFound, code, "GET of what was deleted")
		code, _, _ = d.call(t, d.key, http.MethodDelete, at, nil)
		x.Equal(http.StatusNotFound, code, "DELETE request on the same Group should return 404 NotFound")
		x.Empty(find(t, "Groups", "displayName", g["displayName"].(string)))

		code, _, _ = d.call(t, d.key, http.MethodDelete, scim.Base+"/Groups/"+guid(t), nil)
		x.Equal(http.StatusNotFound, code, "Delete a non-existent Group")
	})

	t.Run("Patch Group - Replace Attributes, of which it found none", func(t *testing.T) {
		x := require.New(t)
		g := group(t)
		code, got := post(t, "/Groups", g)
		x.Equal(http.StatusCreated, code, "%v", got)

		x.Equal(http.StatusNoContent, patch(t, "/Groups/"+got["id"].(string)))
		now := one(t, "Groups", "displayName", g["displayName"].(string))
		x.Equal(got["id"], now["id"])
	})

	// Its report, the second time: the one group it read with anybody in it
	// was "not the schema of a group". Two people put in the way it puts them,
	// read back as it reads them, and one taken out the way it takes one out
	// where it got that far (`members[value eq "…"]`).
	t.Run("Patch Group - Remove Member", func(t *testing.T) {
		x := require.New(t)
		code, g := post(t, "/Groups", group(t))
		x.Equal(http.StatusCreated, code, "%v", g)
		code, a := post(t, "/Users", validatorUser(t))
		x.Equal(http.StatusCreated, code, "%v", a)
		code, b := post(t, "/Users", validatorUser(t))
		x.Equal(http.StatusCreated, code, "%v", b)
		at := "/Groups/" + g["id"].(string)

		x.Equal(http.StatusNoContent, patch(t, at, map[string]any{
			"op": "replace", "path": "members", "value": []map[string]any{{"value": a["id"]}, {"value": b["id"]}},
		}))
		code, said, _ := d.raw(t, d.key, http.MethodGet, scim.Base+at, nil)
		x.Equal(http.StatusOK, code, "%s", said)
		x.NoError(readable(said), "%s", said)
		for _, id := range []string{a["id"].(string), b["id"].(string)} {
			x.Contains(string(said), `{"value":"`+id+`","$ref":"`+scim.Base+`/Users/`+id+`","type":"User"}`,
				"a member as RFC 7644 §3.7.2 writes one: `value`, `$ref`, `type`")
		}

		x.Equal(http.StatusNoContent, patch(t, at, map[string]any{
			"op": "remove", "path": `members[value eq "` + a["id"].(string) + `"]`,
		}))
		code, now, _ := d.call(t, d.key, http.MethodGet, scim.Base+at, nil)
		x.Equal(http.StatusOK, code, "%v", now)
		x.Len(now["members"], 1)
		x.Equal(b["id"], now["members"].([]any)[0].(map[string]any)["value"])
	})

	t.Run("Create a New Group Resource", func(t *testing.T) {
		x := require.New(t)
		code, g := post(t, "/Groups", group(t))
		x.Equal(http.StatusCreated, code, "%v", g)
		code, u := post(t, "/Users", validatorUser(t))
		x.Equal(http.StatusCreated, code, "%v", u)

		x.Equal(http.StatusNoContent, patch(t, "/Groups/"+g["id"].(string),
			map[string]any{"op": "add", "path": "members", "value": []map[string]any{{"value": u["id"]}}}))
		code, now, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Groups/"+g["id"].(string), nil)
		x.Equal(http.StatusOK, code, "%v", now)
		x.Equal(u["id"], now["members"].([]any)[0].(map[string]any)["value"])
	})
}

// TestTheSchemaListsWhatIsKept: `/Schemas` names the attributes this endpoint
// keeps a value of its own for, and no other -- which is what a directory, and
// Microsoft's validator by *Discover schema*, takes it to mean. Each is one
// [validatorUser] sends and reads back; `name` is read and not listed, being
// what somebody is called where no `displayName` says so and not a second name.
func TestTheSchemaListsWhatIsKept(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	listed := func(urn string) []string {
		t.Helper()
		code, got, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Schemas/"+urn, nil)
		x.Equal(http.StatusOK, code, "%v", got)
		var out []string
		for _, a := range got["attributes"].([]any) {
			out = append(out, a.(map[string]any)["name"].(string))
		}

		return out
	}

	// And what it says of the two lists whose values have a type, which is
	// what the page makes its values from: one for each type named. A number
	// may be any of six things; an address is somebody's `work` one, said so --
	// named none, the page sent an address of no type and looked for one.
	types := func(attr string) (any, []string) {
		t.Helper()
		code, got, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Schemas/urn:ietf:params:scim:schemas:core:2.0:User", nil)
		x.Equal(http.StatusOK, code, "%v", got)
		for _, a := range got["attributes"].([]any) {
			if a.(map[string]any)["name"] != attr {
				continue
			}
			var canonical any
			var subs []string
			for _, sub := range a.(map[string]any)["subAttributes"].([]any) {
				subs = append(subs, sub.(map[string]any)["name"].(string))
				if sub.(map[string]any)["name"] == "type" {
					canonical = sub.(map[string]any)["canonicalValues"]
				}
			}

			return canonical, subs
		}
		x.Fail("not listed", attr)

		return nil, nil
	}
	canonical, subs := types("emails")
	x.Equal([]any{"work"}, canonical)
	x.ElementsMatch([]string{"value", "type"}, subs, "`primary` is answered and is no value kept")
	canonical, subs = types("phoneNumbers")
	x.Equal([]any{"work", "mobile", "fax", "home", "pager", "other"}, canonical)
	x.ElementsMatch([]string{"value", "type"}, subs)

	u := validatorUser(t)
	var sent []string
	for k := range u {
		switch k {
		case "schemas", "externalId", enterprise:
			// The envelope; common to every resource; and its own schema.
		default:
			sent = append(sent, k)
		}
	}
	x.ElementsMatch(sent, listed("urn:ietf:params:scim:schemas:core:2.0:User"))

	var ent []string
	for k := range u[enterprise].(map[string]any) {
		ent = append(ent, k)
	}
	// `manager` among them: the page makes a word up for it, which is nobody
	// here and is not kept, and says somebody's id once there is somebody
	// (`TestADirectorySaysWhoSomebodyReportsTo`).
	x.ElementsMatch(ent, listed(enterprise))
}

// TestWhatSomebodyIsCalledIsTheirDisplayName: a directory that says a
// `displayName` and a `name.formatted` that differ -- Entra maps one and
// builds the other out of a given name and a surname -- means the first by
// what somebody is called, whichever order a body lists them in. And one that
// says only a `name.formatted` means that.
func TestWhatSomebodyIsCalledIsTheirDisplayName(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	// Enough of them that an order a JSON object happened to be walked in
	// would show.
	for i := 0; i < 12; i++ {
		u := validatorUser(t)
		u["name"] = map[string]any{"formatted": "Formatted", "givenName": "For", "familyName": "Matted"}
		code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", u)
		x.Equal(http.StatusCreated, code, "%v", got)
		x.Equal(u["displayName"], got["displayName"])
		x.Equal(u["displayName"], d.holder(t, got["id"].(string)).GetProfile().GetDisplayName())

		code, got, _ = d.call(t, d.key, http.MethodPut, scim.Base+"/Users/"+got["id"].(string), u)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal(u["displayName"], got["displayName"])
	}

	u := validatorUser(t)
	delete(u, "displayName")
	u["name"] = map[string]any{"formatted": "Only Formatted"}
	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", u)
	x.Equal(http.StatusCreated, code, "%v", got)
	x.Equal("Only Formatted", got["displayName"])
}
