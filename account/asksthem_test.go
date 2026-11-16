package account_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// copyOf is a second browser holding a copy of the first one's cookies: what
// somebody who read the cookie holds.
func copyOf(t *testing.T, d *deployment, b *browser) *browser {
	t.Helper()

	u, err := url.Parse("http://" + b.host + "/")
	require.NoError(t, err)
	require.NotEmpty(t, b.Jar.Cookies(u), "nothing to copy")

	c := d.browser(t, b.host)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	jar.SetCookies(u, b.Jar.Cookies(u))
	c.Jar = jar

	return c
}

func identitiesOf(t *testing.T, d *deployment, who []byte) int {
	t.Helper()

	vs, err := d.ungated.Identity().List(context.Background(), rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: rstr.HolderRef_builder{Id: who}.Build()}.Build()},
	}.Build())
	require.NoError(t, err)

	return len(vs.GetItems())
}

// without takes one method out of what everybody of contoso does to their own
// account: the tenant's switch for it (`docs/usage/permissions.md`).
func without(t *testing.T, d *deployment, method string) {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()

	own, err := d.ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
			Alias: proto.String("self-service"), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
		Select: rstr.RoleSelect_builder{Methods: proto.Bool(true), DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)

	kept := []string{}
	for _, m := range own.GetMethods() {
		if m != method {
			kept = append(kept, m)
		}
	}
	x.Less(len(kept), len(own.GetMethods()), "%s was not among what everybody does to their own account", method)

	_, err = d.ungated.Role().Patch(ctx, rstr.RolePatchRequest_builder{
		Ref: rstr.RoleRef_builder{Id: own.GetId()}.Build(), Methods: kept, DateUpdated: own.GetDateUpdated(),
	}.Build())
	x.NoError(err)
}

// TestWhatTheAppWritesForSomebodyItAsksThemFor: where the account app acts
// with its own key for the person at its page, roster is asked first whether
// that person is still there, and whether they may.
//
// # What it was
//
// A call the page makes of roster goes as the person, through the delegation
// their session holds, and roster answers for both: a session that was ended
// is refused, and so is what the person's tenant does not let them do. Four
// routes the app writes by hand went another way. `/ways`, `/claim`, `/prove`
// and a terminal's code read **who** off the sealed cookie and then acted with
// the app's own key, which is bound everything the app does for anybody.
//
// So a session that had been signed out of -- its delegation revoked at roster
// -- went on linking a provider's account, claiming an address and answering
// whether a password was the person's, from any copy of the cookie, until the
// cookie's own clock ran out. And a tenant that took `IdentityService/Add` or
// `EmailService/Add` out of what its people do to their own accounts, which
// the manual says turns it off for everybody, had turned off the page's button
// and nothing else: the route behind it still wrote (#277).
func TestWhatTheAppWritesForSomebodyItAsksThemFor(t *testing.T) {
	asJSON := func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }

	t.Run("a session that ended is nobody", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), terminal)
		password(t, d)

		b := d.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`, asJSON)
		x.Equal(http.StatusNoContent, code, body)

		// What somebody who read the cookie holds, after she signs out.
		stale := copyOf(t, d, b)
		code, _ = b.do(t, http.MethodDelete, "/session", "", nil)
		x.Equal(http.StatusNoContent, code)

		code, _ = stale.rpc(t, "/roster.MeService/Get", `{}`)
		x.Equal(http.StatusUnauthorized, code, "the control: roster refuses what the page asks as her")

		for _, secret := range []string{"correct horse battery staple", "wrong"} {
			code, body := stale.do(t, http.MethodPost, "/prove", `{"kind":"password","secret":"`+secret+`"}`, asJSON)
			x.Equal(http.StatusUnauthorized, code, "/prove said whether %q is her password: %s", secret, body)
		}

		before := identitiesOf(t, d, d.erin)
		d.idp.Subject, d.idp.Claims = "9999", map[string]any{}
		code, at := stale.landed(t, http.MethodPost, "/ways?connection=other")
		x.Equal(before, identitiesOf(t, d, d.erin), "a provider's account was linked by a session that had ended (%d, %s)", code, at)

		carol, err := d.ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "carol",
		}.Build())
		x.NoError(err)
		_, err = d.ungated.Email().Add(t.Context(), rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: carol.GetId()}.Build(), Address: "elsewhere@example.net",
		}.Build())
		x.NoError(err)

		code, body = stale.do(t, http.MethodPost, "/claim", `{"address":"elsewhere@example.net"}`, asJSON)
		x.Equal(http.StatusUnauthorized, code, body)
		x.Never(func() bool { return d.sent("elsewhere@example.net") != "" }, 300*time.Millisecond, 20*time.Millisecond,
			"a link was mailed for a session that had ended")

		code, body = stale.do(t, http.MethodGet, "/device/pending?user_code=WDJB-MJHT", "", nil)
		x.Contains([]int{http.StatusUnauthorized, http.StatusForbidden}, code, body)
	})

	t.Run("a round trip begun is not finished by a session that ended meanwhile", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		password(t, d)

		b := d.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`, asJSON)
		x.Equal(http.StatusNoContent, code, body)

		// Out to the provider, and held there: the redirect is not followed.
		held := copyOf(t, d, b)
		held.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		d.idp.Subject, d.idp.Claims = "9998", map[string]any{}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.app.URL+"/ways?connection=other", nil)
		x.NoError(err)
		req.Host = "contoso.test"
		res, err := held.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusFound, res.StatusCode)
		out := res.Header.Get("Location")

		// She signs out. The provider's page is still open somewhere.
		code, _ = b.do(t, http.MethodDelete, "/session", "", nil)
		x.Equal(http.StatusNoContent, code)

		// The provider answers, and the browser comes back to the callback
		// under the tenant's name, with the round trip's own cookie.
		res, err = held.Get(out)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusFound, res.StatusCode, "the provider did not send the browser back")
		back, err := url.Parse(res.Header.Get("Location"))
		x.NoError(err)

		finish := func(by *browser) int {
			code, _ := by.do(t, http.MethodGet, back.RequestURI(), "", nil)

			return code
		}

		before := identitiesOf(t, d, d.erin)
		x.Contains([]int{http.StatusFound, http.StatusUnauthorized, http.StatusForbidden}, finish(held))
		x.Equal(before, identitiesOf(t, d, d.erin), "the round trip finished for a session that had ended")
	})

	t.Run("and what a tenant turned off, the page's routes do not do", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		ctx := t.Context()

		// frank arrives through the provider and is bound no role at all.
		frank, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "frank",
		}.Build())
		x.NoError(err)
		_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: frank.GetId()}.Build(), Provider: "example", Subject: "4001",
		}.Build())
		x.NoError(err)

		without(t, d, rstr.IdentityService_Add_FullMethodName)
		without(t, d, rstr.EmailService_Add_FullMethodName)

		b := d.browser(t, "contoso.test")
		d.idp.Subject = "4001"
		code, _ := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusOK, code)

		code, body := b.rpc(t, "/roster.IdentityService/Add",
			`{"holder":{"id":"`+std(frank.GetId())+`"},"provider":"other","subject":"9003"}`)
		x.Equal(http.StatusForbidden, code, "the control: roster refuses him, as himself: %s", body)

		d.idp.Subject = "9003"
		code, at := b.landed(t, http.MethodPost, "/ways?connection=other")
		x.Equal(1, identitiesOf(t, d, frank.GetId()), "the page's route linked what roster refuses him (%d, %s)", code, at)

		// An address on somebody else's row that nobody proved, which a claim
		// would move to him once its link is answered.
		carol, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "carol",
		}.Build())
		x.NoError(err)
		_, err = d.ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: carol.GetId()}.Build(), Address: "elsewhere@example.net",
		}.Build())
		x.NoError(err)

		code, body = b.do(t, http.MethodPost, "/claim", `{"address":"elsewhere@example.net"}`, asJSON)
		x.Equal(http.StatusForbidden, code, body)
		x.Never(func() bool { return d.sent("elsewhere@example.net") != "" }, 300*time.Millisecond, 20*time.Millisecond,
			"a link was mailed for a claim his tenant does not let him make")
	})
}
