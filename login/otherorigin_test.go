package login_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
)

// from is a request as a browser sends one from some page: what the page's
// site is to this one, and where the page is.
func from(site, origin string) func(*http.Request) {
	return func(r *http.Request) {
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
	}
}

// elsewhere is every way a browser says a request is another origin's: a host
// beside this one, which a cookie's `SameSite` reads as the same site; another
// site altogether; and a browser too old to say which, that says where.
var elsewhere = map[string]func(*http.Request){
	"a sibling host":                      from("same-site", "https://blog.example.test"),
	"another site":                        from("cross-site", "https://elsewhere.example"),
	"a browser too old to say, elsewhere": from("", "https://elsewhere.example"),
}

// ask is one request to the app, with what a form may send without asking.
func (d *deployment) ask(t *testing.T, b *http.Client, method, path, kind, body string, with func(*http.Request)) *http.Response {
	t.Helper()

	var in io.Reader
	if body != "" {
		in = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, d.app.URL+path, in)
	require.NoError(t, err)
	if kind != "" {
		req.Header.Set("Content-Type", kind)
	}
	if with != nil {
		with(req)
	}

	res, err := b.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { res.Body.Close() })

	return res
}

// TestTheLoginAppsOwnRoutesTakeNoRequestFromAnotherOrigin: what changes
// something at the Login App is asked by its own page, or by something that is
// not a browser, and by no other origin.
//
// # What it was
//
// Nothing. No route looked at where a request was from, and `SameSite=Lax` on
// the session cookie was the whole of it -- which is about another **site**,
// where another host of the same registrable domain is the same one, and
// which is about nothing at all for the routes that need no cookie. Those are
// most of them: `POST /consent`, `POST /logout` and `POST /device` are bound by
// the challenge in their body and by nothing the browser holds, and `POST
// /session` reads a JSON body whatever its content type and signs the browser
// in as whoever the body names. Each answered another origin's form as it
// answers its own page: 303, 200, 200, 204 (#278). Right is 403 for all of
// them, and nothing done.
//
// It is the account app's rule and the account app's wrapper
// (`frontdoor.Guard`, `TestTheAppsOwnRoutesTakeNoRequestFromAnotherOrigin`).
// Nothing a flow needs is another origin's request that changes something:
// what Hydra and a provider send the browser here with are navigations, which
// only read.
func TestTheLoginAppsOwnRoutesTakeNoRequestFromAnotherOrigin(t *testing.T) {
	form := "application/x-www-form-urlencoded"
	creds := `{"alias":"erin","password":"` + password + `"}`

	t.Run("nobody is signed in by another origin's form", func(t *testing.T) {
		d := serveWith(t, login.Ask)
		d.hydra.raise("c1", "contoso-web")

		for name, with := range elsewhere {
			// `text/plain`, which a form may send and a JSON reader took.
			res := d.ask(t, d.browser(t), http.MethodPost, "/session?login_challenge=c1", "text/plain", creds, with)
			require.Equal(t, http.StatusForbidden, res.StatusCode, name)
			require.Empty(t, res.Cookies(), "%s: a browser was handed a session by another origin's form", name)
		}
	})

	t.Run("nor is a consent answered, a sign-out confirmed or a device's code typed", func(t *testing.T) {
		x := require.New(t)
		d := serveWith(t, login.Ask)
		b := d.browser(t)

		d.hydra.raise("c1", "contoso-web")
		_, code := d.signIn(t, b, "c1", "erin", password)
		x.Equal(http.StatusOK, code)
		d.hydra.asked("l1", "contoso-web", false)
		d.hydra.printed("d1", "WDJB-MJHT")

		for name, with := range elsewhere {
			res := d.ask(t, b, http.MethodPost, "/consent", form,
				url.Values{"consent_challenge": {"c1"}, "allow": {"1"}}.Encode(), with)
			x.Equal(http.StatusForbidden, res.StatusCode, "POST /consent, %s", name)

			res = d.ask(t, b, http.MethodPost, "/logout", form,
				url.Values{"logout_challenge": {"l1"}, "allow": {"1"}}.Encode(), with)
			x.Equal(http.StatusForbidden, res.StatusCode, "POST /logout, %s", name)

			res = d.ask(t, b, http.MethodPost, "/device", form,
				url.Values{"device_challenge": {"d1"}, "user_code": {"WDJB-MJHT"}}.Encode(), with)
			x.Equal(http.StatusForbidden, res.StatusCode, "POST /device, %s", name)

			res = d.ask(t, b, http.MethodPost, "/accept?login_challenge=c1", "", "", with)
			x.Equal(http.StatusForbidden, res.StatusCode, "POST /accept, %s", name)

			res = d.ask(t, b, http.MethodPost, "/session/continue?login_challenge=c1", "text/plain", `{}`, with)
			x.Equal(http.StatusForbidden, res.StatusCode, "POST /session/continue, %s", name)

			res = d.ask(t, b, http.MethodDelete, "/session?login_challenge=c1", "", "", with)
			x.Equal(http.StatusForbidden, res.StatusCode, "DELETE /session, %s", name)
		}

		// And nothing was done: no consent granted or refused, no sign-out
		// answered, no code handed to Hydra, and the one login accepted is the
		// one the page's own walk made.
		accepted, rejected := d.hydra.said()
		x.Equal(1, accepted)
		x.False(rejected, "a consent was refused by another origin's form")
		_, claims := d.hydra.told()
		x.Nil(claims, "a consent was granted by another origin's form")
		x.False(d.hydra.ended("l1"), "a sign-out was confirmed by another origin's form")
		x.False(d.hydra.refused("l1"))
		_, typed := d.hydra.sent("d1")
		x.False(typed, "a device's code was handed to hydra by another origin's form")

		// She is still signed in, and her own page's answer is taken.
		res := d.ask(t, b, http.MethodPost, "/consent", form,
			url.Values{"consent_challenge": {"c1"}, "allow": {"1"}}.Encode(), from("same-origin", d.app.URL))
		x.Less(res.StatusCode, http.StatusBadRequest, "the page's own answer was refused")
		_, claims = d.hydra.told()
		x.Equal("erin", claims["preferred_username"])
	})

	// Hydra's redirects and a provider's are navigations from another site,
	// and each lands on a `GET`.
	t.Run("what only reads is not asked where it is from", func(t *testing.T) {
		x := require.New(t)
		d := serveWith(t, login.Ask)
		b := d.browser(t)

		d.hydra.raise("c1", "contoso-web")
		_, code := d.signIn(t, b, "c1", "erin", password)
		x.Equal(http.StatusOK, code)
		d.hydra.raise("c2", "contoso-web")
		d.hydra.asked("l1", "contoso-web", false)

		hydra := from("cross-site", "")
		for _, path := range []string{
			"/login?login_challenge=c2",
			"/flow?login_challenge=c2",
			"/consent?consent_challenge=c1",
			"/logout?logout_challenge=l1",
			"/device?device_challenge=d1",
			"/signed-out",
		} {
			res := d.ask(t, b, http.MethodGet, path, "", "", hydra)
			x.Equal(http.StatusOK, res.StatusCode, path)
		}

		// A provider's way back is refused for carrying no state of this
		// browser's, which is the callback's own answer and not the guard's.
		res := d.ask(t, b, http.MethodGet, "/callback/contoso/entra?code=x&state=y", "", "", hydra)
		x.Equal(http.StatusBadRequest, res.StatusCode)
	})

	t.Run("and no page of it is drawn inside another's", func(t *testing.T) {
		x := require.New(t)
		d := serve(t)
		d.hydra.raise("c1", "contoso-web")

		res := d.ask(t, d.browser(t), http.MethodGet, "/login?login_challenge=c1", "", "", nil)
		x.Equal(http.StatusOK, res.StatusCode)
		x.Equal("DENY", res.Header.Get("X-Frame-Options"))
		x.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
	})
}

// TestTheRoundTripsCookieIsThisHostsAlone: the cookie that binds a browser to
// the round trip it began carries the `__Host-` prefix wherever cookies are
// `Secure`, as the session's always has.
//
// It was `login_state`, bare. A cookie of a bare name is any host of the
// domain's to set, so a host beside this one could hand a browser the state of
// a round trip of its own choosing and have that browser's callback finish it.
// The account app's took the prefix for that reason (#277) and this one had
// not: `Set-Cookie: login_state=…`; right is `__Host-login_state=…`, and a
// callback carrying the bare one is refused.
func TestTheRoundTripsCookieIsThisHostsAlone(t *testing.T) {
	// begun is a round trip begun and gone to the directory and back as far as
	// the address the directory sends the browser to, with the cookie the
	// browser was handed for it. No jar: a jar keeps a `Secure` cookie from a
	// server it reached over plain http and never sends it back, and a test
	// server is plain http.
	begun := func(t *testing.T, d *deployment) (*http.Cookie, string) {
		t.Helper()
		x := require.New(t)
		b := d.browser(t)
		b.Jar = nil

		res, err := b.Get(d.app.URL + "/provider?login_challenge=c1&connection=entra")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusFound, res.StatusCode)
		x.Len(res.Cookies(), 1)

		back, err := b.Get(res.Header.Get("location"))
		x.NoError(err)
		defer back.Body.Close()
		x.Equal(http.StatusFound, back.StatusCode)

		return res.Cookies()[0], back.Header.Get("location")
	}

	// lands is the browser arriving back with a cookie of that name.
	lands := func(t *testing.T, d *deployment, at, name, value string) *http.Response {
		t.Helper()
		b := d.browser(t)
		b.Jar = nil

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, at, nil)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{Name: name, Value: value})
		res, err := b.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { res.Body.Close() })

		return res
	}

	t.Run("where cookies are Secure", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.InsecureCookie = false })
		d.connect(t, p, "entra")
		d.identity(t, "entra", "erin-at-entra")
		p.Subject = "erin-at-entra"
		d.hydra.raise("c1", "contoso-web")

		c, back := begun(t, d)
		x.Equal("__Host-login_state", c.Name)
		x.True(c.Secure)
		x.True(c.HttpOnly)
		x.Equal("/", c.Path)
		x.Empty(c.Domain, "a `__Host-` cookie names no domain, or a browser refuses it")

		res := lands(t, d, back, "login_state", c.Value)
		x.Equal(http.StatusBadRequest, res.StatusCode, "a cookie a host beside this one could set finished the round trip")
		accepted, _ := d.hydra.said()
		x.Zero(accepted)

		res = lands(t, d, back, c.Name, c.Value)
		x.Equal(http.StatusSeeOther, res.StatusCode)

		// And it is cleared under the name, and with the mark, it was set with:
		// a browser takes no `__Host-` cookie that is not `Secure`, a clearing
		// one included.
		cleared := false
		for _, v := range res.Cookies() {
			if v.Name == c.Name && v.MaxAge < 0 {
				cleared = v.Secure && v.Path == "/"
			}
		}
		x.True(cleared, "the round trip's cookie was left in the browser: %v", res.Header.Values("Set-Cookie"))
	})

	// A development rig over plain http, which said so: a browser takes no
	// `__Host-` cookie there, so the name is bare, as the account app's is.
	t.Run("and bare where the deployment said they are not", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		d := serve(t)
		d.connect(t, p, "entra")
		d.hydra.raise("c1", "contoso-web")

		c, _ := begun(t, d)
		x.Equal("login_state", c.Name)
		x.False(c.Secure)
	})
}
