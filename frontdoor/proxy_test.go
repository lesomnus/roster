package frontdoor_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/frontdoor"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestABrowserSpeaksConnectToTheAppAndRosterAnswersAsThePerson is the spike
// `ts/plan.md` § F asked for: one `Me.Get`, spoken by a browser to the app's
// own origin in the protocol the console speaks to roster, handed on with the
// delegation swapped in, answered by roster about the person who signed in.
//
// It is the whole of what the account app is built on afterwards -- `ts/gen`
// and the store working against the app's origin unchanged -- so what is
// pinned here is the road and its three refusals: no session, a method the
// app did not ask for, and a request no Connect client would make.
func TestABrowserSpeaksConnectToTheAppAndRosterAnswersAsThePerson(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// A roster, with a control plane so that keys are a thing.
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

	seeded, err := cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops"})
	x.NoError(err)

	// Somebody with a password.
	const password = "correct horse battery staple"
	erin, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: seeded.Tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)
	_, err = s.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: erin.GetId()}.Build(),
		Secret: []byte(password),
	}.Build())
	x.NoError(err)

	// The app, as a holder in contoso with a tenant key: what § E settled.
	front, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: seeded.Tenant.Bytes()}.Build(),
		Alias:  "front-door",
	}.Build())
	x.NoError(err)
	role, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: seeded.Tenant.Bytes()}.Build(),
		Alias:  "front-door",
		Methods: []string{
			rstr.VouchService_Delegate_FullMethodName,
			rstr.DelegationService_Revoke_FullMethodName,
			rstr.MeService_Get_FullMethodName,
			rstr.HolderService_List_FullMethodName,
		},
	}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: front.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	_, err = s.Ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: front.GetId()}.Build(),
		Alias:   "front-door",
		Secret:  sum,
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	// roster served twice off one server: gRPC for the app's own calls, and
	// Connect over HTTP for what the proxy hands on -- the second listener a
	// deployment has for the console, `README.md` § A browser.
	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop() })

	opts := append(auth.Inject(auth.BearerProvider(token)),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(l.Addr().String(), opts...)
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })

	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)
	upstream := httptest.NewServer(h)
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	x.NoError(err)

	// The door, and the app's mux: the two forms at `/session`, and everything
	// else the page says to roster through the proxy.
	d, err := frontdoor.New(frontdoor.Config{
		Sessions:   authsession.New(authsession.NewMemStore(), authsession.Insecure()),
		Vouch:      rstr.NewVouchServiceClient(conn),
		Delegation: rstr.NewDelegationServiceClient(conn),
		Methods:    []string{rstr.MeService_Get_FullMethodName, rstr.HolderService_List_FullMethodName},
		Tenant:     func(context.Context, string) (string, error) { return "contoso", nil },
	})
	x.NoError(err)

	m := http.NewServeMux()
	m.Handle("/session", d.Handler())
	m.Handle("/session/", d.Handler())
	m.Handle("/", d.Proxy(target, func(context.Context, string) (string, error) { return token, nil }))
	app := httptest.NewServer(m)
	t.Cleanup(app.Close)

	jar, err := cookiejar.New(nil)
	x.NoError(err)
	browser := &http.Client{Jar: jar}

	connect := func(c *http.Client, method, path, body string, with func(*http.Request)) (int, string) {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, method, app.URL+path, strings.NewReader(body))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		if with != nil {
			with(req)
		}

		res, err := c.Do(req)
		x.NoError(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(b)
	}

	t.Run("nobody signed in reaches nothing", func(t *testing.T) {
		x := require.New(t)

		code, _ := connect(browser, http.MethodPost, "/roster.MeService/Get", `{}`, nil)
		x.Equal(http.StatusUnauthorized, code)
	})

	// The first form, the way the page does it.
	res, err := browser.Post(app.URL+"/session", "application/json",
		strings.NewReader(`{"alias":"erin","password":"`+password+`"}`))
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode, "erin did not sign in")

	t.Run("the page asks roster who it is, and roster says erin", func(t *testing.T) {
		x := require.New(t)

		code, body := connect(browser, http.MethodPost, "/roster.MeService/Get", `{}`, nil)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"erin"`, "roster answered about somebody else, or nobody: %s", body)
		x.NotContains(body, "front-door", "roster answered about the app rather than the person")
	})

	t.Run("a method the app did not ask for stops at the app", func(t *testing.T) {
		x := require.New(t)

		code, _ := connect(browser, http.MethodPost, "/roster.TenantService/List", `{}`, nil)
		x.Equal(http.StatusForbidden, code)
	})

	t.Run("a request no Connect client would make is refused", func(t *testing.T) {
		x := require.New(t)

		// A cross-site form can post a body with a cookie; it cannot set this
		// header. That is the whole of the CSRF argument.
		code, _ := connect(browser, http.MethodPost, "/roster.MeService/Get", `{}`, func(r *http.Request) {
			r.Header.Del("Connect-Protocol-Version")
		})
		x.Equal(http.StatusForbidden, code)

		code, _ = connect(browser, http.MethodGet, "/roster.MeService/Get", ``, nil)
		x.Equal(http.StatusMethodNotAllowed, code)
	})

	t.Run("and the delegation never reached the browser", func(t *testing.T) {
		x := require.New(t)

		for _, c := range jar.Cookies(mustURL(t, app.URL)) {
			x.NotContains(c.Value, keys.PrefixDelegation, "a delegation is in a cookie")
		}
	})
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	require.NoError(t, err)

	return u
}
