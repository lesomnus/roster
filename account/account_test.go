package account_test

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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
	"github.com/lesomnus/roster/server/vouch"
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

	// mail is what the app asked to have delivered: the mailbox, and the link.
	mu   sync.Mutex
	mail []struct{ to, link string }
}

func (d *deployment) sent(to string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.mail) - 1; i >= 0; i-- {
		if d.mail[i].to == to {
			return d.mail[i].link
		}
	}

	return ""
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
				"/roster.HolderService/Update", "/roster.ApiKeyService/Erase", "/roster.ApiKeyService/List", "/roster.EmailService/List",
				"/roster.EmailService/Add", "/roster.EmailService/Erase", "/roster.ApiKeyService/Issue",
				"/roster.CredentialService/Set", "/roster.CredentialService/Enrol", "/roster.CredentialService/Erase",
				"/roster.HolderService/Get", "/roster.EmailService/Get", "/roster.EmailService/Verify", "/roster.EmailService/Confirm",
				"/roster.VouchService/Link", "/roster.VouchService/Redeem", "/roster.VouchService/Reset", "/roster.VouchService/Verify",
				"/roster.DelegationService/List", "/roster.DelegationService/Erase",
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
		Methods: []string{
			"/roster.IdentityService/Add", "/roster.EmailService/List", "/roster.EmailService/Get",
			"/roster.EmailService/Add", "/roster.EmailService/Verify",
			"/roster.CredentialService/Set", "/roster.CredentialService/Enrol",
		},
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
	// And an address, which is where a recovery link goes.
	_, err = s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: bob.GetId()}.Build(), Address: "bob@fabrikam.com",
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
		// The mailer, standing in for one: it keeps what it was asked to send.
		Mail: func(ctx context.Context, to, subject, link string) error {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.mail = append(d.mail, struct{ to, link string }{to, link})

			return nil
		},
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

// TestSomebodyRecoversTheirAccountByMail is the recovery flow: a link mailed to
// the address on the row, a mailbox proved, and a **password** handed over --
// not a session -- shown once, with everything issued before it void.
func TestSomebodyRecoversTheirAccountByMail(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	b := d.browser(t, "fabrikam.test")

	// Whatever is typed is accepted, and only the mailbox learns whether a
	// message went out -- so a stranger's address answers the same.
	code, _ := b.do(t, http.MethodPost, "/recover", `{"address":"nobody@fabrikam.com"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusAccepted, code)
	x.Empty(d.sent("nobody@fabrikam.com"), "a link was mailed to nobody")

	code, _ = b.do(t, http.MethodPost, "/recover", `{"address":"bob@fabrikam.com"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusAccepted, code)

	var link string
	x.Eventually(func() bool { link = d.sent("bob@fabrikam.com"); return link != "" }, 2*time.Second, 20*time.Millisecond,
		"no link was mailed to bob")
	x.Contains(link, "/redeem?token=rl_")

	// The link, clicked: a page with a new password on it and no session.
	res, err := b.Get(link)
	x.NoError(err)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode, string(page))
	x.Contains(string(page), "Your new password")
	password := between(string(page), `user-select:all">`, `</code>`)
	x.NotEmpty(password, "the page shows no password: %s", page)

	code, _ = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusUnauthorized, code, "a recovery link signed somebody in")

	// Twice is nothing: the link was spent.
	res, err = b.Get(link)
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNotFound, res.StatusCode, "a recovery link was redeemed twice")

	// The new password signs bob in; the old one does not.
	code, _ = b.do(t, http.MethodPost, "/session", `{"alias":"bob","password":"`+password+`"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, "the recovered password does not sign in")
	code, _ = d.browser(t, "fabrikam.test").do(t, http.MethodPost, "/session", `{"alias":"bob","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusUnauthorized, code, "the old password still signs in")
}

// TestSomebodyVerifiesAnAddressOfTheirOwn is the verification flow: an address
// added from the page, a link mailed to **that** address, and a click that
// stamps the row and signs nobody in.
func TestSomebodyVerifiesAnAddressOfTheirOwn(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	d.idp.subject = "3001"
	d.idp.claims = map[string]any{"email": "erin@contoso.com"}

	b := d.browser(t, "contoso.test")
	code, _ := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code)

	// Added through the proxy, as the person, with her own reference -- which
	// the page has from `Me.Get` and this test has from the seed.
	code, body := b.rpc(t, "/roster.EmailService/Add", `{"holder":{"id":"`+std(d.erin)+`"},"address":"erin@contoso.com"}`)
	x.Equal(http.StatusOK, code, body)
	var added struct {
		Id           string `json:"id"`
		DateVerified any    `json:"dateVerified"`
	}
	x.NoError(json.Unmarshal([]byte(body), &added))
	x.Nil(added.DateVerified)
	id, err := base64.StdEncoding.DecodeString(added.Id)
	x.NoError(err)

	code, body = b.do(t, http.MethodPost, "/verify", `{"id":"`+base64.RawURLEncoding.EncodeToString(id)+`"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusAccepted, code, body)

	link := d.sent("erin@contoso.com")
	x.Contains(link, "/confirm?token=rl_", "no link was mailed to the address on the row")

	// Clicked from a browser with no session at all: the mailbox is the proof.
	res, err := d.browser(t, "contoso.test").Get(link)
	x.NoError(err)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode, string(page))
	x.Contains(string(page), "Address confirmed")

	code, body = b.rpc(t, "/roster.EmailService/List", `{"filters":[{"holder":{"id":"`+std(d.erin)+`"}}]}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"dateVerified"`, "the address was confirmed and not stamped: %s", body)
}

// std is bytes as Connect's JSON carries them.
func std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func between(s, a, z string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, z)
	if j < 0 {
		return ""
	}

	return s[:j]
}

// TestSomebodyEnrolsAnAuthenticatorAppAndSignsInWithIt is the second factor
// from the page: enrolled through the proxy on their own row, proved at once
// through `/prove` so it counts, and then the two forms -- password, then a
// code -- the way the page sends them.
func TestSomebodyEnrolsAnAuthenticatorAppAndSignsInWithIt(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	d.idp.subject = "3001"
	d.idp.claims = map[string]any{"email": "erin@contoso.com"}

	b := d.browser(t, "contoso.test")
	code, _ := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code)

	// A password first, on her own row: there is none to prove, and a first
	// one is set for somebody -- so it goes in the operator way here.
	_, err := d.ungated.Credential().Set(context.Background(), rstr.CredentialSetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: d.erin}.Build(), Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	code, body := b.rpc(t, "/roster.CredentialService/Enrol", `{"ref":{"id":"`+std(d.erin)+`"},"kind":"totp","name":"phone"}`)
	x.Equal(http.StatusOK, code, body)
	var enrolled struct{ Seed string }
	x.NoError(json.Unmarshal([]byte(body), &enrolled))
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrolled.Seed)
	x.NoError(err)

	// Not yet proved: the second form does not offer it, so a mis-scanned QR
	// cannot strand her. Proved from the page, it does.
	// A spent step is not accepted twice, so the sign-in below uses the next
	// step's code, which the verifier's window still admits.
	codeAt := func(d int64) string { return vouch.CodeAt(seed, time.Now().Unix()/30+d) }
	code, body = b.do(t, http.MethodPost, "/prove", `{"kind":"totp","name":"phone","secret":"`+codeAt(0)+`"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	// The two forms, in a fresh browser, as the page sends them.
	fresh := d.browser(t, "contoso.test")
	code, body = fresh.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusOK, code, "the password alone signed her in, or was refused: %s", body)
	x.Contains(body, `"factors":[{"kind":"totp","name":"phone"}]`, "the second form was not told what to ask for: %s", body)

	code, body = fresh.do(t, http.MethodPost, "/session/continue", `{"kind":"totp","name":"phone","secret":"`+codeAt(1)+`"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	code, body = fresh.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"erin"`)
}
