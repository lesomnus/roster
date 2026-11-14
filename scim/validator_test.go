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
// each its own.
func validatorUser(t *testing.T) map[string]any {
	n := made(t)

	return map[string]any{
		"schemas":           []string{"urn:ietf:params:scim:schemas:core:2.0:User", enterprise},
		"userName":          "login-" + n + "@contoso.example",
		"externalId":        guid(t),
		"displayName":       "Display " + n,
		"preferredLanguage": "ko-KR",
		"active":            true,
		"emails":            []map[string]any{{"value": "mail-" + n + "@contoso.example", "type": "work", "primary": true}},
		"phoneNumbers":      []map[string]any{{"value": "+1 555 01" + n[:2], "type": "work"}},
		enterprise:          map[string]any{"department": "Department " + n, "employeeNumber": n},
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
		x.Equal(sent["phoneNumbers"].([]map[string]any)[0]["value"], got["phoneNumbers"].([]any)[0].(map[string]any)["value"], "phoneNumbers")
		x.Equal(sent[enterprise], got[enterprise], "the enterprise schema's")
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
	// `manager` is listed and not made up: it is somebody else's id, said once
	// there is somebody else (`TestADirectorySaysWhoSomebodyReportsTo`).
	x.ElementsMatch(append(ent, "manager"), listed(enterprise))
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
