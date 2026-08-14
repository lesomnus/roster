package sso_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/examples/sso"
	rstr "github.com/lesomnus/roster/rstr"
)

// The whole flow, run.
//
// An example that has never been executed is a guess about an API, and the two
// halves here are exactly the ones that are easy to guess wrong: what a
// provider hands back, and what roster answers for somebody it has never seen.
// So the provider below is real enough to be talked to over HTTP -- discovery,
// JWKS, authorize, token -- and roster is a real server on a real connection.

const clientID = "example-app"

// idp is an OpenID Provider, small but not a mock: it serves the documents
// `go-oidc` fetches and signs tokens `go-oidc` verifies. A mock would agree
// with whatever this package believes, which is the one thing not worth
// checking.
type idp struct {
	*httptest.Server

	key *rsa.PrivateKey

	// subject is who the next sign-in will be, and claims is what else the
	// token will carry.
	subject string
	claims  map[string]any
}

func newIdp(t *testing.T) *idp {
	t.Helper()
	x := require.New(t)

	// 2048 because go-jose refuses less.
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)

	p := &idp{key: k}
	m := http.NewServeMux()

	m.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.URL,
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"jwks_uri":                              p.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	m.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &k.PublicKey, Algorithm: "RS256", Use: "sig"},
		}})
	})

	// Where a browser would sign in. There is nobody to type a password here,
	// so it agrees at once and sends the browser back with the state it was
	// given -- which is the part the app checks.
	m.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		to, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := to.Query()
		q.Set("code", "the-code")
		q.Set("state", r.URL.Query().Get("state"))
		to.RawQuery = q.Encode()

		http.Redirect(w, r, to.String(), http.StatusFound)
	})

	m.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		claims := map[string]any{
			"iss": p.URL,
			"aud": clientID,
			"sub": p.subject,
			"exp": 4102444800, // 2100
			"iat": 1700000000,
		}
		for k, v := range p.claims {
			claims[k] = v
		}

		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "unused",
			"token_type":   "Bearer",
			"id_token":     p.sign(t, claims),
		})
	})

	p.Server = httptest.NewServer(m)
	t.Cleanup(p.Close)

	return p
}

func (p *idp) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	x := require.New(t)

	b, err := json.Marshal(claims)
	x.NoError(err)

	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, nil)
	x.NoError(err)

	o, err := s.Sign(b)
	x.NoError(err)

	v, err := o.CompactSerialize()
	x.NoError(err)

	return v
}

// deployment is roster, and the app in front of it.
type deployment struct {
	roster rstr.Client
	tenant pdid.Id

	// ungated is what the deployment does its own work through -- putting a
	// tenant there, inviting somebody. The login app's credential cannot and
	// must not, which is itself worth seeing.
	ungated rstr.Server

	idp *idp
	app *httptest.Server
}

// serve builds roster and the app in front of it.
//
// `enrol` is a constructor rather than an [sso.Enrol] because the policies that
// write rows need the client, and the client does not exist until this has
// built it.
func serve(t *testing.T, enrol func(rstr.Client) sso.Enrol, tenants map[string]string) *deployment {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	seeded, err := cmd.Seed(ctx, s, cmd.Seeding{
		Tenant:   "acme",
		Holder:   "admin",
		Operator: "ops",
	})
	x.NoError(err)

	// What the login app is allowed to do, spelled out. roster refuses everything
	// to a holder with no binding, so this is not optional wiring -- it is the
	// answer to "which credential does the login app call roster with", and
	// writing the methods out is the answer to "and what may it do with it".
	//
	// A deployment mints an API key for this holder (`roster key add`) and the
	// connection carries it. Here it is `auth.Plain`, which believes what the
	// caller writes -- right for a test, and never for a deployment.
	svc, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: seeded.Tenant.Bytes()}.Build(),
		Alias:  "login-app",
	}.Build())
	x.NoError(err)

	role, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: seeded.Tenant.Bytes()}.Build(),
		Alias:  "login-app",
		Methods: []string{
			// The tenant is part of naming an identity now, so the login app
			// has to be able to resolve the one its front door names.
			"/roster.TenantService/Get",
			"/roster.IdentityService/Get",
			"/roster.IdentityService/Add",
			"/roster.HolderService/Get",
			"/roster.HolderService/Add",
		},
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: svc.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// The credential is a property of the connection, not of every call. That
	// is what lets the app below hold a plain `rstr.Client` and know nothing
	// about how this deployment authenticates.
	who, err := pdid.From(svc.GetId())
	x.NoError(err)

	conn := serveRoster(t, s.Grpc(ctx, cmd.Config{}), auth.PlainProvider(who.String()))
	client := rstr.NewClient(conn)

	d := &deployment{roster: client, tenant: seeded.Tenant, ungated: s.Ungated, idp: newIdp(t)}

	sessions := authsession.New(authsession.NewMemStore(), authsession.Insecure())

	// The app's own address has to be known before the app is built, because
	// the provider is told where to send the browser back.
	front := httptest.NewUnstartedServer(nil)
	addr := "http://" + front.Listener.Addr().String()

	a, err := sso.New(ctx, sso.Config{
		Issuer:       d.idp.URL,
		ClientID:     clientID,
		ClientSecret: "unused",
		RedirectURL:  addr + "/callback",
		Provider:     "example",
		Scopes:       []string{"email"},
		Tenants:      tenants,
	}, client, sessions, enrol(client))
	x.NoError(err)

	// The example's mux is `/login` and `/callback` and nothing else, which is
	// right -- it is mounted beside the app's own pages. This is those pages.
	m := http.NewServeMux()
	m.Handle("/login", a.Handler())
	m.Handle("/callback", a.Handler())
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("home")) })

	front.Config.Handler = m
	front.Start()
	t.Cleanup(front.Close)

	d.app = front

	return d
}

// serveRoster answers roster on a listener that is a channel, and dials it with
// a credential.
//
// `pdtest.Serve` is the same thing without the credential; this adds
// `auth.Inject`, which is the whole of how a client of a payday app says who it
// is. A deployment does exactly this with an API key instead.
func serveRoster(t *testing.T, g *grpc.Server, as auth.Provider) *grpc.ClientConn {
	t.Helper()
	x := require.New(t)

	l := bufconn.Listen(1 << 20)
	go func() { _ = g.Serve(l) }()

	opts := append(auth.Inject(as),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return l.DialContext(ctx)
		}),
	)

	conn, err := grpc.NewClient("passthrough://bufconn", opts...)
	x.NoError(err)

	t.Cleanup(func() {
		conn.Close()
		g.Stop()
		l.Close()
	})

	return conn
}

// signIn walks a browser from /login to wherever it ends up.
func (d *deployment) signIn(t *testing.T) *http.Response {
	t.Helper()
	return d.signInAs(t, "")
}

// signInAs is the same, arriving under a given name -- which is how a
// multi-tenant deployment tells its operators apart.
func (d *deployment) signInAs(t *testing.T, host string) *http.Response {
	t.Helper()
	x := require.New(t)

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	req, err := http.NewRequest(http.MethodGet, d.app.URL+"/login", nil)
	x.NoError(err)
	if host != "" {
		req.Host = host
	}

	res, err := c.Do(req)
	x.NoError(err)
	t.Cleanup(func() { res.Body.Close() })

	return res
}

func TestSomebodyRosterAlreadyKnows(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})
	d.idp.subject = "1078"

	// Invited: the Holder is put there first, and the identity linked to it.
	// This is the two-step shape, and it is the one a deployment that has not
	// decided anything should be in.
	h, err := d.roster.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	_, err = d.roster.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		Provider: "example",
		Subject:  "1078",
	}.Build())
	x.NoError(err)

	res := d.signIn(t)
	x.Equal(http.StatusOK, res.StatusCode, "signed in and followed the redirect home")

	// The cookie is this app's session and says nothing about the provider.
	var got *http.Cookie
	for _, c := range res.Request.Response.Cookies() {
		if c.Value != "" && c.Name != "sso_state" {
			got = c
		}
	}
	x.NotNil(got, "a session was minted")
}

// TestSomebodyNobodyInvited is the case the example exists for.
func TestSomebodyNobodyInvited(t *testing.T) {
	x := require.New(t)

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})
	d.idp.subject = "2000"

	res := d.signIn(t)
	x.Equal(http.StatusForbidden, res.StatusCode,
		"a valid account at the provider is not an account here")
}

// TestEnrolled is the usual shape: the front door names the tenant.
//
// A tenant is the same service under a different operator's domain, so which
// one somebody is signing in to is which name they came to -- not what their
// email says, which is where they authenticate and often a different
// organisation entirely.
func TestEnrolled(t *testing.T) {
	ctx := t.Context()

	// The app is reached on 127.0.0.1 in this test, so that is acme's front
	// door. A deployment maps the names it actually serves.
	d := serve(t, sso.Enrolling, map[string]string{"127.0.0.1": "acme"})

	t.Run("a name this deployment serves", func(t *testing.T) {
		x := require.New(t)

		d.idp.subject = "6000"
		d.idp.claims = map[string]any{
			"email":          "grace@somewhere-else.example",
			"email_verified": true,
			"name":           "Grace",
		}

		res := d.signIn(t)
		x.Equal(http.StatusOK, res.StatusCode,
			"their email is at another organisation entirely, which is not this question")

		v, err := d.roster.Identity().Get(ctx, rstr.IdentityGetRequest_builder{
			Ref: rstr.IdentityRef_builder{
				Subject: rstr.IdentityRefBySubject_builder{
					TenantId: d.tenant.Bytes(),
					Provider: proto.String("example"),
					Subject:  proto.String("6000"),
				}.Build(),
			}.Build(),
			Select: rstr.IdentitySelect_builder{
				Holder: rstr.HolderSelect_builder{
					Alias:  proto.Bool(true),
					Tenant: rstr.TenantSelect_builder{Alias: proto.Bool(true)}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("acme", v.GetHolder().GetTenant().GetAlias())
		x.Equal("grace", v.GetHolder().GetAlias())
	})

	t.Run("and one it does not serve", func(t *testing.T) {
		x := require.New(t)

		// Asked of the app rather than driven through a browser: a second host
		// would need its own `redirect_uri` registered with the provider, so a
		// test that only rewrote the `Host` header would be stopped by the state
		// cookie belonging to the other name -- 400, for the wrong reason.
		d2 := serve(t, sso.Enrolling, map[string]string{"acme.example.com": "acme"})
		d2.idp.subject = "6100"
		d2.idp.claims = map[string]any{"email": "eve@x.example", "email_verified": true}

		res := d2.signIn(t)
		x.Equal(http.StatusForbidden, res.StatusCode,
			"127.0.0.1 is not a name this deployment serves")
	})
}

// TestTheSameAccountAtTwoOperators is what a tenant being the wall means,
// followed through.
//
// `Identity` is unique on (tenant, provider, subject), so the same Google
// account can sign up to acme's service and to beta's. Those are two Holders
// with two histories and two sets of permissions, and nothing relates them --
// which is the point rather than a limitation: a row that spanned tenants would
// have no owner, no answer to who may erase it, and no tenant whose trail it
// belongs to.
//
// The lookup is inside a tenant, so beta's door does not find acme's row at
// all. There is no comparison to make and none to forget.
func TestTheSameAccountAtTwoOperators(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// This app is acme's front door. It is acme's and only acme's, because its
	// credential is a Holder of acme and the wall narrows what it may read to
	// that -- see the note on `serve`. A login app that fronts several
	// operators needs a credential whose scope covers them, which is an API
	// key rather than a person.
	d := serve(t, sso.Enrolling, map[string]string{"127.0.0.1": "acme"})

	beta, err := d.ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{
		Alias: "beta",
	}.Build())
	x.NoError(err)

	// The same human already has an account with **beta**, linked the way an
	// invitation would link it.
	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: beta.GetId()}.Build(),
		Alias:  "heidi",
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		Provider: "example",
		Subject:  "8000",
	}.Build())
	x.NoError(err)

	// They arrive at acme with the same Google account.
	d.idp.subject = "8000"
	d.idp.claims = map[string]any{
		"email": "heidi@somewhere.example", "email_verified": true, "name": "Heidi",
	}

	res := d.signIn(t)
	x.Equal(http.StatusOK, res.StatusCode, "they sign up to acme as well")

	// And beta's row is untouched: acme's door never saw it, because the
	// lookup names a tenant and that one was acme's.
	got, err := d.ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{
			rstr.IdentityFilter_builder{
				Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
			}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.Len(got.GetItems(), 1, "beta's holder still has exactly the one")

	all, err := d.ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(all.GetItems(), 2, "and there are two rows for one human, in two tenants")
}

// TestTwoInOneTenantIsStillRefused: the account-takeover shape has not moved.
//
// Putting the tenant in the key widens what is allowed **across** tenants and
// changes nothing inside one -- two Holders of acme claiming the same subject
// at the same provider is still whoever-logs-in-next-wins.
func TestTwoInOneTenantIsStillRefused(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	one, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "one",
	}.Build())
	x.NoError(err)

	two, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "two",
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: one.GetId()}.Build(),
		Provider: "example",
		Subject:  "9000",
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: two.GetId()}.Build(),
		Provider: "example",
		Subject:  "9000",
	}.Build())
	x.Error(err, "two Holders of one tenant cannot be one subject")
}

// TestTheStateIsChecked is the CSRF defence, and it is worth its own test
// because nothing else notices when it stops working.
func TestTheStateIsChecked(t *testing.T) {
	x := require.New(t)

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	// Straight to the callback, the way somebody else's page would send a
	// browser: a code, a state, and no cookie from `/login`.
	res, err := http.Get(d.app.URL + "/callback?code=the-code&state=whatever")
	x.NoError(err)
	defer res.Body.Close()

	x.Equal(http.StatusBadRequest, res.StatusCode)

	b := make([]byte, 64)
	n, _ := res.Body.Read(b)
	x.NotContains(strings.ToLower(string(b[:n])), "state",
		"and it does not say which half was wrong")
}
