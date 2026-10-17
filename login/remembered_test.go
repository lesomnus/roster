package login_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestABrowserThisAppForgotIsAskedAgain is the other half of a skipped form:
// Hydra skipping it is only as good as this app being able to read whoever it
// skipped it for, because the consent hop reads the claims as them.
//
// It was taken for granted. A browser outlives this app's session in more ways
// than a clock -- a restart under a seal key made at start opens none of the
// cookies the last process sealed, a person erases the delegation from their
// account page, a cookie is cleared -- and each was a browser Hydra waved
// through and a token with `sub` alone in it, for as long as Hydra remembered
// (#121). So each of them is asked again, and a browser this app does know is
// still not.
func TestABrowserThisAppForgotIsAskedAgain(t *testing.T) {
	// arrives is a login challenge Hydra raised for a browser it remembers as
	// `subject`, and this browser bringing it to the app.
	arrives := func(t *testing.T, d *deployment, b *http.Client, challenge, subject string) *http.Response {
		t.Helper()
		d.hydra.remembers(challenge, "contoso-web", subject)

		res, err := b.Get(d.app.URL + "/login?login_challenge=" + challenge)
		require.NoError(t, err)
		t.Cleanup(func() { res.Body.Close() })

		return res
	}

	// signedIn is a browser that went through the form once, for c1.
	signedIn := func(t *testing.T, d *deployment) *http.Client {
		t.Helper()
		b := d.browser(t)
		d.hydra.raise("c1", "contoso-web")

		_, code := d.signIn(t, b, "c1", "erin", password)
		require.Equal(t, http.StatusOK, code)

		return b
	}

	remembering := func(c *login.Config) { c.Remember = time.Hour }

	t.Run("one it knows is waved through", func(t *testing.T) {
		x := require.New(t)
		d := serveAs(t, login.Skip, remembering)
		b := signedIn(t, d)

		res := arrives(t, d, b, "c2", d.who["contoso"].String())
		x.Equal(http.StatusSeeOther, res.StatusCode, "a browser this app knows was asked again")
		x.Equal("/consent?consent_challenge=c2", res.Header.Get("Location"))
	})

	t.Run("one it has no session for draws the form", func(t *testing.T) {
		x := require.New(t)
		d := serveAs(t, login.Skip, remembering)
		b := d.browser(t)

		res := arrives(t, d, b, "c1", d.who["contoso"].String())
		x.Equal(http.StatusOK, res.StatusCode, "a browser nobody here can read was waved through")
		accepted, _ := d.hydra.said()
		x.Zero(accepted, "hydra was told who this is before anybody said")

		// And signing in is the flow it would have been: Hydra is told the
		// person it remembered, and the token carries them.
		_, code := d.signIn(t, b, "c1", "erin", password)
		x.Equal(http.StatusOK, code)
		subject, _ := d.hydra.told()
		x.Equal(d.who["contoso"].String(), subject)

		res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)
		_, claims := d.hydra.told()
		x.Equal("erin", claims["preferred_username"])
	})

	t.Run("and so does one whose delegation roster no longer takes", func(t *testing.T) {
		x := require.New(t)
		d := serveAs(t, login.Skip, remembering)
		b := signedIn(t, d)

		// What the account page's list of sessions does: the delegation is
		// erased, and the cookie this app sealed goes on opening.
		vs, err := d.s.Ungated.Delegation().List(t.Context(), rstr.DelegationListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems())
		for _, v := range vs.GetItems() {
			_, err := d.s.Ungated.Delegation().Erase(t.Context(), rstr.DelegationRef_builder{Id: v.GetId()}.Build())
			x.NoError(err)
		}

		res := arrives(t, d, b, "c2", d.who["contoso"].String())
		x.Equal(http.StatusOK, res.StatusCode, "a session whose delegation is gone was read as somebody")
	})

	t.Run("and so does one that is somebody else's", func(t *testing.T) {
		x := require.New(t)
		d := serveAs(t, login.Skip, remembering)
		b := signedIn(t, d)

		// Hydra remembers this browser as a person this app's session is not:
		// the claims it could read are not theirs.
		res := arrives(t, d, b, "c2", d.who["fabrikam"].String())
		x.Equal(http.StatusOK, res.StatusCode, "one person's session was read for another")
	})

	// A client that asked for no screen is told so rather than drawn one: a
	// form in a frame nobody looks at is a renewal that never answers.
	t.Run("and prompt=none is told login_required", func(t *testing.T) {
		x := require.New(t)
		d := serveAs(t, login.Skip, remembering)
		b := d.browser(t)
		d.hydra.asking["c1"] = url.Values{"prompt": {"none"}}

		res := arrives(t, d, b, "c1", d.who["contoso"].String())
		x.Equal(http.StatusSeeOther, res.StatusCode)
		x.Equal("/refused", res.Header.Get("Location"))
		x.Equal("login_required", d.hydra.refusedLogin("c1"))
		accepted, _ := d.hydra.said()
		x.Zero(accepted)
	})
}

// TestAConsentReadsOnlyItsOwnSubject is the same check one hop on: a token
// names one person, and what it says about them is read as that person or not
// at all.
//
// A browser's session can change hands between the hops of a flow -- another
// tab signs in as somebody else -- and the consent hop read whoever the session
// was, into a token Hydra had already given the first person's `sub`.
func TestAConsentReadsOnlyItsOwnSubject(t *testing.T) {
	x := require.New(t)
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Remember = time.Hour })
	b := d.browser(t)

	d.hydra.raise("c1", "contoso-web")
	_, code := d.signIn(t, b, "c1", "erin", password)
	x.Equal(http.StatusOK, code)

	// The login was accepted as somebody this browser's session is not.
	d.hydra.mu.Lock()
	d.hydra.subjectOf["c1"] = d.who["fabrikam"].String()
	d.hydra.mu.Unlock()

	res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
	x.NoError(err)
	defer res.Body.Close()

	_, claims := d.hydra.told()
	x.NotContains(claims, "preferred_username", "one person's claims went into another's token")

	// Nor is it handed out with less in it: it is nobody's to read.
	d.hydra.mu.Lock()
	why := d.hydra.refusal
	d.hydra.mu.Unlock()
	x.Equal("login_required", why)
}

// TestATokenIsNotHandedOutWithoutTheClaimsItAskedFor is the consent hop with
// nobody to read: refused, rather than granted with `sub` alone.
//
// It was granted, and that is the token #121 was about. A client cannot tell a
// token missing its claims from a person who has none, so the failure surfaced
// far from here -- a relying party keying its accounts on `email` signing
// nobody in. `login_required` says what happened, and the sign-in a client
// starts on hearing it is a skipped login this app asks again.
func TestATokenIsNotHandedOutWithoutTheClaimsItAskedFor(t *testing.T) {
	refusal := func(d *deployment) (string, map[string]any) {
		d.hydra.mu.Lock()
		defer d.hydra.mu.Unlock()

		return d.hydra.refusal, d.hydra.claims
	}

	t.Run("a client that asked for the person is refused", func(t *testing.T) {
		x := require.New(t)
		d := serve(t)
		b := d.browser(t)
		d.hydra.raise("c1", "contoso-web")

		res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)
		x.Equal("/denied", res.Header.Get("Location"))

		why, claims := refusal(d)
		x.Equal("login_required", why)
		x.Nil(claims, "a token was handed out with sub alone")
	})

	t.Run("and one that asked for sub alone gets it", func(t *testing.T) {
		x := require.New(t)
		d := serve(t)
		b := d.browser(t)
		d.hydra.raiseWith("c1", "contoso-web", "openid", "offline")

		res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)
		x.Equal("/done", res.Header.Get("Location"), "a client that asked for nobody's claims was refused for not having them")

		why, _ := refusal(d)
		x.Empty(why)
	})
}
