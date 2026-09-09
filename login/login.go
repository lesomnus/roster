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

	// Remember is how long Hydra should skip the form for a browser that has
	// already signed in. Zero asks every time.
	Remember time.Duration

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

	// Client is the OAuth client registered with Hydra for this operator. It is
	// what a challenge is matched against, and it is why this app needs no
	// hostname.
	Client string
}

// App is the Login App.
type App struct {
	c    Config
	conn *grpc.ClientConn

	roster rstr.Client
	me     rstr.MeServiceClient
	door   *frontdoor.Door
	admin  admin

	byClient map[string]*operator
}

type operator struct {
	id     pdid.Id
	alias  string
	key    string
	client string
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
		admin:    admin{base: c.Hydra, header: c.HydraHeader, client: http.DefaultClient},
		byClient: map[string]*operator{},
	}

	for alias, o := range c.Operators {
		if o.Client == "" {
			conn.Close()

			return nil, fmt.Errorf("login: %s: Client: which OAuth client is this operator's", alias)
		}

		v, err := a.roster.Tenant().Get(withKey(ctx, o.Key), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: proto.String(alias)}.Build(),
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

		if was, ok := a.byClient[o.Client]; ok {
			conn.Close()

			// Two operators on one client is a flow with two answers, and the
			// answer decides whose password is checked. Refused at start.
			return nil, fmt.Errorf("login: client %q is both %q's and %q's", o.Client, was.alias, alias)
		}
		a.byClient[o.Client] = &operator{id: id, alias: alias, key: o.Key, client: o.Client}
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

	// Where Hydra sends the browser.
	m.HandleFunc("GET /login", a.begin)
	m.HandleFunc("GET /consent", a.consent)

	// The sign-in protocol, behind the flow the challenge names.
	m.Handle("/session", a.inFlow(a.door.Handler()))
	m.Handle("/session/", a.inFlow(a.door.Handler()))

	// What turns a finished sign-in into Hydra's answer.
	m.Handle("POST /accept", a.inFlow(http.HandlerFunc(a.accept)))

	// The browser half of `frontdoor`, so the page needs no toolchain.
	m.HandleFunc("GET /frontdoor.js", frontdoor.Script)

	if a.c.Page != nil {
		m.Handle("/", a.c.Page)
	} else {
		m.HandleFunc("/", a.page)
	}

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

	a.page(w, r)
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

// consent is the second redirect, and the only place this app reads a person.
func (a *App) consent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	challenge := r.URL.Query().Get("consent_challenge")

	v, err := a.admin.consent(ctx, challenge)
	if err != nil {
		a.broken(w, r, err)

		return
	}
	o, ok := a.byClient[v.Client.Id]
	if !ok {
		a.broken(w, r, fmt.Errorf("login: no operator holds the client %q", v.Client.Id))

		return
	}

	// As the person, with this app's key beside their delegation: one call, and
	// what comes back is theirs by construction rather than by this app
	// remembering to filter.
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

	to, err := a.admin.acceptConsent(ctx, challenge, v, claims)
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
