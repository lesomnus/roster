package login_test

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
	"github.com/lesomnus/roster/server/vouch"
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

	mu        sync.Mutex
	client    map[string]string // challenge -> the client it was raised for
	subject   string            // what `acceptLoginRequest` was told
	claims    map[string]any    // what `acceptConsentRequest` was told
	accepted  int
	rejected  bool
	forgotten []string // the subjects hydra was told to forget
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
	m.HandleFunc("DELETE /admin/oauth2/auth/sessions/login", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.forgotten = append(h.forgotten, r.URL.Query().Get("subject"))
		h.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("PUT /admin/oauth2/auth/requests/consent/reject", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.rejected = true
		h.mu.Unlock()

		writeJson(w, map[string]string{"redirect_to": "/denied"})
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

func (h *hydra) forgot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]string(nil), h.forgotten...)
}

// times is how often hydra was told to forget one subject.
func (h *hydra) times(subject string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := 0
	for _, v := range h.forgotten {
		if v == subject {
			n++
		}
	}

	return n
}

func (h *hydra) said() (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.accepted, h.rejected
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
	a     *login.App

	// An operator, over the wire. `SyncService` publishes from an interceptor,
	// so a write through `Ungated` writes the row and tells nobody
	// (`cmd/sync_test.go` says so in as many words) -- and this test is about
	// what a stream carries.
	conn *grpc.ClientConn
	ops  context.Context

	who map[string]pdid.Id // alias -> the person in that operator's tenant
}

func serve(t *testing.T) *deployment { return serveWith(t, login.Skip) }

func serveWith(t *testing.T, how login.Consent) *deployment { return serveAs(t, how, nil) }

// serveAs is the deployment with one more say over its configuration, for the
// settings only one test is about -- the enrolment policy, so far.
func serveAs(t *testing.T, how login.Consent, with func(*login.Config)) *deployment {
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
	opsKey := ""

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
				"/roster.TenantService/Update",
				"/roster.VouchService/Verify",
				"/roster.VouchService/Delegate",
				"/roster.VouchService/Accept",
				"/roster.DelegationService/Revoke",
				"/roster.SyncService/Watch",
				"/roster.ConnectionService/Get",
				"/roster.ConnectionService/List",
				"/roster.IdentityService/Get",
				"/roster.IdentityService/Add",
				"/roster.EmailService/Get",
				"/roster.EmailService/Add",

				// What `enrol: enrolling` needs, and what the key `roster login
				// provision` mints deliberately does not hold. Here so that one
				// test can be about the policy rather than about the grant.
				"/roster.HolderService/Add",
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

		// Two clients for one operator, because a customer with two products
		// has two and one sign-in.
		operators[alias] = login.Operator{Key: token, Clients: []string{alias + "-web", alias + "-mobile"}}

		if alias != "contoso" {
			continue
		}

		// Somebody who may operate on contoso's people, for the pokes below.
		// Over the wire, because that is the only way a write is published.
		ops, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "poker"}.Build())
		x.NoError(err)
		everything, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
			Tenant: at, Alias: "poke-all", Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role:   rstr.RoleRef_builder{Id: everything.GetId()}.Build(),
			Holder: rstr.HolderRef_builder{Id: ops.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		var sum2 []byte
		opsKey, sum2, err = keys.Mint(keys.PrefixTenant)
		x.NoError(err)
		_, err = s.Ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: ops.GetId()}.Build(), Alias: "poker", Secret: sum2,
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)
	}

	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop() })

	d.conn, err = grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	t.Cleanup(func() { d.conn.Close() })
	d.ops = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+opsKey)

	seal := make([]byte, authsession.KeySize)
	_, err = rand.Read(seal)
	x.NoError(err)
	sealed, err := authsession.NewSealed(seal)
	x.NoError(err)

	cfg := login.Config{
		// The page, standing in for the build: what these tests are about is
		// the flow, and `ts/login/` is checked by the compiler and by
		// `scripts/e2e.sh`.
		Page:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("the form")) }),
		Consent:        how,
		Roster:         l.Addr().String(),
		Insecure:       true,
		Hydra:          d.hydra.URL,
		Sessions:       authsession.New(sealed, authsession.Insecure()),
		Operators:      operators,
		InsecureCookie: true,
	}
	if with != nil {
		with(&cfg)
	}

	a, err := login.New(ctx, cfg)
	x.NoError(err)
	t.Cleanup(func() { a.Close() })

	d.a = a
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
func (d *deployment) signIn(t *testing.T, b *http.Client, challenge, who, secret string) (string, int) {
	t.Helper()
	x := require.New(t)

	at := func(path string) string {
		return d.app.URL + path + "?login_challenge=" + url.QueryEscape(challenge)
	}

	body, err := json.Marshal(map[string]string{"alias": who, "password": secret})
	x.NoError(err)
	res, err := b.Post(at("/session"), "application/json", strings.NewReader(string(body)))
	x.NoError(err)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return "", res.StatusCode
	}

	res, err = b.Post(at("/accept"), "", nil)
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

	to, code := d.signIn(t, b, "c1", "erin", password)
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

			_, code := d.signIn(t, b, tt.client, "erin", password)
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

// TestASecondClientIsTheSameOperator, which is what an operator with two
// products has.
//
// One sign-in and two relying parties is the case Hydra is for at all, so a
// Login App that could only hold one client per customer would answer half of
// what it exists to answer.
func TestASecondClientIsTheSameOperator(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	b := d.browser(t)

	d.hydra.raise("m1", "contoso-mobile")

	res, err := b.Get(d.app.URL + "/login?login_challenge=m1")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	_, code := d.signIn(t, b, "m1", "erin", password)
	x.Equal(http.StatusOK, code)

	subject, _ := d.hydra.told()
	x.Equal(d.who["contoso"].String(), subject)
}

// TestTheConsentScreenIsDrawnWhenTheDeploymentAsksForOne is the other half of
// the decision `skip` makes silently today.
//
// Under `skip` the consent hop is a redirect and nothing is drawn, which is
// right for the clients this app can have -- every one of them was registered
// by this deployment for one of its own operators. Under `ask` it is a screen,
// and nothing is granted until somebody says so.
func TestTheConsentScreenIsDrawnWhenTheDeploymentAsksForOne(t *testing.T) {
	consent := func(t *testing.T, d *deployment, b *http.Client, challenge string) *http.Response {
		t.Helper()
		res, err := b.Get(d.app.URL + "/consent?consent_challenge=" + challenge)
		require.NoError(t, err)

		return res
	}

	t.Run("skip draws nothing and grants", func(t *testing.T) {
		x := require.New(t)
		d := serve(t)
		b := d.browser(t)
		d.hydra.raise("c1", "contoso-web")

		_, code := d.signIn(t, b, "c1", "erin", password)
		x.Equal(http.StatusOK, code)

		res := consent(t, d, b, "c1")
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)

		_, claims := d.hydra.told()
		x.Equal("erin", claims["preferred_username"])
	})

	t.Run("ask draws a screen and grants nothing yet", func(t *testing.T) {
		x := require.New(t)
		d := serveWith(t, login.Ask)
		b := d.browser(t)
		d.hydra.raise("c2", "contoso-web")

		_, code := d.signIn(t, b, "c2", "erin", password)
		x.Equal(http.StatusOK, code)

		res := consent(t, d, b, "c2")
		res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode, "a screen was asked for and a redirect came back")

		// What the screen says is what `/flow` answers: the page is one
		// document for both screens and reads which it is from its own URL, so
		// this is where *which app is asking* is decided.
		res, err := b.Get(d.app.URL + "/flow?consent_challenge=c2")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode)

		var asking struct {
			Brand  string   `json:"brand"`
			Client string   `json:"client"`
			Scope  []string `json:"scope"`
		}
		x.NoError(json.NewDecoder(res.Body).Decode(&asking))
		x.Equal("contoso-web", asking.Client, "the screen has nothing to say which app is asking")
		x.Contains(asking.Scope, "profile", "the screen has nothing to say what it is asking for")
		x.NotEmpty(asking.Brand, "the screen has nothing to call the operator")

		// **Nothing granted.** A screen that has been drawn and not answered
		// must leave the flow where it was, or the screen is decoration.
		_, claims := d.hydra.told()
		x.Nil(claims)
	})

	t.Run("and grants on a yes", func(t *testing.T) {
		x := require.New(t)
		d := serveWith(t, login.Ask)
		b := d.browser(t)
		d.hydra.raise("c3", "contoso-web")

		_, code := d.signIn(t, b, "c3", "erin", password)
		x.Equal(http.StatusOK, code)

		res := consent(t, d, b, "c3")
		res.Body.Close()

		res, err := b.PostForm(d.app.URL+"/consent", url.Values{
			"consent_challenge": {"c3"}, "allow": {"1"},
		})
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)

		_, claims := d.hydra.told()
		x.Equal("erin", claims["preferred_username"])
	})

	t.Run("and rejects on a no", func(t *testing.T) {
		x := require.New(t)
		d := serveWith(t, login.Ask)
		b := d.browser(t)
		d.hydra.raise("c4", "contoso-web")

		_, code := d.signIn(t, b, "c4", "erin", password)
		x.Equal(http.StatusOK, code)

		res := consent(t, d, b, "c4")
		res.Body.Close()

		res, err := b.PostForm(d.app.URL+"/consent", url.Values{"consent_challenge": {"c4"}})
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)

		_, rejected := d.hydra.said()
		x.True(rejected, "a no granted anyway")

		_, claims := d.hydra.told()
		x.Nil(claims)
	})
}

// TestSigningSomebodyOutEverywhereReachesHydra is the hole this closes, and it
// is a quiet one: without it an operator signs somebody out, roster's own
// credentials stop working, and Hydra goes on remembering them -- so the next
// product they open gets a fresh token with no form in between.
//
// roster does not know Hydra exists and this test does not change that. What it
// watches is `SyncService`, which says what has stopped being good about
// somebody in roster's own vocabulary; turning that into a `DELETE` is the
// Login App's, because the Login App is what knows about Hydra.
func TestSigningSomebodyOutEverywhereReachesHydra(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	ctx := t.Context()

	go func() { _ = d.a.Watch(ctx) }()

	// A moment for the streams to be open, or the event is one nothing was
	// listening for -- which is what this stream promises and does not replay.
	erin := d.who["contoso"]
	x.Eventually(func() bool {
		_, err := rstr.NewHolderServiceClient(d.conn).Invalidate(d.ops, rstr.HolderInvalidateRequest_builder{
			Ref: rstr.HolderRef_builder{Id: erin.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		return slices.Contains(d.hydra.forgot(), erin.String())
	}, 10*time.Second, 100*time.Millisecond, "hydra was never told to forget her")

	// And nobody else. A stream narrowed by the wall hears one tenant per key,
	// and an event about contoso's erin must not reach fabrikam's.
	for _, s := range d.hydra.forgot() {
		x.NotEqual(d.who["fabrikam"].String(), s, "an event about contoso reached fabrikam")
	}
}

// TestSomebodyBackInGoodStandingIsNotSignedOutAgain, which is what the stream
// carrying **state and not a delta** costs an app that does not think about it.
//
// `date_invalidated` is monotonic and never cleared, so every later event about
// somebody carries it still set. An app that revoked on "is it set" would sign
// somebody out the moment after they signed back in -- once per event, forever.
func TestSomebodyBackInGoodStandingIsNotSignedOutAgain(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	ctx := t.Context()

	go func() { _ = d.a.Watch(ctx) }()

	h := rstr.NewHolderServiceClient(d.conn)
	erin := d.who["contoso"]
	ref := rstr.HolderRef_builder{Id: erin.Bytes()}.Build()

	// Somebody else in the same tenant, to mark the stream with. Events arrive
	// in order on one stream, so once a mark has been acted on everything sent
	// before it has been too -- which is the difference between waiting for a
	// fact and sleeping for a guess. The first version of this test slept, and
	// was flaky on a loaded runner and nowhere else.
	other := addPerson(t, ctx, d, "marker")
	marks := 0
	mark := func() {
		t.Helper()
		_, err := h.Invalidate(d.ops, rstr.HolderInvalidateRequest_builder{
			Ref: rstr.HolderRef_builder{Id: other.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		marks++
		x.Eventually(func() bool { return d.hydra.times(other.String()) >= marks },
			10*time.Second, 20*time.Millisecond, "the stream stopped carrying anything")
	}

	// The stream replays nothing, so an event sent before it was listening is
	// an event nobody hears. Poked until one lands, which is also what says the
	// watcher is up.
	x.Eventually(func() bool {
		_, err := h.Invalidate(d.ops, rstr.HolderInvalidateRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		return d.hydra.times(erin.String()) > 0
	}, 10*time.Second, 100*time.Millisecond, "hydra was never told to forget her")

	mark()
	was := d.hydra.times(erin.String())

	// A suspension is its own reason to forget her, and the count moves once.
	_, err := h.Disable(d.ops, rstr.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	mark()
	after := d.hydra.times(erin.String())
	x.Greater(after, was, "a suspension did not reach hydra")

	// And back in good standing, which is **not** a third sign-out. The event
	// carries the same two timestamps as the one before it: still set, and not
	// newer than what was acted on.
	_, err = h.Enable(d.ops, rstr.HolderEnableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	mark()
	x.Equal(after, d.hydra.times(erin.String()), "she was signed out again for coming back")
}

// addPerson puts somebody in contoso, for a test that needs a second one.
func addPerson(t *testing.T, ctx context.Context, d *deployment, alias string) pdid.Id {
	t.Helper()

	tn, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: d.who["contoso"].Bytes()}.Build(),
		Select: rstr.HolderSelect_builder{Tenant: rstr.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	require.NoError(t, err)

	v, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: tn.GetTenant().GetId()}.Build(), Alias: alias,
	}.Build())
	require.NoError(t, err)

	id, err := pdid.From(v.GetId())
	require.NoError(t, err)

	return id
}

// enrolTotp gives somebody an authenticator and proves it once, which is what
// makes it count: roster writes the row with its step at zero and does not
// offer a factor at a sign-in until one `Verify` has passed.
func (d *deployment) enrolTotp(t *testing.T, ctx context.Context, who pdid.Id) []byte {
	t.Helper()
	x := require.New(t)

	res, err := d.s.Ungated.Credential().Enrol(ctx, rstr.CredentialEnrolRequest_builder{
		Ref:  rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(res.GetSeed())
	x.NoError(err)

	// The **previous** step, which roster takes inside its skew window. The
	// current one would be spent by this call, and a spent step does not work
	// twice -- so a sign-in a moment later, with the app showing the same six
	// digits, would be refused for the right reason at the wrong time.
	got, err := rstr.NewVouchServiceClient(d.conn).Verify(d.ops, rstr.VouchVerifyRequest_builder{
		Who:    rstr.VouchWho_builder{Id: who.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30-1)),
	}.Build())
	x.NoError(err)

	// `satisfied` and not `ok`: `ok` is *this sign-in is finished*, and one
	// factor of two never is. What says the code was right -- which is the
	// whole of what confirming a factor asks -- is that its kind is in there.
	x.Contains(got.GetSatisfied(), vouch.KindTotp, "the factor that was just enrolled did not verify")

	return seed
}

// post is one call in a flow, with the browser's cookie and the challenge.
func (d *deployment) post(t *testing.T, b *http.Client, path, challenge string, body any) *http.Response {
	t.Helper()
	x := require.New(t)

	at := d.app.URL + path + "?login_challenge=" + url.QueryEscape(challenge)
	if body == nil {
		res, err := b.Post(at, "", nil)
		x.NoError(err)

		return res
	}

	v, err := json.Marshal(body)
	x.NoError(err)
	res, err := b.Post(at, "application/json", strings.NewReader(string(v)))
	x.NoError(err)

	return res
}

// TestASecondFactorIsAskedForAndTheFlowWaitsForIt.
//
// The half-signed-in state is `frontdoor`'s and roster's between them -- a
// continuation held beside a session with an empty grant -- and what this adds
// is the one thing neither of them can decide: that **Hydra is not told
// anybody** until it is finished. A Login App that accepted after the first
// form would hand a product a token for somebody who proved half of what the
// deployment asked for, which is worse than having no second factor at all,
// because the operator believes they have one.
func TestASecondFactorIsAskedForAndTheFlowWaitsForIt(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	b := d.browser(t)
	ctx := t.Context()

	erin := d.who["contoso"]
	seed := d.enrolTotp(t, ctx, erin)
	d.hydra.raise("f1", "contoso-web")

	res, err := b.Get(d.app.URL + "/login?login_challenge=f1")
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	// The password alone, which is now half of it. Not a refusal and not a
	// sign-in: a third answer, and what it carries is what the page needs to
	// draw the second form.
	res = d.post(t, b, "/session", "f1", map[string]string{"alias": "erin", "password": password})
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode, "a password alone finished a sign-in with a second factor on it")

	var half struct {
		Satisfied []string `json:"satisfied"`
		Available []string `json:"available"`
	}
	x.NoError(json.NewDecoder(res.Body).Decode(&half))
	x.Equal([]string{vouch.KindPassword}, half.Satisfied)
	x.Equal([]string{vouch.KindTotp}, half.Available, "the page has nothing to draw the second form from")

	// And Hydra is told nobody. This is the assertion the whole test is for.
	res = d.post(t, b, "/accept", "f1", nil)
	res.Body.Close()
	x.Equal(http.StatusUnauthorized, res.StatusCode, "a half-signed-in browser was accepted")

	subject, _ := d.hydra.told()
	x.Empty(subject, "hydra was told somebody who had not finished")

	// The code, from the app in front of them.
	res = d.post(t, b, "/session/continue", "f1", map[string]string{
		"kind":   vouch.KindTotp,
		"secret": vouch.CodeAt(seed, time.Now().Unix()/30),
	})
	res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode, "the code was refused")

	// And now, and only now.
	res = d.post(t, b, "/accept", "f1", nil)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	subject, _ = d.hydra.told()
	x.Equal(erin.String(), subject)
}

// TestAWrongSecondFactorFinishesNothing, which is the other half of the
// sentence above: the flow does not merely wait, it refuses.
func TestAWrongSecondFactorFinishesNothing(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	b := d.browser(t)
	ctx := t.Context()

	d.enrolTotp(t, ctx, d.who["contoso"])
	d.hydra.raise("f2", "contoso-web")

	res, err := b.Get(d.app.URL + "/login?login_challenge=f2")
	x.NoError(err)
	res.Body.Close()

	res = d.post(t, b, "/session", "f2", map[string]string{"alias": "erin", "password": password})
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	res = d.post(t, b, "/session/continue", "f2", map[string]string{
		"kind": vouch.KindTotp, "secret": "000000",
	})
	res.Body.Close()
	x.Equal(http.StatusUnauthorized, res.StatusCode, "a wrong code finished a sign-in")

	res = d.post(t, b, "/accept", "f2", nil)
	res.Body.Close()
	x.Equal(http.StatusUnauthorized, res.StatusCode)

	subject, _ := d.hydra.told()
	x.Empty(subject, "hydra was told somebody who answered the second form wrong")
}
