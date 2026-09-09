// Package login is the Login App: the box Hydra hands a `login_challenge` to,
// and the one that answers `acceptLoginRequest{subject}`.
//
// # What it is, in one paragraph
//
// Hydra speaks OIDC and has no user database. It stops mid-flow, redirects the
// browser here, and waits to be told a `subject` -- and choosing that string is
// the problem roster exists for. This app draws the form, asks roster whether
// the secret is somebody's, and hands the `Holder.id` back as the subject. What
// each product then trusts is a token Hydra signed, with a `sub` that is a row
// in roster rather than a provider's own identifier.
//
// # It is a consumer
//
// Like `account/` and `ldap/` it reaches roster **only over the wire**, and
// `scripts/test.sh` holds it to it: this package may not import `internal`,
// `cmd` or `server`. What it does import is `frontdoor` -- the two forms, the
// half session, the delegation held beside the cookie -- because everything up
// to the last hop is the same sign-in every front door does. The last hop is
// all that differs: `Door.Accept` ends a login in a cookie of its own, and this
// ends it in `acceptLoginRequest`.
//
// # Which operator, and where that comes from
//
// Not from the hostname. `account/` resolves the browser's `Host` because it
// has nothing else; this app has something better, and it arrives with the
// browser: the challenge names the **OAuth client**, one per operator, read
// from Hydra over the admin API. So which tenant a flow belongs to is Hydra's
// word rather than a header the browser wrote or a mapping this app parsed --
// and it is re-derived from the challenge on every request, never carried in
// anything the browser holds.
//
// The key follows from it: one `rt_` per operator, and every call about a flow
// goes out with the key of the tenant that flow's client resolved to. See
// `docs/login.md`, § "And what its own credential has to reach", for why that
// is several narrow credentials rather than one wide one.
package login

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/frontdoor"
	rstr "github.com/lesomnus/roster/rstr"
)

// Methods is what a delegation this app mints is allowed to do.
//
// One method. This app draws no account screens: what it needs the person for
// is the claims that go in the token, and `Me.Get` answers all of them at once
// -- alias, name, addresses, teams. A delegation narrows to the intersection of
// this and what the person may do, so asking for less than they hold is free
// and asking for more buys nothing.
var Methods = []string{rstr.MeService_Get_FullMethodName}

// Challenge is the query parameter every request in a flow carries.
//
// The same name Hydra redirects with, so the page needs no template: it lands
// on `/login?login_challenge=…` and reads its own URL.
//
// # Why not a cookie
//
// It was one, for half an hour. Hydra's challenge is an opaque string of about
// two kilobytes and a cookie is capped at four, which is close enough that the
// first real flow through `compose.yaml` returned 400 with nothing set. A limit
// that near is not one to design against.
//
// Nothing is lost by carrying it in the open. A challenge is not a secret --
// the browser arrived holding it, in its address bar -- and it is not trusted
// either: what a request may do is decided by looking it up **at Hydra**, and a
// browser that sends a different valid one gets that flow rather than this
// one's. The tenant is never the browser's word in either shape.
const Challenge = "login_challenge"

// consentChallenge is the second one Hydra redirects with.
const consentChallenge = "consent_challenge"

// Config is what a deployment has to say.
type Config struct {
	// Roster is where the data plane speaks gRPC.
	Roster string

	// Insecure dials roster without TLS.
	Insecure bool

	// Hydra is its admin API, e.g. `http://hydra:4445`. Private by
	// construction: anybody who reaches it can sign anybody in as anybody.
	Hydra string

	// HydraHeader is sent with every admin call, for a deployment that put a
	// proxy in front of that port. Its arrangement, not this app's.
	HydraHeader http.Header

	// Sessions is this app's own, holding one browser between the first form
	// and the consent screen. It is not the login: what a product ends up with
	// is Hydra's, and this one is closed as soon as the flow finishes.
	Sessions *authsession.Sessions

	// Operators is who this app fronts, by the tenant's alias. One entry is one
	// operator and the code is the same either way.
	Operators map[string]Operator

	// Remember is how long Hydra should skip the form, and the consent screen,
	// for a browser that has already been through them. Zero asks every time.
	Remember time.Duration

	// Consent is whether the consent hop draws a screen; see [Consent].
	Consent Consent

	// InsecureCookie drops `Secure` from the session cookie, for a page served
	// over plain http in development. It is `authsession`'s and is said there;
	// this app sets no cookie of its own.
	InsecureCookie bool

	// Page is the sign-in page, when a deployment serves its own rather than
	// the one embedded here. What it may not leave out is the last hop --
	// `POST /accept` -- because that is the half no other front door has.
	Page http.Handler
}

// Operator is one customer this app is a front door for.
//
// The two facts are one block rather than two maps keyed the same way, because
// two maps is a place to add an entry to one and not the other.
type Operator struct {
	// Key is this app's `rt_` for that tenant: a key on a holder inside it, so
	// the wall narrows what it may read with no discipline asked of this app.
	// `roster key add --tenant contoso --holder login-app --allow …`.
	Key string

	// Clients are the OAuth clients registered with Hydra for this operator.
	// A challenge naming one of them is a flow about this tenant, which is why
	// this app needs no hostname. More than one because an operator with two
	// products has two clients and one sign-in.
	Clients []string
}

// Consent is what happens at the consent hop.
type Consent int

const (
	// Skip grants what the client asked for and draws nothing. The default, and
	// right for the clients this app can have: every one of them was registered
	// by this deployment for one of its own operators.
	Skip Consent = iota

	// Ask draws a screen and grants nothing until somebody says so.
	Ask
)

// ParseConsent is [Consent] as a deployment writes it.
func ParseConsent(v string) (Consent, error) {
	switch v {
	case "", "skip":
		return Skip, nil
	case "ask":
		return Ask, nil
	}

	return Skip, fmt.Errorf("%q is not one of skip, ask", v)
}

// App is the Login App.
type App struct {
	c    Config
	conn *grpc.ClientConn

	roster rstr.Client
	me     rstr.MeServiceClient
	sync   rstr.SyncServiceClient
	door   *frontdoor.Door
	admin  admin

	byClient map[string]*operator

	// The same rows as `byClient`, once each: an operator with two clients is
	// one stream and not two.
	operators []*operator
}

type operator struct {
	id      pdid.Id
	alias   string
	name    string
	key     string
	clients []string
}

// New dials roster once and resolves each key to its tenant, so a key that
// cannot see the operator it is for is refused at start rather than at
// somebody's first sign-in.
func New(ctx context.Context, c Config) (*App, error) {
	switch {
	case c.Roster == "":
		return nil, errors.New("login: Roster: where the data plane speaks gRPC")
	case c.Hydra == "":
		return nil, errors.New("login: Hydra: where its admin API answers")
	case c.Sessions == nil:
		return nil, errors.New("login: Sessions: the cookie is the app's, so the app makes it")
	case len(c.Operators) == 0:
		return nil, errors.New("login: Operators: one per customer this app fronts; none is nobody to front")
	}

	// The credential of every call is whichever operator's key the context
	// carries, put there by `flow` from the challenge the request named. A call
	// made with none goes out with none and is refused by roster, which is the
	// right answer for a request no flow resolved.
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
		return nil, fmt.Errorf("login: %s: %w", c.Roster, err)
	}

	a := &App{
		c:        c,
		conn:     conn,
		roster:   rstr.NewClient(conn),
		me:       rstr.NewMeServiceClient(conn),
		sync:     rstr.NewSyncServiceClient(conn),
		admin:    admin{base: c.Hydra, header: c.HydraHeader, client: http.DefaultClient},
		byClient: map[string]*operator{},
	}

	for alias, o := range c.Operators {
		if len(o.Clients) == 0 {
			conn.Close()

			return nil, fmt.Errorf("login: %s: Clients: which OAuth clients are this operator's", alias)
		}

		v, err := a.roster.Tenant().Get(withKey(ctx, o.Key), rstr.TenantGetRequest_builder{
			Ref:    rstr.TenantRef_builder{Alias: proto.String(alias)}.Build(),
			Select: rstr.TenantSelect_builder{Name: proto.Bool(true)}.Build(),
		}.Build())
		if err != nil {
			conn.Close()

			return nil, fmt.Errorf("login: the key for %q cannot see %q: %w", alias, alias, err)
		}
		id, err := pdid.From(v.GetId())
		if err != nil {
			conn.Close()

			return nil, err
		}

		name := v.GetName()
		if name == "" {
			name = alias
		}
		who := &operator{id: id, alias: alias, name: name, key: o.Key, clients: o.Clients}
		a.operators = append(a.operators, who)
		for _, client := range o.Clients {
			if was, ok := a.byClient[client]; ok {
				conn.Close()

				// Two operators on one client is a flow with two answers, and
				// the answer decides whose password is checked. Refused at
				// start.
				return nil, fmt.Errorf("login: client %q is both %q's and %q's", client, was.alias, alias)
			}
			a.byClient[client] = who
		}
	}

	a.door, err = frontdoor.New(frontdoor.Config{
		Sessions:   c.Sessions,
		Vouch:      rstr.NewVouchServiceClient(conn),
		Delegation: rstr.NewDelegationServiceClient(conn),
		Methods:    Methods,

		// The host is not read. `frontdoor` asks for a tenant per request and
		// takes a `ctx` to answer from, which is the whole of what this app
		// needs: `flow` has already put the operator there, resolved from the
		// challenge through Hydra.
		Tenant: func(ctx context.Context, host string) (string, error) {
			o, ok := operatorOf(ctx)
			if !ok {
				return "", frontdoor.ErrUnknownHost
			}

			return o.alias, nil
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

// Handler is the app, as routes.
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()

	// Where Hydra sends the browser. Both serve the same page, which reads
	// which screen it is from the challenge in its own URL.
	m.HandleFunc("GET /login", a.begin)
	m.HandleFunc("GET /consent", a.consent)
	m.HandleFunc("POST /consent", a.decide)

	// What the page needs about a flow, and the only thing it asks for. Unlike
	// the console and the account page there is not one Connect call from this
	// browser: what a flow is about is Hydra's to say, and only this app may
	// ask Hydra.
	m.HandleFunc("GET /flow", a.flow)

	// The sign-in protocol, behind the flow the challenge names.
	m.Handle("/session", a.inFlow(a.door.Handler()))
	m.Handle("/session/", a.inFlow(a.door.Handler()))

	// What turns a finished sign-in into Hydra's answer.
	m.Handle("POST /accept", a.inFlow(http.HandlerFunc(a.accept)))

	// The built page, and its assets.
	m.Handle("/", a.page())

	return m
}

// begin is the redirect from Hydra: the start of one browser's flow.
func (a *App) begin(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get(Challenge)

	// Asked for now, though nothing on the page needs it: a challenge that
	// names a client this app fronts nobody for is a misconfiguration, and the
	// place to find out is here rather than after somebody has typed a
	// password into a form that was never going to work.
	v, _, err := a.whose(r.Context(), challenge)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	// Hydra already knows who this is -- a browser that signed in for another
	// client, within `remember`. There is no form to draw and nothing to ask
	// roster: the subject is Hydra's own and this app must not second-guess it.
	if v.Skip {
		to, err := a.admin.acceptLogin(r.Context(), challenge, v.Subject, a.c.Remember)
		if err != nil {
			a.broken(w, r, err)

			return
		}

		http.Redirect(w, r, to, http.StatusSeeOther)

		return
	}

	a.page().ServeHTTP(w, r)
}

// accept is the last hop: this app's session becomes Hydra's answer.
func (a *App) accept(w http.ResponseWriter, r *http.Request) {
	who, ok := a.door.Who(r.Context(), r)
	if !ok {
		// Not signed in, or half way. Either is the same to a caller: there is
		// nobody here to name.
		http.Error(w, "no", http.StatusUnauthorized)

		return
	}

	to, err := a.admin.acceptLogin(r.Context(), r.URL.Query().Get(Challenge), who.String(), a.c.Remember)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	writeJson(w, map[string]string{"redirect_to": to})
}

// consent is the second redirect: what the client is asking for, and whether
// anybody has to be asked about it.
//
// Hydra's own `skip` is answered first and is not a mode: it means this browser
// has already consented and Hydra remembered, so there is nothing to ask and
// asking again is the dialog people learn to click through.
func (a *App) consent(w http.ResponseWriter, r *http.Request) {
	v, o, err := a.asking(r.Context(), r.URL.Query().Get(consentChallenge))
	if err != nil {
		a.broken(w, r, err)

		return
	}

	if v.Skip || a.c.Consent == Skip {
		a.grant(w, r, v, o)

		return
	}

	// The page, which will ask `/flow` what this client wants. Drawn here
	// rather than rendered here, so there is one screen and not two of them in
	// two technologies.
	a.page().ServeHTTP(w, r)
}

// flow is what the page needs about the challenge in its own URL, and the only
// thing it asks this app for.
func (a *App) flow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var (
		v   *loginRequest
		o   *operator
		err error

		client string
		scope  []string
	)
	if c := r.URL.Query().Get(consentChallenge); c != "" {
		var req *consentRequest
		if req, o, err = a.asking(ctx, c); err == nil {
			client, scope = named(req.Client), req.Scope
		}
	} else if v, o, err = a.whose(ctx, r.URL.Query().Get(Challenge)); err == nil {
		client, scope = named(v.Client), v.Scope
	}
	if err != nil {
		a.broken(w, r, err)

		return
	}

	writeJson(w, map[string]any{"brand": o.name, "client": client, "scope": scope})
}

// named is what to call a client on a screen.
func named(c client) string {
	if c.Name != "" {
		return c.Name
	}

	return c.Id
}

// decide is the screen's answer, and the only two there are.
func (a *App) decide(w http.ResponseWriter, r *http.Request) {
	challenge := r.FormValue(consentChallenge)

	if r.FormValue("allow") == "" {
		to, err := a.admin.rejectConsent(r.Context(), challenge)
		if err != nil {
			a.broken(w, r, err)

			return
		}

		// The flow is over either way, so the credential this app minted goes
		// the same way it does on a yes.
		http.SetCookie(w, a.door.End(r.Context(), r))
		http.Redirect(w, r, to, http.StatusSeeOther)

		return
	}

	v, o, err := a.asking(r.Context(), challenge)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	a.grant(w, r, v, o)
}

// grant reads the person once, as them, and answers Hydra.
//
// The only place this app reads anybody. What it reads is `Me.Get`, which
// answers everything a claim could come from in one call, and what it puts in
// the token is decided by the scope the client asked for -- `claimsOf`.
func (a *App) grant(w http.ResponseWriter, r *http.Request, v *consentRequest, o *operator) {
	ctx := r.Context()

	claims := map[string]any{}
	as, err := a.door.Acting(withKey(ctx, o.key), r)
	switch {
	case err == nil:
		me, err := a.me.Get(as, rstr.MeGetRequest_builder{}.Build())
		if err != nil {
			a.broken(w, r, err)

			return
		}
		claims = claimsOf(me, v.Scope)

	case errors.Is(err, frontdoor.ErrNotSignedIn):
		// Hydra remembered the subject and this app's own session is gone --
		// a second client, or a browser that came back. The token still gets a
		// `sub`, which is Hydra's; what it does not get is the claims, because
		// reading them needs a credential nobody here holds any more.

	default:
		a.broken(w, r, err)

		return
	}

	to, err := a.admin.acceptConsent(ctx, v.Challenge, v, claims, a.c.Remember)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	// The flow is over: the delegation this app minted has done the one thing
	// it was for, and a credential that outlives its use is a credential
	// somebody has to remember to revoke.
	http.SetCookie(w, a.door.End(ctx, r))

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// asking is the consent request and the operator it belongs to.
func (a *App) asking(ctx context.Context, challenge string) (*consentRequest, *operator, error) {
	v, err := a.admin.consent(ctx, challenge)
	if err != nil {
		return nil, nil, err
	}

	o, ok := a.byClient[v.Client.Id]
	if !ok {
		return nil, nil, fmt.Errorf("login: no operator holds the client %q", v.Client.Id)
	}

	// Hydra answers the challenge it was asked about, and the rest of this
	// reads it from there rather than from the URL again.
	if v.Challenge == "" {
		v.Challenge = challenge
	}

	return v, o, nil
}

// whose is the operator a challenge belongs to.
//
// Two lookups and no cache: the challenge is read from Hydra on every request
// that names one, so which tenant a flow is about is never something this app
// remembers or the browser carries.
func (a *App) whose(ctx context.Context, challenge string) (*loginRequest, *operator, error) {
	v, err := a.admin.login(ctx, challenge)
	if err != nil {
		return nil, nil, err
	}

	o, ok := a.byClient[v.Client.Id]
	if !ok {
		return nil, nil, fmt.Errorf("login: no operator holds the client %q", v.Client.Id)
	}

	return v, o, nil
}

// inFlow puts the operator a request's challenge names into its context.
func (a *App) inFlow(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challenge := r.URL.Query().Get(Challenge)
		if challenge == "" {
			http.Error(w, "no", http.StatusBadRequest)

			return
		}

		_, o, err := a.whose(r.Context(), challenge)
		if err != nil {
			a.broken(w, r, err)

			return
		}

		next.ServeHTTP(w, r.WithContext(withOperator(withKey(r.Context(), o.key), o)))
	})
}

// broken is everything that is this deployment's fault rather than the
// browser's, which from a page is one answer and from a log is the whole of it.
//
// One answer because the alternatives all tell a browser something about the
// deployment: which client is unknown, that Hydra is down, that a challenge was
// already spent. None of that is a person's to learn from a login page, and all
// of it is an operator's to read here.
func (a *App) broken(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "login", "path", r.URL.Path, "err", err)
	http.Error(w, "this login is not working", http.StatusBadGateway)
}
