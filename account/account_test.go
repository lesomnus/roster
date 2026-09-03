package account_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// deployment is a roster fronting two operators, and the account app in front
// of both -- which is the shape the example could not be and this package is
// for. contoso's people arrive through a provider; fabrikam's have passwords.
type deployment struct {
	s        *cmd.Server
	ungated  rstr.Server
	idp      *idp
	app      *httptest.Server
	contoso  pdid.Id
	fabrikam pdid.Id
	erin     []byte
}

func serve(t *testing.T, enrol account.Enrol) *deployment {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	key := make([]byte, 32)
	_, err := rand.Read(key)
	x.NoError(err)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Vouch:   cmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(key)}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	_, err = cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops"})
	x.NoError(err)

	d := &deployment{s: s, ungated: s.Ungated, idp: newIdp(t)}
	t.Setenv("EXAMPLE_SECRET", "unused")

	// Two operators, each with a name this app serves them under and a key for
	// the app -- one per tenant, minted for a holder in that tenant whose role
	// names what a front door calls.
	var tokens = map[string]string{}
	for _, alias := range []string{"contoso", "fabrikam"} {
		var tn *rstr.Tenant
		if alias == "contoso" {
			tn, err = s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
				Ref: rstr.TenantRef_builder{Alias: proto.String(alias)}.Build(),
			}.Build())
		} else {
			tn, err = s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: alias, Name: "Fabrikam Inc"}.Build())
		}
		x.NoError(err)
		at := rstr.TenantRef_builder{Id: tn.GetId()}.Build()
		id, err := pdid.From(tn.GetId())
		x.NoError(err)
		if alias == "contoso" {
			d.contoso = id
		} else {
			d.fabrikam = id
		}

		_, err = s.Ungated.Host().Add(ctx, rstr.HostAddRequest_builder{Tenant: at, Name: alias + ".test"}.Build())
		x.NoError(err)

		// contoso's providers are the fake, twice under two names -- the way an
		// operator has Entra for staff and GitHub for contractors -- because
		// roster refuses a second account at **one** provider for one person,
		// so attaching a second account needs a second provider to attach it at.
		if alias == "contoso" {
			for _, name := range []string{"example", "other"} {
				_, err = s.Ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
					Tenant: at, Name: name, Issuer: d.idp.URL, ClientId: clientId,
					Scopes: []string{"email"}, SecretRef: "env:EXAMPLE_SECRET",
				}.Build())
				x.NoError(err)
			}
		}

		front, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "account"}.Build())
		x.NoError(err)
		role, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
			Tenant: at, Alias: "front-door",
			Methods: []string{
				"/roster.TenantService/Get", "/roster.ConnectionService/List", "/roster.ConnectionService/Get",
				"/roster.IdentityService/Get", "/roster.IdentityService/Add", "/roster.HolderService/Add",
				"/roster.VouchService/Delegate", "/roster.VouchService/Accept", "/roster.DelegationService/Revoke",
				"/roster.FrontService/WhoseHost", "/roster.FrontService/WhereFrom",
				"/roster.MeService/Get", "/roster.MeService/Unlink", "/roster.MeService/SignOutEverywhere",
				"/roster.HolderService/Update", "/roster.HolderService/RevokeKey", "/roster.EmailService/List",
				"/roster.EmailService/Add", "/roster.EmailService/Erase", "/roster.ApiKeyService/Issue",
				"/roster.CredentialService/Set", "/roster.CredentialService/Enrol",
			},
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: front.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		token, sum, err := keys.Mint(keys.PrefixTenant)
		x.NoError(err)
		_, err = s.Ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: front.GetId()}.Build(), Alias: "account", Secret: sum,
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)
		tokens[alias] = token
	}

	// erin, in contoso, arrives through the provider and holds a role naming
	// what the account screens call about her own row.
	erin, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "erin",
	}.Build())
	x.NoError(err)
	d.erin = erin.GetId()
	_, err = s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: erin.GetId()}.Build(), Provider: "example", Subject: "3001",
	}.Build())
	x.NoError(err)
	self, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "self",
		Methods: []string{"/roster.IdentityService/Add", "/roster.EmailService/List"},
	}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: self.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: erin.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// bob, in fabrikam, has a password and nothing else.
	bob, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(), Alias: "bob",
	}.Build())
	x.NoError(err)
	_, err = s.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: bob.GetId()}.Build(), Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	// roster on the wire, twice off one server: gRPC and Connect.
	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop() })

	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	connect, err := url.Parse(up.URL)
	x.NoError(err)

	front := httptest.NewUnstartedServer(nil)
	base, err := url.Parse("http://" + front.Listener.Addr().String())
	x.NoError(err)

	a, err := account.New(ctx, account.Config{
		Roster:   l.Addr().String(),
		Connect:  connect,
		Insecure: true,
		Keys:     tokens,
		Base:     base,
		Enrol:    enrol,
		Sessions: authsession.New(authsession.NewMemStore(), authsession.Insecure()),
		// The page, standing in for the built UI: the round trips end here.
		Static: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("home")) }),
	})
	x.NoError(err)
	t.Cleanup(func() { a.Close() })

	front.Config.Handler = a.Handler()
	front.Start()
	t.Cleanup(front.Close)
	d.app = front

	return d
}

// browser is a client with a jar that arrives under one of the names the app
// serves, whatever address the test server actually listens on.
type browser struct {
	*http.Client
	base string
	host string
}

func (d *deployment) browser(t *testing.T, host string) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	return &browser{
		Client: &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// The provider's round trip crosses to the fake and back; every hop
			// back to the app arrives under the tenant's name.
			if strings.HasPrefix(req.URL.String(), d.app.URL) {
				req.Host = via[0].Host
			}

			return nil
		}},
		base: d.app.URL,
		host: host,
	}
}

func (b *browser) do(t *testing.T, method, path, body string, with func(*http.Request)) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, b.base+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Host = b.host
	if with != nil {
		with(req)
	}
	res, err := b.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(out)
}

// rpc speaks Connect to the app's origin, the way the page does.
func (b *browser) rpc(t *testing.T, method, body string) (int, string) {
	t.Helper()

	return b.do(t, http.MethodPost, method, body, func(r *http.Request) {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Connect-Protocol-Version", "1")
	})
}

// TestTheAccountAppFrontsTwoOperators is the whole of P4 in one sitting: the
// providers come from roster, the key comes from the host, the round trip to a
// provider ends in a session, a password does the same one tenant over, and the
// page reaches roster as the person through the app's own origin.
func TestTheAccountAppFrontsTwoOperators(t *testing.T) {
	d := serve(t, account.Invited())

	t.Run("the sign-in page learns who it is for from roster", func(t *testing.T) {
		x := require.New(t)

		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/providers", "", nil)
		x.Equal(http.StatusOK, code, body)
		var v struct {
			Tenant    struct{ Alias, Name string }
			Providers []struct{ Name, Issuer string }
			Password  bool
		}
		x.NoError(json.Unmarshal([]byte(body), &v))
		x.Equal("contoso", v.Tenant.Alias)
		x.Len(v.Providers, 2)
		x.Equal("example", v.Providers[0].Name)
		x.Equal(d.idp.URL, v.Providers[0].Issuer)
		x.NotContains(body, "EXAMPLE_SECRET", "the secret's whereabouts reached the page")

		code, body = d.browser(t, "fabrikam.test").do(t, http.MethodGet, "/providers", "", nil)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"Fabrikam Inc"`)
		x.Contains(body, `"providers":[]`, "fabrikam has no provider and was told otherwise")

		code, _ = d.browser(t, "nobody.test").do(t, http.MethodGet, "/providers", "", nil)
		x.Equal(http.StatusNotFound, code, "a name nobody serves was answered")
	})

	t.Run("somebody contoso knows signs in through the provider", func(t *testing.T) {
		x := require.New(t)
		d.idp.subject = "3001"
		d.idp.claims = map[string]any{"email": "erin@contoso.com", "email_verified": true}

		b := d.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusOK, code, "the round trip did not end at the page: %s", body)

		code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"erin"`, "roster answered about somebody else: %s", body)
	})

	t.Run("and a stranger is refused, because the deployment said Invited", func(t *testing.T) {
		x := require.New(t)
		d.idp.subject = "9999"
		d.idp.claims = map[string]any{"email": "stranger@contoso.com"}

		b := d.browser(t, "contoso.test")
		code, _ := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusForbidden, code)

		code, _ = b.rpc(t, "/roster.MeService/Get", `{}`)
		x.Equal(http.StatusUnauthorized, code, "a stranger reached roster")
	})

	t.Run("fabrikam's person signs in with a password, under fabrikam's key", func(t *testing.T) {
		x := require.New(t)

		b := d.browser(t, "fabrikam.test")
		code, body := b.do(t, http.MethodPost, "/session", `{"alias":"bob","password":"correct horse battery staple"}`,
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
		x.Equal(http.StatusNoContent, code, body)

		// contoso's key cannot see bob; that this answers is the host having
		// picked fabrikam's.
		code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"bob"`)
	})

	t.Run("somebody signed in attaches a second account to their own row", func(t *testing.T) {
		x := require.New(t)
		d.idp.subject = "3001"
		d.idp.claims = map[string]any{"email": "erin@contoso.com"}

		b := d.browser(t, "contoso.test")
		code, _ := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusOK, code)

		// A second account at the **same** provider is refused by roster -- a
		// second one is a link that found the wrong row -- so this is what the
		// page says when somebody tries.
		d.idp.subject = "3002"
		code, body := b.do(t, http.MethodPost, "/ways?connection=example", "", nil)
		x.Equal(http.StatusConflict, code, body)

		// At another provider it lands on her: the provider says who it is, the
		// session says whose account it is for, and the request says nothing.
		code, body = b.do(t, http.MethodPost, "/ways?connection=other", "", nil)
		x.Equal(http.StatusOK, code, body)

		code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"other"`, "the second account did not land on erin: %s", body)
		x.Contains(body, `"3001"`)
	})

	t.Run("a method the page did not ask for stops at the app", func(t *testing.T) {
		x := require.New(t)

		b := d.browser(t, "fabrikam.test")
		code, _ := b.do(t, http.MethodPost, "/session", `{"alias":"bob","password":"correct horse battery staple"}`,
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
		x.Equal(http.StatusNoContent, code)

		code, _ = b.rpc(t, "/roster.TenantService/List", `{}`)
		x.Equal(http.StatusForbidden, code)
	})
}

// TestAStrangerIsEnrolledWhereTheDeploymentSaysSo is the other policy.
func TestAStrangerIsEnrolledWhereTheDeploymentSaysSo(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Enrolling())
	d.idp.subject = "7777"
	d.idp.claims = map[string]any{"email": "newcomer@contoso.com", "name": "New Comer"}

	b := d.browser(t, "contoso.test")
	code, body := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code, body)

	code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"newcomer"`, "the stranger was not enrolled as the local part of their address")

	// In contoso and nowhere else: the key that enrolled them was contoso's.
	vs, err := d.ungated.Holder().List(context.Background(), rstr.HolderListRequest_builder{
		Filters: []*rstr.HolderFilter{rstr.HolderFilter_builder{
			Tenant: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)
	for _, h := range vs.GetItems() {
		x.NotEqual("newcomer", h.GetAlias())
	}
}
