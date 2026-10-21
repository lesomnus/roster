package login

import (
	"errors"
	"log/slog"
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
	tenant    *tenant
	trip      *arrives.Trip
	challenge string
}

// provider starts a sign-in through one of the tenant's providers.
//
// `?connection=entra` names it. Left out, an tenant with exactly one provider
// goes there and one with several is refused, because guessing would send
// somebody to a directory they are not in.
func (a *App) provider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	o, ok := tenantOf(ctx)
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

	state, err := arrives.Nonce()
	if err != nil {
		a.broken(w, r, err)

		return
	}
	trip, err := a.arrives.Begin(ctx, o.id, o.alias, name, a.origin(r), state)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			http.Error(w, "connection: no such provider here", http.StatusNotFound)

			return
		}
		a.broken(w, r, err)

		return
	}
	a.flows.Put(state, flow{tenant: o, trip: trip, challenge: r.URL.Query().Get(Challenge)}, 10*time.Minute)

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   !a.c.InsecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, trip.URL, http.StatusFound)
}

// callback finishes the round trip, and the flow with it.
//
// It carries no challenge of its own: every flow of a connection comes back to
// that connection's one registered URL, so which flow this is comes from the
// state and nowhere else -- and the connection the URL names has to be the one
// that flow went to (`arrives.Trip`).
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

	o := f.tenant
	as := withTenant(withAt(ctx, o.at), o)

	who, err := a.arrives.Claim(as, f.trip, r)
	if err != nil {
		if errors.Is(err, arrives.ErrUnfinished) {
			a.broken(w, r, err)

			return
		}
		slog.WarnContext(ctx, "login: a provider's answer was refused",
			"tenant", o.alias, "err", err)
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	// Somebody this tenant has never seen is the one decision that is not
	// roster's and not this app's.
	holder, err := a.arrives.Known(as, a.c.Enrol, &who)
	if err != nil {
		if errors.Is(err, arrives.ErrUninvited) {
			http.Error(w, "this account has not been invited", http.StatusForbidden)

			return
		}
		a.broken(w, r, err)

		return
	}

	// What the tenant fills their profile with, where it has nothing --
	// which is the tenant's to say, and is nothing unless it said so. After
	// `Known`, so it is about somebody this tenant has; and never a reason to
	// refuse them, which is why a failure is only said.
	if err := a.arrives.Fill(as, holder, who); err != nil {
		slog.WarnContext(ctx, "login: the profile was not filled",
			"provider", who.Provider, "holder", holder.String(), "err", err)
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

// origin is where a provider sends the browser back under: `Base` if the
// deployment named one, else this request's own origin. Each connection's
// redirect is a path of its own beneath it (`arrives.Trip`), which is what a
// deployment registers with that provider:
// `https://login.example.com/callback/<tenant>/<connection>`.
//
// One origin for every tenant, unlike the account app's -- which has a host per
// tenant and derives it per request. Hydra sends every browser to this app
// under one name, so a host per tenant would be a name this app does not have;
// the tenant is a path segment instead, and which flow a callback belongs to is
// the state's to say.
func (a *App) origin(r *http.Request) string {
	if a.c.Base != nil {
		return a.c.Base.String()
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	return scheme + "://" + r.Host
}
