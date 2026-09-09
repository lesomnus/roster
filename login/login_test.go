package login_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

const password = "correct horse battery staple"

// hydra is as much of Hydra as this app talks to: four endpoints, and a record
// of what it was told.
//
// A fake rather than the real thing because what these tests are about is the
// **flow** -- which operator a challenge resolves to, which key its calls go
// out with, and what ends up in the token -- and none of that is decided by
// Hydra. What Hydra decides is the protocol around it, and `compose.yaml` runs
// the real one for that.
type hydra struct {
	*httptest.Server

	mu       sync.Mutex
	client   map[string]string // challenge -> the client it was raised for
	subject  string            // what `acceptLoginRequest` was told
	claims   map[string]any    // what `acceptConsentRequest` was told
	accepted int
}

func newHydra(t *testing.T) *hydra {
	h := &hydra{client: map[string]string{}}

	m := http.NewServeMux()
	m.HandleFunc("GET /admin/oauth2/auth/requests/login", func(w http.ResponseWriter, r *http.Request) {
		h.raised(w, r, "login_challenge")
	})
	m.HandleFunc("GET /admin/oauth2/auth/requests/consent", func(w http.ResponseWriter, r *http.Request) {
		h.raised(w, r, "consent_challenge")
	})
	m.HandleFunc("PUT /admin/oauth2/auth/requests/login/accept", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Subject string `json:"subject"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		h.mu.Lock()
		h.subject, h.accepted = body.Subject, h.accepted+1
		h.mu.Unlock()

		writeJson(w, map[string]string{"redirect_to": "/consent?consent_challenge=" + r.URL.Query().Get("login_challenge")})
	})
	m.HandleFunc("PUT /admin/oauth2/auth/requests/consent/accept", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Session struct {
				IdToken map[string]any `json:"id_token"`
			} `json:"session"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		h.mu.Lock()
		h.claims = body.Session.IdToken
		h.mu.Unlock()

		writeJson(w, map[string]string{"redirect_to": "/done"})
	})

	h.Server = httptest.NewServer(m)
	t.Cleanup(h.Close)

	return h
}

func (h *hydra) raised(w http.ResponseWriter, r *http.Request, param string) {
	c := r.URL.Query().Get(param)

	h.mu.Lock()
	client, ok := h.client[c]
	h.mu.Unlock()

	if !ok {
		http.Error(w, `{"error":"no such challenge"}`, http.StatusNotFound)

		return
	}

	writeJson(w, map[string]any{
		"challenge":       c,
		"client":          map[string]string{"client_id": client},
		"requested_scope": []string{"openid", "profile", "email"},
	})
}

// raise is a browser arriving at a client's `/login`, as far as this app sees.
func (h *hydra) raise(challenge, client string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.client[challenge] = client
}

func (h *hydra) told() (string, map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.subject, h.claims
}

func writeJson(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// deployment is a roster with two operators and the Login App in front of both.
type deployment struct {
	s     *cmd.Server
	hydra *hydra
	app   *httptest.Server

	who map[string]pdid.Id // alias -> the person in that operator's tenant
}

func serve(t *testing.T) *deployment {
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
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))

	_, err = cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops"})
	x.NoError(err)

	d := &deployment{s: s, hydra: newHydra(t), who: map[string]pdid.Id{}}
	operators := map[string]login.Operator{}

	// Two operators, each with a person who has a password and a key for this
	// app -- one per tenant, on a holder inside it.
	for _, alias := range []string{"contoso", "fabrikam"} {
		var tn *rstr.Tenant
		if alias == "contoso" {
			tn, err = s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
				Ref: rstr.TenantRef_builder{Alias: proto.String(alias)}.Build(),
			}.Build())
		} else {
			tn, err = s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: alias}.Build())
		}
		x.NoError(err)
		at := rstr.TenantRef_builder{Id: tn.GetId()}.Build()

		// Somebody to sign in. The **same password in both**, on purpose: two
		// operators' people reuse one, and what must not follow is that a flow
		// for one of them reaches the other.
		who, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: at, Alias: "erin", Name: "Erin of " + alias,
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
			Ref: rstr.HolderRef_builder{Id: who.GetId()}.Build(), Secret: []byte(password),
		}.Build())
		x.NoError(err)
		id, err := pdid.From(who.GetId())
		x.NoError(err)
		d.who[alias] = id

		// And the one method a delegation this app mints is for, so the
		// intersection is not empty.
		role, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
			Tenant: at, Alias: "person", Methods: login.Methods,
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: rstr.HolderRef_builder{Id: who.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		// The app, as a holder in this tenant, with a key that acts as it.
		front, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "login-app"}.Build())
		x.NoError(err)
		frontRole, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
			Tenant: at, Alias: "front-door",
			Methods: append([]string{
				"/roster.TenantService/Get",
				"/roster.VouchService/Verify",
				"/roster.VouchService/Delegate",
				"/roster.DelegationService/Revoke",
			}, login.Methods...),
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role:   rstr.RoleRef_builder{Id: frontRole.GetId()}.Build(),
			Holder: rstr.HolderRef_builder{Id: front.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		token, sum, err := keys.Mint(keys.PrefixTenant)
		x.NoError(err)
		_, err = s.Ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: front.GetId()}.Build(), Alias: "login-app", Secret: sum,
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)

		operators[alias] = login.Operator{Key: token, Client: alias + "-web"}
	}

	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop() })

	seal := make([]byte, authsession.KeySize)
	_, err = rand.Read(seal)
	x.NoError(err)
	sealed, err := authsession.NewSealed(seal)
	x.NoError(err)

	a, err := login.New(ctx, login.Config{
		Roster:         l.Addr().String(),
		Insecure:       true,
		Hydra:          d.hydra.URL,
		Sessions:       authsession.New(sealed, authsession.Insecure()),
		Operators:      operators,
		InsecureCookie: true,
	})
	x.NoError(err)
	t.Cleanup(func() { a.Close() })

	d.app = httptest.NewServer(a.Handler())
	t.Cleanup(d.app.Close)

	return d
}

// browser is one, with a jar and no redirects followed: the redirects are the
// assertions.
func (d *deployment) browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// signIn is the page's two calls: the form, then the last hop.
func (d *deployment) signIn(t *testing.T, b *http.Client, who, secret string) (string, int) {
	t.Helper()
	x := require.New(t)

	body, err := json.Marshal(map[string]string{"alias": who, "password": secret})
	x.NoError(err)
	res, err := b.Post(d.app.URL+"/session", "application/json", strings.NewReader(string(body)))
	x.NoError(err)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return "", res.StatusCode
	}

	res, err = b.Post(d.app.URL+"/accept", "", nil)
	x.NoError(err)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", res.StatusCode
	}

	var to struct {
		To string `json:"redirect_to"`
	}
	x.NoError(json.NewDecoder(res.Body).Decode(&to))

	return to.To, res.StatusCode
}

// TestALoginAppTellsHydraWhoSignedIn is the whole of it, once, end to end.
//
// A browser arrives with a challenge, types a password, and what Hydra is told
// is a `Holder.id` -- which is the claim `docs/position.md` makes and the one
// nothing in this repository stood behind until now.
func TestALoginAppTellsHydraWhoSignedIn(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	b := d.browser(t)

	d.hydra.raise("c1", "contoso-web")

	// The redirect from Hydra: a form, and a flow this browser is now in.
	res, err := b.Get(d.app.URL + "/login?login_challenge=c1")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	to, code := d.signIn(t, b, "erin", password)
	x.Equal(http.StatusOK, code)
	x.Contains(to, "consent_challenge=c1")

	subject, _ := d.hydra.told()
	x.Equal(d.who["contoso"].String(), subject, "hydra was told the wrong subject")

	// The consent hop, which is the only place this app reads a person.
	res, err = b.Get(d.app.URL + "/consent?consent_challenge=c1")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusSeeOther, res.StatusCode)

	_, claims := d.hydra.told()
	x.Equal("erin", claims["preferred_username"])
	x.Equal("Erin of contoso", claims["name"])

	// No `methods`, ever: it is roster's answer about roster and a copy of it
	// in a token is a copy that goes stale without the product noticing.
	x.NotContains(claims, "methods")
}

// TestAFlowReachesOnlyItsOwnOperator, which is what one instance fronting
// several of them has to be held to.
//
// Both operators have an `erin` and both have the same password, because people
// reuse them. What must not follow is that the flow raised for contoso's client
// signs in fabrikam's person, or the other way round.
func TestAFlowReachesOnlyItsOwnOperator(t *testing.T) {
	x := require.New(t)
	d := serve(t)

	for _, tt := range []struct{ client, alias string }{
		{"contoso-web", "contoso"},
		{"fabrikam-web", "fabrikam"},
	} {
		t.Run(tt.client, func(t *testing.T) {
			x := require.New(t)
			b := d.browser(t)
			d.hydra.raise(tt.client, tt.client)

			res, err := b.Get(d.app.URL + "/login?login_challenge=" + tt.client)
			x.NoError(err)
			defer res.Body.Close()
			x.Equal(http.StatusOK, res.StatusCode)

			_, code := d.signIn(t, b, "erin", password)
			x.Equal(http.StatusOK, code)

			subject, _ := d.hydra.told()
			x.Equal(d.who[tt.alias].String(), subject,
				"a flow for %s signed in somebody else", tt.alias)
		})
	}

	// And a challenge for a client no operator holds is nobody's flow. It is
	// the deployment's mistake rather than the browser's, and the browser is
	// told so and nothing else.
	b := d.browser(t)
	d.hydra.raise("stray", "nobody-web")
	res, err := b.Get(d.app.URL + "/login?login_challenge=stray")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadGateway, res.StatusCode)
}
