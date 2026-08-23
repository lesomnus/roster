package sso_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
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
	"testing"
	"time"

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
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
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

	// keyring is what this deployment wraps a seed with, so that a test may
	// enrol one and the served instance can read it.
	keyring vouch.Keyring
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
	cdrv, cdsn := pdtest.DB(t)

	// With a control plane, which is what the deployment this example describes
	// has. Without one roster serves `auth.Plain` and believes whatever a
	// caller writes -- fine for the provider half, and no use at all for the
	// password half: a delegation is bound to the caller it was issued to, and
	// `Plain` names a caller that cannot say which row it is.
	key := make([]byte, 32)
	_, err := rand.Read(key)
	x.NoError(err)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},

		// A keyring, because a deployment that holds second factors has one
		// and this is the app that shows what two forms look like.
		Vouch: cmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(key)}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))

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
	// A deployment mints an API key for this holder and the connection carries
	// it, which is what happens below: an `rt_`, because this app is one
	// operator's front door and therefore somebody inside that tenant.
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

			// The password half, and the page it exists for. Written out
			// rather than a pattern, so that what this app may do reads as a
			// list somebody chose.
			"/roster.VouchService/Delegate",

			// And the provider half, which is a **separate** grant: this app
			// runs an OIDC flow and checks the token itself, and `Accept` is
			// roster believing it. An app that only checks passwords does not
			// get this line -- see D49, and the warning `roster key add` gives.
			"/roster.VouchService/Accept",
			"/roster.VouchService/Revoke",
			"/roster.MeService/Get",
			"/roster.MeService/Unlink",
			"/roster.MeService/Link",
			"/roster.MeService/SignOutEverywhere",
			"/roster.MeService/IssueKey",
			"/roster.MeService/RevokeKey",

			// What the front door asks before it knows anything, which is how
			// it stops holding a copy of which tenant serves which name.
			"/roster.FrontService/WhoseHost",
			"/roster.FrontService/WhereFrom",
		},
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: svc.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// The credential is a property of the connection, not of every call. That
	// is what lets the app below know nothing about how this deployment
	// authenticates -- and it is why a delegation travels in a header of its
	// own rather than in `authorization`, which this connection has already
	// filled in.
	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	_, err = s.Ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: svc.GetId()}.Build(),
		Alias:  "front-door",
		Secret: sum,

		// A key holds no more than its holder, so this list is an attenuation
		// of the role above rather than a second grant. Written out to the same
		// thing, because there is nothing this app does that its key should
		// not.
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)

	conn := serveRoster(t, g, auth.BearerProvider(token))
	client := rstr.NewClient(conn)

	d := &deployment{
		roster: client, tenant: seeded.Tenant, ungated: s.Ungated,
		idp: newIdp(t), keyring: s.Keyring,
	}

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
	}, conn, sessions, enrol(client))
	x.NoError(err)

	// The example's mux is `/login` and `/callback` and nothing else, which is
	// right -- it is mounted beside the app's own pages. This is those pages.
	m := http.NewServeMux()
	m.Handle("/login", a.Handler())
	m.Handle("/callback", a.Handler())
	m.Handle("/session", a.Handler())
	m.Handle("/session/continue", a.Handler())
	m.Handle("/me", a.Handler())
	m.Handle("/me/", a.Handler())
	m.Handle("/account", a.Handler())
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

// TestAPasswordSignInReadsItsOwnRecord is what D24 says this app is for:
// specifying the delegation rather than demonstrating it.
//
// It is the whole of PLAN.md D23 in one flow. The app holds a credential that
// can read every tenant it serves; the page below is somebody's own record, and
// it is read with a credential narrowed to that person and to one method. The
// two ways it is not done are the two D23 refuses -- the app's own key, and the
// app filtering rows it should not have been handed.
func TestAPasswordSignInReadsItsOwnRecord(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
		Name:   "Erin",
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		Address: "erin@acme.example",
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		Provider: "example",
		Subject:  "1078",
	}.Build())
	x.NoError(err)

	// Erin may do two things, so that what the page shows can be narrower than
	// what she holds -- which is the whole point of the assertion below.
	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant:  rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{"/roster.MeService/Get", "/roster.HolderService/Get"},
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// The deployment's own work: setting somebody's first password is not
	// something the front door may do, and `VouchService/Set` is not on its
	// role. See `roster init` and PLAN.md's list, item 10.
	_, err = vouch.New(d.ungated, d.ungated).Set(ctx, rstr.VouchSetRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	post := func(alias, password string) *http.Response {
		t.Helper()

		body := strings.NewReader(`{"alias":"` + alias + `","password":"` + password + `"}`)
		res, err := c.Post(d.app.URL+"/session", "application/json", body)
		x.NoError(err)
		t.Cleanup(func() { res.Body.Close() })

		return res
	}

	t.Run("a wrong password is one answer and no cookie", func(t *testing.T) {
		x := require.New(t)

		res := post("erin", "hunter2")
		x.Equal(http.StatusUnauthorized, res.StatusCode)
		x.Empty(res.Cookies())

		// And a person who is not there is the same answer, which is the thing
		// roster went to trouble to make one and this must not undo.
		x.Equal(http.StatusUnauthorized, post("nobody-at-all", "hunter2").StatusCode)
	})

	res := post("erin", "correct horse battery staple")
	x.Equal(http.StatusNoContent, res.StatusCode)
	x.NotEmpty(res.Cookies(), "a session was minted")

	t.Run("and the page is read as the person", func(t *testing.T) {
		x := require.New(t)

		got, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer got.Body.Close()
		x.Equal(http.StatusOK, got.StatusCode)

		var v record
		x.NoError(json.NewDecoder(got.Body).Decode(&v))

		x.Equal("erin", v.Alias)
		x.Equal("Erin", v.Name)
		x.Equal([]string{"erin@acme.example"}, v.Emails)

		// One list rather than two, because a person reading this is asking
		// *how can I get in* and the answer does not sort itself into what
		// roster holds and what somebody else does.
		x.Len(v.SignsIn, 2)
		x.Equal("password", v.SignsIn[0].Kind)
		x.Equal("example", v.SignsIn[1].Kind)
		x.NotEmpty(v.SignsIn[1].Id, "a screen with a remove button has nothing to name")

		// Erin holds two methods and the delegation was minted for one, so this
		// is what she may do **through this app** rather than everything she
		// may do. A page drawn from the wider list would show a button that is
		// refused when pressed, which is the drift the field exists to prevent
		// -- in the direction nobody had looked.
		x.Equal([]string{"/roster.MeService/Get"}, v.MayCall)
	})

	t.Run("and signing out revokes it rather than forgetting it", func(t *testing.T) {
		x := require.New(t)

		// One before, so that "none after" is a count that moved.
		n, err := d.ungated.Delegation().List(ctx, rstr.DelegationListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(n.GetItems(), 1)

		req, err := http.NewRequest(http.MethodDelete, d.app.URL+"/session", nil)
		x.NoError(err)

		out, err := c.Do(req)
		x.NoError(err)
		defer out.Body.Close()
		x.Equal(http.StatusNoContent, out.StatusCode)

		// Gone from roster, not merely dropped here. Forgetting it would leave
		// a live credential for that person until its own clock ran out, which
		// is the case D23 said "revoking it is a delete" about and which
		// nothing could do until `VouchService/Revoke` existed.
		after, err := d.ungated.Delegation().List(ctx, rstr.DelegationListRequest_builder{}.Build())
		x.NoError(err)
		x.Empty(after.GetItems(), "signing out left a live delegation behind")

		// And the page is closed to this browser.
		got, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer got.Body.Close()
		x.Equal(http.StatusForbidden, got.StatusCode)
	})

	// The other half of the first form, which every suite here had left
	// unexercised.
	//
	// A sign-in page collects one field and what people type into it is far more
	// often an address than an alias, so `frontdoor` takes either and decides
	// which one the body named -- an address is a lookup through `Email` and an
	// alias is a reference, and they are two different resolutions of the same
	// question. Everything written about this app signs in by alias, so the
	// branch a deployment's users actually take was the one nothing ran.
	//
	// What that hides is quiet in the worst way: a body naming an address
	// resolves to nobody, and the answer to nobody is deliberately the answer to
	// a wrong password. So the failure is not an error anywhere -- it is every
	// person on the deployment being told their password is wrong, and retyping
	// it.
	//
	// Last, and with a jar of its own, so that the counts the subtests above
	// assert are counts of what they were about.
	t.Run("and the same person named by the address they typed", func(t *testing.T) {
		x := require.New(t)

		jar, err := cookiejar.New(nil)
		x.NoError(err)

		c := &http.Client{Jar: jar}

		res, err := c.Post(d.app.URL+"/session", "application/json",
			strings.NewReader(`{"address":"erin@acme.example","password":"correct horse battery staple"}`))
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusNoContent, res.StatusCode)

		// And it is Erin. Signing in is a status code, and a status code says
		// only that somebody was signed in -- so the record is read and the
		// name in it is what makes this a test about *which* person an address
		// resolves to rather than about whether the branch runs at all.
		got, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer got.Body.Close()
		x.Equal(http.StatusOK, got.StatusCode)

		var v record
		x.NoError(json.NewDecoder(got.Body).Decode(&v))
		x.Equal("erin", v.Alias)

		// An address nobody here has is the same answer as a wrong password,
		// which is the sentence roster's `verify` is written around and which
		// this branch is the one place in this app that could undo. The lookup
		// fails a step earlier than the comparison does, so a refusal of its own
		// shape would be a list of which addresses have accounts, readable by
		// anyone with a form.
		miss, err := c.Post(d.app.URL+"/session", "application/json",
			strings.NewReader(`{"address":"nobody@acme.example","password":"correct horse battery staple"}`))
		x.NoError(err)
		defer miss.Body.Close()
		x.Equal(http.StatusUnauthorized, miss.StatusCode)
	})
}

// TestTheProviderHalfHasADelegationNow, which is the seam D23 left, closed.
//
// It used to be `TestTheProviderHalfHasNoDelegation`, and it asserted a
// refusal: a sign-in through the provider never calls `Vouch` -- the secret is
// somebody else's to check -- so there was nothing for a delegation to ride
// back on, and `/me` answered `403` rather than quietly falling back to the
// app's own credential. That refusal was right, and it was a gap said out loud
// rather than a design.
//
// D49 is the decision it was waiting for. roster does not check the token --
// being the relying party is what `connection.proto` says roster is not -- so
// the app checks it and `Vouch.Accept` hands the claim over. `frontdoor` mints
// the session and holds the delegation beside it, which is the same thing it
// does after a password, because none of that was ever about how somebody was
// proved.
//
// What still has to be true is the thing the old test was protecting: the page
// is drawn with a credential **for the person**, and never with the app's own.
func TestTheProviderHalfHasADelegationNow(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(c rstr.Client) sso.Enrol { return sso.Enrolling(c) }, map[string]string{"127.0.0.1": "acme"})
	d.idp.subject = "2049"
	d.idp.claims = map[string]any{"email": "newcomer@acme.example", "email_verified": true}

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	res, err := c.Get(d.app.URL + "/login")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode, "signed in and followed the redirect home")

	got, err := c.Get(d.app.URL + "/me")
	x.NoError(err)
	defer got.Body.Close()
	x.Equal(http.StatusOK, got.StatusCode,
		"the provider half still cannot draw the page it signed somebody in for")

	// One was minted, and it is the person's rather than the app's: what
	// `Accept` answers is a delegation over a **holder**, which is what makes
	// the page narrower than what this app may do.
	vs, err := d.ungated.Delegation().List(ctx, rstr.DelegationListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1)

	var body map[string]any
	x.NoError(json.NewDecoder(got.Body).Decode(&body))
	x.Equal("newcomer", body["alias"], "the page was drawn about somebody else")
}

// TestTheTenantComesFromRoster is item 1 doing its job in the app that used to
// hold the copy.
//
// `Config.Tenants` is a map in the configuration of every app that fronts a
// deployment, going stale in each independently. A `Host` row is the same fact
// written once, by whoever runs the deployment, where the tenant it names
// lives.
func TestTheTenantComesFromRoster(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// No map at all, which used to be refused.
	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, nil)

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	_, err = vouch.New(d.ungated, d.ungated).Set(ctx, rstr.VouchSetRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	post := func() *http.Response {
		t.Helper()

		body := strings.NewReader(`{"alias":"erin","password":"correct horse battery staple"}`)
		res, err := http.Post(d.app.URL+"/session", "application/json", body)
		x.NoError(err)
		t.Cleanup(func() { res.Body.Close() })

		return res
	}

	// Nothing has said which tenant serves this name yet, so there is nobody to
	// sign in as -- and the answer is the one a wrong password gets, because
	// which of the two it was is not a browser's to learn.
	x.Equal(http.StatusUnauthorized, post().StatusCode)

	// The row is the whole of the configuration.
	u, err := url.Parse(d.app.URL)
	x.NoError(err)

	_, err = d.ungated.Host().Add(ctx, rstr.HostAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Name:   front.Hostname(u.Host),
	}.Build())
	x.NoError(err)

	x.Equal(http.StatusNoContent, post().StatusCode,
		"the app could not learn from roster which tenant it is serving")
}

// TestTwoFormsAndTheAppRemembersNothing is D21's split, in the app it was
// written about.
//
// The question that found D21: **an app showing a second form has to remember
// who passed the first one.** An app developer wants to know who somebody is
// and does not want to be in the sign-in business at all, so making them carry
// that is handing them the one part of the process they were trying to avoid.
//
// So this app holds a cookie -- *which browser* is mid-sign-in, which is bound
// to a browser and therefore its own -- and roster holds the attempt. The
// browser's cookie names nobody it may act as until the second form is
// answered.
func TestTwoFormsAndTheAppRemembersNothing(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	v := vouch.New(d.ungated, d.ungated, vouch.WithKeys(d.keyring))

	_, err = v.Set(ctx, rstr.VouchSetRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	enrolled, err := v.Enrol(ctx, rstr.VouchEnrolRequest_builder{
		Who:  rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrolled.GetSeed())
	x.NoError(err)

	// Confirmed with the previous step's code, which leaves the current one
	// unspent for the sign-in below. A factor nobody has proved is deliberately
	// not offered.
	got, err := v.Verify(ctx, rstr.VouchVerifyRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30-1)),
	}.Build())
	x.NoError(err)
	x.True(got.GetOk())

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	first, err := c.Post(d.app.URL+"/session", "application/json",
		strings.NewReader(`{"alias":"erin","password":"correct horse battery staple"}`))
	x.NoError(err)
	defer first.Body.Close()

	t.Run("the first form is answered and is not a sign-in", func(t *testing.T) {
		x := require.New(t)

		x.Equal(http.StatusOK, first.StatusCode)

		var body struct {
			Satisfied []string `json:"satisfied"`
			Available []string `json:"available"`
		}
		x.NoError(json.NewDecoder(first.Body).Decode(&body))
		x.Equal([]string{"password"}, body.Satisfied)
		x.Equal([]string{"totp"}, body.Available)

		// The cookie names nobody it may act as, so the page behind it is shut.
		res, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusForbidden, res.StatusCode,
			"one factor drew a page it had not finished signing in for")
	})

	t.Run("and the second one finishes it", func(t *testing.T) {
		x := require.New(t)

		code := vouch.CodeAt(seed, time.Now().Unix()/30)

		done, err := c.Post(d.app.URL+"/session/continue", "application/json",
			strings.NewReader(`{"kind":"totp","secret":"`+code+`"}`))
		x.NoError(err)
		defer done.Body.Close()
		x.Equal(http.StatusNoContent, done.StatusCode)

		res, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode)
	})

	// And roster is left holding nothing about the attempt: spending it is an
	// erase, which is the whole of what single-use means there.
	t.Run("and the attempt is gone", func(t *testing.T) {
		x := require.New(t)

		n, err := d.ungated.Continuation().List(ctx, rstr.ContinuationListRequest_builder{}.Build())
		x.NoError(err)
		x.Empty(n.GetItems())
	})
}

// TestAWrongSecondFactorCostsTheFirstFormAgain.
//
// The half-session is ended with the attempt, so somebody who gets the code
// wrong starts over -- and starting over is where the lockout counts, which is
// what makes the second factor a metered surface rather than an unmetered one.
func TestAWrongSecondFactorCostsTheFirstFormAgain(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	v := vouch.New(d.ungated, d.ungated, vouch.WithKeys(d.keyring))

	_, err = v.Set(ctx, rstr.VouchSetRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	enrolled, err := v.Enrol(ctx, rstr.VouchEnrolRequest_builder{
		Who:  rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrolled.GetSeed())
	x.NoError(err)

	_, err = v.Verify(ctx, rstr.VouchVerifyRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30-1)),
	}.Build())
	x.NoError(err)

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	res, err := c.Post(d.app.URL+"/session", "application/json",
		strings.NewReader(`{"alias":"erin","password":"correct horse battery staple"}`))
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	wrong, err := c.Post(d.app.URL+"/session/continue", "application/json",
		strings.NewReader(`{"kind":"totp","secret":"000000"}`))
	x.NoError(err)
	wrong.Body.Close()
	x.Equal(http.StatusUnauthorized, wrong.StatusCode)

	// The half-session went with it, so a second try at the second form has
	// nothing to try against.
	again, err := c.Post(d.app.URL+"/session/continue", "application/json",
		strings.NewReader(`{"kind":"totp","secret":"`+vouch.CodeAt(seed, time.Now().Unix()/30)+`"}`))
	x.NoError(err)
	again.Body.Close()
	x.Equal(http.StatusUnauthorized, again.StatusCode,
		"a browser kept guessing the second factor without paying for the first")
}

// record is the shape the account page reads, mirrored here rather than
// exported: what the app answers with is its own, and a test that shared the
// type would stop noticing when it changed.
type record struct {
	Alias   string   `json:"alias"`
	Name    string   `json:"name"`
	Emails  []string `json:"emails"`
	SignsIn []struct {
		Kind  string `json:"kind"`
		Id    string `json:"id"`
		Which string `json:"which"`
	} `json:"signs_in"`
	MayCall []string `json:"may_call"`
	Keys    []struct {
		Id      string   `json:"id"`
		Alias   string   `json:"alias"`
		Methods []string `json:"methods"`
		Used    string   `json:"used"`
	} `json:"keys"`
}

// TestTheAccountScreenIsServed is D24 §4 reachable, and it is the whole of what
// a test can say about a page.
//
// What it draws is the three calls beside it, and those have tests of their own:
// the record, the unlink, and signing out everywhere. This says the screen
// exists, says nothing about it that a browser would have to agree with, and
// leaves the rest where it can be asserted.
func TestTheAccountScreenIsServed(t *testing.T) {
	x := require.New(t)

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	res, err := http.Get(d.app.URL + "/account")
	x.NoError(err)
	defer res.Body.Close()

	x.Equal(http.StatusOK, res.StatusCode)
	x.Contains(res.Header.Get("content-type"), "text/html")

	// A copy in a proxy is a copy of one person's record served to the next.
	x.Equal("no-store", res.Header.Get("cache-control"))

	b, err := io.ReadAll(res.Body)
	x.NoError(err)
	x.Contains(string(b), "how you sign in")
}

// TestSomebodyRemovesAWayInFromTheirOwnPage is the act the screen's one button
// makes, end to end.
func TestSomebodyRemovesAWayInFromTheirOwnPage(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		Provider: "example",
		Subject:  "1078",
	}.Build())
	x.NoError(err)

	_, err = vouch.New(d.ungated, d.ungated).Set(ctx, rstr.VouchSetRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	res, err := c.Post(d.app.URL+"/session", "application/json",
		strings.NewReader(`{"alias":"erin","password":"correct horse battery staple"}`))
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode)

	read := func() record {
		t.Helper()

		got, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer got.Body.Close()

		var v record
		x.NoError(json.NewDecoder(got.Body).Decode(&v))

		return v
	}

	v := read()
	x.Len(v.SignsIn, 2)

	id := ""
	for _, w := range v.SignsIn {
		if w.Id != "" {
			id = w.Id
		}
	}
	x.NotEmpty(id)

	req, err := http.NewRequest(http.MethodDelete, d.app.URL+"/me/ways/"+id, nil)
	x.NoError(err)

	out, err := c.Do(req)
	x.NoError(err)
	out.Body.Close()
	x.Equal(http.StatusNoContent, out.StatusCode)

	x.Len(read().SignsIn, 1, "the way in did not go")

	// And the password is what is left, so the rule has something to refuse
	// next -- which is the state a person reaches by removing things until
	// there is one.
	x.Equal("password", read().SignsIn[0].Kind)
}

// TestSigningOutEverywhereEndsBothHalves is the third button on that screen,
// and the only one nothing was pinning.
//
// It is the one act here that is written in two places at once. roster is told
// *everything issued before this moment is void*, which is one column and
// reaches every app and every other browser; this app ends the cookie it minted,
// which roster does not know exists. Neither half can be seen from where the
// other is written, so either can stop working in silence -- and what that
// silence is, both ways round, is somebody who pressed "sign out everywhere"
// and is still signed in. If roster's half went missing the laptop they pressed
// it about would still be reading their record; if this app's half went missing
// the browser in their hand would be.
//
// The two acts beside it -- reading the record and removing a way in -- have had
// an end-to-end test each since the screen was written, and the comment above
// [TestTheAccountScreenIsServed] says all three do. This is the third one.
func TestSigningOutEverywhereEndsBothHalves(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	_, err = vouch.New(d.ungated, d.ungated).Set(ctx, rstr.VouchSetRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	// A browser that has signed in, as a browser: its own jar, so that two of
	// them are two people as far as everything below is concerned.
	signIn := func() *http.Client {
		t.Helper()

		jar, err := cookiejar.New(nil)
		x.NoError(err)

		c := &http.Client{Jar: jar}

		res, err := c.Post(d.app.URL+"/session", "application/json",
			strings.NewReader(`{"alias":"erin","password":"correct horse battery staple"}`))
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusNoContent, res.StatusCode)

		return c
	}

	// Whether this browser can still draw the page, which is the only thing a
	// browser can observe about any of this.
	reads := func(c *http.Client) int {
		t.Helper()

		res, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer res.Body.Close()

		return res.StatusCode
	}

	epoch := func() *rstr.Holder {
		t.Helper()

		v, err := d.ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
			Ref:    rstr.HolderRef_builder{Id: h.GetId()}.Build(),
			Select: rstr.HolderSelect_builder{DateInvalidated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)

		return v
	}

	// Before anybody has signed in, because this is the press that must not
	// reach roster at all.
	//
	// `acting` is the whole of what makes this the person's own act. Without it
	// the call still goes out -- on the connection this app authenticates with,
	// whose actor is the login app's own Holder -- and `SignOutEverywhere` takes
	// no subject, so what it would write is an epoch on the front door itself,
	// from an unauthenticated POST anybody on the internet can send. That is a
	// refusal worth an assertion rather than an obvious one: the handler reads
	// as if the credential were the person's, and it only is because one line
	// above it made it so.
	t.Run("a browser that never signed in cannot press it", func(t *testing.T) {
		x := require.New(t)

		res, err := http.Post(d.app.URL+"/me/sign-out-everywhere", "application/json", nil)
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusForbidden, res.StatusCode)

		v, err := d.ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
			Ref: rstr.HolderRef_builder{
				Slug: rstr.HolderRefBySlug_builder{
					Alias:  proto.String("login-app"),
					Tenant: rstr.TenantRef_builder{Alias: proto.String("acme")}.Build(),
				}.Build(),
			}.Build(),
			Select: rstr.HolderSelect_builder{DateInvalidated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Nil(v.GetDateInvalidated(),
			"an anonymous POST wrote an epoch on the app's own holder")
	})

	// Two browsers, because the half that reaches the other one cannot be seen
	// from the browser that pressed the button: this app drops what it holds for
	// **this** browser whatever roster answered, so a `SignOutEverywhere` that
	// never left the process would look exactly like a working one from here.
	laptop := signIn()
	phone := signIn()

	x.Equal(http.StatusOK, reads(laptop))
	x.Equal(http.StatusOK, reads(phone))
	x.Nil(epoch().GetDateInvalidated(), "nothing has been signed out of yet")

	both, err := d.ungated.Delegation().List(ctx, rstr.DelegationListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(both.GetItems(), 2, "two browsers, two credentials")

	res, err := laptop.Post(d.app.URL+"/me/sign-out-everywhere", "application/json", nil)
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode)

	t.Run("this browser's own session is over", func(t *testing.T) {
		x := require.New(t)

		x.Equal(http.StatusForbidden, reads(laptop),
			"the browser that pressed it is still signed in to this app")

		// And the browser was told, rather than merely being forgotten here. A
		// session dropped on the server and left in the jar is somebody who
		// looks signed in to every page that draws a name before it makes a
		// call, until the first call fails -- which on this screen is after the
		// button they pressed appeared to do nothing.
		var ended bool
		for _, c := range res.Cookies() {
			if c.MaxAge < 0 || c.Value == "" {
				ended = true
			}
		}
		x.True(ended, "the cookie was dropped here and left in the browser")

		// What is deliberately not asserted here is the count of rows in
		// roster's table. `SignOut` revokes the delegation it was holding, and
		// `Revoke` finds one through the same lookup the epoch was just written
		// in front of -- so it finds nothing and removes nothing, and both rows
		// are left for `Sweep` to collect when their own hour runs out. Neither
		// is reachable in the meantime, for exactly that reason, which is why
		// this is a note and not a defect: a count that moved would be a nicer
		// table and not a different answer to any question a browser can ask.
	})

	t.Run("and so is the one on the other device", func(t *testing.T) {
		x := require.New(t)

		// The epoch is the only thing that could have reached it. Nothing told
		// this app about the phone -- its delegation is still in the map here
		// and its row is still in roster's table -- so what stops the call is
		// roster reading *issued before the moment this holder was invalidated*
		// as it resolves the credential.
		x.NotNil(epoch().GetDateInvalidated(), "roster was never told")

		// 502 and not 403, which is worth being exact about: this app cannot
		// know the string it is holding has gone dead, so it asks with it and
		// roster refuses. A page that answered 403 here would be one that had
		// checked something locally -- and anything local is a copy of an
		// answer only roster has.
		x.Equal(http.StatusBadGateway, reads(phone),
			"the other browser kept reading the record it was signed out of")
	})

	t.Run("and it is a moment rather than a lock", func(t *testing.T) {
		x := require.New(t)

		// Which is the difference between signing out and being suspended, and
		// it is only visible from here: `date_invalidated` voids what was issued
		// before it and says nothing about what comes after, so the person can
		// sign straight back in. An implementation that read the column as "this
		// account is closed" would pass every assertion above and lock somebody
		// out of their own account for pressing a button that says sign out.
		x.Equal(http.StatusOK, reads(signIn()))
	})
}

// TestAddingAWayInIsRoutedNow is §4's undrawn half, reaching roster.
//
// The roadmap named it and nothing routed it: *a person removes one and signs
// out everywhere, and adding one is the sign-in flow reached by somebody
// already signed in, which the reference app does not route.*
//
// It is the same redirect. What differs is which cookie goes with it and what
// the callback does at the end. This app has **one** provider, so what it can
// show is the routing and the refusal a person meets when they try to add the
// provider they already use -- one per person, which `server/core` refuses
// because *a second one is a link that found the wrong row*. The success path
// wants a second provider and is asserted where roster is, in
// `cmd/melink_test.go`.
func TestAddingAWayInIsRoutedNow(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(c rstr.Client) sso.Enrol { return sso.Enrolling(c) }, map[string]string{"127.0.0.1": "acme"})
	d.idp.subject = "3001"
	d.idp.claims = map[string]any{"email": "adder@acme.example", "email_verified": true}

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	res, err := c.Get(d.app.URL + "/login")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	was, err := d.ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(was.GetItems(), 1, "signing in wrote something other than one identity")

	t.Run("and until a role says so, it is refused", func(t *testing.T) {
		x := require.New(t)

		// `MeService.Link` is not waived, so somebody the login app enrolled
		// holds nothing that allows it -- and the app does not grant it, on
		// purpose: doing so would mean its key could bind any role to anybody.
		d.idp.subject = "3002"

		res, err := c.Post(d.app.URL+"/me/ways", "application/json", nil)
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusForbidden, res.StatusCode)
	})

	// What a deployment does about it: one role, one method, written where it
	// decides what an ordinary account is.
	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant:  rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:   "self-service",
		Methods: []string{rstr.MeService_Link_FullMethodName},
	}.Build())
	x.NoError(err)

	people, err := d.ungated.Holder().List(ctx, rstr.HolderListRequest_builder{}.Build())
	x.NoError(err)
	for _, v := range people.GetItems() {
		_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: rstr.HolderRef_builder{Id: v.GetId()}.Build(),
		}.Build())
		x.NoError(err)
	}

	// A fresh browser, so the delegation carries the role that now exists.
	jar2, err := cookiejar.New(nil)
	x.NoError(err)

	c = &http.Client{Jar: jar2}

	d.idp.subject = "3001"

	back, err := c.Get(d.app.URL + "/login")
	x.NoError(err)
	defer back.Body.Close()
	x.Equal(http.StatusOK, back.StatusCode)

	// The same provider answering as a different account, which is somebody
	// trying to add a second way in at the one they already use.
	d.idp.subject = "3002"

	add, err := c.Post(d.app.URL+"/me/ways", "application/json", nil)
	x.NoError(err)
	defer add.Body.Close()

	// A page a person can act on rather than a five-hundred: it means they are
	// already signed in with this provider. The errand reached roster, which is
	// what this asserts -- before it, nothing routed at all.
	x.Equal(http.StatusConflict, add.StatusCode)

	now, err := d.ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(now.GetItems(), 1, "a refused link wrote a row anyway")
}

// TestAddingAWayInNeedsASessionFirst.
//
// The order is the whole of the safety. An errand that attached an account to
// whoever's browser landed on the callback would look exactly like this flow
// while being a way into somebody else's account -- so the session is read at
// the start, and again at the end, and the one at the end is the one that
// decides.
func TestAddingAWayInNeedsASessionFirst(t *testing.T) {
	x := require.New(t)

	d := serve(t, func(c rstr.Client) sso.Enrol { return sso.Enrolling(c) }, map[string]string{"127.0.0.1": "acme"})

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	res, err := c.Post(d.app.URL+"/me/ways", "application/json", nil)
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusForbidden, res.StatusCode,
		"a browser with no session started an errand that attaches an account to somebody")
}

// TestSomebodyMintsAKeyFromTheirOwnPage, which `docs/OPERATING.md` listed under
// *what is not here* for as long as the operator's version existed.
//
// The operator's is the console: it lists somebody's keys beside their
// passwords and providers, mints one, revokes one. This is the same three acts
// with no subject anywhere in them, which is what lets a deployment offer it
// without handing somebody a role that reaches everybody in their tenant.
func TestSomebodyMintsAKeyFromTheirOwnPage(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, func(rstr.Client) sso.Enrol { return sso.Invited() }, map[string]string{"127.0.0.1": "acme"})

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	// What she may do, which is what she may put on a key -- `server/core`
	// refuses a list wider than the person writing it, and that rule is the
	// whole of what makes a self-service mint button safe.
	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "hers",
		Methods: []string{
			rstr.MeService_Get_FullMethodName,
			rstr.MeService_IssueKey_FullMethodName,
			rstr.MeService_RevokeKey_FullMethodName,
		},
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	_, err = vouch.New(d.ungated, d.ungated).Set(ctx, rstr.VouchSetRequest_builder{
		Who:    rstr.VouchWho_builder{Id: h.GetId()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	c := &http.Client{Jar: jar}

	res, err := c.Post(d.app.URL+"/session", "application/json",
		strings.NewReader(`{"alias":"erin","password":"correct horse battery staple"}`))
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode)

	mint := func(body string) *http.Response {
		t.Helper()

		res, err := c.Post(d.app.URL+"/me/keys", "application/json", strings.NewReader(body))
		x.NoError(err)
		t.Cleanup(func() { res.Body.Close() })

		return res
	}

	out := mint(`{"alias":"the-nightly-job","methods":["/roster.MeService/Get"]}`)
	x.Equal(http.StatusOK, out.StatusCode)

	var minted struct {
		Token string `json:"token"`
		Key   struct {
			Id string `json:"id"`
		} `json:"key"`
	}
	x.NoError(json.NewDecoder(out.Body).Decode(&minted))

	// An `rt_` and not an `rk_`: which kind a key is is a fact about which
	// server answered rather than a field, and this one is the data plane.
	x.True(strings.HasPrefix(minted.Token, "rt_"), "the token was %q", minted.Token)

	t.Run("and her own page lists it, without the secret", func(t *testing.T) {
		x := require.New(t)

		got, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer got.Body.Close()

		body, err := io.ReadAll(got.Body)
		x.NoError(err)

		var v record
		x.NoError(json.Unmarshal(body, &v))
		x.Len(v.Keys, 1)
		x.Equal("the-nightly-job", v.Keys[0].Alias)
		x.Equal([]string{"/roster.MeService/Get"}, v.Keys[0].Methods)
		x.Empty(v.Keys[0].Used, "a key nothing has presented was shown as used")

		// A key is readable exactly once. What is stored is a hash, so there is
		// nowhere a second look could come from -- and this asserts the page
		// does not keep one of its own.
		x.NotContains(string(body), minted.Token)
	})

	t.Run("and nothing wider than she is", func(t *testing.T) {
		x := require.New(t)

		out := mint(`{"alias":"reaching","methods":["/roster.HolderService/Erase"]}`)
		x.Equal(http.StatusForbidden, out.StatusCode,
			"a self-service page handed out a permission its person does not hold")
	})

	t.Run("and she can revoke it", func(t *testing.T) {
		x := require.New(t)

		req, err := http.NewRequest(http.MethodDelete, d.app.URL+"/me/keys/"+minted.Key.Id, nil)
		x.NoError(err)

		out, err := c.Do(req)
		x.NoError(err)
		defer out.Body.Close()
		x.Equal(http.StatusNoContent, out.StatusCode)

		got, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		defer got.Body.Close()

		var v record
		x.NoError(json.NewDecoder(got.Body).Decode(&v))
		x.Empty(v.Keys)
	})
}
