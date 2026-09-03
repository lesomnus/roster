// Package account is roster's own front door: the app a customer's people sign
// in at and manage their own account through.
//
// It is the shippable half of what `examples/sso` teaches. The example is one
// operator, one provider from its configuration, and a page of hand-written
// JSON routes; this is many operators, their providers read from roster, and a
// browser that speaks Connect to this origin and is handed on to roster as the
// person. Nothing below the app changes between the two -- roster still answers
// *who is this subject* and nothing else -- and the example stays, because a
// thirty-line consumer is what keeps the interface honest.
//
// # It is a consumer, and only a consumer
//
// This package imports `rstr` (the generated clients), `frontdoor`, payday's
// `authsession`, and one function from `server/front` -- `Hostname`, exported
// there so that both sides normalise a name the same way and neither may
// disagree. Never a server **stack**, `internal/*` or `cmd/*`: it reaches
// roster over the wire with a credential an operator minted, and if it could
// reach past that the account app would be the second thing in this repository
// that can, which is one more than there should be (`ts/plan.md`, invariant 1).
//
// # One tenant key per operator it fronts
//
// A deployment key resolves to a frame with no tenant and the policy hands it
// `frame.Everything`; on an internet-facing app that is an actor reaching every
// operator's rows, kept out of the wrong ones by nothing but this code. A tenant
// key resolves to a holder inside a tenant and the wall does the narrowing. So
// [Config.Keys] is one `rt_` per tenant, by the tenant's alias, and every call
// this app makes about a host is made with the key of the tenant that host
// resolves to -- `cmd/accountkey_test.go` is the fact this rests on.
//
// # The providers are roster's rows, not this app's configuration
//
// Which provider a tenant's people arrive through is a `Connection`: issuer,
// client id, scopes, and *where the deployment keeps the secret* (`secret_ref`,
// `env:CONTOSO_ENTRA_SECRET`), a string roster stores and never reads. This app
// reads the rows with the tenant's key, resolves the reference ([Config.Secret]),
// and does the OIDC exchange -- being the relying party, which is what roster
// is not. An operator adds a provider in the console and this app offers it on
// the next sign-in page it draws; there is nothing to restart.
//
// # What is this deployment's to decide
//
// Exactly one thing, as in the example: a provider says *subject 1078… signed
// in* and roster has never heard of them. Whether a stranger with a valid
// account gets one here, and as whom, is [Enrol], and two are shipped.
package account

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/frontdoor"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// Config is what a deployment says.
type Config struct {
	// Roster is where the data plane speaks gRPC, for this app's own calls.
	Roster string

	// Connect is where the same server speaks Connect over HTTP -- the
	// `server.http` listener -- which is what the browser's calls are handed on
	// to (`frontdoor.Door.Proxy`).
	Connect *url.URL

	// Insecure dials `Roster` without TLS. A development setting.
	Insecure bool

	// Keys is one tenant key per operator this app fronts, by the tenant's
	// alias. Minted by an operator -- the console's *arrives through* panel, or
	// `roster key add --tenant contoso --holder account` -- for a holder in that
	// tenant whose role names what a front door calls (see [Methods]).
	Keys map[string]string

	// Base is this app's public origin, the one registered with every provider
	// as the redirect: `https://login.example.com`. Empty derives it from each
	// request, which suits one tenant behind one name and nothing else, since a
	// provider will only send a browser back to a URL it was told about.
	Base *url.URL

	// Secret turns a `Connection.secret_ref` into the client secret it names.
	// Nil is [EnvSecret].
	Secret func(ref string) (string, error)

	// Enrol is what happens to somebody a provider vouches for and roster has
	// never seen. Nil is [Invited]: nobody.
	Enrol Enrol

	// Sessions is this app's own cookie. Never roster's.
	Sessions *authsession.Sessions

	// Static is the page, or nil for a placeholder that says where the API is.
	Static http.Handler

	// Mail delivers what roster mints and does not deliver: a recovery link, a
	// verification link. `to` is the address on the row and never anything a
	// request said; `link` is the URL that finishes the flow at this app. Nil
	// is a deployment that cannot, and the two flows say so with 501 rather
	// than minting a link nobody will ever read.
	Mail func(ctx context.Context, to, subject, link string) error
}

// Methods is what the delegation this app mints for a person allows: exactly
// the calls the account screens make, each with the person's own reference,
// which the page passes and this app never takes from a request. A role the
// person holds still has to name each one -- a delegation narrows to the
// intersection -- so a deployment that does not want self-service key minting
// simply grants no role naming `ApiKey.Issue`.
var Methods = []string{
	rstr.MeService_Get_FullMethodName,
	rstr.MeService_Unlink_FullMethodName,
	rstr.MeService_SignOutEverywhere_FullMethodName,
	rstr.HolderService_Get_FullMethodName,
	rstr.HolderService_Update_FullMethodName,
	rstr.HolderService_RevokeKey_FullMethodName,
	rstr.IdentityService_Add_FullMethodName,
	rstr.EmailService_List_FullMethodName,
	rstr.EmailService_Get_FullMethodName,
	rstr.EmailService_Add_FullMethodName,
	rstr.EmailService_Erase_FullMethodName,
	rstr.EmailService_Verify_FullMethodName,
	rstr.ApiKeyService_Issue_FullMethodName,
	rstr.CredentialService_Set_FullMethodName,
	rstr.CredentialService_Enrol_FullMethodName,
	rstr.CredentialService_Erase_FullMethodName,
	rstr.DelegationService_List_FullMethodName,
	rstr.DelegationService_Erase_FullMethodName,
}

// EnvSecret resolves `env:NAME` and refuses every other scheme. A second scheme
// -- a file, a secrets manager -- is a deployment's to add through
// [Config.Secret]; roster's own vocabulary is this one.
func EnvSecret(ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, "env:")
	if !ok || name == "" {
		return "", fmt.Errorf("account: secret_ref %q: only env:NAME is understood here", ref)
	}
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("account: secret_ref %q: %s is not set", ref, name)
	}

	return v, nil
}

// ErrUnknownHost is a name this app serves nobody under.
var ErrUnknownHost = errors.New("account: no operator here serves this name")

// tenant is one operator this app fronts, resolved once at start.
type tenant struct {
	id    pdid.Id
	alias string
	key   string
}

// App is the front door.
type App struct {
	c      Config
	conn   *grpc.ClientConn
	roster rstr.Client
	front  rstr.FrontServiceClient
	vouch  rstr.VouchServiceClient
	door   *frontdoor.Door

	byId    map[pdid.Id]*tenant
	byAlias map[string]*tenant

	// hosts is which tenant a name resolved to, asked of roster once per name
	// and remembered. A negative answer is remembered too, briefly, so that a
	// crawler cannot make every request a round trip.
	hosts sync.Map // front.Hostname(host) -> hostAnswer

	// oidc is the discovery document per (tenant, connection), done once.
	oidc sync.Map // tenantId + "\x00" + name -> *oidc.Provider

	// flows is every sign-in or link started and not yet finished, by state.
	flows *flows
}

type hostAnswer struct {
	t     *tenant
	until time.Time
}

// New dials roster once and resolves each key to its tenant, so a key for
// contoso that cannot see contoso is refused at start rather than at somebody's
// first sign-in.
func New(ctx context.Context, c Config) (*App, error) {
	switch {
	case c.Roster == "":
		return nil, errors.New("account: Roster: where the data plane speaks gRPC")
	case c.Connect == nil:
		return nil, errors.New("account: Connect: where the same server speaks Connect over HTTP")
	case len(c.Keys) == 0:
		return nil, errors.New("account: Keys: one tenant key per operator this app fronts; none is nobody to front")
	case c.Sessions == nil:
		return nil, errors.New("account: Sessions: the cookie is the app's, so the app makes it")
	}
	if c.Secret == nil {
		c.Secret = EnvSecret
	}
	if c.Enrol == nil {
		c.Enrol = Invited()
	}

	// The credential of every call is whichever tenant's key the context
	// carries, put there by `resolve` from the host the request arrived under.
	// A call made with none goes out with none and is refused by roster, which
	// is the right answer for a request nobody resolved.
	creds := credentials.NewTLS(nil)
	if c.Insecure {
		creds = insecure.NewCredentials()
	}
	opts := append(auth.Inject(auth.ProviderFunc(func(ctx context.Context) context.Context {
		if k, ok := keyOf(ctx); ok {
			return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+k)
		}

		return ctx
	})), grpc.WithTransportCredentials(creds))

	conn, err := grpc.NewClient(c.Roster, opts...)
	if err != nil {
		return nil, fmt.Errorf("account: %s: %w", c.Roster, err)
	}

	a := &App{
		c:       c,
		conn:    conn,
		roster:  rstr.NewClient(conn),
		front:   rstr.NewFrontServiceClient(conn),
		vouch:   rstr.NewVouchServiceClient(conn),
		byId:    map[pdid.Id]*tenant{},
		byAlias: map[string]*tenant{},
		flows:   &flows{by: map[string]flow{}},
	}

	for alias, key := range c.Keys {
		v, err := a.roster.Tenant().Get(withKey(ctx, key), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: proto.String(alias)}.Build(),
		}.Build())
		if err != nil {
			conn.Close()

			return nil, fmt.Errorf("account: the key for %q cannot see %q: %w", alias, alias, err)
		}
		id, err := pdid.From(v.GetId())
		if err != nil {
			conn.Close()

			return nil, err
		}

		t := &tenant{id: id, alias: alias, key: key}
		a.byId[id] = t
		a.byAlias[alias] = t
	}

	a.door, err = frontdoor.New(frontdoor.Config{
		Sessions:   c.Sessions,
		Vouch:      rstr.NewVouchServiceClient(conn),
		Delegation: rstr.NewDelegationServiceClient(conn),
		Methods:    Methods,
		Tenant: func(ctx context.Context, host string) (string, error) {
			t, err := a.tenantOf(ctx, host)
			if err != nil {
				return "", frontdoor.ErrUnknownHost
			}

			return t.alias, nil
		},
	})
	if err != nil {
		conn.Close()

		return nil, err
	}

	return a, nil
}

// Close hangs up.
func (a *App) Close() error { return a.conn.Close() }

// Handler is the whole app on one mux.
//
// The two forms and the sign-out at `/session`; `/login` and `/callback` for a
// provider's round trip, `/ways` for the same round trip by somebody already
// signed in who is attaching a second account; `/providers` for what the
// sign-in page draws; every `/roster.*` call handed on as the person; and the
// page itself under everything else.
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()

	m.Handle("/session", a.door.Handler())
	m.Handle("/session/", a.door.Handler())
	m.HandleFunc("GET /providers", a.providers)
	m.HandleFunc("GET /login", a.login)
	m.HandleFunc("POST /ways", a.addWay)
	m.HandleFunc("GET /callback", a.callback)
	m.HandleFunc("POST /recover", a.recover)
	m.HandleFunc("GET /redeem", a.redeem)
	m.HandleFunc("POST /verify", a.verify)
	m.HandleFunc("GET /confirm", a.confirm)
	// Every `/roster.<Service>/<Method>` is the page speaking Connect to this
	// origin, handed on as the person; everything else is the page itself. One
	// handler for both because `ServeMux` matches a prefix only up to a slash,
	// and a service name has a dot where the slash would be.
	proxy := a.door.Proxy(a.c.Connect, a.bearer)
	page := a.c.Static
	if page == nil {
		page = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "roster account: no page is configured; the API is at /session, /providers and /roster.*", http.StatusNotFound)
		})
	}
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/roster.") {
			proxy.ServeHTTP(w, r)
			return
		}
		page.ServeHTTP(w, r)
	})

	return a.resolve(m)
}

// resolve puts the tenant a request is about -- and its key -- in the context,
// so that everything below makes its calls as that operator's front door.
//
// A name this app serves nobody under is not refused here: the page is still
// served, and it is the page that says so, from `/providers`.
func (a *App) resolve(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if t, err := a.tenantOf(ctx, r.Host); err == nil {
			ctx = withTenant(ctx, t)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tenantOf is which operator a name means, asked of roster once and remembered.
//
// Asked with **any** key this app holds: `FrontService.WhoseHost` reads through
// no wall and answers one identifier, so which tenant's key asks does not
// change the answer -- and before the answer there is no tenant to choose one
// by.
func (a *App) tenantOf(ctx context.Context, host string) (*tenant, error) {
	name := front.Hostname(host)
	if v, ok := a.hosts.Load(name); ok {
		h := v.(hostAnswer)
		if time.Now().Before(h.until) {
			if h.t == nil {
				return nil, ErrUnknownHost
			}

			return h.t, nil
		}
	}

	var any string
	for _, t := range a.byAlias {
		any = t.key

		break
	}

	res, err := a.front.WhoseHost(withKey(ctx, any), rstr.FrontWhoseHostRequest_builder{Host: name}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound || status.Code(err) == codes.InvalidArgument {
			a.hosts.Store(name, hostAnswer{until: time.Now().Add(10 * time.Second)})

			return nil, ErrUnknownHost
		}

		return nil, err
	}

	id, err := pdid.From(res.GetTenant())
	if err != nil {
		return nil, err
	}
	t, ok := a.byId[id]
	if !ok {
		// roster serves this name for an operator this app holds no key for:
		// nobody to act as, so the same answer as a name nobody serves.
		a.hosts.Store(name, hostAnswer{until: time.Now().Add(10 * time.Second)})

		return nil, ErrUnknownHost
	}
	a.hosts.Store(name, hostAnswer{t: t, until: time.Now().Add(time.Minute)})

	return t, nil
}

// bearer is the credential a proxied call goes out with: the key of the tenant
// this request resolved to.
func (a *App) bearer(ctx context.Context, host string) (string, error) {
	t, ok := tenantFrom(ctx)
	if !ok {
		return "", ErrUnknownHost
	}

	return t.key, nil
}

// providers is what the sign-in page draws: which operator this is, and the
// providers their people arrive through. Public fields only -- the issuer is
// where a browser is about to be sent anyway -- and never `secret_ref`.
func (a *App) providers(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantFrom(r.Context())
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}

	cs, err := a.connections(r.Context(), t)
	if err != nil {
		fmt.Fprintf(os.Stderr, "account: providers for %s: %v\n", t.alias, err)
		http.Error(w, "cannot read providers", http.StatusBadGateway)
		return
	}

	tn, err := a.roster.Tenant().Get(withKey(r.Context(), t.key), rstr.TenantGetRequest_builder{
		Ref:    rstr.TenantRef_builder{Id: t.id.Bytes()}.Build(),
		Select: rstr.TenantSelect_builder{Alias: proto.Bool(true), Name: proto.Bool(true), Labels: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		http.Error(w, "cannot read the operator", http.StatusBadGateway)
		return
	}

	type provider struct {
		Name   string `json:"name"`
		Issuer string `json:"issuer"`
	}
	out := struct {
		Tenant struct {
			Alias  string            `json:"alias"`
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"tenant"`
		Providers []provider `json:"providers"`
		Password  bool       `json:"password"`
	}{Password: true, Providers: []provider{}}
	out.Tenant.Alias = tn.GetAlias()
	out.Tenant.Name = tn.GetName()
	out.Tenant.Labels = tn.GetLabels()
	for _, c := range cs {
		out.Providers = append(out.Providers, provider{Name: c.GetName(), Issuer: c.GetIssuer()})
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// connections is the tenant's providers, read with the tenant's own key.
func (a *App) connections(ctx context.Context, t *tenant) ([]*rstr.Connection, error) {
	vs, err := a.roster.Connection().List(withKey(ctx, t.key), rstr.ConnectionListRequest_builder{
		Filters: []*rstr.ConnectionFilter{rstr.ConnectionFilter_builder{
			Tenant: rstr.TenantRef_builder{Id: t.id.Bytes()}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return nil, err
	}

	return vs.GetItems(), nil
}

// relying is this app as the relying party for one connection of one tenant:
// the discovery done, the secret resolved, the redirect fixed.
func (a *App) relying(ctx context.Context, t *tenant, name string, r *http.Request) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	c, err := a.roster.Connection().Get(withKey(ctx, t.key), rstr.ConnectionGetRequest_builder{
		Ref: rstr.ConnectionRef_builder{
			At: rstr.ConnectionRefByAt_builder{
				Tenant: rstr.TenantRef_builder{Id: t.id.Bytes()}.Build(),
				Name:   proto.String(name),
			}.Build(),
		}.Build(),
		Select: rstr.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, nil, err
	}

	k := t.id.String() + "\x00" + name
	var p *oidc.Provider
	if v, ok := a.oidc.Load(k); ok {
		p = v.(*oidc.Provider)
	} else {
		p, err = oidc.NewProvider(ctx, c.GetIssuer())
		if err != nil {
			return nil, nil, fmt.Errorf("discovery at %s: %w", c.GetIssuer(), err)
		}
		a.oidc.Store(k, p)
	}

	secret := ""
	if ref := c.GetSecretRef(); ref != "" {
		secret, err = a.c.Secret(ref)
		if err != nil {
			return nil, nil, err
		}
	}

	return &oauth2.Config{
		ClientID:     c.GetClientId(),
		ClientSecret: secret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.redirect(r),
		Scopes:       append([]string{oidc.ScopeOpenID}, c.GetScopes()...),
	}, p.Verifier(&oidc.Config{ClientID: c.GetClientId()}), nil
}

// redirect is where a provider sends the browser back: `Base` if the
// deployment named one, else this request's own origin.
func (a *App) redirect(r *http.Request) string {
	if a.c.Base != nil {
		return a.c.Base.JoinPath("/callback").String()
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	return scheme + "://" + r.Host + "/callback"
}

// A flow is one round trip to a provider, started and not yet finished.
//
// Held here rather than in the state parameter, because the callback arrives
// under `Base`'s name and not the tenant's, so the tenant has to be remembered
// and the state has to be a nonce and nothing else. The cookie binds the
// browser to it: a callback carrying a state this browser did not start is
// refused, whoever else's it was.
type flow struct {
	tenant     *tenant
	connection string
	link       bool
	who        pdid.Id // for a link: whose account is being added to
	expires    time.Time
}

type flows struct {
	mu sync.Mutex
	by map[string]flow
}

func (f *flows) put(state string, v flow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for k, w := range f.by {
		if now.After(w.expires) {
			delete(f.by, k)
		}
	}
	f.by[state] = v
}

func (f *flows) take(state string) (flow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.by[state]
	delete(f.by, state)
	if !ok || time.Now().After(v.expires) {
		return flow{}, false
	}

	return v, true
}

const stateCookie = "account_state"

// login starts a sign-in through one of the tenant's providers.
//
// `?connection=entra` names it; left out, a tenant with exactly one provider
// goes there, and one with several is asked, since guessing would send a
// person to a directory they are not in.
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	a.start(w, r, false, pdid.Nil)
}

// addWay is the same round trip by somebody already signed in, attaching what
// the provider proves to **their** account. The session says whose; the
// request never does.
func (a *App) addWay(w http.ResponseWriter, r *http.Request) {
	who, ok := a.door.Who(r.Context(), r)
	if !ok {
		http.Error(w, "no", http.StatusForbidden)
		return
	}
	a.start(w, r, true, who)
}

func (a *App) start(w http.ResponseWriter, r *http.Request, link bool, who pdid.Id) {
	ctx := r.Context()
	t, ok := tenantFrom(ctx)
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}

	name := r.URL.Query().Get("connection")
	if name == "" {
		cs, err := a.connections(ctx, t)
		if err != nil || len(cs) != 1 {
			http.Error(w, "connection: which provider", http.StatusBadRequest)
			return
		}
		name = cs[0].GetName()
	}

	cfg, _, err := a.relying(ctx, t, name, r)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			http.Error(w, "connection: no such provider here", http.StatusNotFound)
			return
		}
		fmt.Fprintf(os.Stderr, "account: %s/%s: %v\n", t.alias, name, err)
		http.Error(w, "cannot start", http.StatusInternalServerError)
		return
	}

	state, err := nonce()
	if err != nil {
		http.Error(w, "cannot start", http.StatusInternalServerError)
		return
	}
	a.flows.put(state, flow{tenant: t, connection: name, link: link, who: who, expires: time.Now().Add(10 * time.Minute)})

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, cfg.AuthCodeURL(state), http.StatusFound)
}

// callback finishes the round trip.
func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/", MaxAge: -1})

	state := r.URL.Query().Get("state")
	c, err := r.Cookie(stateCookie)
	if err != nil || state == "" || c.Value != state {
		// One answer for a missing cookie, a missing parameter and a
		// mismatch: saying which would tell whoever sent this browser how far
		// they got.
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	f, ok := a.flows.take(state)
	if !ok {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	cfg, verifier, err := a.relying(ctx, f.tenant, f.connection, r)
	if err != nil {
		http.Error(w, "cannot finish", http.StatusInternalServerError)
		return
	}

	who, err := claim(ctx, cfg, verifier, r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	who.Tenant = f.tenant.id
	who.TenantAlias = f.tenant.alias
	who.Provider = f.connection

	as := withKey(ctx, f.tenant.key)

	if f.link {
		_, err := a.roster.Identity().Add(as, rstr.IdentityAddRequest_builder{
			Holder:   rstr.HolderRef_builder{Id: f.who.Bytes()}.Build(),
			Provider: who.Provider,
			Subject:  who.Subject,
		}.Build())
		switch status.Code(err) {
		case codes.OK:
			http.Redirect(w, r, "/", http.StatusFound)
		case codes.AlreadyExists:
			http.Error(w, "that account is already a way in", http.StatusConflict)
		case codes.InvalidArgument:
			http.Error(w, "you already sign in with this provider", http.StatusConflict)
		default:
			fmt.Fprintf(os.Stderr, "account: link %s/%s: %v\n", who.Provider, who.Subject, err)
			http.Error(w, "cannot add", http.StatusInternalServerError)
		}

		return
	}

	// Somebody this tenant has never seen is the one decision that is not
	// roster's and not this package's: [Enrol].
	if err := a.known(as, who); err != nil {
		if errors.Is(err, ErrUninvited) {
			http.Error(w, "this account has not been invited", http.StatusForbidden)
			return
		}
		fmt.Fprintf(os.Stderr, "account: %s/%s: %v\n", who.Provider, who.Subject, err)
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	// `Accept` resolves the claim on roster's side and mints the delegation;
	// the session is this app's and says nothing about the provider.
	if err := a.door.Accept(as, w, f.tenant.id.String(), who.Provider, who.Subject); err != nil {
		fmt.Fprintf(os.Stderr, "account: accept %s/%s: %v\n", who.Provider, who.Subject, err)
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// known makes sure the claim names somebody here, enrolling them if the
// deployment's policy says so, and links the identity itself so a policy
// cannot forget to or do it a second way.
func (a *App) known(ctx context.Context, who Caller) error {
	_, err := a.roster.Identity().Get(ctx, rstr.IdentityGetRequest_builder{
		Ref: rstr.IdentityRef_builder{
			Subject: rstr.IdentityRefBySubject_builder{
				TenantId: who.Tenant.Bytes(),
				Provider: proto.String(who.Provider),
				Subject:  proto.String(who.Subject),
			}.Build(),
		}.Build(),
	}.Build())
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.NotFound:
	default:
		return err
	}

	id, err := a.c.Enrol(ctx, a.roster, who)
	if err != nil {
		return err
	}
	if _, err := a.roster.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: id.Bytes()}.Build(),
		Provider: who.Provider,
		Subject:  who.Subject,
	}.Build()); err != nil {
		return fmt.Errorf("link %s/%s: %w", who.Provider, who.Subject, err)
	}

	return nil
}

// claim is what the provider said, verified: the exchange and the token check
// that make this app the relying party.
func claim(ctx context.Context, cfg *oauth2.Config, verifier *oidc.IDTokenVerifier, code string) (Caller, error) {
	if code == "" {
		return Caller{}, errors.New("no code")
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return Caller{}, err
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return Caller{}, errors.New("no id_token")
	}
	id, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Caller{}, err
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if err := id.Claims(&claims); err != nil {
		return Caller{}, err
	}

	return Caller{Subject: id.Subject, Email: claims.Email, Verified: claims.Verified, Name: claims.Name}, nil
}

func nonce() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// The two things a request carries below `resolve`: the tenant it is about,
// and -- for the outgoing calls -- that tenant's key.

type tenantKey struct{}
type keyKey struct{}

func withTenant(ctx context.Context, t *tenant) context.Context {
	return withKey(context.WithValue(ctx, tenantKey{}, t), t.key)
}

func tenantFrom(ctx context.Context) (*tenant, bool) {
	t, ok := ctx.Value(tenantKey{}).(*tenant)

	return t, ok && t != nil
}

func withKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyKey{}, key)
}

func keyOf(ctx context.Context) (string, bool) {
	k, ok := ctx.Value(keyKey{}).(string)

	return k, ok && k != ""
}

// recover starts a recovery: a link mailed to the address a person names.
//
// roster answers the same for an address that is here and one that is not --
// a token either way -- and so does this: 202 whatever was typed, because a
// form a stranger can fill in must not be an oracle for who is here. What
// differs is whether a message goes out, and only the mailbox learns that.
//
// The link proves the mailbox and nothing more, and what it buys is decided in
// `redeem`: a new password, shown once, rather than a session -- so a mailbox
// read once is a password the person changes, not an account somebody holds.
func (a *App) recover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, ok := tenantFrom(ctx)
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}
	if a.c.Mail == nil {
		http.Error(w, "this deployment cannot send mail, so it cannot recover an account this way", http.StatusNotImplemented)
		return
	}

	var body struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Address == "" {
		http.Error(w, "address: where to send the link", http.StatusBadRequest)
		return
	}
	address := front.Address(body.Address)

	res, err := a.vouch.Link(withKey(ctx, t.key), rstr.VouchLinkRequest_builder{
		Who: rstr.VouchWho_builder{Tenant: t.alias, Address: address}.Build(),
	}.Build())
	if err != nil {
		fmt.Fprintf(os.Stderr, "account: recover at %s: %v\n", t.alias, err)
		http.Error(w, "cannot start", http.StatusInternalServerError)
		return
	}

	// roster answers a token whether or not the address is here, so that the
	// answer says nothing; this app, holding the tenant's key, may ask -- and
	// mails only an address that is somebody's, so that a form a stranger can
	// fill in is not a way to have this deployment send mail anywhere. The
	// browser is answered the same either way, and in the background, so the
	// timing says nothing either.
	link := a.finish(r, "/redeem", res.GetToken())
	go func() {
		ctx := context.WithoutCancel(ctx)
		_, err := a.roster.Email().Get(withKey(ctx, t.key), rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{
				At: rstr.EmailRefByAt_builder{TenantId: t.id.Bytes(), Address: proto.String(address)}.Build(),
			}.Build(),
		}.Build())
		if err != nil {
			return
		}
		if err := a.c.Mail(ctx, address, "Recover your account", link); err != nil {
			fmt.Fprintf(os.Stderr, "account: mail to %s: %v\n", address, err)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
}

// redeem finishes a recovery: the link proves the mailbox, and the person is
// handed a new password, once.
//
// Not a session. `Vouch.Redeem` would mint one, and this app asks it to, then
// uses it for nothing but the proof: what somebody who has lost their password
// needs is a password, and `Vouch.Reset` makes one -- and voids everything
// issued before it, which is what recovering from a takeover requires. Their
// own row asks for the current password on `Set` and there is none to give,
// which is exactly why this road exists.
func (a *App) redeem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	// The link names the tenant through the person it was minted for, and the
	// key that minted it is the one that redeems it -- so try each tenant's
	// key; a link minted under one answers under no other.
	for _, t := range a.byAlias {
		as := withKey(ctx, t.key)
		res, err := a.vouch.Redeem(as, rstr.VouchRedeemRequest_builder{
			Token:   token,
			Methods: []string{rstr.MeService_Get_FullMethodName},
		}.Build())
		if err != nil || !res.GetVerified().GetOk() {
			continue
		}

		reset, err := a.vouch.Reset(as, rstr.VouchResetRequest_builder{
			Who: rstr.VouchWho_builder{Id: res.GetVerified().GetHolder()}.Build(),
		}.Build())
		if err != nil {
			fmt.Fprintf(os.Stderr, "account: reset after redeem at %s: %v\n", t.alias, err)
			http.Error(w, "the link is good and the password could not be made; ask an operator", http.StatusInternalServerError)
			return
		}

		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		fmt.Fprintf(w, recovered, htmlEscape(reset.GetSecret()))
		return
	}

	http.Error(w, "this link is not one, or is no longer", http.StatusNotFound)
}

// verify mails a link that proves one of the signed-in person's addresses.
//
// `Email.Verify` is called as the person, through their delegation -- the same
// call the page could make itself -- and the reason it is a route here is
// delivery: the app mails, roster does not. The address is read off the row,
// never from the request.
func (a *App) verify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if a.c.Mail == nil {
		http.Error(w, "this deployment cannot send mail, so it cannot verify an address this way", http.StatusNotImplemented)
		return
	}
	as, err := a.door.Acting(ctx, r)
	if err != nil {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	t, ok := tenantFrom(ctx)
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}
	as = withKey(as, t.key)

	var body struct {
		Id string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	id, err := base64.RawURLEncoding.DecodeString(body.Id)
	if err != nil || len(id) != 16 {
		http.Error(w, "id: which address", http.StatusBadRequest)
		return
	}
	ref := rstr.EmailRef_builder{Id: id}.Build()

	// Read as the person, so that what they cannot see they cannot verify;
	// and the row's holder compared with the session's, because a role that
	// reads addresses reads the tenant's addresses and this button is about
	// their own. The link is then minted as this app -- the key that mints a
	// link is the key that confirms it, and the click comes from a mail
	// client with no session.
	who, _ := a.door.Who(ctx, r)
	row, err := a.roster.Email().Get(as, rstr.EmailGetRequest_builder{
		Ref:    ref,
		Select: rstr.EmailSelect_builder{Address: proto.Bool(true), Holder: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil || !bytesEqual(row.GetHolder().GetId(), who.Bytes()) {
		http.Error(w, "no", http.StatusNotFound)
		return
	}
	res, err := a.roster.Email().Verify(withKey(ctx, t.key), rstr.EmailVerifyRequest_builder{Ref: ref}.Build())
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		fmt.Fprintf(os.Stderr, "account: verify %s: %v\n", row.GetAddress(), err)
		http.Error(w, "cannot start", http.StatusInternalServerError)
		return
	}

	link := a.finish(r, "/confirm", res.GetToken())
	if err := a.c.Mail(ctx, row.GetAddress(), "Confirm your address", link); err != nil {
		fmt.Fprintf(os.Stderr, "account: mail to %s: %v\n", row.GetAddress(), err)
		http.Error(w, "cannot send", http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// confirm spends a verification link. Nobody is signed in by it, and nobody
// need be signed in to click it: the key that minted it confirms it.
func (a *App) confirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	for _, t := range a.byAlias {
		res, err := a.roster.Email().Confirm(withKey(ctx, t.key), rstr.EmailConfirmRequest_builder{Token: token}.Build())
		if err != nil {
			continue
		}

		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		fmt.Fprintf(w, confirmed, htmlEscape(res.GetEmail().GetAddress()))
		return
	}

	http.Error(w, "this link is not one, or is no longer", http.StatusNotFound)
}

// finish is the URL a mailed link finishes at: `Base` if the deployment named
// one, else this request's own origin.
func (a *App) finish(r *http.Request, path, token string) string {
	u := a.redirectBase(r)
	u.Path = path
	u.RawQuery = url.Values{"token": {token}}.Encode()

	return u.String()
}

func (a *App) redirectBase(r *http.Request) *url.URL {
	if a.c.Base != nil {
		u := *a.c.Base

		return &u
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	return &url.URL{Scheme: scheme, Host: r.Host}
}

func htmlEscape(v string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(v)
}

// The two pages a mailed link ends at. Plain HTML, because a person arriving
// from a mail client has no session and the page has one thing to say.
const recovered = `<!doctype html><meta charset="utf-8"><title>recovered</title>
<main style="font-family:system-ui;max-width:32rem;margin:3rem auto;padding:0 1rem">
<h1>Your new password</h1>
<p>Sign in with it now, then change it. It is shown <strong>once</strong>; everything you were signed in to before has been signed out.</p>
<p><code style="font-size:1.2rem;user-select:all">%s</code></p>
<p><a href="/">Sign in</a></p></main>`

const confirmed = `<!doctype html><meta charset="utf-8"><title>confirmed</title>
<main style="font-family:system-ui;max-width:32rem;margin:3rem auto;padding:0 1rem">
<h1>Address confirmed</h1>
<p><code>%s</code> is yours, as of now. Nothing was signed in by this link.</p>
<p><a href="/">Back to your account</a></p></main>`

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
