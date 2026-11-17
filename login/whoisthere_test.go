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

// acceptedAs is who a login challenge was accepted as, or nothing: what a real
// Hydra goes on to mint a token for.
func (h *hydra) acceptedAs(challenge string) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.subjectOf[challenge]
}

// TestAnAcceptAsksRosterWhoIsThere is `POST /accept` asking the one thing a
// sealed cookie cannot answer: whether the sign-in it names still stands.
//
// The cookie says who signed in and opens until its own clock runs out, which
// with `remember` set is hours. Whether that sign-in still stands is roster's
// to say -- signed out of, signed out everywhere, suspended -- and each of
// those ends the delegation there and reaches no cookie. `GET /login` asked
// (`reads`, on Hydra's `skip`); `POST /accept` took the cookie's word and told
// Hydra, so each of the three below answered 200 with somewhere to go, and
// Hydra went on to mint tokens for somebody roster had ended. Right is 401, and
// Hydra told nobody.
//
// It is the account app's rule, one front door along (`account.App.present`):
// what an app does for somebody with its own authority, it asks roster about
// them first.
func TestAnAcceptAsksRosterWhoIsThere(t *testing.T) {
	remembering := func(c *login.Config) { c.Remember = time.Hour }

	// formed is a browser that answered the form at c1 and has told Hydra
	// nothing yet: the session is there, made for this very challenge, so what
	// refuses it below is roster's answer and nothing else.
	formed := func(t *testing.T, d *deployment) *http.Client {
		t.Helper()
		b := d.browser(t)
		d.hydra.raise("c1", "contoso-web")

		res := d.post(t, b, "/session", "c1", map[string]string{"alias": "erin", "password": password})
		res.Body.Close()
		require.Equal(t, http.StatusNoContent, res.StatusCode)

		return b
	}

	// refused is the last hop answering 401, and Hydra hearing of nobody.
	refused := func(t *testing.T, d *deployment, res *http.Response, why string) {
		t.Helper()
		defer res.Body.Close()

		require.Equal(t, http.StatusUnauthorized, res.StatusCode, why)
		accepted, _ := d.hydra.said()
		require.Zero(t, accepted, "hydra was told somebody: %s", why)
	}

	t.Run("somebody still there is named", func(t *testing.T) {
		x := require.New(t)
		d := serveAs(t, login.Skip, remembering)
		b := formed(t, d)

		res := d.post(t, b, "/accept", "c1", nil)
		defer res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode)
		x.Equal(d.who["contoso"].String(), d.hydra.acceptedAs("c1"))
	})

	t.Run("somebody signed out everywhere is not", func(t *testing.T) {
		d := serveAs(t, login.Skip, remembering)
		b := formed(t, d)

		_, err := d.s.Ungated.Holder().Invalidate(t.Context(), rstr.HolderInvalidateRequest_builder{
			Ref: rstr.HolderRef_builder{Id: d.who["contoso"].Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		refused(t, d, d.post(t, b, "/accept", "c1", nil), "a sign-in ended everywhere was accepted")
	})

	t.Run("nor is somebody suspended", func(t *testing.T) {
		d := serveAs(t, login.Skip, remembering)
		b := formed(t, d)

		_, err := d.s.Ungated.Holder().Disable(t.Context(), rstr.HolderDisableRequest_builder{
			Ref: rstr.HolderRef_builder{Id: d.who["contoso"].Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		refused(t, d, d.post(t, b, "/accept", "c1", nil), "somebody suspended was accepted")
	})

	// `DELETE /session` clears the cookie in the browser that asked and revokes
	// the delegation. A sealed cookie has no row to delete, so a copy of it made
	// before goes on opening -- and it is the delegation being gone that has to
	// answer for it.
	t.Run("nor is a copy of a cookie that was signed out of", func(t *testing.T) {
		x := require.New(t)
		d := serveAs(t, login.Skip, remembering)
		b := formed(t, d)

		at, err := url.Parse(d.app.URL)
		x.NoError(err)
		kept := b.Jar.Cookies(at)
		x.NotEmpty(kept)

		req, err := http.NewRequest(http.MethodDelete, d.app.URL+"/session?login_challenge=c1", nil)
		x.NoError(err)
		res, err := b.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusNoContent, res.StatusCode)

		req, err = http.NewRequest(http.MethodPost, d.app.URL+"/accept?login_challenge=c1", nil)
		x.NoError(err)
		for _, c := range kept {
			req.AddCookie(c)
		}
		res, err = http.DefaultClient.Do(req)
		x.NoError(err)

		refused(t, d, res, "a cookie kept from before its sign-out was accepted")
	})
}

// TestASessionOfOneTenantDoesNotAnswerAnothersChallenge: the tenant of a flow
// is Hydra's word about its challenge, and the session a form minted is of the
// tenant that form's challenge resolved to. `POST /accept` compared neither, so
// a browser signed in at contoso's challenge answered fabrikam's: 200, and
// Hydra told contoso's person as the subject of fabrikam's flow -- a token for
// a product of fabrikam's, naming somebody fabrikam has never had. Right is
// 401.
//
// `TestAFlowReachesOnlyItsOwnOperator` holds the password to its flow's tenant.
// This holds the last hop to it.
func TestASessionOfOneTenantDoesNotAnswerAnothersChallenge(t *testing.T) {
	x := require.New(t)
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Remember = time.Hour })
	b := d.browser(t)

	d.hydra.raise("c1", "contoso-web")
	_, code := d.signIn(t, b, "c1", "erin", password)
	x.Equal(http.StatusOK, code)

	d.hydra.raiseWith("f1", "fabrikam-web", "openid")
	res := d.post(t, b, "/accept", "f1", nil)
	defer res.Body.Close()

	x.Equal(http.StatusUnauthorized, res.StatusCode, "contoso's session answered fabrikam's challenge")
	x.Empty(d.hydra.acceptedAs("f1"), "hydra was told a subject for fabrikam's flow by a session of contoso's")
}

// TestASignInAnswersOnlyTheChallengeItWasMadeFor: a challenge Hydra raises
// without `skip` is one it wants somebody to sign in for -- the browser is new
// to it, or the client asked for a fresh sign-in (`prompt=login`, `max_age`).
// `POST /accept` answered any challenge with whatever session the browser held,
// so a sign-in an hour old finished a flow that had asked for one made now, no
// form answered, and Hydra stamped the moment as `auth_time`: 200. Right is
// 401, until the form at **that** challenge is answered.
//
// Nothing a page does is lost by it. The page calls `/accept` straight after a
// sign-in at the same challenge and never otherwise; a browser Hydra remembers
// is answered at `GET /login`, which asks roster; and a provider's round trip
// is answered where it comes back.
func TestASignInAnswersOnlyTheChallengeItWasMadeFor(t *testing.T) {
	x := require.New(t)
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Remember = time.Hour })
	b := d.browser(t)

	d.hydra.raise("c1", "contoso-web")
	_, code := d.signIn(t, b, "c1", "erin", password)
	x.Equal(http.StatusOK, code)

	// A later flow of the same tenant, which Hydra did not skip.
	d.hydra.raise("c2", "contoso-web")
	d.hydra.mu.Lock()
	d.hydra.asking["c2"] = url.Values{"prompt": {"login"}}
	d.hydra.mu.Unlock()

	res := d.post(t, b, "/accept", "c2", nil)
	res.Body.Close()
	x.Equal(http.StatusUnauthorized, res.StatusCode, "a sign-in made for one challenge answered a later one")
	x.Empty(d.hydra.acceptedAs("c2"), "hydra was told somebody signed in for a challenge no form was answered at")

	// And the form answered there is the sign-in it asked for.
	_, code = d.signIn(t, b, "c2", "erin", password)
	x.Equal(http.StatusOK, code)
	x.Equal(d.who["contoso"].String(), d.hydra.acceptedAs("c2"))
}

// TestAConsentWhoseSubjectCannotBeReadIsRefused, whatever its client asked for.
//
// The consent hop reads the subject as themselves, and a subject it cannot
// read was refused `login_required` only when the scope carried a claim: a
// client that asked for `openid` and nothing of the person was granted, on the
// reading that `sub` was all it was ever going to get. But what cannot be read
// is not only a session that lapsed between two hops. It is also somebody
// signed out everywhere, suspended or erased a moment ago -- and the scope is
// the authorization request's to choose, so every flow could be made one that
// asks for no claim. Granted, with tokens to follow; right is `login_required`
// and nothing granted.
func TestAConsentWhoseSubjectCannotBeReadIsRefused(t *testing.T) {
	x := require.New(t)
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Remember = time.Hour })
	b := d.browser(t)

	d.hydra.raiseWith("c1", "contoso-web", "openid", "offline")
	_, code := d.signIn(t, b, "c1", "erin", password)
	x.Equal(http.StatusOK, code)

	// Between the two hops of one flow.
	_, err := d.s.Ungated.Holder().Invalidate(t.Context(), rstr.HolderInvalidateRequest_builder{
		Ref: rstr.HolderRef_builder{Id: d.who["contoso"].Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	res, err := b.Get(d.app.URL + "/consent?consent_challenge=c1")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusSeeOther, res.StatusCode)

	// Where the browser is sent is what says which of the two Hydra was told:
	// the fake answers an accepted consent with `/done` and a refused one with
	// `/denied`, and a grant of `sub` alone carries no claim to look for.
	x.Equal("/denied", res.Header.Get("Location"), "a consent was granted for somebody roster no longer answers for")

	d.hydra.mu.Lock()
	defer d.hydra.mu.Unlock()
	x.Equal("login_required", d.hydra.refusal)
}
