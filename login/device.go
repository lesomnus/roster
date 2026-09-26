package login

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// A device with no browser, signing in at this deployment's issuer: RFC 8628,
// the OAuth 2.0 Device Authorization Grant.
//
// # What this is, and what it is not
//
// It is **not** how a terminal gets a roster credential. That is the account
// app's (`account/device.go`), it shipped first, and it is a different flow for a
// different caller: a bare terminal talking to roster has no `client_id` and
// roster answers nothing without a credential, so an app that already holds a key
// does that one. Neither replaces the other:
//
//	the account app's   a terminal that wants a roster credential
//	this one            a device that wants an OAuth token from this issuer,
//	                    for whatever it is a client of
//
// A smart television signing in to a video product is the second. The product is
// an OAuth client of this deployment's Hydra, the television is polling Hydra's
// token endpoint, and what roster is asked for is the same thing it is asked for
// in every other flow: who this person is.
//
// # Why it is two handlers and no state
//
// Because the protocol is Hydra's. Everything RFC 8628 is careful about -- the
// code's alphabet, its lifetime, the `slow_down` a client that polls too fast is
// told, binding the code to the device that asked -- is Hydra's to do, and it does
// it (`oauth2.device_authorization.user_code.character_set`, `ttl.device_user_code`).
// What Hydra does not do is ask a person, which is this app's part in every other
// flow too.
//
// So: Hydra sends the browser here with a `device_challenge`, this draws a field,
// and the answer goes back to Hydra as `.../requests/device/accept`. What Hydra
// redirects to after that is `/login?login_challenge=…` -- the ordinary flow, the
// ordinary screens, the ordinary `POST /accept`. Nothing here holds a code, a
// timer or a poll count, because holding any of them would be holding a second
// copy of something Hydra is already authoritative about.
//
// There is one consequence of that worth stating: this screen **cannot say what
// is being authorised**, because there is no `GET .../requests/device` to ask. A
// person types a code and is then signed in and shown the consent screen, which is
// where the client and its scopes are named -- so the thing they are agreeing to is
// still in front of them before anything is granted, one screen later than it
// would be.
//
// # And which tenant a device flow is about
//
// The client's **registered** redirect, which is `arrivedAt`'s fallback and the
// case it was written for: *the device grant has no redirect at all*. That is not
// a detail a deployment can leave to chance, and it is the one thing this half
// asks of one:
//
//	a client that can do the device grant registers a redirect URI naming the
//	tenant's own host, even though the device grant never sends a browser to it
//
// It is used for nothing but saying which tenant. Ugly, and it is the same cost
// #36 recorded when it took the discriminator off the client id: what says which
// customer a flow is about has to be a name a customer claimed, and a device flow
// carries no name of its own. `roster login doctor` reports a client that can do
// this grant and has no redirect to be read, because the alternative is finding
// out when somebody reads a code off a television.

// deviceChallenge is what Hydra redirects to the verification screen with.
//
// `device_challenge`, which is the fourth of them -- and the one whose name is
// worth checking against Hydra rather than guessing: its refusal for the wrong
// one is *Query parameter 'device_challenge' is not defined but should have been*,
// which is the message a deployment would otherwise see instead of a screen.
const deviceChallenge = "device_challenge"

// device is the fourth screen: where somebody types the code a device printed.
//
// Drawn and not rendered, like the consent and logout screens and for the same
// reason -- one screen, not two of them in two technologies. Nothing is read from
// Hydra first, because there is nothing to read (see this file's head), so the
// only thing this checks is that there is a challenge at all: a browser that
// arrived without one has not come from Hydra, and drawing a form whose every
// answer would be refused is worse than saying so.
func (a *App) device(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get(deviceChallenge) == "" {
		a.broken(w, r, errors.New("login: no challenge"))

		return
	}

	a.page().ServeHTTP(w, r)
}

// verify is that screen's answer: the code, handed to Hydra.
//
// `{to}` rather than a 303, which is `POST /logout`'s shape and for its reason:
// the page is one document that fetches, so an answer it can read beats a redirect
// it has to follow blind. The browser goes where `to` says, which is Hydra, which
// sends it back here as an ordinary login challenge.
//
// # What it does about a wrong code
//
// Passes Hydra's refusal on as a **400 with no detail**, and the detail is what is
// deliberate. Hydra tells this app whether a code was never issued, already spent
// or expired; a screen that told a person which would be answering *is this code
// real* to whoever is typing, which is the oracle RFC 8628 §5.1 is about. One
// answer, and the person who has the code in front of them retypes it.
//
// Hydra is also the rate limit, which is why there is no counter here. A code is
// bound to one device flow and Hydra refuses it after its own window; a second
// meter in this app would be a second number to get wrong.
func (a *App) verify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	challenge := r.FormValue(deviceChallenge)
	if challenge == "" {
		a.broken(w, r, errors.New("login: no challenge"))

		return
	}

	// Trimmed and nothing else. What a code may be spelled with is Hydra's
	// (`oauth2.device_authorization.user_code.character_set`), so this app
	// lowering it or dropping its dashes would be this app deciding what Hydra's
	// codes look like -- and it would be wrong the day a deployment changes that
	// setting. Whitespace is the one thing no character set contains, and it is
	// what a paste out of a terminal brings with it.
	code := strings.TrimSpace(r.FormValue("user_code"))
	if code == "" {
		http.Error(w, "no code", http.StatusBadRequest)

		return
	}

	to, err := a.admin.acceptDeviceCode(ctx, challenge, code)
	if err != nil {
		// The one place in this app that tells Hydra saying no apart from Hydra
		// not answering, because it is the one endpoint whose input a **person**
		// composed. Logged whole either way; what differs is what the screen is
		// told, and a 502 for a mistyped code would send somebody to find an
		// administrator.
		var no *refusal
		if errors.As(err, &no) && no.Refused() {
			slog.WarnContext(ctx, "login: device code refused", "err", err)
			http.Error(w, "no", http.StatusBadRequest)

			return
		}

		a.broken(w, r, err)

		return
	}

	writeJson(w, map[string]any{"to": to})
}
