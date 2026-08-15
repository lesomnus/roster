package cmd_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cmd"
)

// TestTheConsoleSignsInAsAnRpc is the whole console front door, as the browser
// runs it.
//
// Every call is a generated one — nothing here reads a document to know what
// signing in looks like, which is why the service is in the schema rather than
// being an HTTP endpoint beside it.
func TestTheConsoleSignsInAsAnRpc(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)
	secret := passwordFrom(t, out)

	g, err := s.GrpcControl(ctx, cmd.Config{})
	x.NoError(err)

	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	x.NoError(err)
	c := &http.Client{Jar: jar}

	post := func(path, body string) (int, string) {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(body))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")

		res, err := c.Do(req)
		x.NoError(err)
		defer res.Body.Close()

		b, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(b)
	}

	const signIn = "/roster.AuthService/SignIn"
	const signOut = "/roster.AuthService/SignOut"

	t.Run("a wrong password is one answer", func(t *testing.T) {
		x := require.New(t)

		code, _ := post(signIn, `{"alias":"ops","password":"no"}`)
		x.Equal(http.StatusUnauthorized, code)
		x.Empty(jar.Cookies(mustURL(t, srv.URL)), "a refusal set a cookie")
	})

	t.Run("and the right one sets the cookie", func(t *testing.T) {
		x := require.New(t)

		code, body := post(signIn, `{"alias":"ops","password":"`+secret+`"}`)
		x.Equal(http.StatusOK, code, body)

		cs := jar.Cookies(mustURL(t, srv.URL))
		x.Len(cs, 1)
		x.NotEmpty(cs[0].Value)

		// The credential is **not** in the answer. It is in the cookie, which
		// this page never sees.
		x.NotContains(body, cs[0].Value)
	})

	t.Run("which is a credential for the next call", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"ops"`)
	})

	// The thing the generated `Add` structurally cannot do: answer with the
	// secret it just made.
	t.Run("and issues a key, readable once", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.IssueService/IssueKey",
			`{"service":"custody","alias":"production","methods":["/roster.VouchService/Verify"]}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"rk_`, "the key was not in the answer")

		// And never again. The row holds a hash.
		code, body = post("/roster.ApiKeyService/List", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"production"`)
		x.NotContains(body, `"rk_`)
	})

	t.Run("a key that allows nothing is refused", func(t *testing.T) {
		x := require.New(t)

		code, _ := post("/roster.IssueService/IssueKey",
			`{"service":"custody","alias":"empty","methods":[]}`)
		x.Equal(http.StatusBadRequest, code)
	})

	// `Add` is closed now that `Issue` exists: serving it would offer a key
	// whose secret somebody else chose, in a prefix they picked.
	t.Run("and Add is not a way around it", func(t *testing.T) {
		x := require.New(t)

		code, _ := post("/roster.ApiKeyService/Add",
			`{"alias":"mine","secret":"AAAA","methods":["/roster.*/*"]}`)
		x.NotEqual(http.StatusOK, code)
	})

	t.Run("signing out ends it", func(t *testing.T) {
		x := require.New(t)

		code, body := post(signOut, `{}`)
		x.Equal(http.StatusOK, code, body)

		code, _ = post("/roster.MeService/Get", `{}`)
		x.Equal(http.StatusUnauthorized, code, "the session outlived the sign-out")
	})
}

func mustURL(t *testing.T, v string) *url.URL {
	t.Helper()

	u, err := url.Parse(v)
	require.NoError(t, err)

	return u
}
