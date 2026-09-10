package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/lesomnus/payday/auth/authsession"

	"github.com/lesomnus/roster/internal/idptest"
)

// The product half of the picture in `docs/login.md`, walked.
//
// The issuer here is `internal/idptest` rather than Hydra, and that is the
// point of the fake existing: what this app does is verify a signed token from
// somewhere and keep a session of its own, and none of it is about **who**
// signed it. `scripts/hydra.sh` is where the real issuer is walked.

const audience = "hello-app"

func serve(t *testing.T, p *idptest.Idp) (*app, *httptest.Server) {
	t.Helper()
	x := require.New(t)

	ctx := t.Context()
	provider, err := oidc.NewProvider(ctx, p.URL)
	x.NoError(err)

	key := make([]byte, authsession.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	sealed, err := authsession.NewSealed(key)
	x.NoError(err)

	a := &app{
		verifier: provider.Verifier(&oidc.Config{ClientID: audience}),
		sessions: authsession.New(sealed, authsession.WithCookie("product_session"), authsession.Insecure()),
		flows:    map[string]flow{},
	}

	s := httptest.NewServer(a.handler())
	t.Cleanup(s.Close)

	a.cfg = &oauth2.Config{
		ClientID: audience,
		Endpoint: provider.Endpoint(),
		// The fake redirects straight back, so the whole round trip runs
		// against this test's own server.
		RedirectURL: s.URL + "/callback",
		Scopes:      []string{oidc.ScopeOpenID, "profile", "email"},
	}

	return a, s
}

func browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

// TestAProductAppSignsSomebodyInAndKeepsItsOwnSession is the whole of it.
func TestAProductAppSignsSomebodyInAndKeepsItsOwnSession(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, audience)
	p.Subject = "01a085ca-e032-8bf3-ac02-b2ded21ed192"
	p.Claims = map[string]any{
		"preferred_username": "erin",
		"name":               "Erin of contoso",
		"groups":             []any{"ops", "release"},
	}

	_, s := serve(t, p)
	b := browser(t)

	// One `GET /` and the redirects are followed: to the issuer, back to
	// `/callback`, and on to the page.
	res, err := b.Get(s.URL + "/")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	body := read(t, res)

	// The identity is `sub`, and it is roster's `Holder.id` rather than
	// anything the issuer made up about a name.
	x.Contains(body, p.Subject)
	x.Contains(body, "erin")
	x.Contains(body, "ops, release")

	// And the session is this app's from here on: a second request carries a
	// cookie and asks the issuer nothing.
	res, err = b.Get(s.URL + "/")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)
	x.Contains(read(t, res), p.Subject)

	var held bool
	for _, c := range b.Jar.Cookies(mustParse(t, s.URL)) {
		if c.Name == "product_session" {
			held = true
			// Opaque, and worth asserting: what a browser holds is a key, not
			// the token. `authsession` opens with that argument.
			x.NotContains(c.Value, p.Subject)
		}
	}
	x.True(held, "no session cookie was set")
}

// TestSigningOutEndsThisAppsSessionAndNotTheIssuers is the subtlety, and it
// took a wrong test to find: signing out here and opening the page again signs
// somebody straight back in.
//
// That is right. This app's sign-out is a delete of **its** session; the issuer
// was not asked and still remembers the browser, so the next `GET /` starts a
// flow that Hydra answers without a form (`login.remember`). Nothing is broken
// and nothing is leaking -- but a person who clicked "sign out" and landed
// signed in would say otherwise, which is what the OIDC logout endpoints are
// for and why `docs/login.md` says back-channel logout is the product's half.
func TestSigningOutEndsThisAppsSessionAndNotTheIssuers(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, audience)
	p.Subject = "somebody"

	_, s := serve(t, p)
	b := browser(t)

	res, err := b.Get(s.URL + "/")
	x.NoError(err)
	defer res.Body.Close()
	x.Contains(read(t, res), "somebody")

	// Redirects **not** followed from here, and that is the finding rather than
	// a detail of the harness: `/sign-out` redirects to `/`, and following it
	// starts a flow, comes back with a token and mints a new session -- so a
	// client that followed would end the chain holding a fresh cookie and this
	// would read as the sign-out having failed.
	no := &http.Client{Jar: b.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	res, err = no.Get(s.URL + "/sign-out")
	x.NoError(err)
	defer res.Body.Close()

	// The cookie is gone, which is the whole of what this app can do.
	for _, c := range b.Jar.Cookies(mustParse(t, s.URL)) {
		x.NotEqual("product_session", c.Name, "the session survived a sign-out")
	}

	// And the next page is a **redirect to the issuer** rather than a page --
	// so what happens next is the issuer's answer and not this app's.
	res, err = no.Get(s.URL + "/")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusFound, res.StatusCode)
	x.Contains(res.Header.Get("location"), p.URL)
}

// TestACallbackWithoutItsOwnStateIsRefused: the one hop somebody else can aim
// at this app.
func TestACallbackWithoutItsOwnStateIsRefused(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, audience)
	p.Subject = "somebody"

	_, s := serve(t, p)

	// No cookie at all.
	res, err := http.Get(s.URL + "/callback?code=the-code&state=made-up")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadRequest, res.StatusCode)

	// A browser that started one, answering with a different state.
	b := browser(t)
	no := &http.Client{Jar: b.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err = no.Get(s.URL + "/")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusFound, res.StatusCode)

	res, err = no.Get(s.URL + "/callback?code=the-code&state=not-the-one")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadRequest, res.StatusCode)
}

// TestATokenForSomebodyElsesApp is the check `authoidc` refuses to be built
// without, tried: a token of the same issuer, minted for another relying party.
func TestATokenForSomebodyElsesApp(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, audience)
	p.Subject = "somebody"

	a, _ := serve(t, p)

	other := p.Sign(t, map[string]any{
		"iss": p.URL,
		"aud": "another-app",
		"sub": "somebody",
		"exp": 4102444800,
	})
	_, err := a.verifier.Verify(t.Context(), other)
	x.Error(err, "a token for another relying party was accepted")
}

func read(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	return string(b)
}

func mustParse(t *testing.T, v string) *url.URL {
	t.Helper()
	u, err := url.Parse(v)
	require.NoError(t, err)

	return u
}
