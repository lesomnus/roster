package login_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/login"
)

// whereTo is what `POST /consent` answered: a status, and where it says the
// browser goes.
func whereTo(t *testing.T, res *http.Response) (int, string) {
	t.Helper()
	defer res.Body.Close()

	var v struct {
		To string `json:"redirect_to"`
	}
	if res.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(res.Body).Decode(&v))
	}

	return res.StatusCode, v.To
}

// TestAConsentIsAnsweredWhereThePageCanFollowIt: the consent screen's answer is
// read by the page that drew it, as every other screen's is.
//
// `POST /consent` answered a 303 to Hydra. The page asks with `fetch`, and a
// `fetch` cannot follow a redirect to another origin: the issuer is not this
// app's origin and sends no CORS headers, so the answer was an error the page
// could not read, the buttons stayed disabled, and a deployment that turned
// `consent: ask` on had a screen nobody could get past. Nothing here saw it --
// the rig's walk is curl, which follows anything, and the page's own spec runs
// against a made-up server whose redirect was to itself.
//
// `{"redirect_to": …}`, which is `POST /accept`'s answer and for its reason:
// the page reads where to go and goes there.
func TestAConsentIsAnsweredWhereThePageCanFollowIt(t *testing.T) {
	asked := func(t *testing.T, scope ...string) (*deployment, *http.Client) {
		t.Helper()
		d := serveWith(t, login.Ask)
		b := d.browser(t)
		d.hydra.raiseWith("c1", "contoso-web", scope...)

		_, code := d.signIn(t, b, "c1", "erin", password)
		require.Equal(t, http.StatusOK, code)

		return d, b
	}

	t.Run("a yes", func(t *testing.T) {
		x := require.New(t)
		d, b := asked(t, "openid", "profile")

		res, err := b.PostForm(d.app.URL+"/consent", url.Values{"consent_challenge": {"c1"}, "allow": {"1"}})
		x.NoError(err)
		code, to := whereTo(t, res)
		x.Equal(http.StatusOK, code, "a consent was answered with something a page's fetch cannot read")
		x.Equal("/done", to)
		x.Equal("no-store", res.Header.Get("Cache-Control"))

		_, claims := d.hydra.told()
		x.Equal("erin", claims["preferred_username"])
	})

	t.Run("a no", func(t *testing.T) {
		x := require.New(t)
		d, b := asked(t, "openid", "profile")

		res, err := b.PostForm(d.app.URL+"/consent", url.Values{"consent_challenge": {"c1"}})
		x.NoError(err)
		code, to := whereTo(t, res)
		x.Equal(http.StatusOK, code)
		x.Equal("/denied", to)

		_, rejected := d.hydra.said()
		x.True(rejected)
	})

	// What the consent hop refuses on its own account goes back to the client
	// through Hydra as well, and is answered the same way.
	t.Run("and a yes this app refuses", func(t *testing.T) {
		x := require.New(t)
		d, b := asked(t, "openid", "profile")
		d.hydra.mu.Lock()
		d.hydra.audience["c1"] = []string{"urn:example:api:levyan"}
		d.hydra.mu.Unlock()

		res, err := b.PostForm(d.app.URL+"/consent", url.Values{"consent_challenge": {"c1"}, "allow": {"1"}})
		x.NoError(err)
		code, to := whereTo(t, res)
		x.Equal(http.StatusOK, code)
		x.Equal("/denied", to)

		d.hydra.mu.Lock()
		defer d.hydra.mu.Unlock()
		x.Equal("invalid_target", d.hydra.refusal)
	})

	// A browser Hydra sent here is still redirected: nothing is fetching.
	t.Run("and a consent nobody is asked about is still a redirect", func(t *testing.T) {
		x := require.New(t)
		d := serve(t)
		b := d.browser(t)
		d.hydra.raise("c1", "contoso-web")
		_, code := d.signIn(t, b, "c1", "erin", password)
		x.Equal(http.StatusOK, code)

		res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)
		x.Equal("/done", res.Header.Get("Location"))
	})
}

// TestADeviceIsGrantedOnAScreen: a consent that belongs to a device's flow is
// drawn, whatever the deployment's `consent` says and whatever Hydra remembers.
//
// The screen where somebody types a device's code cannot say what is being
// authorised: Hydra offers no way to read a device challenge, so there is no
// client to name on it. This app's answer was that the consent screen names it
// one hop later, *before anything is granted* -- and under `consent: skip`,
// which is the default, there is no consent screen. Somebody who typed a code
// and signed in had granted a device a token with nothing having named whose
// device it was: 303. With a browser Hydra remembers, typing the code was the
// whole of it. Right is 200, the screen, and nothing granted until it is
// answered.
//
// `skip` is right where it was meant: a browser a client sent here came from
// that client. A code is typed by somebody who was sent an address.
//
// # How it is told
//
// By the request Hydra recorded. Its consent request has no field that says a
// device began the flow (v26.2.0, read off the image); what differs is
// `request_url`, which is the authorization endpoint with the whole request for
// a client's redirect and the verification endpoint for a device's code. The
// fake here answers as the real one was seen to, and `docker/device.sh` holds
// it against the real one.
func TestADeviceIsGrantedOnAScreen(t *testing.T) {
	// typed is somebody who typed a device's code and signed in: the consent
	// challenge Hydra raises next is theirs to open.
	typed := func(t *testing.T, how login.Consent) (*deployment, *http.Client) {
		t.Helper()
		d := serveWith(t, how)
		b := d.browser(t)
		d.hydra.printedFor("c1", "contoso-tv")

		_, code := d.signIn(t, b, "c1", "erin", password)
		require.Equal(t, http.StatusOK, code, "a device's flow resolved no tenant")

		return d, b
	}

	// drawn is the consent hop answering with the screen, Hydra told nothing,
	// and the screen able to say which client it is about.
	drawn := func(t *testing.T, d *deployment, b *http.Client) {
		t.Helper()
		x := require.New(t)

		res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode, "a device was granted with no screen naming it")

		_, rejected := d.hydra.said()
		_, claims := d.hydra.told()
		x.Nil(claims, "a device was granted before anybody was asked")
		x.False(rejected)

		res, err = b.Get(d.app.URL + "/flow?consent_challenge=c1")
		x.NoError(err)
		defer res.Body.Close()
		var asking struct {
			Client string `json:"client"`
		}
		x.NoError(json.NewDecoder(res.Body).Decode(&asking))
		x.Equal("contoso-tv", asking.Client, "the screen has nothing to say whose device this is")
	}

	t.Run("where the deployment draws no consent", func(t *testing.T) {
		x := require.New(t)
		d, b := typed(t, login.Skip)
		drawn(t, d, b)

		// And the screen's yes is what grants it.
		res, err := b.PostForm(d.app.URL+"/consent", url.Values{"consent_challenge": {"c1"}, "allow": {"1"}})
		x.NoError(err)
		code, to := whereTo(t, res)
		x.Equal(http.StatusOK, code)
		x.Equal("/done", to)
		_, claims := d.hydra.told()
		x.Equal("erin", claims["preferred_username"])
	})

	t.Run("and where hydra remembers one", func(t *testing.T) {
		d, b := typed(t, login.Ask)
		d.hydra.consents("c1")
		drawn(t, d, b)
	})

	// The other half of the same line: what Hydra remembers of a client's own
	// redirect is still answered without asking again.
	t.Run("a client's redirect hydra remembers a consent for is not asked again", func(t *testing.T) {
		x := require.New(t)
		d := serveWith(t, login.Ask)
		b := d.browser(t)
		d.hydra.raise("c1", "contoso-web")
		_, code := d.signIn(t, b, "c1", "erin", password)
		x.Equal(http.StatusOK, code)
		d.hydra.consents("c1")

		res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusSeeOther, res.StatusCode)
	})
}
