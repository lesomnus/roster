package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// TestTheSchemasSayWhatTheEndpointDoes: `/Schemas` is what a directory is
// configured against, and says of each attribute what a request shows.
//
// It said three things the endpoint does not do. `userName` was `immutable`
// and a replace of it is taken and read back; it had `uniqueness: none` and a
// second person under one is `409 uniqueness`; `emails` was `immutable` and
// the address a directory says is answered first. So `userName` is
// `readWrite` with `uniqueness: server`, and `emails` and its `value` are
// `readWrite`.
func TestTheSchemasSayWhatTheEndpointDoes(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)

	code, doc, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Schemas/urn:ietf:params:scim:schemas:core:2.0:User", nil)
	x.Equal(http.StatusOK, code, "%v", doc)
	attr := map[string]map[string]any{}
	for _, v := range doc["attributes"].([]any) {
		a := v.(map[string]any)
		attr[a["name"].(string)] = a
	}
	sub := map[string]map[string]any{}
	for _, v := range attr["emails"]["subAttributes"].([]any) {
		a := v.(map[string]any)
		sub[a["name"].(string)] = a
	}

	patch := func(path string, value any) map[string]any {
		return map[string]any{
			"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
			"Operations": []map[string]any{{"op": "replace", "path": path, "value": value}},
		}
	}

	t.Run("userName is written", func(t *testing.T) {
		x := require.New(t)
		x.Equal("readWrite", attr["userName"]["mutability"])

		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+erinId, patch("userName", "erin.kim@corp.contoso.example"))
		x.Equal(http.StatusOK, code, "%v", got)
		code, got, _ = d.call(t, d.key, http.MethodGet, scim.Base+"/Users/"+erinId, nil)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal("erin.kim@corp.contoso.example", got["userName"])
	})

	t.Run("userName is one person's", func(t *testing.T) {
		x := require.New(t)
		x.Equal("server", attr["userName"]["uniqueness"])

		body := erin()
		body["userName"], body["externalId"] = "erin.kim@corp.contoso.example", fredOid
		body["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "fred@contoso.example"}}
		code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
		x.Equal(http.StatusConflict, code, "%v", got)
		x.Equal("uniqueness", got["scimType"])
	})

	t.Run("emails are written", func(t *testing.T) {
		x := require.New(t)
		x.Equal("readWrite", attr["emails"]["mutability"])
		x.Equal("readWrite", sub["value"]["mutability"])

		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+erinId, patch(`emails[type eq "work"].value`, "erin.k@contoso.example"))
		x.Equal(http.StatusOK, code, "%v", got)
		code, got, _ = d.call(t, d.key, http.MethodGet, scim.Base+"/Users/"+erinId, nil)
		x.Equal(http.StatusOK, code, "%v", got)
		emails := got["emails"].([]any)
		x.Equal("erin.k@contoso.example", emails[0].(map[string]any)["value"])
	})
}
