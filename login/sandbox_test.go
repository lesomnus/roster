package login_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/login"
)

// TestTheSandboxWalksTheWholeFlow.
//
// `login.Sandbox` is not a test double and nothing asserts behaviour through
// it: what it says is `ok` to a password it has in a map. What this checks is
// the one thing that **can** rot, and would rot silently -- that the pages and
// the routes still fit each other. The sandbox serves the real `login.html` and
// the real consent template, so a page that starts posting somewhere nothing
// answers is a page that breaks here first and in a browser second.
func TestTheSandboxWalksTheWholeFlow(t *testing.T) {
	s := httptest.NewServer(login.NewSandbox().Handler())
	t.Cleanup(s.Close)

	browser := func(t *testing.T) *http.Client {
		jar, err := cookiejar.New(nil)
		require.NoError(t, err)

		return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	post := func(t *testing.T, b *http.Client, path, body string) *http.Response {
		t.Helper()
		res, err := b.Post(s.URL+path, "application/json", strings.NewReader(body))
		require.NoError(t, err)

		return res
	}
	get := func(t *testing.T, b *http.Client, path string) *http.Response {
		t.Helper()
		res, err := b.Get(s.URL + path)
		require.NoError(t, err)

		return res
	}
	body := func(t *testing.T, res *http.Response) string {
		t.Helper()
		defer res.Body.Close()
		v, err := io.ReadAll(res.Body)
		require.NoError(t, err)

		return string(v)
	}

	t.Run("the index says who there is to be", func(t *testing.T) {
		x := require.New(t)
		v := body(t, get(t, browser(t), "/"))
		x.Contains(v, "erin")
		x.Contains(v, "frank")
		x.Contains(v, "/login?login_challenge=sandbox")
	})

	t.Run("the form is the real one", func(t *testing.T) {
		x := require.New(t)
		v := body(t, get(t, browser(t), "/login?login_challenge=sandbox"))

		// The two things the page needs from whatever serves it.
		x.Contains(v, "./frontdoor.js")
		x.Contains(v, `id="more"`)

		res := get(t, browser(t), "/frontdoor.js")
		defer res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode, "the page imports a module nothing serves")
	})

	t.Run("a password alone finishes it for somebody with no factor", func(t *testing.T) {
		x := require.New(t)
		b := browser(t)
		get(t, b, "/login?login_challenge=sandbox").Body.Close()

		res := post(t, b, "/session", `{"alias":"erin","password":"nope"}`)
		res.Body.Close()
		x.Equal(http.StatusUnauthorized, res.StatusCode)

		res = post(t, b, "/session", `{"alias":"erin","password":"correct horse battery staple"}`)
		res.Body.Close()
		x.Equal(http.StatusNoContent, res.StatusCode)

		x.Contains(body(t, post(t, b, "/accept", "")), "/consent")
		x.Contains(body(t, get(t, b, "/consent?consent_challenge=sandbox")), `name="allow"`)

		res, err := b.PostForm(s.URL+"/consent", map[string][]string{"allow": {"1"}})
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)

		v := body(t, get(t, b, "/done"))
		x.Contains(v, "preferred_username")

		// The claims, and not the prose around them -- the page says the word
		// `methods` on purpose, to say it is never there.
		claims := v[strings.Index(v, "<pre>"):strings.Index(v, "</pre>")]
		x.NotContains(claims, "methods", "the sandbox shows a token claim the real one never mints")
	})

	t.Run("and the second form is asked for somebody with one", func(t *testing.T) {
		x := require.New(t)
		b := browser(t)
		get(t, b, "/login?login_challenge=sandbox").Body.Close()

		res := post(t, b, "/session", `{"alias":"frank","password":"correct horse battery staple"}`)
		v := body(t, res)
		x.Equal(http.StatusOK, res.StatusCode, "a password alone finished it for somebody with a factor")
		x.Contains(v, "totp", "the page has nothing to draw the second form from")

		res = post(t, b, "/accept", "")
		res.Body.Close()
		x.Equal(http.StatusUnauthorized, res.StatusCode, "half way was accepted")

		res = post(t, b, "/session/continue", `{"kind":"totp","secret":"000000"}`)
		res.Body.Close()
		x.Equal(http.StatusUnauthorized, res.StatusCode)

		res = post(t, b, "/session/continue", `{"kind":"totp","secret":"123456"}`)
		res.Body.Close()
		x.Equal(http.StatusNoContent, res.StatusCode)

		x.Contains(body(t, post(t, b, "/accept", "")), "/consent")

		// And a no is a no.
		get(t, b, "/consent?consent_challenge=sandbox").Body.Close()
		res, err := b.PostForm(s.URL+"/consent", map[string][]string{})
		x.NoError(err)
		res.Body.Close()

		x.Contains(body(t, get(t, b, "/done")), "not allowed")
	})
}
