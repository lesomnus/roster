package login_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth/authsession"

	"github.com/lesomnus/roster/login"
)

// kept is a copy of the cookies a browser holds for the app now, and a client
// that presents them whatever the browser is told to do with its own later.
//
// What stands in for *the delegation is still alive at roster*: a sealed
// cookie cannot be ended, so a copy of one goes on opening, and whether it is
// still somebody is whether roster still takes the delegation inside it.
func (d *deployment) kept(t *testing.T, b *http.Client) func(path string) *http.Response {
	t.Helper()
	x := require.New(t)

	at, err := url.Parse(d.app.URL)
	x.NoError(err)
	cookies := b.Jar.Cookies(at)
	x.NotEmpty(cookies, "the browser holds no cookie to keep a copy of")

	still := d.browser(t)
	still.Jar = nil

	return func(path string) *http.Response {
		t.Helper()

		req, err := http.NewRequest(http.MethodGet, d.app.URL+path, nil)
		x.NoError(err)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, err := still.Do(req)
		x.NoError(err)
		t.Cleanup(func() { res.Body.Close() })

		return res
	}
}

// TestEveryEndingRevokesTheDelegation: where this app ends a browser's session
// it ends the delegation the session held, at roster, which is the half that
// is a credential.
//
// `Door.End` revoked with the context it was handed. `DELETE /session` hands it
// one its flow narrowed to a tenant; a sign-out through the issuer, and the
// consent hop of a deployment that remembers nothing, handed it one no flow
// had narrowed. So `DelegationService/Revoke` went out as the bare deployment
// key, was refused, and the refusal was dropped: the cookie was cleared in
// that browser and the delegation lived on, for hours where `remember` is
// hours. A copy of the cookie presented to a flow Hydra skips answered 303,
// the person still read; right is 200, the form.
//
// The session says which tenant it is of, so the revocation is made there
// whoever asks for it.
func TestEveryEndingRevokesTheDelegation(t *testing.T) {
	for _, ending := range []struct {
		name     string
		remember time.Duration
		consent  login.Consent
		end      func(t *testing.T, d *deployment, b *http.Client)
	}{
		{"the app's own sign-out", time.Hour, login.Skip, func(t *testing.T, d *deployment, b *http.Client) {
			req, err := http.NewRequest(http.MethodDelete, d.app.URL+"/session?login_challenge=c1", nil)
			require.NoError(t, err)
			res, err := b.Do(req)
			require.NoError(t, err)
			res.Body.Close()
			require.Equal(t, http.StatusNoContent, res.StatusCode)
		}},
		{"a sign-out an app proved it started", time.Hour, login.Skip, func(t *testing.T, d *deployment, b *http.Client) {
			d.hydra.askedOf("l1", "contoso-web", d.who["contoso"].String(), true)

			res, err := b.Get(d.app.URL + "/logout?logout_challenge=l1")
			require.NoError(t, err)
			res.Body.Close()
			require.Equal(t, http.StatusSeeOther, res.StatusCode)
		}},
		{"a sign-out somebody confirmed", time.Hour, login.Skip, func(t *testing.T, d *deployment, b *http.Client) {
			d.hydra.askedOf("l1", "contoso-web", d.who["contoso"].String(), false)

			res, err := b.PostForm(d.app.URL+"/logout", url.Values{"logout_challenge": {"l1"}, "allow": {"1"}})
			require.NoError(t, err)
			res.Body.Close()
			require.Equal(t, http.StatusOK, res.StatusCode)
		}},
		{"the consent hop, where nothing is remembered", 0, login.Skip, func(t *testing.T, d *deployment, b *http.Client) {
			res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
			require.NoError(t, err)
			res.Body.Close()
			require.Equal(t, http.StatusSeeOther, res.StatusCode)
		}},
		{"a consent somebody refused, where nothing is remembered", 0, login.Ask, func(t *testing.T, d *deployment, b *http.Client) {
			res, err := b.PostForm(d.app.URL+"/consent", url.Values{"consent_challenge": {"c1"}})
			require.NoError(t, err)
			res.Body.Close()
			require.Less(t, res.StatusCode, http.StatusBadRequest)
		}},
	} {
		t.Run(ending.name, func(t *testing.T) {
			x := require.New(t)
			d := serveAs(t, ending.consent, func(c *login.Config) { c.Remember = ending.remember })
			b := d.browser(t)

			d.hydra.raise("c1", "contoso-web")
			_, code := d.signIn(t, b, "c1", "erin", password)
			x.Equal(http.StatusOK, code)

			still := d.kept(t, b)
			ending.end(t, d, b)

			// A flow Hydra skips, which is accepted only for a browser this
			// app can still read as Hydra's subject.
			d.hydra.remembers("c2", "contoso-web", d.who["contoso"].String())
			res := still("/login?login_challenge=c2")
			x.Equal(http.StatusOK, res.StatusCode,
				"the session was ended and the delegation it held is still somebody at roster")
		})
	}
}

// TestASignOutEndsOnlyItsOwnSubjectsSession: a logout challenge is about one
// subject's session at Hydra, and Hydra says whose. This app accepts it and
// ended **this browser's** own session with it, whoever that was: a browser
// signed in as erin that opened a logout challenge about somebody else had its
// cookie cleared. A logout challenge is raised for a hint presented by any
// browser, so that was anybody's to cause. Right is the cookie left alone, and
// the delegation with it.
//
// Hydra is still told yes. Whether somebody else's session at the issuer ends
// is the challenge's own business, and was never this browser's cookie's.
func TestASignOutEndsOnlyItsOwnSubjectsSession(t *testing.T) {
	for _, way := range []struct {
		name  string
		rp    bool
		leave func(t *testing.T, d *deployment, b *http.Client) *http.Response
	}{
		{"one an app proved it started", true, func(t *testing.T, d *deployment, b *http.Client) *http.Response {
			res, err := b.Get(d.app.URL + "/logout?logout_challenge=l1")
			require.NoError(t, err)

			return res
		}},
		{"one somebody confirmed", false, func(t *testing.T, d *deployment, b *http.Client) *http.Response {
			res, err := b.PostForm(d.app.URL+"/logout", url.Values{"logout_challenge": {"l1"}, "allow": {"1"}})
			require.NoError(t, err)

			return res
		}},
	} {
		t.Run(way.name, func(t *testing.T) {
			x := require.New(t)
			d := serveAs(t, login.Skip, func(c *login.Config) { c.Remember = time.Hour })
			b := d.browser(t)

			d.hydra.raise("c1", "contoso-web")
			_, code := d.signIn(t, b, "c1", "erin", password)
			x.Equal(http.StatusOK, code)

			// About somebody this browser is not.
			d.hydra.askedOf("l1", "fabrikam-web", d.who["fabrikam"].String(), way.rp)
			res := way.leave(t, d, b)
			defer res.Body.Close()

			x.True(d.hydra.ended("l1"), "hydra was not told the answer to its own challenge")
			for _, c := range res.Cookies() {
				x.NotEqual(authsession.InsecureCookie, c.Name,
					"a logout about somebody else wrote this browser's own session cookie")
			}

			d.hydra.remembers("c2", "contoso-web", d.who["contoso"].String())
			again, err := b.Get(d.app.URL + "/login?login_challenge=c2")
			x.NoError(err)
			defer again.Body.Close()
			x.Equal(http.StatusSeeOther, again.StatusCode,
				"a logout about somebody else signed this browser out")
		})
	}
}
