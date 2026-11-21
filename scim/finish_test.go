package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// TestACreateThatMeetsThePersonItWasMakingFinishesThem: a create is
// `Provision`, and then `Tell` of the name the directory looks somebody up by
// where that is not their address. A create cut between the two -- its asker
// gave up, roster did not answer the second -- leaves somebody the lookup by
// that name does not find, and so does somebody who signed in through the
// connection before the directory began, whose sign-in name is not their
// address. The create that follows met them and was `409 uniqueness` on every
// cycle.
//
// Where the lookup by `externalId` and the lookup by the address find the
// same person and the lookup by `userName` finds nobody, the create is theirs
// said again: they are told the name, as a create tells it, and the answer is
// `201` with them as they are. Nothing else of the body is written: they were
// made already. Anything else that finds somebody is the `409` it was, so a
// second create of the same User is still refused, which Microsoft's
// validator asks.
func TestACreateThatMeetsThePersonItWasMakingFinishesThem(t *testing.T) {
	d := stand(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	// provisioned is what a create writes first, and nothing after it.
	provisioned := func(t *testing.T, alias, oid string) string {
		t.Helper()
		h, err := d.s.Ungated.Holder().Provision(ctx, rstr.HolderProvisionRequest_builder{
			Tenant: at, Alias: alias, Name: alias, Provider: "entra", Subject: oid, Address: alias + "@contoso.example",
		}.Build())
		require.NoError(t, err)
		k, err := pdid.From(h.GetId())
		require.NoError(t, err)

		return k.String()
	}
	user := func(oid, userName, address string) map[string]any {
		b := erin()
		b["externalId"], b["userName"], b["displayName"] = oid, userName, "Somebody Else"
		b["emails"] = []map[string]any{{"primary": true, "type": "work", "value": address}}

		return b
	}

	kim := provisioned(t, "kim", fredOid)
	lee := provisioned(t, "lee", ginaOid)

	t.Run("finished", func(t *testing.T) {
		x := require.New(t)
		code, got, _ := d.call(t, d.key, http.MethodGet, filter("userName", "kim.lee@corp.contoso.example"), nil)
		x.Equal(http.StatusOK, code, "%v", got)
		x.EqualValues(0, got["totalResults"], "the lookup a directory makes first finds nobody")

		code, got, hd := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", user(fredOid, "Kim.Lee@corp.contoso.example", "kim@contoso.example"))
		x.Equal(http.StatusCreated, code, "%v", got)
		x.Equal(kim, got["id"], "the person it was making")
		x.Equal(scim.Base+"/Users/"+kim, hd.Get("Location"))
		x.Equal("kim.lee@corp.contoso.example", got["userName"])
		x.Equal("kim", got["displayName"], "as they are, and nothing else of the body written")

		code, got, _ = d.call(t, d.key, http.MethodGet, filter("userName", "kim.lee@corp.contoso.example"), nil)
		x.Equal(http.StatusOK, code, "%v", got)
		x.EqualValues(1, got["totalResults"], "found by it afterwards")
		x.Equal(kim, got["Resources"].([]any)[0].(map[string]any)["id"])
	})

	for _, tc := range []struct {
		what   string
		body   map[string]any
		detail string
	}{
		{"the same create again", user(fredOid, "kim.lee@corp.contoso.example", "kim@contoso.example"), ""},
		{"the externalId alone", user(fredOid, "nobody.else@corp.contoso.example", "nobody@contoso.example"), "somebody here already has that externalId"},
		{"the address alone", user(guid(t), "nobody.else@corp.contoso.example", "lee@contoso.example"), "somebody here already has that address"},
		{"two people", user(ginaOid, "nobody.else@corp.contoso.example", "kim@contoso.example"), ""},
		{"the userName finding anybody", user(ginaOid, "kim.lee@corp.contoso.example", "lee@contoso.example"), ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", tc.body)
			x.Equal(http.StatusConflict, code, "%v", got)
			x.Equal("uniqueness", got["scimType"])
			if tc.detail != "" {
				x.Equal(tc.detail, got["detail"])
			}
		})
	}
	require.Empty(t, d.holder(t, lee).GetDirectoryName(), "nobody refused is told a name")

	t.Run("suspended before the name is told", func(t *testing.T) {
		x := require.New(t)
		oid := guid(t)
		moon := provisioned(t, "moon", oid)
		body := user(oid, "moon.m@corp.contoso.example", "moon@contoso.example")
		body["active"] = false

		code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
		x.Equal(http.StatusCreated, code, "%v", got)
		x.Equal(moon, got["id"])
		x.Equal(false, got["active"])
		x.Equal("inactive", d.holder(t, moon).GetDirectory())
	})
}
