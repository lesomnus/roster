// Package sso signs somebody in with an external identity provider and finds
// out who they are here.
//
// # The scenario
//
// One app, its own sign-in page, and Google (or Entra, or GitHub) instead of a
// password. That is the shape payday's guide says needs no Hydra: an app that
// is the only relying party sets its own cookie and reads it back. Hydra earns
// its place when a second app has to trust the first one's sign-in, and it
// changes nothing about the part below -- Hydra has no user database either, so
// something still has to answer "who is this subject".
//
// # The three roles, because two of them are easy to confuse
//
//	relying party    redirect, callback, exchange the code   this package
//	provider         issues the token                        Google, Entra, Hydra
//	resource server  verifies a token it was handed          payday's authoidc
//
// This package is the first. It is thirty lines of `golang.org/x/oauth2` and
// `go-oidc` and there is nothing framework-shaped about it, which is why payday
// does not have a package for it: what varies between providers -- the claim
// that holds an email, whether there is a hosted domain, Entra's per-tenant
// endpoints -- survives being wrapped and would just be configuration for a
// wrapper instead of configuration for the library.
//
// # One operator's front door
//
// This app authenticates to roster as a Holder, and a Holder belongs to one
// tenant -- so the wall narrows what it may read to that one. That is right for
// a front door serving one operator and is the reason this example is written
// as one. A deployment fronting several needs a credential whose actor is not
// inside a tenant, which is an API key rather than a person.
//
// # What is actually this deployment's to decide
//
// One thing, and it is the reason this example exists: a provider says
// "subject 1078… signed in", and roster has never heard of them.
//
// roster cannot answer that. Whether a stranger with a valid Google account
// gets an account here, and in which tenant, is a policy -- and every
// deployment has a different one. So it is [Enrol], which this package calls
// and does not write. Two are shipped below to make the choice visible rather
// than to be the answer.
//
// # What is not a decision
//
// Linking the identity. [Enrol] answers with a Holder and this package writes
// the [rstr.Identity] row itself, so a policy cannot forget it or write it a
// second way. roster refuses a subject that looks unstable and refuses a second
// account for the same provider; those rules are in `server/core/identity.go`
// and they only work if everything goes through them.
package sso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/frontdoor"
	rstr "github.com/lesomnus/roster/rstr"
	rosterhost "github.com/lesomnus/roster/server/front"
)

// Config is what a deployment is told.
type Config struct {
	// Issuer is the provider's, and discovery is done against it:
	// "https://accounts.google.com", "https://token.actions.example/…".
	Issuer string

	ClientID     string
	ClientSecret string

	// RedirectURL is where the provider sends the browser back, and must be
	// exactly what is registered with them.
	RedirectURL string

	// Provider is what **roster** calls this provider -- "google", "entra",
	// "github". It is stored on every [rstr.Identity] this signs in, so it is
	// chosen once and never changed: changing it orphans every row that has it.
	//
	// Separate from [Config.Issuer] on purpose. An issuer is a URL that can
	// move -- Entra's carries a tenant id -- and a name that moved with it
	// would make the same person a new person.
	Provider string

	// Scopes beyond `openid`. `email` is usual and is what [Enrolling] needs to
	// name somebody.
	Scopes []string

	// Tenants is which tenant each name this deployment serves belongs to:
	// "acme.example.com" -> "acme".
	//
	// # Why the host and not the email
	//
	// A tenant is the same service under a different operator's own domain, so
	// the front door somebody came to *is* the operator whose service they are
	// signing in to. Their email says where they authenticate, which is a
	// different question and often a different organisation -- one of acme's
	// people can perfectly well have a personal Google account.
	//
	// # It is how a row is named, not a check applied afterwards
	//
	// `Identity` is unique on (tenant, provider, subject), so the tenant is
	// part of naming one. Somebody who signs in at acme's door and then at
	// beta's is two Holders with two histories -- one human signing up to two
	// operators' services, which is what a tenant being the wall already means.
	// Nothing relates them and nothing should.
	//
	// **Optional now, and it was not.** Left empty, this asks roster --
	// `FrontService.WhoseHost` -- which is where the fact belongs: a `Host` row
	// is the tenant's own, written once by whoever runs the deployment, rather
	// than a copy in the configuration of every app that fronts it.
	//
	// It stays here because a map is right for an app that fronts one operator
	// and knows it at build time, and because an example that only showed the
	// remote answer would hide how little is needed for the simple case.
	//
	// An identity is unique **within a tenant**, so a sign-in cannot look
	// anybody up until it knows which one. There is no mode where that is
	// skipped -- only two places the answer can come from.
	Tenants map[string]string
}

// Caller is who the provider said signed in, before this deployment has
// decided whether it knows them.
//
// It is the claims and nothing else: no row has been read, and there may be no
// row to read. That is the same line `auth.Identity` sits on in payday.
type Caller struct {
	// Provider and Subject are the pair roster stores. Subject is the
	// provider's immutable identifier -- never a username, never an email.
	Provider string
	Subject  string

	// Email and Name are what the token happened to carry, and either may be
	// empty. A policy that requires one says so itself.
	Email string
	Name  string

	// Verified is the provider's word on whether it checked the email. A
	// policy that reads the address at all has to look at this: an unverified
	// one is a string the person typed.
	Verified bool

	// Host is the name the browser reached this app at.
	//
	// It is `r.Host`, which is whatever the client sent, so it is a claim like
	// the rest of this struct. Behind a proxy that rewrites it, the
	// deployment's trusted header is what belongs here.
	Host string

	// Tenant is the operator whose service this is, worked out from
	// [Config.Tenants] before anything is looked up -- so a policy never has to
	// decide it, and cannot decide it differently from the lookup.
	//
	// Never empty by the time a policy sees it: a request that arrived under a
	// name this deployment does not serve is refused before that.
	Tenant string
}

// Enrol decides what happens when somebody signs in and roster has never seen
// them.
//
// It answers with the Holder they are, and this package links the identity to
// it. Refusing is a legitimate answer and is the shipped default -- see
// [Invited].
//
// It is called with the claims and nothing else, because nothing else is known
// yet. Everything a policy needs beyond them -- which tenant a domain belongs
// to, whether a seat is free, whether somebody has to approve -- is the
// deployment's, held wherever the deployment keeps it.
type Enrol func(ctx context.Context, c Caller) (pdid.Id, error)

// ErrUnknown is what an [Enrol] answers when this person gets no account.
//
// It is separate from a failure so that the handler can tell them apart: one is
// 403 and a page saying to ask for an invitation, the other is 500 and a line
// in the log.
var ErrUnknown = errors.New("sso: nobody here")

// App is the relying party.
type App struct {
	cfg      oauth2.Config
	verifier *oidc.IDTokenVerifier
	provider string

	roster   rstr.Client
	sessions *authsession.Sessions
	enrol    Enrol
	tenants  map[string]string

	// `MeService` and `FrontService` are hand-written and so are not among the
	// entity services `rstr.Client` bundles. `VouchService` is not here at all
	// any more: this app never calls it directly, which is what extracting the
	// sign-in means.
	me_   rstr.MeServiceClient
	front rstr.FrontServiceClient

	// The two forms and the credential they end in, which is not this app's
	// to write: `frontdoor`, mounted below on this app's own mux.
	door *frontdoor.Door

	// after is where the browser goes once it is signed in.
	after string
}

// New reads the provider's discovery document and builds the app.
//
// It is done here rather than per request because it is an HTTP round trip to
// somebody else's server: an app that did it on every sign-in would be down
// whenever they were slow.
// `conn` is the same connection `roster` was built on, and it is taken as well
// because `rstr.Client` bundles the **entity** services -- `VouchService` and
// `MeService` are written by hand and are not among them. One argument rather
// than two clients so that a caller cannot hand over two connections
// authenticating as two different callers.
func New(ctx context.Context, c Config, conn *grpc.ClientConn, s *authsession.Sessions, enrol Enrol) (*App, error) {
	roster := rstr.NewClient(conn)
	front := rstr.NewFrontServiceClient(conn)

	if c.Provider == "" {
		return nil, errors.New("sso: Provider: what roster should call this provider")
	}
	if enrol == nil {
		return nil, errors.New("sso: Enrol: say what happens to somebody nobody has invited; Invited() is the refusing one")
	}

	p, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return nil, fmt.Errorf("sso: %s: %w", c.Issuer, err)
	}

	door, err := frontdoor.New(frontdoor.Config{
		Sessions: s,
		Vouch:    rstr.NewVouchServiceClient(conn),

		// What **this app's screens** need, rather than what the person holds.
		// A delegation narrows to the intersection, so asking for less than
		// they may do is free -- and it is why the list is written here, where
		// somebody can read it beside the handlers that use it, instead of in
		// the package that mints it.
		Methods: []string{
			rstr.MeService_Get_FullMethodName,
			rstr.MeService_Link_FullMethodName,
			rstr.MeService_Unlink_FullMethodName,
			rstr.MeService_SignOutEverywhere_FullMethodName,
			rstr.MeService_IssueKey_FullMethodName,
			rstr.MeService_RevokeKey_FullMethodName,
		},

		Tenant: func(ctx context.Context, host string) (string, error) {
			t, err := tenantOf(ctx, roster, front, c.Tenants, host)
			if errors.Is(err, ErrUnknown) {
				// The one cause `frontdoor` says "no" to, in the word this app
				// uses for it. Everything else reaches it as it is and is
				// answered as broken, which is the difference between a person
				// being told their password was wrong and being told the place
				// they are typing it at is down.
				return "", frontdoor.ErrUnknownHost
			}

			return t, err
		},
	})
	if err != nil {
		return nil, err
	}

	return &App{
		cfg: oauth2.Config{
			ClientID:     c.ClientID,
			ClientSecret: c.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  c.RedirectURL,
			Scopes:       append([]string{oidc.ScopeOpenID}, c.Scopes...),
		},
		// The audience is this client. It is the check most often left out, and
		// leaving it out accepts a token minted for somebody else's app at the
		// same provider.
		verifier: p.Verifier(&oidc.Config{ClientID: c.ClientID}),
		provider: c.Provider,

		roster:   roster,
		sessions: s,
		enrol:    enrol,
		tenants:  c.Tenants,
		after:    "/",

		door:  door,
		me_:   rstr.NewMeServiceClient(conn),
		front: front,
	}, nil
}

// Handler is the whole of this app's surface.
//
// `/login` and `/callback` are the provider's round trip. `/session` is the
// other front door -- a password, checked by roster -- and `/me` is what it
// answers with spent. `/account` is the screen over all of it, which is D24 §4;
// `password.go` says why the second front door is here at all and what it does
// not reach, and `account.go` says why the screen is one file of HTML.
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/login", a.login)
	m.HandleFunc("/callback", a.callback)
	m.Handle("/session", a.door.Handler())
	m.Handle("/session/{rest...}", a.door.Handler())
	m.HandleFunc("GET /me", a.me)
	m.HandleFunc("POST /me/ways", a.addWay)
	m.HandleFunc("DELETE /me/ways/{id}", a.unlink)
	m.HandleFunc("POST /me/sign-out-everywhere", a.everywhere)
	m.HandleFunc("POST /me/keys", a.mintKey)
	m.HandleFunc("DELETE /me/keys/{id}", a.revokeKey)
	m.HandleFunc("GET /account", a.Account)

	// The module `account.html` imports. Mounted by this app rather than by
	// `frontdoor` itself, because an app that bundles its own copy should be
	// able to leave it out -- and it can only do that if it was never mounted
	// for it.
	m.HandleFunc("GET /frontdoor.js", frontdoor.Script)

	return m
}

// stateCookie is where the state parameter is kept between the two requests.
//
// It has to be somewhere, and a cookie is the somewhere that needs no store: the
// provider hands the state back and the browser hands the cookie back, so the
// callback can compare two things that travelled by different routes. That
// comparison is the whole of the CSRF defence here -- without it, anybody can
// send a browser to `/callback` with a code of their own and sign that browser
// in as themselves.
const stateCookie = "sso_state"

// linkCookie is the same parameter for the other errand.
//
// Two cookies rather than one with a mode in it, because the provider redirects
// to one callback and the callback has to know which errand it is finishing.
// Which cookie is present says it, and a browser that somehow carries both is
// refused rather than having one chosen for it.
//
// What it deliberately does **not** carry is who the linking is for. That is
// the session, read at the moment the callback runs -- so a browser cannot
// carry an assertion about whose account an account is being attached to, which
// is the one thing that would turn this into a way in for somebody else.
const linkCookie = "sso_link"

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	a.start(w, r, stateCookie)
}

// addWay is `POST /me/ways`: the sign-in flow, reached by somebody already
// signed in.
//
// §4 named this and nothing routed it: *a person removes one and signs out
// everywhere, and adding one is the sign-in flow reached by somebody already
// signed in, which the reference app does not route.*
//
// It is the same redirect. What differs is which cookie goes with it and what
// the callback does at the end -- and, before any of that, that there **is** a
// session: an errand that attaches an account to somebody has to know who
// somebody is before it starts, not after the provider answers.
func (a *App) addWay(w http.ResponseWriter, r *http.Request) {
	if _, err := a.acting(r.Context(), r); err != nil {
		// Checked here as well as at the end, and the one at the end is the one
		// that decides. This is so that somebody who is not signed in is told
		// now rather than after a round trip to a provider.
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	a.start(w, r, linkCookie)
}

// start is the redirect both errands make, and the cookie is what tells them
// apart.
func (a *App) start(w http.ResponseWriter, r *http.Request, cookie string) {
	state, err := nonce()
	if err != nil {
		http.Error(w, "cannot start", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		// Long enough to sign in at the provider and no longer.
		MaxAge: int((10 * time.Minute).Seconds()),
	})

	http.Redirect(w, r, a.cfg.AuthCodeURL(state), http.StatusFound)
}

func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Which errand this is finishing, said by which cookie came back. A browser
	// carrying both is refused rather than having one chosen for it: two
	// errands were started and only one can be finished with one code, and
	// picking would make the answer depend on an order nothing states.
	signIn := matches(r, stateCookie)
	linking := matches(r, linkCookie)

	// Spent either way, and both, because a browser that started two and
	// finished neither should not be able to finish the other one later.
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: linkCookie, Path: "/", MaxAge: -1})

	if signIn == linking {
		// Neither, or both. One answer for a missing cookie, a missing
		// parameter, a mismatch and an ambiguity: they are the same event from
		// here, and saying which would tell whoever sent this browser how far
		// they got.
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	if linking {
		a.link(w, r)
		return
	}

	who, err := a.who(ctx, r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	who.Host = r.Host

	t, err := tenantOf(ctx, a.roster, a.front, a.tenants, r.Host)
	switch {
	case errors.Is(err, ErrUnknown):
		// Reached under a name this deployment does not serve. There is no
		// tenant to sign in to and nothing to guess.
		http.Error(w, "this account has not been invited", http.StatusForbidden)
		return

	case err != nil:
		// And a roster that could not be reached is not a name nobody serves.
		// This said "this account has not been invited" to an outage -- a
		// sentence about the person, made up, about a lookup that never
		// happened -- and the same person retrying at a healthy roster would
		// have signed straight in.
		fmt.Fprintf(os.Stderr, "sso: whose host %q: %v\n", r.Host, err)
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	who.Tenant = t

	// Still asked, and the answer is still what decides whether this person may
	// sign in at all -- an identity that reaches nobody here is somebody who
	// was never invited. What is no longer used is the holder it resolves to:
	// `Door.Accept` resolves the same claim on the other side of the wire, and
	// two resolutions of one claim is one more place for them to disagree.
	_, tenant, err := a.find(ctx, who)
	switch {
	case errors.Is(err, ErrUnknown):
		http.Error(w, "this account has not been invited", http.StatusForbidden)
		return
	case err != nil:
		// Said to the log and not to the browser: what went wrong here is this
		// deployment's, and a page that repeated it would be telling whoever
		// asked how roster is wired.
		fmt.Fprintf(os.Stderr, "sso: %s/%s: %v\n", who.Provider, who.Subject, err)
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	// The session is this app's and says nothing about the provider. What the
	// browser carries afterwards is a cookie only this server can read; the
	// token is spent and is not kept.
	//
	// Through `frontdoor`, which mints the session **and** holds a delegation
	// beside it -- so this browser can read its own record afterwards. The
	// comment at the top of `password.go` used to say the exchange that would
	// allow it was a decision nobody had taken; it is D49, and `Vouch.Accept`
	// is it.
	//
	// `holder` is not passed and is not needed: `Accept` resolves the claim
	// itself, which is the same resolution `find` just made. What is passed is
	// the tenant, because that is the fact this app established from the host
	// and roster cannot read off a token.
	if err := a.door.Accept(ctx, w, tenant.String(), who.Provider, who.Subject); err != nil {
		fmt.Fprintf(os.Stderr, "sso: accept %s/%s: %v\n", who.Provider, who.Subject, err)
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.after, http.StatusFound)
}

// matches answers whether this cookie came back with the state it was set with.
//
// A missing cookie, an empty one and a mismatch are one answer, which is what
// keeps the callback from saying how far somebody got.
func matches(r *http.Request, name string) bool {
	c, err := r.Cookie(name)

	return err == nil && c.Value != "" && c.Value == r.URL.Query().Get("state")
}

// link finishes the other errand: attach what the provider just proved to the
// person who is already signed in.
//
// # Who it attaches to
//
// The **session**, read here and not carried through the redirect. A browser
// that could say whose account this is for is a browser that could attach an
// account to somebody else, and it would look exactly like this flow while
// doing it. So the only assertion that travels is the state parameter, which
// says nothing about anybody.
//
// # And roster is the one that refuses
//
// Not this app. `MeService.Link` hangs the row off the frame's actor, refuses a
// second identity of one provider, and refuses an account already attached to
// somebody -- without saying whose. This handler turns those into pages and
// adds nothing of its own, which is the shape every other handler here has.
func (a *App) link(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	as, err := a.acting(ctx, r)
	if err != nil {
		// Signed out between starting and finishing, or never signed in. Either
		// way there is nobody to attach this to, and there is deliberately no
		// fallback that signs them in instead: an errand that silently becomes
		// a different errand is one nobody can reason about.
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	who, err := a.who(ctx, r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	res, err := a.me_.Link(as, rstr.MeLinkRequest_builder{
		Provider: who.Provider,
		Subject:  who.Subject,
	}.Build())
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			// That account is already a way in, here or for somebody else.
			// Told apart from nothing, for the reason `MeService.Link` gives.
			http.Error(w, "that account is already a way in", http.StatusConflict)
			return
		}
		if status.Code(err) == codes.InvalidArgument {
			// One provider per person, which `server/core` refuses in as many
			// words: *a second one is a link that found the wrong row, and
			// linking it would join two people into one.* A person can act on
			// this -- it means they are already signed in with this provider,
			// under a different account -- so they are told rather than shown a
			// five-hundred.
			http.Error(w, "you already sign in with this provider", http.StatusConflict)
			return
		}
		if status.Code(err) == codes.PermissionDenied {
			// This deployment has not granted linking. A real answer rather
			// than a five-hundred: it is a policy and somebody can change it.
			http.Error(w, "this deployment does not let people add their own", http.StatusForbidden)
			return
		}

		fmt.Fprintf(os.Stderr, "sso: link %s/%s: %v\n", who.Provider, who.Subject, err)
		http.Error(w, "cannot add", http.StatusInternalServerError)
		return
	}

	// Back to the page that asked, with what was written, so it can offer to
	// remove it without asking again.
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"id\":%q}\n", base64.RawURLEncoding.EncodeToString(res.GetId()))
}

func (a *App) who(ctx context.Context, code string) (Caller, error) {
	if code == "" {
		return Caller{}, errors.New("no code")
	}

	tok, err := a.cfg.Exchange(ctx, code)
	if err != nil {
		return Caller{}, err
	}

	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		// An access token without an id_token is OAuth2 and not OIDC, and an
		// access token says nothing about who the person is. Refused rather
		// than worked around with a `/userinfo` call, which would be trusting
		// a bearer token this app did not verify.
		return Caller{}, errors.New("no id_token")
	}

	id, err := a.verifier.Verify(ctx, raw)
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

	return Caller{
		Provider: a.provider,
		Subject:  id.Subject,
		Email:    claims.Email,
		Verified: claims.Verified,
		Name:     claims.Name,
	}, nil
}

// find is roster's half: who this subject already is, or who [Enrol] says they
// become.
func (a *App) find(ctx context.Context, c Caller) (holder pdid.Id, tenant pdid.Id, err error) {
	// Which tenant's rows to look in. An identity is unique **within a
	// tenant**, so this is not a check applied afterwards -- it is part of
	// naming the row at all, and there is no lookup to make without it.
	//
	// Resolved per sign-in because that is what an example should show; a
	// deployment that serves a fixed set of names holds these.
	t, err := a.roster.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String(c.Tenant)}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, pdid.Nil, fmt.Errorf("tenant %q: %w", c.Tenant, err)
	}

	// The three together are a unique index, so payday generated a way to name
	// a row by them -- there is nothing to list and filter here.
	v, err := a.roster.Identity().Get(ctx, rstr.IdentityGetRequest_builder{
		Ref: rstr.IdentityRef_builder{
			Subject: rstr.IdentityRefBySubject_builder{
				TenantId: t.GetId(),
				Provider: proto.String(c.Provider),
				Subject:  proto.String(c.Subject),
			}.Build(),
		}.Build(),
		Select: rstr.IdentitySelect_builder{
			Holder: rstr.HolderSelect_builder{
				Tenant: rstr.TenantSelect_builder{}.Build(),
			}.Build(),
		}.Build(),
	}.Build())

	switch status.Code(err) {
	case codes.OK:
		// Found, and that is the end of it. The row was looked up inside this
		// tenant, so it cannot be somebody else's -- which is why there is no
		// comparison here and no way to forget one.
		return ids(v.GetHolder().GetId(), v.GetHolder().GetTenant().GetId())

	case codes.NotFound:
		// Somebody the provider vouches for that **this tenant** has never
		// seen. They may well have an account with another operator on this
		// same roster, with the same Google account -- that is one human
		// signing up to two services and nothing here relates the two. This is
		// the decision, and it is not this package's.

	default:
		return pdid.Nil, pdid.Nil, err
	}

	id, err := a.enrol(ctx, c)
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	// The link, written here rather than by the policy. roster's rules about
	// what a good pair looks like are on `Identity.Add`, and a policy that
	// wrote the row itself would be a second way in that skips them.
	if _, err := a.roster.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: id.Bytes()}.Build(),
		Provider: c.Provider,
		Subject:  c.Subject,
	}.Build()); err != nil {
		return pdid.Nil, pdid.Nil, fmt.Errorf("link %s/%s: %w", c.Provider, c.Subject, err)
	}

	h, err := a.roster.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: id.Bytes()}.Build(),
		Select: rstr.HolderSelect_builder{Tenant: rstr.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	return ids(h.GetId(), h.GetTenant().GetId())
}

func ids(holder, tenant []byte) (pdid.Id, pdid.Id, error) {
	h, err := pdid.From(holder)
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	t, err := pdid.From(tenant)
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	return h, t, nil
}

func nonce() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Invited refuses everybody roster has not been told about.
//
// It is the shipped default and the one to reach for first: a deployment that
// has not thought about who gets an account has not decided to let strangers
// in, and this is what "has not decided" should do.
//
// Signing somebody in then becomes two steps -- somebody adds the Holder and
// links the identity, and the person signs in -- which is the same shape as
// being invited to anything else.
func Invited() Enrol {
	return func(ctx context.Context, c Caller) (pdid.Id, error) {
		return pdid.Nil, ErrUnknown
	}
}

// Enrolling gives a new person a Holder in the tenant whose front door they
// came to.
//
// What it means, said plainly: anybody the provider will authenticate, arriving
// at a name this deployment serves, gets an account without anybody approving
// it. For a company running this for its own people behind its own Workspace
// that is often exactly right. For a provider anybody can register at, it is a
// way in.
//
// A deployment that wants less than that wraps it, which is why [Enrol] is a
// function and not a setting:
//
//	func onlyFrom(next sso.Enrol, domains ...string) sso.Enrol {
//		return func(ctx context.Context, c sso.Caller) (pdid.Id, error) {
//			if !c.Verified {
//				return pdid.Nil, sso.ErrUnknown
//			}
//			_, d, _ := strings.Cut(c.Email, "@")
//			if !slices.Contains(domains, strings.ToLower(d)) {
//				return pdid.Nil, sso.ErrUnknown
//			}
//			return next(ctx, c)
//		}
//	}
//
// That check is about **who this person is**, which is a different question
// from which operator they are signing in to -- and the reason the two are not
// one setting. `email_verified` is in it because an unverified address is a
// string the person typed.
func Enrolling(c rstr.Client) Enrol {
	return func(ctx context.Context, caller Caller) (pdid.Id, error) {
		// The local part of the email is what somebody types to name this
		// person. It is unique within a tenant and not across them, so two
		// operators can each have a `frank` -- which is what the wall is for. A
		// deployment that would rather people were not guessable writes
		// something else here, and one whose provider gives no email has to.
		alias, _, ok := strings.Cut(caller.Email, "@")
		if !ok || alias == "" {
			return pdid.Nil, fmt.Errorf("enrol %s/%s: no email to name them by", caller.Provider, caller.Subject)
		}

		v, err := c.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Alias: proto.String(caller.Tenant)}.Build(),
			Alias:  alias,
			Name:   caller.Name,
		}.Build())
		if err != nil {
			return pdid.Nil, fmt.Errorf("enrol %s: %w", caller.Email, err)
		}

		// And **not** what they may do, which is the line this app does not
		// cross.
		//
		// `MeService.Link` is not waived the way the other three are -- adding
		// a way in is a feature a deployment offers rather than something
		// somebody must be able to do with no role -- so somebody enrolled here
		// cannot add one until a role says they may, and the account page tells
		// them so in a sentence.
		//
		// This app could grant it. Doing so would mean its key holds
		// `RoleService/Add` and `BindingService/Add`, which is *may bind any
		// role it can name to anybody it can see* -- a wide credential held by
		// the one service reachable from the open internet, bought to hand out
		// a single method. What a deployment does instead is write the role
		// once, wherever it decides what an ordinary account is.
		return pdid.From(v.GetId())
	}
}

// tenantOf is which operator this browser arrived at, by alias.
//
// From the configured map when there is one, and otherwise from roster. The
// second is where the fact belongs -- a `Host` row is written once by whoever
// runs the deployment, rather than copied into the configuration of every app
// that fronts it, going stale in each independently -- and it is what
// `FrontService` exists for.
//
// Read through this app's own credential, before anybody has been resolved to
// anybody: that is the situation `FrontService` is shaped for, and it answers
// with a tenant identifier and nothing else.
func tenantOf(
	ctx context.Context,
	roster rstr.Client,
	front rstr.FrontServiceClient,
	tenants map[string]string,
	host string,
) (string, error) {
	if len(tenants) > 0 {
		// Normalized by roster's own function rather than by this app's copy
		// of it. There were two, and two answers to *what name did this
		// browser arrive at* is one of them looking up a row the other never
		// wrote: a portless `[::1]` stayed bracketed here and was stored
		// unbracketed there, so the lookup missed and the page said nobody is
		// here. `Hostname` says as much about itself -- both sides need it and
		// neither may disagree.
		t, ok := tenants[rosterhost.Hostname(host)]
		if !ok {
			return "", ErrUnknown
		}

		return t, nil
	}

	v, err := front.WhoseHost(ctx, rstr.FrontWhoseHostRequest_builder{Host: host}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// The same answer the configured map gives for a name that is not
			// in it, and it has to be the same one: a caller that cannot tell
			// *nobody is here* from *nobody answered* has to guess, and both
			// guesses are wrong in public -- one tells a stranger a host exists,
			// the other tells somebody with the right password that it was
			// wrong. `WhoseHost` passes `NotFound` through unchanged for
			// exactly this reader.
			return "", ErrUnknown
		}

		return "", err
	}

	// By alias, because that is what `VouchWho` and the identity lookup take.
	// One read, and it is the same read `find` would have made anyway.
	t, err := roster.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref:    rstr.TenantRef_builder{Id: v.GetTenant()}.Build(),
		Select: rstr.TenantSelect_builder{Alias: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		return "", err
	}

	return t.GetAlias(), nil
}
