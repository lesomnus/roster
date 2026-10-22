package account_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestSomebodyWhoArrivesThroughGoogleDeletesTheirAccount is `MeService.Leave`
// for somebody whose way in is Google's sign-in, which takes no request to
// authenticate anybody again and says when it last did only where the app's
// project lets it (`arrives/google.go`).
//
// So this app asks Google for `auth_time` on every round trip and for nothing
// Google does not take; what Google answers is believed, and a sign-in that
// says no time or an old one deletes nothing -- nor gives her a first password,
// which would be a way in that outlives the session asking for it. The road
// that is left is the one the page names: a recovery link to the address Google
// vouched for, which hands her a password, and a sign-in with it.
func TestSomebodyWhoArrivesThroughGoogleDeletesTheirAccount(t *testing.T) {
	g := idptest.New(t, clientId)
	d := serve(t, account.Invited(), func(c *account.Config) { c.Google = g.URL })
	ctx := t.Context()

	_, err := d.ungated.Tenant().Patch(ctx, rstr.TenantPatchRequest_builder{
		Ref:              rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		Config:           rstr.TenantConfig_builder{Leave: true}.Build(),
		DateUpdatedForce: proto.Bool(true),
	}.Build())
	require.NoError(t, err)
	_, err = d.ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		Name:   "google", Issuer: g.URL, ClientId: clientId,
		Scopes: []string{"email", "profile"}, SecretRef: "env:EXAMPLE_SECRET",
	}.Build())
	require.NoError(t, err)
	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: d.erin}.Build(), Provider: "google", Subject: "erin-at-google",
	}.Build())
	require.NoError(t, err)
	g.Subject = "erin-at-google"

	// signedIn is erin, through Google, which says it authenticated her at
	// `at` -- or says nothing, for a zero one, as a project without *Session
	// age claims* does. It vouches for her address each time, which is
	// written down and stamped at the first sign-in.
	signedIn := func(t *testing.T, path string, at time.Time) *browser {
		t.Helper()
		g.Claims = map[string]any{"email": "erin@contoso.com", "email_verified": true}
		if !at.IsZero() {
			g.Claims["auth_time"] = at.Unix()
		}
		b := d.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, code, body)

		return b
	}
	leaves := func(t *testing.T, b *browser) (int, string) {
		t.Helper()

		return b.rpc(t, "/roster.MeService/Leave", `{"confirm":"erin"}`)
	}

	t.Run("Google is asked when it last asked her, and nothing it does not take", func(t *testing.T) {
		x := require.New(t)

		for _, path := range []string{"/login?connection=google", "/login?connection=google&again=1"} {
			signedIn(t, path, time.Now())
			asked := g.Asked()
			x.JSONEq(`{"id_token":{"auth_time":{"essential":true}}}`, asked.Get("claims"), path)
			x.False(asked.Has("max_age"), "%s asked Google to ask again, which it does not take", path)
			x.False(asked.Has("prompt"), path)
		}

		// And a provider that is not Google is not asked in its words.
		d.idp.Subject = "3001"
		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example&again=1", "", nil)
		x.Equal(http.StatusOK, code, body)
		x.False(d.idp.Asked().Has("claims"), "a provider that is not Google was asked in Google's words")
	})

	t.Run("a Google that says no time deletes nothing", func(t *testing.T) {
		x := require.New(t)

		code, body := leaves(t, signedIn(t, "/login?connection=google&again=1", time.Time{}))
		x.Equal(http.StatusBadRequest, code, "a sign-in that said no time deleted an account: %s", body)
		x.Contains(body, "failed_precondition")
	})

	t.Run("nor one that last asked her this morning", func(t *testing.T) {
		x := require.New(t)

		code, body := leaves(t, signedIn(t, "/login?connection=google&again=1", time.Now().Add(-3*time.Hour)))
		x.Equal(http.StatusBadRequest, code, "a Google session from this morning deleted an account: %s", body)
		x.Contains(body, "failed_precondition")
	})

	t.Run("nor does it give her a first password", func(t *testing.T) {
		x := require.New(t)

		// The form the page draws for somebody who has none, from the session
		// Google's word could not make fresh: a password set from it would be
		// a way in that outlives it.
		was := signedIn(t, "/login?connection=google", time.Time{})
		code, body := was.rpc(t, "/roster.CredentialService/Set",
			`{"ref":{"id":"`+std(d.erin)+`"},"secret":"`+std([]byte("a password of erin's own"))+`"}`)
		x.Equal(http.StatusBadRequest, code, "a session that could not say when gave its holder a password: %s", body)
		x.Contains(body, "failed_precondition")
	})

	t.Run("a recovery link is the road", func(t *testing.T) {
		x := require.New(t)

		x.Contains(func() string {
			code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/providers", "", nil)
			x.Equal(http.StatusOK, code, body)

			return body
		}(), `"recover":true`, "the page is not told a recovery link can be had here")

		// Signed out, at the form the page points her to.
		b := d.browser(t, "contoso.test")
		code, _ := b.do(t, http.MethodPost, "/recover", `{"address":"erin@contoso.com"}`,
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
		x.Equal(http.StatusAccepted, code)

		var link string
		x.Eventually(func() bool { link = d.sent("erin@contoso.com"); return link != "" }, 2*time.Second, 20*time.Millisecond,
			"no link was mailed to the address Google vouched for")

		res, err := b.Get(link)
		x.NoError(err)
		page, _ := io.ReadAll(res.Body)
		res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode, string(page))
		password := between(string(page), `user-select:all">`, `</code>`)
		x.NotEmpty(password, "the link handed over no password: %s", page)

		// A sign-in with it is a moment ago, so it deletes.
		code, body := b.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"`+password+`"}`,
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
		x.Equal(http.StatusNoContent, code, body)

		code, body = leaves(t, b)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, "dateErased")
	})
}
