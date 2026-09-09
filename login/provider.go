package login

import (
	"errors"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/arrives"
)

// The other way in: an account at a provider, ending where the password ends.
//
// # Why it is the same ending
//
// `POST /accept` turns this app's own session into `acceptLoginRequest`. So
// does this, and by the same call -- the provider round trip replaces the two
// forms and nothing after them. What Hydra is told is a `Holder.id` either way,
// which is the whole reason the same person may sign in with Entra on Monday
// and a password on Saturday and be one `sub` to a product.
//
// # What is this app's and not `arrives`'
//
// The state parameter and the cookie that binds a browser to it, and the
// challenge. The challenge is the part no other front door has: the account app
// finishes at its own page, and this one has to come back to a flow Hydra
// started, so the challenge is remembered beside the tenant rather than put in
// the redirect -- a provider sends a browser only to a URL it was registered
// with, and one per flow is not that.

// stateCookie binds a browser to the round trip it started.
const stateCookie = "login_state"

// A flow is one round trip to a provider, started and not yet finished.
type flow struct {
	operator   *operator
	connection string
	challenge  string
}

// provider starts a sign-in through one of the operator's providers.
//
// `?connection=entra` names it. Left out, an operator with exactly one provider
// goes there and one with several is refused, because guessing would send
// somebody to a directory they are not in.
func (a *App) provider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	o, ok := operatorOf(ctx)
	if !ok {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	name := r.URL.Query().Get("connection")
	if name == "" {
		cs, err := a.arrives.Connections(ctx, o.id)
		if err != nil || len(cs) != 1 {
			http.Error(w, "connection: which provider", http.StatusBadRequest)

			return
		}
		name = cs[0].GetName()
	}

	cfg, _, err := a.arrives.Relying(ctx, o.id, name, a.redirect(r))
	if err != nil {
		if status.Code(err) == codes.NotFound {
			http.Error(w, "connection: no such provider here", http.StatusNotFound)

			return
		}
		a.broken(w, r, err)

		return
	}

	state, err := arrives.Nonce()
	if err != nil {
		a.broken(w, r, err)

		return
	}
	a.flows.Put(state, flow{operator: o, connection: name, challenge: r.URL.Query().Get(Challenge)}, 10*time.Minute)

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   !a.c.InsecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, cfg.AuthCodeURL(state), http.StatusFound)
}

// callback finishes the round trip, and the flow with it.
//
// It carries no challenge of its own: the redirect was registered with the
// provider once and every operator's flow comes back to it, so which flow this
// is comes from the state and nowhere else.
func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/", MaxAge: -1})

	state := r.URL.Query().Get("state")
	c, err := r.Cookie(stateCookie)
	if err != nil || state == "" || c.Value != state {
		// One answer for a missing cookie, a missing parameter and a mismatch:
		// saying which would tell whoever sent this browser how far they got.
		http.Error(w, "no", http.StatusBadRequest)

		return
	}
	f, ok := a.flows.Take(state)
	if !ok {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	o := f.operator
	as := withOperator(withKey(ctx, o.key), o)

	cfg, verifier, err := a.arrives.Relying(as, o.id, f.connection, a.redirect(r))
	if err != nil {
		a.broken(w, r, err)

		return
	}

	who, err := a.arrives.Claim(ctx, cfg, verifier, r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}
	who.Tenant = o.id
	who.TenantAlias = o.alias
	who.Provider = f.connection

	// Somebody this operator has never seen is the one decision that is not
	// roster's and not this app's.
	holder, err := a.arrives.Known(as, a.c.Enrol, who)
	if err != nil {
		if errors.Is(err, arrives.ErrUninvited) {
			http.Error(w, "this account has not been invited", http.StatusForbidden)

			return
		}
		a.broken(w, r, err)

		return
	}

	// The session, for the same reason the password half has one: the consent
	// hop reads this person's record **as them** to fill the token's claims,
	// and there is no other credential here that may.
	if err := a.door.Accept(as, w, o.id.String(), who.Provider, who.Subject); err != nil {
		a.broken(w, r, err)

		return
	}

	// A round trip somebody started at this page rather than from a flow --
	// there is no challenge to answer and nothing to redirect to but the app.
	if f.challenge == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)

		return
	}

	to, err := a.admin.acceptLogin(ctx, f.challenge, holder.String(), a.c.Remember)
	if err != nil {
		a.broken(w, r, err)

		return
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// redirect is where a provider sends the browser back: `Base` if the deployment
// named one, else this request's own origin.
//
// One URL for every operator, unlike the account app's -- which has a host per
// tenant and derives it per request. Hydra sends every browser to this app
// under one name, so a redirect per operator would be a name this app does not
// have. Which operator a callback belongs to is the state's to say.
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
