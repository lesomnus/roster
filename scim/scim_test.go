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
	"slices"
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
	"github.com/lesomnus/roster/server/forget"
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

	// rpc is where roster itself answers, which the endpoint is in front of
	// and a key that leaked would be presented to directly.
	rpc string
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
	role, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: at, Alias: "scim", Methods: scim.Bound()}.Build())
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

	d.rpc = l.Addr().String()
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
		Select: rstr.HolderSelect_builder{All: z.Ptr(true)}.Build(),
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

// TestSomebodyErasedHereIsNeitherFoundNorMadeUntilForgotten: an erase is not
// something a directory hears. It finds nobody by their address or their id,
// and making them again is refused as taken -- an erase leaves the identity and
// the address where they were -- until they are forgotten; then it makes them
// again, as somebody new. Which is why somebody a directory still provisions is
// unassigned there before they are erased here.
func TestSomebodyErasedHereIsNeitherFoundNorMadeUntilForgotten(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()

	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	k, err := pdid.Parse(got["id"].(string))
	x.NoError(err)

	_, err = d.s.Ungated.Holder().Erase(ctx, rstr.HolderRef_builder{Id: k.Bytes()}.Build())
	x.NoError(err)

	for attr, value := range map[string]string{"userName": "erin@contoso.example", "externalId": erinOid} {
		code, got, _ = d.call(t, d.key, http.MethodGet, filter(attr, value), nil)
		x.Equal(http.StatusOK, code)
		x.EqualValues(0, got["totalResults"], attr)
	}
	code, got, _ = d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])

	_, err = forget.Forget(ctx, d.s.Ent, k, trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)
	code, got, _ = d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	x.NotEqual(k.String(), got["id"], "made again, and as somebody new")
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

// TestSomebodyMatchedIsNotSuspendedByTheirDirectoryUntilTheyAreLinked:
// somebody a tenant had before its directory provisioned anybody is found by
// their address, and the `externalId` the directory then writes onto them is
// kept and believed by nothing -- so they are answered as the directory calls
// them, found by it, and still not somebody it speaks for: it cannot suspend
// them. Linking them is a way in and an operator's, and once it is written
// the directory suspends them as it does anybody, and what was kept is gone.
func TestSomebodyMatchedIsNotSuspendedByTheirDirectoryUntilTheyAreLinked(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "fred", Name: "Fred"}.Build())
	x.NoError(err)
	ref := rstr.HolderRef_builder{Id: h.GetId()}.Build()
	_, err = d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{Holder: ref, Address: "fred@contoso.example"}.Build())
	x.NoError(err)

	code, got, _ := d.call(t, d.key, http.MethodGet, filter("userName", "fred@contoso.example"), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"])
	id := got["Resources"].([]any)[0].(map[string]any)["id"].(string)

	// What Entra writes onto somebody it matched: its own id for them.
	said := map[string]any{"Operations": []map[string]any{{"op": "Add", "path": "externalId", "value": fredOid}}}
	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, said)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal(fredOid, got["externalId"], "it reads back what it wrote")
	x.Equal(fredOid, d.holder(t, id).GetDirectorySubject())

	ids, err := d.s.Ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: ref}.Build()},
	}.Build())
	x.NoError(err)
	x.Empty(ids.GetItems(), "and nothing signs in by it")

	code, got, _ = d.call(t, d.key, http.MethodGet, filter("externalId", fredOid), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"], "found by what the directory calls them")

	left := map[string]any{"Operations": []map[string]any{{"op": "Replace", "path": "active", "value": "False"}}}
	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, left)
	x.Equal(http.StatusForbidden, code, "%v", got)
	x.Nil(d.holder(t, id).GetDateDisabled(), "matched is not spoken for")

	// An operator who believes it links them, which is what `roster scim
	// adopt` writes.
	_, err = d.s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{Holder: ref, Provider: "entra", Subject: fredOid}.Build())
	x.NoError(err)

	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, said)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal(fredOid, got["externalId"])
	x.Empty(d.holder(t, id).GetDirectorySubject(), "how roster knows them now, so nothing is kept beside it")

	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, left)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal(false, got["active"])
	x.NotNil(d.holder(t, id).GetDateDisabled())
}

// TestAChangedUserNameIsKeptAndAnsweredAndTheAddressStays: a directory saying
// somebody's `userName` or address changed moves nothing -- an address is
// where a link is mailed and how a sign-in is matched, and it moves by the
// person or an operator. What it said is kept, and it is answered with it: the
// User reads back as written, and a lookup by the new name finds them, as one
// by the address they still have does. Once the address is theirs here,
// nothing is kept beside it.
func TestAChangedUserNameIsKeptAndAnsweredAndTheAddressStays(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()

	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	x.Equal("erin@contoso.example", got["userName"])
	k, err := pdid.Parse(id)
	x.NoError(err)
	ref := rstr.HolderRef_builder{Id: k.Bytes()}.Build()

	addresses := func() []string {
		t.Helper()
		es, err := d.s.Ungated.Email().List(ctx, rstr.EmailListRequest_builder{
			Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{Holder: ref}.Build()},
		}.Build())
		x.NoError(err)
		var out []string
		for _, e := range es.GetItems() {
			out = append(out, e.GetAddress())
		}

		return out
	}

	// Entra's way of saying it: the user name, and the work address by its
	// own path.
	renamed := map[string]any{"Operations": []map[string]any{
		{"op": "Replace", "path": "userName", "value": "Erin.Park@Contoso.example"},
		{"op": "Replace", "path": `emails[type eq "work"].value`, "value": "Erin.Park@Contoso.example"},
	}}
	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, renamed)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal("erin.park@contoso.example", got["userName"], "it reads back what it wrote")
	x.Equal("erin.park@contoso.example", got["emails"].([]any)[0].(map[string]any)["value"])
	x.Equal("erin.park@contoso.example", d.holder(t, id).GetDirectoryAddress())
	x.Equal([]string{"erin@contoso.example"}, addresses(), "and the address did not move")

	for _, name := range []string{"Erin.Park@contoso.example", "erin@contoso.example"} {
		code, got, _ = d.call(t, d.key, http.MethodGet, filter("userName", name), nil)
		x.Equal(http.StatusOK, code)
		x.EqualValues(1, got["totalResults"], name)
		x.Equal(id, got["Resources"].([]any)[0].(map[string]any)["id"], name)
	}

	// Nobody else is made under the name it said, either.
	body := erin()
	body["userName"], body["externalId"] = "erin.park@contoso.example", fredOid
	body["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "erin.park@contoso.example"}}
	code, got, _ = d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])

	// An operator gives them the address, and the directory says the same
	// again -- as a whole User this time, which a replace is. It is one of
	// theirs now and not the first, so it is still what is answered.
	_, err = d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{Holder: ref, Address: "erin.park@contoso.example"}.Build())
	x.NoError(err)
	body = erin()
	body["userName"] = "Erin.Park@Contoso.example"
	body["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "Erin.Park@Contoso.example"}}
	code, got, _ = d.call(t, d.key, http.MethodPut, scim.Base+"/Users/"+id, body)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal("erin.park@contoso.example", got["userName"])
	x.Len(got["emails"], 2, "each address once")
	x.Equal(erinOid, got["externalId"])
	x.Empty(d.holder(t, id).GetDirectorySubject(), "a whole User naming them as roster does keeps nothing")

	// And takes the old one away. The address the directory says is the one
	// they are answered with anyway, so nothing is kept beside it.
	_, err = d.s.Ungated.Email().Erase(ctx, rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{
		TenantId: d.contoso.Bytes(), Address: proto.String("erin@contoso.example"),
	}.Build()}.Build())
	x.NoError(err)
	code, got, _ = d.call(t, d.key, http.MethodPut, scim.Base+"/Users/"+id, body)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal("erin.park@contoso.example", got["userName"])
	x.Equal([]string{"erin.park@contoso.example"}, addresses())
	x.Empty(d.holder(t, id).GetDirectoryAddress())
}

// group is one request about a group, answered with its status and its body.
func (d *deployment) group(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, got, _ := d.call(t, d.key, method, scim.Base+"/Groups"+path, body)

	return code, got
}

// membersOf is who roster has in a group, by alias, and the role each holds
// there.
func (d *deployment) membersOf(t *testing.T, group string) map[string]string {
	t.Helper()
	x := require.New(t)
	k, err := pdid.Parse(group)
	x.NoError(err)

	vs, err := d.s.Ungated.GroupMembership().List(t.Context(), rstr.GroupMembershipListRequest_builder{
		Filters: []*rstr.GroupMembershipFilter{rstr.GroupMembershipFilter_builder{
			Group: rstr.GroupRef_builder{Id: k.Bytes()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)

	out := map[string]string{}
	for _, v := range vs.GetItems() {
		v, err := d.s.Ungated.GroupMembership().Get(t.Context(), rstr.GroupMembershipGetRequest_builder{
			Ref: rstr.GroupMembershipRef_builder{Id: v.GetId()}.Build(),
			Select: rstr.GroupMembershipSelect_builder{
				Holder: rstr.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
				Role:   rstr.RoleSelect_builder{Alias: z.Ptr(true)}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)
		out[v.GetHolder().GetAlias()] = v.GetRole().GetAlias()
	}

	return out
}

// someone is a person the directory made, by their id at the endpoint.
func (d *deployment) someone(t *testing.T, name, oid string) string {
	t.Helper()
	body := erin()
	body["userName"], body["externalId"], body["displayName"] = name+"@contoso.example", oid, name
	body["emails"] = []map[string]any{{"primary": true, "type": "work", "value": name + "@contoso.example"}}
	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
	require.Equal(t, http.StatusCreated, code, "%v", got)

	return got["id"].(string)
}

const opsOid = "9c8b7a69-5847-4635-a291-0f1e2d3c4b5a"

// TestADirectoryProvisionsAGroupTheWayEntraDoes is a group's whole life at the
// endpoint, in the order and the dialect Entra writes it: looked up by name
// with its members left out, made empty, its members added and removed,
// renamed, and deleted -- which takes everybody out and keeps the row.
//
// What makes the group the directory's to change is that its own holder made
// it, as anybody of a tenant manages a group they make: that holder is in it,
// as its manager, and is no member to the directory.
func TestADirectoryProvisionsAGroupTheWayEntraDoes(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erin, fred := d.someone(t, "erin", erinOid), d.someone(t, "fred", fredOid)

	lookup := "?excludedAttributes=members&filter=" + url.QueryEscape(`displayName eq "Ops Team"`)
	code, got := d.group(t, http.MethodGet, lookup, nil)
	x.Equal(http.StatusOK, code, "%v", got)
	x.EqualValues(0, got["totalResults"])

	code, got = d.group(t, http.MethodPost, "", map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Group", "http://schemas.microsoft.com/2006/11/ResourceManagement/ADSCIM/2.0/Group"},
		"externalId":  opsOid,
		"displayName": "Ops Team",
		"meta":        map[string]any{"resourceType": "Group"},
	})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	x.Equal("Ops Team", got["displayName"])
	x.Equal(opsOid, got["externalId"])
	x.Empty(got["members"])
	x.Equal(map[string]string{"scim": "group-manager"}, d.membersOf(t, id), "made by the directory's holder, which so manages it")

	k, err := pdid.Parse(id)
	x.NoError(err)
	row, err := d.s.Ungated.Group().Get(t.Context(), rstr.GroupGetRequest_builder{
		Ref: rstr.GroupRef_builder{Id: k.Bytes()}.Build(), Select: rstr.GroupSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("ops-team", row.GetAlias())
	x.Equal(map[string]string{"roster.directory": opsOid}, row.GetLabels())

	// Found by its name now, without regard to case, and by what the
	// directory calls it; made again, refused.
	code, got = d.group(t, http.MethodGet, "?excludedAttributes=members&filter="+url.QueryEscape(`displayName eq "ops team"`), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"])
	found := got["Resources"].([]any)[0].(map[string]any)
	x.Equal(id, found["id"])
	x.NotContains(found, "members", "left out when the directory says to leave them out")
	code, got = d.group(t, http.MethodGet, "?filter="+url.QueryEscape(`externalId eq "`+opsOid+`"`), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"])
	code, got = d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops Team", "externalId": opsOid})
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])

	// Nor is one made twice under a name no alias can be made of -- where
	// roster would give the second a name of its own -- or under another name
	// for what the directory calls the same group.
	code, got = d.group(t, http.MethodPost, "", map[string]any{"displayName": "운영팀"})
	x.Equal(http.StatusCreated, code, "%v", got)
	code, got = d.group(t, http.MethodPost, "", map[string]any{"displayName": "운영팀"})
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])
	code, got = d.group(t, http.MethodPost, "", map[string]any{"displayName": "Operations", "externalId": opsOid})
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Equal("uniqueness", got["scimType"])

	// Members, Entra's way: named in the value both to add and to remove, and
	// answered with no body.
	add := func(ids ...string) map[string]any {
		vs := []map[string]any{}
		for _, v := range ids {
			vs = append(vs, map[string]any{"$ref": nil, "value": v})
		}

		return map[string]any{"Operations": []map[string]any{{"op": "Add", "path": "members", "value": vs}}}
	}
	code, got = d.group(t, http.MethodPatch, "/"+id, add(erin, fred))
	x.Equal(http.StatusNoContent, code, "%v", got)
	code, got = d.group(t, http.MethodPatch, "/"+id, add(erin))
	x.Equal(http.StatusNoContent, code, "somebody already in it is left as they are: %v", got)
	x.Equal(map[string]string{"scim": "group-manager", "erin": "", "fred": ""}, d.membersOf(t, id))

	code, got = d.group(t, http.MethodGet, "/"+id, nil)
	x.Equal(http.StatusOK, code, "%v", got)
	var said []string
	for _, m := range got["members"].([]any) {
		said = append(said, m.(map[string]any)["value"].(string))
	}
	x.ElementsMatch([]string{erin, fred}, said, "its own holder is no member to the directory")

	code, got = d.group(t, http.MethodPatch, "/"+id, map[string]any{"Operations": []map[string]any{
		{"op": "Remove", "path": "members", "value": []map[string]any{{"$ref": nil, "value": erin}}},
	}})
	x.Equal(http.StatusNoContent, code, "%v", got)
	x.Equal(map[string]string{"scim": "group-manager", "fred": ""}, d.membersOf(t, id))

	// Renamed. What it is called follows; the alias a token carries stays.
	code, got = d.group(t, http.MethodPatch, "/"+id, map[string]any{"Operations": []map[string]any{
		{"op": "Replace", "path": "displayName", "value": "Operations"},
	}})
	x.Equal(http.StatusNoContent, code, "%v", got)
	code, got = d.group(t, http.MethodGet, "?filter="+url.QueryEscape(`displayName eq "Operations"`), nil)
	x.Equal(http.StatusOK, code)
	x.EqualValues(1, got["totalResults"])
	row, err = d.s.Ungated.Group().Get(t.Context(), rstr.GroupGetRequest_builder{
		Ref: rstr.GroupRef_builder{Id: k.Bytes()}.Build(), Select: rstr.GroupSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("Operations", row.GetName())
	x.Equal("ops-team", row.GetAlias())

	// Deleted: everybody the directory says is in it is out, and the row is an
	// administrator's to decide about.
	code, got = d.group(t, http.MethodDelete, "/"+id, nil)
	x.Equal(http.StatusNoContent, code, "%v", got)
	x.Equal(map[string]string{"scim": "group-manager"}, d.membersOf(t, id))
	_, err = d.s.Ungated.Group().Get(t.Context(), rstr.GroupGetRequest_builder{Ref: rstr.GroupRef_builder{Id: k.Bytes()}.Build()}.Build())
	x.NoError(err, "the row is kept")
}

// TestADirectoryChangesTheGroupsItMadeAndNoOther: the directory's key holds
// the writes that change a group in each group its holder made, and through no
// binding. So a group an operator made is not one it speaks of, is not taken
// over by a directory naming a group alike, and is not one its key reaches --
// asked of roster itself, past the endpoint, which is what a key that leaked
// would do.
func TestADirectoryChangesTheGroupsItMadeAndNoOther(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()
	erin := d.someone(t, "erin", erinOid)
	who, err := pdid.Parse(erin)
	x.NoError(err)

	// An operator's group, which nothing is bound to: the one a membership
	// write held across the tenant would reach.
	theirs, err := d.s.Ungated.Group().Add(ctx, rstr.GroupAddRequest_builder{Tenant: at, Alias: "on-call", Name: "On Call"}.Build())
	x.NoError(err)
	theirId, err := pdid.From(theirs.GetId())
	x.NoError(err)

	t.Run("not spoken of", func(t *testing.T) {
		x := require.New(t)
		code, got := d.group(t, http.MethodGet, "", nil)
		x.Equal(http.StatusOK, code, "%v", got)
		x.EqualValues(0, got["totalResults"])
		code, _ = d.group(t, http.MethodGet, "/"+theirId.String(), nil)
		x.Equal(http.StatusNotFound, code)
		code, _ = d.group(t, http.MethodPatch, "/"+theirId.String(), map[string]any{"Operations": []map[string]any{
			{"op": "Add", "path": "members", "value": []map[string]any{{"value": erin}}},
		}})
		x.Equal(http.StatusNotFound, code)
	})

	t.Run("not taken over by a group named alike", func(t *testing.T) {
		x := require.New(t)
		code, got := d.group(t, http.MethodPost, "", map[string]any{"displayName": "On Call", "externalId": opsOid})
		x.Equal(http.StatusConflict, code, "%v", got)
		x.Equal("uniqueness", got["scimType"])
		x.Empty(d.membersOf(t, theirId.String()))
	})

	t.Run("not reached by its key", func(t *testing.T) {
		x := require.New(t)
		conn, err := grpc.NewClient(d.rpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
		x.NoError(err)
		defer conn.Close()
		as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+d.key)
		direct := rstr.NewClient(conn)

		// With a group of its own already made, so that the gate lets its
		// holder through for the method at all: the refusal is about whose
		// group it is.
		code, got := d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops Team", "externalId": opsOid})
		x.Equal(http.StatusCreated, code, "%v", got)

		_, err = direct.GroupMembership().Add(as, rstr.GroupMembershipAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
			Group:  rstr.GroupRef_builder{Id: theirs.GetId()}.Build(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = direct.Group().Update(as, rstr.GroupUpdateRequest_builder{
			Ref: rstr.GroupRef_builder{Id: theirs.GetId()}.Build(), Name: proto.String("Theirs no more"),
			DateUpdated: theirs.GetDateUpdated(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = direct.Group().Erase(as, rstr.GroupRef_builder{Id: theirs.GetId()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.Empty(d.membersOf(t, theirId.String()))
	})
}

// TestAGroupsMembersAreSaidTheRFCsWayToo: a member removed by a path of its
// own (`members[value eq "…"]`), everybody removed by naming nobody, and a
// whole Group replacing who is in it. And who a directory may name: a person
// this endpoint speaks of, and never somebody an administrator appointed to
// manage the group, who is theirs to take out.
func TestAGroupsMembersAreSaidTheRFCsWayToo(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	erin, fred := d.someone(t, "erin", erinOid), d.someone(t, "fred", fredOid)

	code, got := d.group(t, http.MethodPost, "", map[string]any{
		"displayName": "Ops Team", "externalId": opsOid,
		"members": []map[string]any{{"value": erin}, {"value": fred}},
	})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	x.Len(got["members"], 2, "a directory that says who is in it as it makes it is taken at its word")

	code, got = d.group(t, http.MethodPatch, "/"+id, map[string]any{"Operations": []map[string]any{
		{"op": "remove", "path": `members[value eq "` + erin + `"]`},
	}})
	x.Equal(http.StatusNoContent, code, "%v", got)
	x.Equal(map[string]string{"scim": "group-manager", "fred": ""}, d.membersOf(t, id))

	// An administrator appoints the tenant's own to manage it beside the
	// directory, who is then in it holding a role.
	k, err := pdid.Parse(id)
	x.NoError(err)
	admin, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias: proto.String("admin"), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	role, err := d.s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
			Alias: proto.String("group-manager"), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.GroupMembership().Add(ctx, rstr.GroupMembershipAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: admin.GetId()}.Build(),
		Group:  rstr.GroupRef_builder{Id: k.Bytes()}.Build(),
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// A whole Group: exactly who it says, of the people it speaks of.
	code, got = d.group(t, http.MethodPut, "/"+id, map[string]any{
		"displayName": "Ops Team", "members": []map[string]any{{"value": erin}},
	})
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal(map[string]string{"scim": "group-manager", "admin": "group-manager", "erin": ""}, d.membersOf(t, id))
	x.Len(got["members"], 1, "a manager is no member to the directory")

	// Everybody, by naming nobody.
	code, got = d.group(t, http.MethodPatch, "/"+id, map[string]any{"Operations": []map[string]any{
		{"op": "remove", "path": "members"},
	}})
	x.Equal(http.StatusNoContent, code, "%v", got)
	x.Equal(map[string]string{"scim": "group-manager", "admin": "group-manager"}, d.membersOf(t, id))

	// And nobody the endpoint does not speak of: the directory's own holder,
	// by an id that is real.
	self, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias: proto.String("scim"), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	selfId, err := pdid.From(self.GetId())
	x.NoError(err)
	code, got = d.group(t, http.MethodPatch, "/"+id, map[string]any{"Operations": []map[string]any{
		{"op": "add", "path": "members", "value": []map[string]any{{"value": selfId.String()}}},
	}})
	x.Equal(http.StatusBadRequest, code, "%v", got)
	x.Equal("invalidValue", got["scimType"])
}

// numbersOf is somebody's numbers as roster has them, by kind.
func (d *deployment) numbersOf(t *testing.T, id string) map[string]string {
	t.Helper()
	k, err := pdid.Parse(id)
	require.NoError(t, err)
	vs, err := d.s.Ungated.Phone().List(t.Context(), rstr.PhoneListRequest_builder{
		Filters: []*rstr.PhoneFilter{rstr.PhoneFilter_builder{Holder: rstr.HolderRef_builder{Id: k.Bytes()}.Build()}.Build()},
	}.Build())
	require.NoError(t, err)

	out := map[string]string{}
	for _, v := range vs.GetItems() {
		out[v.GetKind()] = v.GetNumber()
	}

	return out
}

// TestADirectoryKeepsSomebodysNumbers: `phoneNumbers` are kept a row each, one
// number a kind as a directory says them. A number it changes is replaced and
// one it takes away is gone -- which it may not do to an address, a number
// being no way in -- a kind it does not mention is left, and what it sends
// that is not a number is said in the log and refuses nothing.
func TestADirectoryKeepsSomebodysNumbers(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	body := erin()
	body["phoneNumbers"] = []map[string]any{
		{"type": "work", "value": " +82 2-123-4567 "},
		{"type": "mobile", "value": "010-0000-0000"},
	}
	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", body)
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	x.Equal(map[string]string{"work": "+82 2-123-4567", "mobile": "010-0000-0000"}, d.numbersOf(t, id))
	x.Len(got["phoneNumbers"], 2, "and read back")

	patch := func(ops ...map[string]any) map[string]any {
		t.Helper()
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{"Operations": ops})
		x.Equal(http.StatusOK, code, "%v", got)

		return got
	}

	// Entra's way of saying one changed, and that another is gone.
	patch(
		map[string]any{"op": "Replace", "path": `phoneNumbers[type eq "work"].value`, "value": "+82 2-765-4321"},
		map[string]any{"op": "Remove", "path": `phoneNumbers[type eq "mobile"].value`},
	)
	x.Equal(map[string]string{"work": "+82 2-765-4321"}, d.numbersOf(t, id))

	// The same number said for another kind is that kind's now: a person lists
	// a number once.
	patch(map[string]any{"op": "Add", "path": `phoneNumbers[type eq "mobile"].value`, "value": "+82 2-765-4321"})
	x.Equal(map[string]string{"mobile": "+82 2-765-4321"}, d.numbersOf(t, id))

	// What is not a number is not kept, and is not a refusal: the rest of the
	// write is written.
	got = patch(
		map[string]any{"op": "Add", "path": `phoneNumbers[type eq "work"].value`, "value": "ask at reception"},
		map[string]any{"op": "Replace", "path": "displayName", "value": "Erin Park"},
	)
	x.Equal("Erin Park", got["displayName"])
	x.Equal(map[string]string{"mobile": "+82 2-765-4321"}, d.numbersOf(t, id))

	// A whole User that speaks of numbers says every one of them.
	body = erin()
	body["phoneNumbers"] = []map[string]any{{"type": "fax", "value": "02-000-0000"}}
	code, got, _ = d.call(t, d.key, http.MethodPut, scim.Base+"/Users/"+id, body)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal(map[string]string{"fax": "02-000-0000"}, d.numbersOf(t, id))

	// And one that does not speak of them leaves them: a directory that maps
	// no number takes none away.
	code, got, _ = d.call(t, d.key, http.MethodPut, scim.Base+"/Users/"+id, erin())
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal(map[string]string{"fax": "02-000-0000"}, d.numbersOf(t, id))
	x.Len(got["phoneNumbers"], 1)
}

// TestAKeyMintedBeforeNumbersWereKeptStillProvisions: a directory's key names
// what it calls, and one minted before numbers were kept here names none of
// `PhoneService`. It goes on reading and writing people as it did, and the
// numbers it sends are said in the log and not kept -- until `roster scim
// provision` mints the one that names them.
func TestAKeyMintedBeforeNumbersWereKeptStillProvisions(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()

	var before []string
	for _, m := range scim.Methods {
		if !strings.HasPrefix(m, "/roster.PhoneService/") {
			before = append(before, m)
		}
	}
	self, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias: proto.String("scim"), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	old := d.mint(t, self.GetId(), "scim-before", before)

	body := erin()
	body["phoneNumbers"] = []map[string]any{{"type": "work", "value": "+82 2-123-4567"}}
	code, got, _ := d.call(t, old, http.MethodPost, scim.Base+"/Users", body)
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	x.NotContains(got, "phoneNumbers")
	x.Empty(d.numbersOf(t, id))

	code, got, _ = d.call(t, old, http.MethodGet, scim.Base+"/Users/"+id, nil)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Equal("erin@contoso.example", got["userName"])
}

// relationsOf is what somebody is to anybody as roster has it: the other end
// of each relation of theirs, and what each says.
func (d *deployment) relationsOf(t *testing.T, id string) map[string][]string {
	t.Helper()
	k, err := pdid.Parse(id)
	require.NoError(t, err)
	vs, err := d.s.Ungated.Relation().List(t.Context(), rstr.RelationListRequest_builder{
		Filters: []*rstr.RelationFilter{rstr.RelationFilter_builder{Holder: rstr.HolderRef_builder{Id: k.Bytes()}.Build()}.Build()},
	}.Build())
	require.NoError(t, err)

	out := map[string][]string{}
	for _, v := range vs.GetItems() {
		other, err := pdid.From(v.GetOther().GetId())
		require.NoError(t, err)
		ls := []string{}
		for l := range v.GetLabels() {
			ls = append(ls, l)
		}
		slices.Sort(ls)
		out[other.String()] = ls
	}

	return out
}

const ginaOid = "2b3c4d5e-6f70-8192-a3b4-c5d6e7f8091a"

// TestADirectorySaysWhoSomebodyReportsTo: the enterprise schema's `manager`
// is kept as a relation from the person to their manager, marked by the one
// word this endpoint writes and the LDAP front reads. One at a time, as a
// directory says it: a new one replaces the old, and the word comes out of a
// row somebody else also wrote into rather than the row being erased for it.
// A manager who is nobody here is said in the log and refuses nothing.
func TestADirectorySaysWhoSomebodyReportsTo(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	erin, fred, gina := d.someone(t, "erin", erinOid), d.someone(t, "fred", fredOid), d.someone(t, "gina", ginaOid)
	path := enterprise + ":manager"

	patch := func(ops ...map[string]any) map[string]any {
		t.Helper()
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+erin, map[string]any{"Operations": ops})
		x.Equal(http.StatusOK, code, "%v", got)

		return got
	}
	said := func(got map[string]any) any {
		ent, _ := got[enterprise].(map[string]any)
		m, _ := ent["manager"].(map[string]any)

		return m["value"]
	}

	// Entra's way: the manager's id, bare.
	got := patch(map[string]any{"op": "Add", "path": path, "value": fred})
	x.Equal(fred, said(got), "and read back")
	x.Equal(map[string][]string{fred: {"manager"}}, d.relationsOf(t, erin))

	// The RFC's: an object, with no path.
	got = patch(map[string]any{"op": "replace", "value": map[string]any{enterprise: map[string]any{"manager": map[string]any{"value": gina}}}})
	x.Equal(gina, said(got))
	x.Equal(map[string][]string{gina: {"manager"}}, d.relationsOf(t, erin), "one at a time: who it was is not who it is")

	// An operator says fred is something else to her. The directory saying he
	// is her manager too is the same row saying both, and saying he no longer
	// is leaves what the operator wrote.
	k, err := pdid.Parse(erin)
	x.NoError(err)
	f, err := pdid.Parse(fred)
	x.NoError(err)
	_, err = d.s.Ungated.Relation().Add(ctx, rstr.RelationAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: k.Bytes()}.Build(), Other: rstr.HolderRef_builder{Id: f.Bytes()}.Build(),
		Labels: map[string]string{"mentor": ""},
	}.Build())
	x.NoError(err)
	patch(map[string]any{"op": "Replace", "path": path, "value": fred})
	x.Equal(map[string][]string{fred: {"manager", "mentor"}}, d.relationsOf(t, erin))
	patch(map[string]any{"op": "Replace", "path": path, "value": gina})
	x.Equal(map[string][]string{fred: {"mentor"}, gina: {"manager"}}, d.relationsOf(t, erin))

	// Somebody who is nobody here: said, and nothing else about the write is
	// wrong.
	got = patch(
		map[string]any{"op": "Replace", "path": path, "value": "00000000-0000-4000-8000-000000000000"},
		map[string]any{"op": "Replace", "path": "displayName", "value": "Erin Park"},
	)
	x.Equal("Erin Park", got["displayName"])
	x.Equal(gina, said(got))

	// And nobody.
	got = patch(map[string]any{"op": "Remove", "path": path})
	x.Nil(said(got))
	x.Equal(map[string][]string{fred: {"mentor"}}, d.relationsOf(t, erin))
}
