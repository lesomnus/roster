package login_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
)

// TestGoogleIsAskedWhenItLastAskedThem: Google says when it last authenticated
// somebody only to an app that asks for `auth_time` by name, so every round
// trip to Google's sign-in asks -- and asks nothing Google does not take. Any
// other provider is asked neither, because what it says unasked is all a
// sign-in here has ever read of it.
func TestGoogleIsAskedWhenItLastAskedThem(t *testing.T) {
	for _, c := range []struct {
		name   string
		google bool
	}{
		{"Google", true},
		{"any other provider", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			p := idptest.New(t, "login-app")
			d := serveAs(t, login.Skip, func(cfg *login.Config) {
				if c.google {
					// The fake, as Google's sign-in.
					cfg.Google = p.URL
				}
			})
			d.connect(t, p, "google")
			d.identity(t, "google", "erin-at-google")
			p.Subject = "erin-at-google"
			d.hydra.raise("c1", "contoso-web")

			res := d.through(t, d.browser(t), "c1", "google")
			x.Equal(http.StatusSeeOther, res.StatusCode)

			asked := p.Asked()
			if c.google {
				x.JSONEq(`{"id_token":{"auth_time":{"essential":true}}}`, asked.Get("claims"))
			} else {
				x.False(asked.Has("claims"), "a provider that is not Google was asked in Google's words")
			}
			x.False(asked.Has("max_age"), "a sign-in nobody asked to be a moment ago asked the provider to ask again")
			x.False(asked.Has("prompt"))
		})
	}
}
