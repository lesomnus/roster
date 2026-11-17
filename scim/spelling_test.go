package scim_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// TestADirectoryFindsSomebodyByEitherSpellingOfTheirId: an `externalId` is the
// subject a sign-in through the directory's connection says, and where that
// connection names people by `oid` it is a GUID, which is one id in capitals
// and in small letters. The endpoint looked for it as written, and it was kept
// as written: somebody made by the id in capitals was not found by it in
// small letters, and creating them again by that spelling made a second
// person, whose sign-in the first would never be. One spelling is kept and
// answered, the small letters a sign-in says, and either finds them.
func TestADirectoryFindsSomebodyByEitherSpellingOfTheirId(t *testing.T) {
	d := stand(t)
	ctx := t.Context()
	capitals := func(oid string) string { return strings.ToUpper(oid) }

	t.Run("somebody it makes", func(t *testing.T) {
		x := require.New(t)

		body := erin()
		body["externalId"] = capitals(erinOid)
		code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
		x.Equal(http.StatusCreated, code, "%v", got)
		id, _ := got["id"].(string)
		x.Equal(erinOid, got["externalId"], "the one spelling is what it is answered")

		ids, err := d.s.Ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
			Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: rstr.HolderRef_builder{Id: d.holder(t, id).GetId()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(ids.GetItems(), 1)
		x.Equal(erinOid, ids.GetItems()[0].GetSubject(), "and what a sign-in will say is what was kept")

		for _, asked := range []string{erinOid, capitals(erinOid)} {
			code, got, _ := d.call(t, d.key, http.MethodGet, filter("externalId", asked), nil)
			x.Equal(http.StatusOK, code)
			x.EqualValues(1, got["totalResults"], "nobody is found by %s", asked)
		}

		again := erin()
		again["userName"] = "erin.kim@contoso.example"
		again["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "erin.kim@contoso.example"}}
		code, got, _ = d.call(t, d.key, http.MethodPost, scim.Base+"/Users", again)
		x.Equal(http.StatusConflict, code, "one id made two people: %v", got)
		x.Equal("uniqueness", got["scimType"])
	})

	t.Run("and somebody it matched and only says the id of", func(t *testing.T) {
		x := require.New(t)
		at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

		h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "fred", Name: "Fred"}.Build())
		x.NoError(err)
		_, err = d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(), Address: "fred@contoso.example",
		}.Build())
		x.NoError(err)

		code, got, _ := d.call(t, d.key, http.MethodGet, filter("userName", "fred@contoso.example"), nil)
		x.Equal(http.StatusOK, code)
		x.EqualValues(1, got["totalResults"])
		id := got["Resources"].([]any)[0].(map[string]any)["id"].(string)

		said := map[string]any{"Operations": []map[string]any{{"op": "Add", "path": "externalId", "value": capitals(fredOid)}}}
		code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, said)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal(fredOid, got["externalId"])
		x.Equal(fredOid, d.holder(t, id).GetDirectorySubject(), "kept as an operator who believes it would link it")

		for _, asked := range []string{fredOid, capitals(fredOid)} {
			code, got, _ := d.call(t, d.key, http.MethodGet, filter("externalId", asked), nil)
			x.Equal(http.StatusOK, code)
			x.EqualValues(1, got["totalResults"], "nobody is found by %s", asked)
		}

		// Linked, what it says in capitals is what roster knows them by, and
		// nothing is kept beside it.
		_, err = d.s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(), Provider: "entra", Subject: fredOid,
		}.Build())
		x.NoError(err)
		code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, said)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal(fredOid, got["externalId"])
		x.Empty(d.holder(t, id).GetDirectorySubject(), "the id in capitals was kept as another than the one they sign in by")
	})
}
