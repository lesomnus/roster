package idptest_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/idptest"
)

func TestRegisteredRedirectsAreCheckedBeforeIssuingACode(t *testing.T) {
	const callback = "https://login.example.test/callback/acme/entra"
	for _, tc := range []struct {
		name   string
		change func(url.Values)
	}{
		{"old path", func(q url.Values) { q.Set("redirect_uri", "https://login.example.test/callback") }},
		{"path case", func(q url.Values) { q.Set("redirect_uri", "https://login.example.test/Callback/acme/entra") }},
		{"another tenant", func(q url.Values) { q.Set("redirect_uri", "https://login.example.test/callback/other/entra") }},
		{"another connection", func(q url.Values) { q.Set("redirect_uri", "https://login.example.test/callback/acme/other") }},
		{"another client", func(q url.Values) { q.Set("client_id", "another-app") }},
		{"repeated redirect", func(q url.Values) { q.Add("redirect_uri", callback) }},
		{"repeated client", func(q url.Values) { q.Add("client_id", "login-app") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			p := idptest.New(t, "login-app")
			p.RegisterRedirects(callback)
			q := url.Values{"client_id": {"login-app"}, "redirect_uri": {callback}, "state": {"the-state"}}
			tc.change(q)
			b := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			res, err := b.Get(p.URL + "/authorize?" + q.Encode())
			x.NoError(err)
			defer res.Body.Close()
			x.Equal(http.StatusBadRequest, res.StatusCode)
			x.Empty(res.Header.Get("Location"), "an unregistered callback received a redirect")
			x.Nil(p.Asked(), "a refused request issued a code")

			res, err = b.Post(p.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader("code=the-code"))
			x.NoError(err)
			defer res.Body.Close()
			x.Equal(http.StatusBadRequest, res.StatusCode, "an unissued code was exchanged")
		})
	}

	t.Run("an explicitly empty registration admits nobody", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		p.RegisterRedirects()
		b := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := b.Get(p.URL + "/authorize?" + url.Values{"client_id": {"login-app"}, "redirect_uri": {callback}}.Encode())
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusBadRequest, res.StatusCode)
	})
}
