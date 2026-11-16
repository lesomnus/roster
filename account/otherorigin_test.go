package account_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
)

// TestTheAppsOwnRoutesTakeNoRequestFromAnotherOrigin: what changes something
// at the account app is asked by its own page, or by something that is not a
// browser, and by no other site.
//
// # What it was
//
// The page's road to roster (`/roster.*`) asks for a header a form cannot set,
// and its comment says that is what stops another site. The routes this app
// writes by hand asked for nothing: `/device/approve`, `/verify`, `/claim`,
// `/prove`, `/leave/link`, `/ways` and `POST /session` read a body whatever its
// content type and looked at no `Origin`. `SameSite=Lax` on the cookie was the
// whole of it, and that is about another **site**: another host of the same
// registrable domain -- the organisation's blog, a second app of the tenant's
// -- is the same site, and its form's POST carries the cookie.
//
// The one that mints is `/device/approve`. A terminal anybody started is shown
// a code; a `text/plain` form on a sibling host, opened by an administrator
// signed in at the account page, posted the code, and the terminal's next poll
// was answered a key that acts as the administrator: seen on a served build
// (#277). And `POST /session` needs no cookie at all, so a form on any site
// signed a browser in as somebody of the form's choosing.
func TestTheAppsOwnRoutesTakeNoRequestFromAnotherOrigin(t *testing.T) {
	d := serve(t, account.Invited(), terminal)
	mints(t, d)

	// erin, signed in at the account page.
	b := d.browser(t, "contoso.test")
	code, body := b.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	require.Equal(t, http.StatusNoContent, code, body)

	// from is a request as a browser sends one from some page: what the page's
	// site is to this one, and where the page is.
	from := func(site, origin string) func(*http.Request) {
		return func(r *http.Request) {
			// What a form may send without asking first.
			r.Header.Set("Content-Type", "text/plain")
			if site != "" {
				r.Header.Set("Sec-Fetch-Site", site)
			}
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
		}
	}

	t.Run("a terminal's code is not approved by another site's form", func(t *testing.T) {
		x := require.New(t)

		// A terminal, which is no browser and says nothing of an origin.
		code, body := d.browser(t, "contoso.test").do(t, http.MethodPost, "/device/begin",
			`{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
		x.Equal(http.StatusOK, code, body)
		var began struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
		x.NoError(json.Unmarshal([]byte(body), &began))

		for name, with := range map[string]func(*http.Request){
			"a sibling host":                      from("same-site", "https://blog.contoso.test"),
			"another site":                        from("cross-site", "https://elsewhere.example"),
			"a browser too old to say, elsewhere": from("", "https://elsewhere.example"),
		} {
			code, body := b.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+began.UserCode+`"}`, with)
			x.Equal(http.StatusForbidden, code, "%s: %s", name, body)
		}

		code, body = d.browser(t, "contoso.test").do(t, http.MethodGet, "/device/poll?device_code="+began.DeviceCode, "", nil)
		x.Equal(http.StatusBadRequest, code, body)
		x.Contains(body, "authorization_pending", "a terminal was handed a key by another site's form")

		// The page's own request, which says where it is from.
		code, body = b.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+began.UserCode+`"}`,
			from("same-origin", "http://contoso.test"))
		x.Equal(http.StatusOK, code, body)
	})

	t.Run("nor is anything else that changes something", func(t *testing.T) {
		x := require.New(t)

		for _, path := range []string{"/verify", "/claim", "/prove", "/leave/link", "/ways?connection=example", "/recover", "/session", "/session/continue", "/leave", "/redeem", "/confirm"} {
			code, body := b.do(t, http.MethodPost, path, `{}`, from("same-site", "https://blog.contoso.test"))
			x.Equal(http.StatusForbidden, code, "%s: %s", path, body)
		}

		code, body := b.do(t, http.MethodDelete, "/session", "", from("same-site", "https://blog.contoso.test"))
		x.Equal(http.StatusForbidden, code, "DELETE /session: %s", body)

		// And she is still signed in.
		code, _ = b.rpc(t, "/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code)
	})

	t.Run("what only reads is not asked where it is from", func(t *testing.T) {
		x := require.New(t)

		for _, path := range []string{"/providers", "/session"} {
			code, body := b.do(t, http.MethodGet, path, "", from("cross-site", "https://elsewhere.example"))
			x.Equal(http.StatusOK, code, "%s: %s", path, body)
		}
	})

	t.Run("and no page of it is drawn inside another's", func(t *testing.T) {
		x := require.New(t)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.app.URL+"/providers", nil)
		x.NoError(err)
		req.Host = "contoso.test"
		res, err := b.Do(req)
		x.NoError(err)
		res.Body.Close()

		x.Equal("DENY", res.Header.Get("X-Frame-Options"))
		x.True(strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'"),
			"content-security-policy: %q", res.Header.Get("Content-Security-Policy"))
	})
}
