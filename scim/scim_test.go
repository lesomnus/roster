package scim_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
	"github.com/lesomnus/roster/server/keys"
)

// Entra's object ids for the people below: what `externalId` is once a tenant
// maps it to `objectId`, and what their sign-ins say as `oid`.
const (
	erinOid = "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
	fredOid = "1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f809"
)

// deployment is roster on the wire, a tenant whose directory provisions
// through `entra`, and the SCIM endpoint in front of it.
type deployment struct {
	s       *cmd.Server
	contoso pdid.Id
	key     string // the directory's
	scim    *httptest.Server
}

func stand(t *testing.T) *deployment {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	seal := make([]byte, 32)
	_, err := rand.Read(seal)
	x.NoError(err)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Vouch:   cmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(seal)}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	_, err = cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops"})
	x.NoError(err)

	tn, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)
	d := &deployment{s: s}
	d.contoso, err = pdid.From(tn.GetId())
	x.NoError(err)
	at := rstr.TenantRef_builder{Id: tn.GetId()}.Build()

	_, err = s.Ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
		SubjectClaim: "oid", Provisions: true,
	}.Build())
	x.NoError(err)

	// The directory: what `roster scim provision` writes.
	h, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "scim"}.Build())
	x.NoError(err)
	role, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: at, Alias: "scim", Methods: scim.Methods}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	d.key = d.mint(t, h.GetId(), "scim", scim.Methods)

	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop() })

	e, err := scim.New(scim.Config{Roster: l.Addr().String(), Insecure: true})
	x.NoError(err)
	t.Cleanup(func() { e.Close() })
	d.scim = httptest.NewServer(e)
	t.Cleanup(d.scim.Close)

	return d
}

func (d *deployment) mint(t *testing.T, holder []byte, alias string, methods []string) string {
	t.Helper()
	token, sum, err := keys.Mint(keys.PrefixTenant)
	require.NoError(t, err)
	_, err = d.s.Ungated.ApiKey().Add(t.Context(), rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: holder}.Build(), Alias: alias, Secret: sum, Methods: methods,
	}.Build())
	require.NoError(t, err)

	return token
}

// call is one request, as the directory makes it.
func (d *deployment) call(t *testing.T, key, method, path string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	x := require.New(t)

	var r io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			r = bytes.NewBufferString(v)
		default:
			b, err := json.Marshal(v)
			x.NoError(err)
			r = bytes.NewBuffer(b)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, d.scim.URL+path, r)
	x.NoError(err)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/scim+json")

	res, err := http.DefaultClient.Do(req)
	x.NoError(err)
	defer res.Body.Close()

	var out map[string]any
	b, err := io.ReadAll(res.Body)
	x.NoError(err)
	if len(bytes.TrimSpace(b)) > 0 {
		x.NoError(json.Unmarshal(b, &out), "%s", b)
	}

	return res.StatusCode, out, res.Header
}

func filter(attr, value string) string {
	return scim.Base + "/Users?filter=" + url.QueryEscape(attr+` eq "`+value+`"`)
}

// holder is somebody as roster has them, for what the endpoint does not say.
func (d *deployment) holder(t *testing.T, id string) *rstr.Holder {
	t.Helper()
	k, err := pdid.Parse(id)
	require.NoError(t, err)
	v, err := d.s.Ungated.Holder().Get(t.Context(), rstr.HolderGetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: k.Bytes()}.Build(),
		Select: rstr.HolderSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	require.NoError(t, err)

	return v
}

const enterprise = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"

// erin is the body Entra creates somebody with.
func erin() map[string]any {
	return map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:User", enterprise},
		"externalId":  erinOid,
		"userName":    "Erin@Contoso.example",
		"active":      true,
		"displayName": "Erin Kim",
		"emails":      []map[string]any{{"primary": true, "type": "work", "value": "Erin@Contoso.example"}},
		"name":        map[string]any{"formatted": "Erin Kim", "familyName": "Kim", "givenName": "Erin"},
		enterprise:    map[string]any{"department": "R&D", "employeeNumber": "1001"},
	}
}

// TestADirectoryProvisionsSomebodyTheWayEntraDoes is a person's whole life at
// the endpoint, in the order and the dialect Entra writes it: looked up, made,
// changed, suspended, and deleted -- which suspends them, keeps the row, and
// leaves the endpoint speaking of them no more.
func TestADirectoryProvisionsSomebodyTheWayEntraDoes(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	code, got, _ := d.call(t, d.key, http.MethodGet, filter("userName", "Erin@Contoso.example"), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(0, got["totalResults"])

	code, got, hd := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	id, _ := got["id"].(string)
	x.NotEmpty(id)
	x.Equal(scim.Base+"/Users/"+id, hd.Get("Location"))
	x.Equal("erin@contoso.example", got["userName"])
	x.Equal(erinOid, got["externalId"])
	x.Equal(true, got["active"])
	x.Equal("Erin Kim", got["displayName"])
	x.Equal(map[string]any{"department": "R&D", "employeeNumber": "1001"}, got[enterprise])

	h := d.holder(t, id)
	x.Equal("erin", h.GetAlias())

	code, got, _ = d.call(t, d.key, http.MethodGet, filter("externalId", erinOid), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"])

	code, got, _ = d.call(t, d.key, http.MethodGet, filter(`emails[type eq "work"].value`, "erin@contoso.example"), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"])

	t.Run("changed, in Entra's dialect", func(t *testing.T) {
		x := require.New(t)
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
			"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
			"Operations": []map[string]any{
				{"op": "Replace", "path": "displayName", "value": "Erin K."},
				{"op": "Replace", "path": enterprise + ":department", "value": "Platform"},
				{"op": "Add", "path": "preferredLanguage", "value": "ko-KR"},
				{"op": "Replace", "path": "title", "value": "Engineer"},
			},
		})
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal("Erin K.", got["displayName"])
		x.Equal("ko-KR", got["preferredLanguage"])
		x.Equal("Platform", got[enterprise].(map[string]any)["department"])
	})

	t.Run("suspended with a string for a boolean, and back with a value object", func(t *testing.T) {
		x := require.New(t)
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id,
			`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal(false, got["active"])
		h := d.holder(t, id)
		x.NotNil(h.GetDateDisabled())
		x.Equal("inactive", h.GetDirectory())

		code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id,
			`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"active":true}}]}`)
		x.Equal(http.StatusOK, code, "%v", got)
		x.Equal(true, got["active"])
		x.Nil(d.holder(t, id).GetDateDisabled())
	})

	t.Run("deleted, which is suspended and not erased", func(t *testing.T) {
		x := require.New(t)
		code, _, _ := d.call(t, d.key, http.MethodDelete, scim.Base+"/Users/"+id, nil)
		x.Equal(http.StatusNoContent, code)

		code, _, _ = d.call(t, d.key, http.MethodGet, scim.Base+"/Users/"+id, nil)
		x.Equal(http.StatusNotFound, code)
		code, got, _ := d.call(t, d.key, http.MethodGet, filter("userName", "erin@contoso.example"), nil)
		x.Equal(http.StatusOK, code)
		x.EqualValues(0, got["totalResults"])

		h := d.holder(t, id)
		x.NotNil(h.GetDateDisabled())
		x.Equal("deleted", h.GetDirectory())
		x.Nil(h.GetDateErased(), "the row is the operator's to erase")
	})
}

// TestSomebodyAlreadyHereIsMatchedAndNotMadeAgain: the people a tenant had
// before its directory provisioned anybody -- signed in already, or entered by
// an operator -- are found by their address, which is what a directory looks
// for before it creates, and creating them again is refused as taken.
func TestSomebodyAlreadyHereIsMatchedAndNotMadeAgain(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "fred", Name: "Fred"}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(), Address: "fred@contoso.example",
	}.Build())
	x.NoError(err)

	code, got, _ := d.call(t, d.key, http.MethodGet, filter("userName", "Fred@contoso.example"), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"])
	found := got["Resources"].([]any)[0].(map[string]any)
	id := found["id"].(string)

	body := erin()
	body["userName"], body["externalId"] = "fred@contoso.example", fredOid
	body["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "fred@contoso.example"}}
	code, got, _ = d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])

	// And what the directory owns about them is written all the same.
	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
		"Operations": []map[string]any{{"op": "replace", "path": "displayName", "value": "Fred Park"}},
	})
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal("Fred Park", d.holder(t, id).GetProfile().GetDisplayName())
}

// TestWhatTheDirectoryDoesNotSendIsLeftAlone: it owns what it sends, and a
// tenant that keeps names somewhere else drops them from its mapping -- so a
// replace that does not mention a name leaves it, and only a `remove` takes
// one away.
func TestWhatTheDirectoryDoesNotSendIsLeftAlone(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)

	code, got, _ = d.call(t, d.key, http.MethodPut, scim.Base+"/Users/"+id, map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": "erin@contoso.example", "active": true,
	})
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal("Erin Kim", d.holder(t, id).GetProfile().GetDisplayName())

	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
		"Operations": []map[string]any{{"op": "remove", "path": "displayName"}},
	})
	x.Equal(http.StatusOK, code, "%v", got)
	x.Empty(d.holder(t, id).GetProfile().GetDisplayName())
}

// TestTheDirectoryDoesNotLiftAnOperatorsSuspension: one way, so restarting its
// provisioning says `active` for everybody -- and an operator's suspension is
// not one of the things that undoes.
func TestTheDirectoryDoesNotLiftAnOperatorsSuspension(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	k, err := pdid.Parse(id)
	x.NoError(err)

	_, err = d.s.Ungated.Holder().Disable(t.Context(), rstr.HolderDisableRequest_builder{
		Ref: rstr.HolderRef_builder{Id: k.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}},
	})
	x.Equal(http.StatusForbidden, code, "%v", got)
	x.NotNil(d.holder(t, id).GetDateDisabled())
}

// TestTheEndpointTakesATenantKeyAndNothingElse: no key, a deployment key, a
// scheme that is not a bearer, and a key whose holder may not do the thing --
// each refused as what it is.
func TestTheEndpointTakesATenantKeyAndNothingElse(t *testing.T) {
	d := stand(t)

	// A real one, holding everything the directory's does: what refuses it is
	// that it is no tenant until a call names one, which this never does.
	borrower, err := cmd.HolderNamed(t.Context(), d.s.Control, "scim")
	require.NoError(t, err)
	deployed, sum, err := keys.Mint(keys.PrefixDeployment)
	require.NoError(t, err)
	_, err = d.s.Control.Ungated.ApiKey().Add(t.Context(), rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: borrower.Bytes()}.Build(), Alias: "scim", Secret: sum, Methods: scim.Methods,
	}.Build())
	require.NoError(t, err)

	for _, tc := range []struct {
		desc, header string
	}{
		{"no key", ""},
		{"a deployment key", "Bearer " + deployed},
		{"a password", "Basic " + base64.StdEncoding.EncodeToString([]byte("scim:"+d.key))},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.scim.URL+scim.Base+"/Users", nil)
			require.NoError(t, err)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			res, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			res.Body.Close()
			require.Equal(t, http.StatusUnauthorized, res.StatusCode)
		})
	}

	t.Run("a key that is nobody's", func(t *testing.T) {
		code, _, _ := d.call(t, "rt_"+base64.RawURLEncoding.EncodeToString(make([]byte, 32)), http.MethodGet, scim.Base+"/Users", nil)
		require.Equal(t, http.StatusUnauthorized, code)
	})

	t.Run("a key that may read and not make", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()
		reads := []string{
			rstr.MeService_Get_FullMethodName, rstr.ConnectionService_List_FullMethodName,
			rstr.HolderService_Get_FullMethodName, rstr.EmailService_Get_FullMethodName,
			rstr.IdentityService_Get_FullMethodName,
		}
		h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "reader"}.Build())
		x.NoError(err)
		role, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: at, Alias: "reader", Methods: reads}.Build())
		x.NoError(err)
		_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		code, got, _ := d.call(t, d.mint(t, h.GetId(), "reader", reads), http.MethodPost, scim.Base+"/Users", erin())
		x.Equal(http.StatusForbidden, code, "%v", got)
	})
}

// TestTheEndpointSpeaksOnlyOfPeople: a front door's declared holder and the
// directory's own are rows of the tenant and not people it provisions.
func TestTheEndpointSpeaksOnlyOfPeople(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	door, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: at, Alias: "login-app", Labels: map[string]string{cmd.Declared: "config: login"},
	}.Build())
	x.NoError(err)
	self, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{Alias: proto.String("scim"), Tenant: at}.Build()}.Build(),
	}.Build())
	x.NoError(err)

	for _, id := range [][]byte{door.GetId(), self.GetId()} {
		k, err := pdid.From(id)
		x.NoError(err)
		code, _, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Users/"+k.String(), nil)
		x.Equal(http.StatusNotFound, code)
		code, _, _ = d.call(t, d.key, http.MethodDelete, scim.Base+"/Users/"+k.String(), nil)
		x.Equal(http.StatusNotFound, code)
	}

	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	code, got, _ = d.call(t, d.key, http.MethodGet, scim.Base+"/Users", nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"], "%v", got)
}
