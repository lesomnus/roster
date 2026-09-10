// Package main is a product app that trusts `sso.hday.dev`, and nothing else.
//
// # Which of the two shapes this is
//
// `docs/position.md` says a deployment with several services should not replace
// its reverse proxy but change what the proxy points at -- and that is the
// other demo, `oauth2-proxy` in front of a page that knows nothing. This is the
// shape an app takes when the proxy is not there: **the app is the relying
// party**. It does the exchange, verifies the token, and holds a session of its
// own.
//
// Both exist because they fail differently and a deployment has both. A proxy
// proves the issuer is one a standard relying party accepts; this proves the
// pieces an app written against payday actually uses -- `authoidc.Subject`
// reading `sub` alone, and `authsession` holding an opaque cookie afterwards.
//
// # What it is not
//
// It is not `examples/sso`. That one has a provider **above** roster: somebody
// arrives from Google or Entra, and roster is asked who that is. Pointing it at
// roster's own Hydra would be circular -- the `sub` there is already the
// `Holder.id` it would be looking an `Identity` up by.
//
// This one has roster **below** the issuer, which is the whole picture in
// `docs/login.md` § "What changes when Hydra is in front", from the product's
// side. It never calls roster. It does not hold a roster key, and would not
// know what to do with one: what it knows about somebody is a token, which is
// the point being demonstrated.
//
// # The one line worth copying
//
//	authoidc.Subject
//
// It reads `sub` and nothing else, which is why changing the issuer's hostname
// later does not make everybody a new person. A relying party that keys on
// `(iss, sub)` -- which the spec permits -- would; roster's `sub` is a globally
// unique identifier, and that is what makes keying on it alone correct here.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/lesomnus/payday/auth/authoidc"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "product:", err)
		os.Exit(1)
	}
}

// A flow this app started and has not finished: the state, and where to go
// back to. Kept here rather than in the state parameter, for `arrives.States`'
// reason -- the state has to be a nonce and nothing else, and the cookie is
// what binds one browser to one trip.
type flow struct {
	expires time.Time
}

type app struct {
	cfg      *oauth2.Config
	verifier *oidc.IDTokenVerifier

	// Where the issuer says its own session is ended, read from discovery, and
	// this app's origin to come back to. Empty is an issuer that publishes no
	// such endpoint, and signing out is then this app's half alone.
	endSession string
	base       string
	claims     func(context.Context, *oidc.IDToken) (id string, err error)
	sessions   *authsession.Sessions

	mu    sync.Mutex
	flows map[string]flow
}

func run() error {
	var (
		addr     = flag.String("listen", ":8080", "where to serve")
		issuer   = flag.String("issuer", "", "the OIDC issuer, e.g. https://sso.hday.dev")
		clientId = flag.String("client-id", "", "what this app is called to the issuer")
		secret   = flag.String("client-secret", os.Getenv("PRODUCT_CLIENT_SECRET"), "or PRODUCT_CLIENT_SECRET")
		base     = flag.String("base", "", "this app's public origin, e.g. https://hello.hday.dev")
		insecure = flag.Bool("insecure-cookie", false, "drop Secure, for plain http in development")
	)
	flag.Parse()

	switch {
	case *issuer == "":
		return errors.New("--issuer: the OIDC issuer to trust")
	case *clientId == "":
		return errors.New("--client-id: what this app is called to it")
	case *base == "":
		return errors.New("--base: this app's public origin, which the issuer sends the browser back to")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	p, err := oidc.NewProvider(ctx, *issuer)
	if err != nil {
		return fmt.Errorf("%s: %w", *issuer, err)
	}

	to, err := url.Parse(*base)
	if err != nil {
		return fmt.Errorf("--base: %w", err)
	}

	// The **audience** is not optional, and `authoidc` refuses to be built
	// without one. A verifier that skips it accepts a token minted for any
	// relying party of the same issuer.
	if _, err := authoidc.New(ctx, authoidc.Config{Issuer: *issuer, Audience: *clientId}); err != nil {
		return err
	}

	key := make([]byte, authsession.KeySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	sealed, err := authsession.NewSealed(key)
	if err != nil {
		return err
	}
	opts := []authsession.Option{authsession.WithCookie("product_session"), authsession.WithLifetime(8 * time.Hour)}
	if *insecure {
		opts = append(opts, authsession.Insecure())
	}

	// `end_session_endpoint` is not in `oidc.Provider`'s struct, so it is read
	// off the raw discovery document -- which is the library's own way of
	// saying an app may need what it did not model.
	var discovered struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&discovered)

	a := &app{
		endSession: discovered.EndSession,
		base:       to.String(),
		cfg: &oauth2.Config{
			ClientID:     *clientId,
			ClientSecret: *secret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  to.JoinPath("/callback").String(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: *clientId}),
		sessions: authsession.New(sealed, opts...),
		flows:    map[string]flow{},
	}

	srv := &http.Server{Addr: *addr, Handler: a.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	fmt.Fprintf(os.Stderr, "product: %s, trusting %s as %s\n", *addr, *issuer, *clientId)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

func (a *app) handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /callback", a.callback)
	m.HandleFunc("GET /sign-out", a.signOut)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	m.HandleFunc("/", a.page)

	return m
}

// page is the whole app: signed in, it says who; signed out, it starts a flow.
func (a *app) page(w http.ResponseWriter, r *http.Request) {
	v, err := a.who(r)
	if err != nil {
		a.begin(w, r)

		return
	}

	rows := ""
	for _, kv := range v {
		rows += fmt.Sprintf("<dt>%s</dt><dd>%s</dd>", html.EscapeString(kv[0]), html.EscapeString(kv[1]))
	}

	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	fmt.Fprintf(w, page, rows)
}

// begin sends the browser to the issuer, which is where every one of these
// starts. The state is a nonce and the cookie is what binds this browser to it.
func (a *app) begin(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "cannot start", http.StatusInternalServerError)

		return
	}
	state := base64.RawURLEncoding.EncodeToString(b)

	a.mu.Lock()
	now := time.Now()
	for k, v := range a.flows {
		if now.After(v.expires) {
			delete(a.flows, k)
		}
	}
	a.flows[state] = flow{expires: now.Add(10 * time.Minute)}
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name: "product_state", Value: state, Path: "/",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	http.Redirect(w, r, a.cfg.AuthCodeURL(state), http.StatusFound)
}

// callback is the exchange, the verification, and the session -- in that order,
// and the session is this app's own from there on. The token is used **once**.
func (a *app) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	http.SetCookie(w, &http.Cookie{Name: "product_state", Path: "/", MaxAge: -1})

	state := r.URL.Query().Get("state")
	c, err := r.Cookie("product_state")
	if err != nil || state == "" || c.Value != state {
		// One answer for a missing cookie, a missing parameter and a mismatch.
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	a.mu.Lock()
	f, ok := a.flows[state]
	delete(a.flows, state)
	a.mu.Unlock()
	if !ok || time.Now().After(f.expires) {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	tok, err := a.cfg.Exchange(ctx, r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token", http.StatusBadGateway)

		return
	}
	id, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	// **`sub` and nothing else**, which is `authoidc.Subject`'s whole body. The
	// claims below are read for the page and are not the identity: a product
	// that keyed on `preferred_username` would lose somebody the day they are
	// renamed, and one that keyed on `email` would lose an intern who has none.
	var claims map[string]any
	_ = id.Claims(&claims)

	held := map[string]string{}
	for _, k := range []string{"preferred_username", "name", "email", "iss", "aud", "exp"} {
		if v, ok := claims[k]; ok {
			held[k] = fmt.Sprint(v)
		}
	}
	if gs, ok := claims["groups"].([]any); ok && len(gs) > 0 {
		vs := make([]string, 0, len(gs))
		for _, g := range gs {
			vs = append(vs, fmt.Sprint(g))
		}
		held["groups"] = strings.Join(vs, ", ")
	}

	_, cookie, err := a.sessions.Mint(ctx, authsession.Session{
		Id: id.Subject,

		// Everything this app lets somebody do, which is read one page. A
		// product with rows of its own puts its own answer here.
		Grant: frame.Whole(),
		Held:  held,
	})
	if err != nil {
		http.Error(w, "cannot sign in", http.StatusInternalServerError)

		return
	}

	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// signOut ends this app's session and then asks the **issuer** to end its own.
//
// The second half is the one that was missing, and its absence is the most
// convincing bug report a deployment can produce: the cookie is gone, the next
// page starts a flow, the issuer still remembers the browser and answers it
// without a form, and the person who clicked *sign out* is looking at their
// name again. Nothing leaked. It is still wrong to them, and they are right.
//
// So the redirect goes to `end_session_endpoint` -- discovery's own name for it
// -- and the issuer sends the browser back here afterwards. A deployment whose
// issuer publishes no such endpoint gets what this used to do, which is at
// least this app's half.
//
// `id_token_hint` is not sent because there is nothing to send it: the token is
// used once, at the callback, and thrown away, which is the argument
// `authsession` opens with and is not worth undoing for a hint. What answers
// the question it would have answered is `rp_initiated` at the other end -- the
// issuer knows an app asked, and that is the fact a confirmation screen exists
// to establish.
func (a *app) signOut(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, a.sessions.End(r.Context(), a.sessions.KeyOf(cookiesOf(r))))

	if a.endSession == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)

		return
	}

	to, err := url.Parse(a.endSession)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)

		return
	}
	q := to.Query()
	q.Set("post_logout_redirect_uri", a.base)
	q.Set("client_id", a.cfg.ClientID)
	to.RawQuery = q.Encode()

	http.Redirect(w, r, to.String(), http.StatusSeeOther)
}

// who is the session, read back. Nothing is asked of the issuer here and
// nothing of roster: after the callback the token is gone and this app knows
// what it wrote down.
func (a *app) who(r *http.Request) ([][2]string, error) {
	v, err := a.sessions.Read(r.Context(), a.sessions.KeyOf(cookiesOf(r)))
	if err != nil {
		return nil, err
	}

	out := [][2]string{{"sub", v.Id}}
	for _, k := range []string{"preferred_username", "name", "email", "groups", "iss", "aud"} {
		if s, ok := v.Held[k]; ok && s != "" {
			out = append(out, [2]string{k, s})
		}
	}

	return out, nil
}

func cookiesOf(r *http.Request) []string { return r.Header.Values("cookie") }

// page is what somebody who got here sees, which is the token's claims and a
// sentence about which of them is the identity.
//
// Plain HTML in one string: this is an example of an **app**, and giving it a
// build would be giving a reader something to install before they can read it.
const page = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>signed in</title>
<style>
  :root { color-scheme: light dark;
          --bg: #f6f7f9; --surface: #fff; --surface-2: #f0f2f5; --border: #dde1e6;
          --text: #1f2328; --muted: #6b7280; --accent: #3b5bdb }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #0f1216; --surface: #171b21; --surface-2: #1f242c; --border: #2b323c;
            --text: #e6e8eb; --muted: #9aa3ad; --accent: #7b93ff }
  }
  body { font: 15px/1.6 system-ui, sans-serif; margin: 0; background: var(--bg); color: var(--text) }
  main { max-width: 40rem; margin: 8vh auto; padding: 2rem; background: var(--surface);
         border: 1px solid var(--border); border-radius: 6px }
  h1 { font-size: 1.35rem; margin: 0 0 .25rem }
  p.note { color: var(--muted); margin-top: 0 }
  dl { display: grid; grid-template-columns: auto 1fr; gap: .4rem 1rem; margin: 1.5rem 0 }
  dt { color: var(--muted) }
  dd { margin: 0; overflow-wrap: anywhere; font-family: ui-monospace, monospace; font-size: .9rem }
  a { color: var(--accent) }
  code { background: var(--surface-2); padding: .1em .35em; border-radius: .2em }
</style>
<main>
  <h1>You are signed in.</h1>
  <p class="note">
    This app is the relying party: it did the exchange itself, verified the
    token, and what it holds now is a session of its own. There is no proxy in
    front of it and it has never spoken to roster.
  </p>

  <dl>%s</dl>

  <p class="note">
    <code>sub</code> is the identity and the rest is decoration. It is a
    <code>Holder</code> row in roster, the same one whatever was typed at the
    sign-in — which is why an app keys on it and not on a name that can be
    changed or an address somebody may not have.
  </p>

  <p><a href="/sign-out">sign out of this app</a></p>
</main>
`
