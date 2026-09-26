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
// half session, the delegation held beside the cookie -- and `arrives`, which
// is a `Connection` row turned into a relying party. Both because everything up
// to the last hop is the same sign-in every front door does. The last hop is
// all that differs: `Door.Accept` ends a login in a cookie of its own, and this
// ends it in `acceptLoginRequest`.
//
// # Two ways in, one ending
//
// A password, checked by roster; or an account at a directory, where this app
// is the relying party and roster is deliberately not. They meet at the
// `Holder.id`, which is what Hydra is told either way -- so a person who signs
// in with Entra on Monday and a password on Saturday is one `sub` to every
// product. `provider.go` is the second one, and the only thing about it that is
// not the account app's shape is the redirect: one URL for every tenant,
// because Hydra sends every browser here under one name.
//
// # Which tenant, and where that comes from
//
// Not from the hostname. `account/` resolves the browser's `Host` because it
// has nothing else; this app has something better, and it arrives with the
// browser: the challenge names the **OAuth client**, one per tenant, read
// from Hydra over the admin API. So which tenant a flow belongs to is Hydra's
// word rather than a header the browser wrote or a mapping this app parsed --
// and it is re-derived from the challenge on every request, never carried in
// anything the browser holds.
//
// The key follows from it: one `rt_` per tenant, and every call about a flow
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
	"net/url"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/frontdoor"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
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

// logoutChallenge is the third.
const logoutChallenge = "logout_challenge"

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

	// Key is the **one** credential this instance holds, and a deployment key
	// (`rk_`).
	//
	// It was one `rt_` per tenant, in a map keyed by alias, and the tenant a
	// flow belonged to was read off the OAuth client so that the right key
	// could be picked. Two things per tenant for a roster operator to write, and
	// one of them a secret to distribute and rotate.
	//
	// # An `rk_` used to be refused here, and what changed
	//
	// `docs/login.md` argued against it: an `rk_` resolves to a frame with no
	// tenant, the policy hands it `frame.Everything`, and what keeps contoso's
	// request out of fabrikam's rows is this app's own code. That objection was
	// right and is answered rather than waived -- `Host.acts_as` names the
	// holder a tenant nominates, so every call goes out with `roster-at` and is
	// answered as that holder, in that tenant, with their bindings and nothing
	// wider (`server/keys/at.go`, #43).
	//
	// So the wall is still what separates tenants. What differs is that it is
	// applied per **request** rather than per process, and that adding a tenant
	// is a `Host` row rather than a key.
	//
	// One call goes out without `roster-at` and it is the one that has to:
	// [App.Watch] hears every tenant, which is what an `rk_` is for.
	Key string

	// Remember is how long Hydra should skip the form, and the consent screen,
	// for a browser that has already been through them. Zero asks every time.
	Remember time.Duration

	// Consent is whether the consent hop draws a screen; see [Consent].
	Consent Consent

	// Base is this app's public origin, the one registered with every provider
	// as the redirect: `https://login.example.com`. Empty derives it from each
	// request, which suits a development deployment and nothing else, since a
	// provider will only send a browser back to a URL it was told about.
	//
	// One for the whole app, not one per tenant: Hydra sends every browser
	// here under one name. Which tenant a callback belongs to comes from the
	// state, never from the URL.
	Base *url.URL

	// Secret turns a `Connection.secret_ref` into the client secret it names.
	// Nil is [account.EnvSecret]'s vocabulary, wired by the command.
	Secret func(ref string) (string, error)

	// Enrol is what happens to somebody a provider vouches for and roster has
	// never seen. Nil is [arrives.Invited]: nobody.
	Enrol arrives.Enrol

	// InsecureCookie drops `Secure` from the session cookie, for a page served
	// over plain http in development. It is `authsession`'s and is said there;
	// this app sets no cookie of its own.
	InsecureCookie bool

	// Page is the sign-in page, when a deployment serves its own rather than
	// the one embedded here. What it may not leave out is the last hop --
	// `POST /accept` -- because that is the half no other front door has.
	Page http.Handler
}

// Consent is what happens at the consent hop.
type Consent int

const (
	// Skip grants what the client asked for and draws nothing. The default, and
	// right for the clients this app can have: every one of them was registered
	// by this deployment for one of its own tenants.
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

	// front is the one read that happens before anybody is narrowed: which
	// tenant claims a name. It is the unwalled server's, which is the whole
	// argument `server/front` makes about itself.
	front rstr.FrontServiceClient
	door  *frontdoor.Door
	admin admin

	// arrives is the relying-party half, shared with the account app: the
	// `Connection` rows, the discovery, the exchange and the enrolment.
	arrives *arrives.Providers

	// flows is every round trip to a provider started and not yet finished.
	flows *arrives.States[flow]

	// known is what a name resolved to, so that a flow costs one round trip to
	// roster rather than two per hop.
	//
	// Keyed on the **host**, which is what is read off the authorization
	// request, and holding what the two reads answered: which tenant claims
	// that name, and what it is called. Both are facts a deployment changes
	// rarely and a flow reads several times -- the sign-in page, the accept, the
	// consent -- so this is a cache and not state: dropping it costs round trips
	// and nothing else.
	//
	// What it deliberately does not hold is a **negative**: a name nothing
	// claims is refused and not remembered, so the `Host` row a tenant has just
	// written works on their next attempt rather than after a restart.
	known *hosts
}

// tenant is one customer, as a flow found them.
//
// No key on it, unlike the version this replaces: the credential is the app's
// one `rk_` and `at` is what narrows it -- the name whose `Host` row nominated
// the holder this call is answered as.
type tenant struct {
	id    pdid.Id
	alias string
	name  string
	at    string
}

// New dials roster once and resolves each key to its tenant, so a key that
// cannot see the tenant it is for is refused at start rather than at
// somebody's first sign-in.
func New(ctx context.Context, c Config) (*App, error) {
	switch {
	case c.Roster == "":
		return nil, errors.New("login: Roster: where the data plane speaks gRPC")
	case c.Hydra == "":
		return nil, errors.New("login: Hydra: where its admin API answers")
	case c.Sessions == nil:
		return nil, errors.New("login: Sessions: the cookie is the app's, so the app makes it")
	case c.Key == "":
		return nil, errors.New("login: Key: the one deployment key this app holds; none is nobody to be")
	}

	// **Not** a check that the key is an `rk_`, which is a real mistake and is
	// refused one package out (`cli/login.go`). It is a fact about a prefix
	// `server/keys` owns, and this app may import no server package but
	// `server/front` -- so the check lives where the configuration is read, and
	// this stays a consumer.

	// The credential of every call is whichever tenant's key the context
	// carries, put there by `flow` from the challenge the request named. A call
	// made with none goes out with none and is refused by roster, which is the
	// right answer for a request no flow resolved.
	creds := credentials.NewTLS(nil)
	if c.Insecure {
		creds = insecure.NewCredentials()
	}
	// One credential on every call, and `roster-at` on every call a flow made.
	//
	// It was the credential that varied -- one `rt_` per tenant, picked from the
	// challenge -- and now it is the narrowing. A call with no name attached goes
	// out as the deployment key itself, which is `frame.Everything`: exactly one
	// caller does that on purpose ([App.Watch]) and everything else runs inside a
	// flow, where `inFlow` has already put a name in the context.
	opts := append(auth.Inject(auth.ProviderFunc(func(ctx context.Context) context.Context {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.Key)
		if at, ok := atOf(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, front.HeaderAt, at)
		}

		return ctx
	})), grpc.WithTransportCredentials(creds))

	conn, err := grpc.NewClient(c.Roster, opts...)
	if err != nil {
		return nil, fmt.Errorf("login: %s: %w", c.Roster, err)
	}

	a := &App{
		c:      c,
		conn:   conn,
		roster: rstr.NewClient(conn),
		me:     rstr.NewMeServiceClient(conn),
		sync:   rstr.NewSyncServiceClient(conn),
		front:  rstr.NewFrontServiceClient(conn),
		admin:  admin{base: c.Hydra, header: c.HydraHeader, client: http.DefaultClient},
		flows:  arrives.Held[flow](),
		known:  &hosts{at: map[string]*tenant{}},
	}
	a.arrives = arrives.New(a.roster, c.Secret)

	// Nothing per tenant is resolved here, because there is nothing to resolve:
	// who this app fronts is every tenant with a `Host` row, and that is a
	// question a flow asks rather than a list a start-up walks. What the old
	// shape bought by walking one -- *a key that cannot see the tenant it is for
	// is refused at start rather than at somebody's first sign-in* -- is bought
	// instead by the key being one, and `roster login doctor` is where a
	// deployment asks whether its registrations resolve.

	a.door, err = frontdoor.New(frontdoor.Config{
		Sessions:   c.Sessions,
		Vouch:      rstr.NewVouchServiceClient(conn),
		Delegation: rstr.NewDelegationServiceClient(conn),
		Methods:    Methods,

		// The host is not read. `frontdoor` asks for a tenant per request and
		// takes a `ctx` to answer from, which is the whole of what this app
		// needs: `flow` has already put the tenant there, resolved from the
		// challenge through Hydra.
		Tenant: func(ctx context.Context, host string) (string, error) {
			o, ok := tenantOf(ctx)
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
	// the admin console and the account page there is not one Connect call from this
	// browser: what a flow is about is Hydra's to say, and only this app may
	// ask Hydra.
	m.HandleFunc("GET /flow", a.flow)

	// The sign-in protocol, behind the flow the challenge names.
	m.Handle("/session", a.inFlow(a.door.Handler()))
	m.Handle("/session/", a.inFlow(a.door.Handler()))

	// What turns a finished sign-in into Hydra's answer.
	m.Handle("POST /accept", a.inFlow(http.HandlerFunc(a.accept)))

	// The other way in. `/provider` is in a flow and `/callback` is not: a
	// provider sends the browser back to one registered URL, and which flow
	// that is comes from the state.
	m.Handle("GET /provider", a.inFlow(http.HandlerFunc(a.provider)))
	m.HandleFunc("GET /callback", a.callback)

	// The third screen Hydra redirects to, and the one that is a screen only
	// half the time: drawn when nothing proved a relying party started the
	// sign-out, and answered without drawing when something did.
	m.HandleFunc("GET /logout", a.logout)
	m.HandleFunc("POST /logout", a.leave)

	// And the fourth: where somebody types the code a device with no browser
	// printed. It asks Hydra nothing before drawing, because Hydra has no getter
	// for a device challenge -- see `login/device.go`, which is the whole of this
	// half.
	m.HandleFunc("GET /device", a.device)
	m.HandleFunc("POST /device", a.verify)

	// Where a sign-out ends when it asked to come back nowhere. Hydra's
	// `urls.post_logout_redirect`, and the only screen here that is not part of
	// a flow: there is no challenge on it and nothing to look up, because by
	// the time a browser arrives the thing it is about is already over.
	//
	// It exists because the alternative is Hydra's fallback page, which tells a
	// person who clicked *sign out* that an administrator has not configured
	// something.
	m.HandleFunc("GET /signed-out", func(w http.ResponseWriter, r *http.Request) {
		a.page().ServeHTTP(w, r)
	})

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

// logout is Hydra asking whether somebody meant it, and this says yes.
//
// # Why it draws nothing
//
// `consent: skip`'s argument, one screen along: every client this app can front
// was registered by the deployment for one of its own tenants, so a *sign
// out* that arrived from one of them is a person who clicked *sign out*. A
// confirmation screen there is the dialog people learn to click through, and it
// is in the way of the thing they asked for rather than of a thing they did
// not.
//
// What the screen is actually for is the other case, and it is answered without
// one: a logout that **no relying party started** -- somebody typed the URL, or
// a page they were reading loaded it as an image -- is refused rather than
// asked about. A nuisance a third party can cause is not something to hand a
// person a button for.
//
// # What it ends, and what it does not
//
// Hydra's session for that browser, which is the one that was making *sign out*
// a lie: the product's own session was already gone and the next page came back
// signed in because the issuer still remembered.
//
// It does **not** reach the other products. A person signed in to two apps who
// signs out of one ends the issuer's memory and that app's session, and the
// second app's cookie is its own until it expires. Ending that is back-channel
// logout, which is a thing each product grows -- `docs/login.md` says so beside
// this.
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	challenge := r.URL.Query().Get(logoutChallenge)
	if challenge == "" {
		a.broken(w, r, errors.New("login: no challenge"))

		return
	}

	v, err := a.admin.logout(ctx, challenge)
	if err != nil {
		a.broken(w, r, err)

		return
	}
	if !v.RpInitiated {
		// Nobody **proved** an app asked, which is a narrower thing than
		// nobody asking, and the first cut of this read it as the wider one
		// and refused. What `rp_initiated` actually reports is whether the
		// request carried an `id_token_hint`: no hint, `RPInitiated: false`, and
		// it asks this app anyway. Read off Hydra's own
		// `consent/strategy_default.go` at v2.2.0 and still true at the pinned
		// version, which `docker/flow.sh` asserts from both ends rather than
		// from a file nobody here can see -- so a relying party that signs
		// somebody out
		// without sending the token back got a page saying `no`, which is the
		// defect the cluster found.
		//
		// The answer the spec gives is the one a refusal was standing in for:
		// **ask the person**. A third party can cause a question and nothing
		// else, and the person who did click sign out gets to say yes. Drawn
		// rather than rendered, like the consent screen and for the same
		// reason -- one screen, not two of them in two technologies.
		a.page().ServeHTTP(w, r)

		return
	}

	to, err := a.admin.acceptLogout(ctx, challenge)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	// This app's own session too, for the browser that has one. It lasts as
	// long as Hydra's memory of the same browser, so a person signing out with
	// one open is exactly a person who would otherwise sign in again and find
	// the old one waiting.
	http.SetCookie(w, a.door.End(ctx, r))
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// leave is the confirmation screen's answer, for the logout nobody proved a
// relying party started.
//
// A no is Hydra's `logout/reject` and nowhere to send the browser: there is no
// waiting relying party to redirect to, because that is the whole reason the
// question was asked. The page says so and stays where it is.
func (a *App) leave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	challenge := r.FormValue(logoutChallenge)

	// Asked again rather than trusted from the form: a browser that posts the
	// challenge of a logout an app **did** start would otherwise skip the
	// confirmation this endpoint exists to collect. It is the same read the
	// GET made and Hydra is the one holding the answer.
	v, err := a.admin.logout(ctx, challenge)
	if err != nil {
		a.broken(w, r, err)

		return
	}
	if v.RpInitiated {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	if r.FormValue("allow") == "" {
		if err := a.admin.rejectLogout(ctx, challenge); err != nil {
			a.broken(w, r, err)

			return
		}

		writeJson(w, map[string]any{"signed_out": false})

		return
	}

	to, err := a.admin.acceptLogout(ctx, challenge)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	http.SetCookie(w, a.door.End(ctx, r))
	writeJson(w, map[string]any{"signed_out": true, "to": to})
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

	// The sign-out screen, which is the one shape that asks nothing of roster.
	// What it needs is that it **is** that screen and who this deployment is
	// for; there is no client, because a logout with a client is one Hydra
	// marked `rp_initiated` and this app never draws.
	if c := r.URL.Query().Get(logoutChallenge); c != "" {
		v, err := a.admin.logout(ctx, c)
		if err != nil {
			a.broken(w, r, err)

			return
		}
		if v.RpInitiated {
			http.Error(w, "no", http.StatusBadRequest)

			return
		}

		// A brand when there is one to be sure of. This app fronts a list of
		// tenants, and a logout names a subject rather than a client, so
		// with several of them **which** customer's person this is cannot be
		// told from the request -- and a brand picked from the first row would
		// be a guess drawn on a screen. With one, it is not a guess.
		//
		// And there is no longer a way to know there is one. This app fronts
		// whoever has a `Host` row rather than a configured list, so *how many
		// tenants are there* is a question about the deployment's rows and not
		// about this process -- and the sign-out hop carries no challenge to
		// resolve one from. So the screen is unbranded here, which is what it
		// already was for every deployment fronting more than one.
		writeJson(w, map[string]any{"brand": "", "logout": true})

		return
	}

	var (
		v   *loginRequest
		o   *tenant
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

	// The ways in, for a login screen. A consent screen has a person already
	// and asks this for the client and the scopes alone.
	ways := []map[string]any{}
	password := false
	if v != nil {
		// What roster says, not what this app assumes. A tenant whose people
		// all arrive through a directory turns the password off, and
		// `Vouch.Verify` refuses one -- so a form drawn here would be a form
		// that cannot work.
		tn, err := a.roster.Tenant().Get(withAt(ctx, o.at), rstr.TenantGetRequest_builder{
			Ref:    rstr.TenantRef_builder{Id: o.id.Bytes()}.Build(),
			Select: rstr.TenantSelect_builder{Config: proto.Bool(true)}.Build(),
		}.Build())
		if err != nil {
			a.broken(w, r, err)

			return
		}
		password = tn.OffersPassword()

		cs, err := a.arrives.Connections(withAt(ctx, o.at), o.id)
		if err != nil {
			a.broken(w, r, err)

			return
		}
		for _, c := range cs {
			// The name and the issuer, and nothing else. `secret_ref` is the
			// tenant's and never leaves this process; the client id is
			// theirs too.
			//
			// The issuer is here because it is the only honest way for a page
			// to know **whose** directory this is. A `Connection` may be
			// called `entra`, `ms` or `work` -- that is the tenant's label,
			// and drawing a vendor's mark from it would be drawing from a
			// label, which D22 refuses. A host is a fact:
			// `login.microsoftonline.com` is Microsoft whatever the row is
			// called. It is also where the browser is about to go, so it is
			// not a thing being disclosed.
			//
			// What is still refused is a field that says which mark to draw.
			// The page reads the host and decides.
			ways = append(ways, map[string]any{
				"name":   c.GetName(),
				"issuer": c.GetIssuer(),
			})
		}
	}

	writeJson(w, map[string]any{
		"brand":     o.name,
		"client":    client,
		"scope":     scope,
		"providers": ways,
		"password":  password,
	})
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

		// The credential goes the same way it does on a yes, which is now
		// *stays, unless nothing is remembered*. A no is about this client and
		// not about the person: Hydra still remembers who they are, and the
		// next product they open is a flow that needs the claims.
		if a.c.Remember <= 0 {
			http.SetCookie(w, a.door.End(r.Context(), r))
		}
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
func (a *App) grant(w http.ResponseWriter, r *http.Request, v *consentRequest, o *tenant) {
	ctx := r.Context()

	claims := map[string]any{}
	as, err := a.door.Acting(withAt(ctx, o.at), r)
	switch {
	case err == nil:
		me, err := a.me.Get(as, rstr.MeGetRequest_builder{}.Build())
		if err != nil {
			a.broken(w, r, err)

			return
		}
		claims = claimsOf(me, v.Scope)

	case errors.Is(err, frontdoor.ErrNotSignedIn):
		// Hydra remembers this browser and this app does not: its session
		// outlived nothing, or `remember` is zero and there is no session to
		// have. The token still gets a `sub`, which is Hydra's; what it does
		// not get is the claims, because reading them needs a credential
		// nobody here holds.
		//
		// This used to be **every** flow after the first, because the session
		// was closed at each redirect -- see the end of this function. It is
		// now the edge it reads like: a browser whose session outlived Hydra's
		// answer, or a deployment that remembers nothing.

	default:
		a.broken(w, r, err)

		return
	}

	to, err := a.admin.acceptConsent(ctx, v.Challenge, v, claims, a.c.Remember)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	// And the session **stays**, for as long as Hydra will skip the form.
	//
	// It used to end here, on the rule that a credential which outlives its use
	// is a credential somebody has to remember to revoke. The rule is right and
	// the reading of *its use* was wrong: what this delegation is for is the
	// claims above, and a browser Hydra remembers comes back for those again --
	// a second product, or the same one opened tomorrow morning. Ended here,
	// every one of those flows found no session and put nothing but `sub` in
	// the token, which with `remember` set is most of the tokens a deployment
	// issues. It was written down as a known consequence, in the branch above,
	// and it was a defect: a page reading its own token saw an opaque
	// identifier twice and no name, and reported the sign-in as broken.
	//
	// It is still bounded, by the same clock Hydra uses (`cli/login.go`), and
	// it is still narrow: [Methods] is `Me.Get` alone about one person.
	//
	// `Remember` of zero is a deployment that asks every time, so there is no
	// later flow to hold anything for and the credential goes at the redirect
	// the way it always did.
	if a.c.Remember <= 0 {
		http.SetCookie(w, a.door.End(ctx, r))
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// asking is the consent request and the tenant it belongs to.
func (a *App) asking(ctx context.Context, challenge string) (*consentRequest, *tenant, error) {
	v, err := a.admin.consent(ctx, challenge)
	if err != nil {
		return nil, nil, err
	}

	name, err := a.arrivedAt(ctx, v.Url, v.Client.Id)
	if err != nil {
		return nil, nil, err
	}

	o, err := a.at(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	// Hydra answers the challenge it was asked about, and the rest of this
	// reads it from there rather than from the URL again.
	if v.Challenge == "" {
		v.Challenge = challenge
	}

	return v, o, nil
}

// whose is the tenant a challenge belongs to.
//
// The challenge is read from Hydra on every request that names one, so which
// tenant a flow is about is never something the browser carries. What is cached
// is one step further in -- the name to tenant hop -- and `login/at.go` says why
// that is a cache rather than state.
func (a *App) whose(ctx context.Context, challenge string) (*loginRequest, *tenant, error) {
	v, err := a.admin.login(ctx, challenge)
	if err != nil {
		return nil, nil, err
	}

	name, err := a.arrivedAt(ctx, v.Url, v.Client.Id)
	if err != nil {
		return nil, nil, err
	}

	o, err := a.at(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	return v, o, nil
}

// inFlow puts the tenant a request's challenge names into its context.
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

		next.ServeHTTP(w, r.WithContext(withTenant(withAt(r.Context(), o.at), o)))
	})
}

// broken is everything that is this deployment's fault rather than the
// browser's, which from a page is one answer and from a log is the whole of it.
//
// One answer because the alternatives all tell a browser something about the
// deployment: which client is unknown, that Hydra is down, that a challenge was
// already spent. None of that is a person's to learn from a login page, and all
// of it is an tenant's to read here.
func (a *App) broken(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "login", "path", r.URL.Path, "err", err)
	http.Error(w, "this login is not working", http.StatusBadGateway)
}
