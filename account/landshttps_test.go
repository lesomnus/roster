package account_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// secure is the app as a deployment runs it: its cookies `Secure`, which is
// every deployment but a development rig, and no one origin named for it.
func secure(c *account.Config) {
	c.InsecureCookie = false
	c.Base = nil
}

// TestWhatTheAppSendsSomebodyToIsHttps: where a sign-in lands, and what a
// mailed link opens, is `https` wherever this app's own cookies are.
//
// # What it was
//
// Three places built a URL from what a request said of itself.
//
// `next` -- where a sign-in started from the tenant's console goes back to --
// was held to a name the tenant answers at, and to nothing about how it is
// reached: `http://` was taken. What is sent there is the link that ends in
// the person's whole console session, so anybody who could start a sign-in
// with a URL of their choosing had it travel in the clear.
//
// With no `account.base` -- which is every deployment that fronts more than
// one tenant's name -- a mailed link and a provider's redirect were `http`
// unless the request carried `X-Forwarded-Proto: https`, which is the
// caller's to send or leave out. A recovery link asked for by a stranger went
// out as `http://`.
//
// And `account.terminal` with no `account.base` read the base it had not got:
// every `POST /device/begin`, which needs no session, was a nil dereference
// (#277).
//
// An app whose cookies are `Secure` works over `https` and nothing else, so
// that is what it says of itself, whatever a request says.
func TestWhatTheAppSendsSomebodyToIsHttps(t *testing.T) {
	asJSON := func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }

	t.Run("a sign-in does not land at http", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), secure)

		_, err := d.ungated.Host().Add(t.Context(), rstr.HostAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Name: "console.contoso.test",
		}.Build())
		x.NoError(err)

		begin := func(next string) (int, string) {
			b := d.browser(t, "contoso.test")
			b.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

			return b.do(t, http.MethodGet, "/login?connection=example&next="+url.QueryEscape(next), "", nil)
		}

		code, body := begin("http://console.contoso.test")
		x.Equal(http.StatusBadRequest, code, "a sign-in was started that ends at http: %s", body)

		code, body = begin("http://console.contoso.test:9999")
		x.Equal(http.StatusBadRequest, code, body)

		code, body = begin("https://console.contoso.test")
		x.Equal(http.StatusFound, code, body)
	})

	t.Run("a development rig, which said its cookies are not Secure, is as it was", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())

		_, err := d.ungated.Host().Add(t.Context(), rstr.HostAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Name: "console.contoso.test",
		}.Build())
		x.NoError(err)

		b := d.browser(t, "contoso.test")
		b.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		code, body := b.do(t, http.MethodGet, "/login?connection=example&next="+url.QueryEscape("http://console.contoso.test"), "", nil)
		x.Equal(http.StatusFound, code, body)
	})

	t.Run("a mailed link is https, whatever the request said", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), secure)

		for _, proto := range []string{"", "http"} {
			code, _ := d.browser(t, "fabrikam.test").do(t, http.MethodPost, "/recover", `{"address":"bob@fabrikam.com"}`,
				func(r *http.Request) {
					asJSON(r)
					if proto != "" {
						r.Header.Set("X-Forwarded-Proto", proto)
					}
				})
			x.Equal(http.StatusAccepted, code)

			var link string
			x.Eventually(func() bool { link = d.sent("bob@fabrikam.com"); return link != "" }, 2*time.Second, 20*time.Millisecond)
			x.True(strings.HasPrefix(link, "https://fabrikam.test/redeem?"), "X-Forwarded-Proto %q: mailed %s", proto, link[:strings.Index(link, "?")])
			d.forget()
		}
	})

	t.Run("and a terminal is told where to go by a deployment that named no base", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), terminal, secure)

		code, body := d.browser(t, "contoso.test").do(t, http.MethodPost, "/device/begin",
			`{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
		x.Equal(http.StatusOK, code, body)

		var began struct {
			At       string `json:"verification_uri"`
			Complete string `json:"verification_uri_complete"`
		}
		x.NoError(json.Unmarshal([]byte(body), &began))
		x.Equal("https://contoso.test/device", began.At)
		x.True(strings.HasPrefix(began.Complete, "https://contoso.test/device?user_code="), began.Complete)
	})
}

// forget empties the mailbox, for a test that asks for the same letter twice.
func (d *deployment) forget() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mail = nil
}
