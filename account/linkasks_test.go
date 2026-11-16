package account_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
)

// spend posts the form a mailed link's page asks with: the token in the body,
// to the path the link named, under the tenant it named.
func (b *browser) spend(t *testing.T, link string) (int, string) {
	t.Helper()

	u, err := url.Parse(link)
	require.NoError(t, err)

	return b.do(t, http.MethodPost, u.Path+"?"+url.Values{"at": {u.Query().Get("at")}}.Encode(),
		url.Values{"token": {u.Query().Get("token")}}.Encode(),
		func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") })
}

// TestAMailedLinkAsksBeforeItIsSpent: a link in a message does nothing by
// being fetched. Its page asks, and the answer is what spends it.
//
// # What it was
//
// The link that deletes an account already worked that way, and its handler
// says why: *a link is fetched by more than the person it was sent to: a mail
// client previewing it, a scanner following every link in a message.* The link
// that recovers an account and the one that confirms an address did the whole
// act on the `GET` -- and on a `HEAD`, which a mux answers on a `GET` pattern.
//
// So where mail is scanned, one fetch by the gateway replaced the person's
// password, signed them out of everything, and showed the new password to the
// gateway: the person's own click came second and was told the link was no
// longer one. `POST /recover` takes an address from anybody, so anybody who
// knew a person's address could do that to them, as often as the allowance
// lets a link be sent. And an address somebody had written onto their own
// account was proved theirs by its real owner's mail gateway, nobody having
// opened anything (#277).
func TestAMailedLinkAsksBeforeItIsSpent(t *testing.T) {
	json := func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }

	t.Run("a link that recovers an account", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		b := d.browser(t, "fabrikam.test")

		code, _ := b.do(t, http.MethodPost, "/recover", `{"address":"bob@fabrikam.com"}`, json)
		x.Equal(http.StatusAccepted, code)

		var link string
		x.Eventually(func() bool { link = d.sent("bob@fabrikam.com"); return link != "" }, 2*time.Second, 20*time.Millisecond)

		// Fetched, by something that is not him and holds nothing: twice, and
		// once as a scanner that asks for the headers alone.
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodGet} {
			code, page := d.browser(t, "fabrikam.test").do(t, method, strings.TrimPrefix(link, d.app.URL), "", nil)
			x.Equal(http.StatusOK, code, "%s: %s", method, page)
			x.NotContains(page, "Your new password", "%s: a fetch was handed a password", method)
		}

		// Nothing happened: the password he had still signs him in.
		code, _ = d.browser(t, "fabrikam.test").do(t, http.MethodPost, "/session",
			`{"alias":"bob","password":"correct horse battery staple"}`, json)
		x.Equal(http.StatusNoContent, code, "a fetch of the link replaced his password")

		// And the link is still his to use: the page's answer spends it.
		code, page := b.spend(t, link)
		x.Equal(http.StatusOK, code, page)
		x.Contains(page, "Your new password")

		code, _ = b.spend(t, link)
		x.Equal(http.StatusNotFound, code, "a recovery link was spent twice")
	})

	t.Run("a link that confirms an address", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		d.idp.Subject = "3001"
		d.idp.Claims = map[string]any{}

		b := d.browser(t, "contoso.test")
		code, _ := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusOK, code)

		code, body := b.rpc(t, "/roster.EmailService/Add", `{"holder":{"id":"`+std(d.erin)+`"},"address":"erin@contoso.com"}`)
		x.Equal(http.StatusOK, code, body)
		var added struct {
			Id string `json:"id"`
		}
		x.NoError(jsonUnmarshal(body, &added))
		id, err := base64.StdEncoding.DecodeString(added.Id)
		x.NoError(err)

		code, body = b.do(t, http.MethodPost, "/verify", `{"id":"`+base64.RawURLEncoding.EncodeToString(id)+`"}`, json)
		x.Equal(http.StatusAccepted, code, body)
		link := d.sent("erin@contoso.com")
		x.Contains(link, "/confirm?")

		proved := func() bool {
			code, body := b.rpc(t, "/roster.EmailService/List", `{"filters":[{"holder":{"id":"`+std(d.erin)+`"}}]}`)
			x.Equal(http.StatusOK, code, body)

			var listed struct {
				Items []struct {
					DateVerified *string `json:"dateVerified"`
				} `json:"items"`
			}
			x.NoError(jsonUnmarshal(body, &listed))
			x.Len(listed.Items, 1)

			return listed.Items[0].DateVerified != nil
		}

		for _, method := range []string{http.MethodGet, http.MethodHead} {
			code, page := d.browser(t, "contoso.test").do(t, method, strings.TrimPrefix(link, d.app.URL), "", nil)
			x.Equal(http.StatusOK, code, "%s: %s", method, page)
			x.NotContains(page, "Address confirmed")
		}
		x.False(proved(), "a fetch of the link proved the address")

		// From a browser with no session at all: the mailbox is the proof, and
		// pressing the button is the act.
		code, page := d.browser(t, "contoso.test").spend(t, link)
		x.Equal(http.StatusOK, code, page)
		x.Contains(page, "Address confirmed")
		x.True(proved())
	})

	// What a page asks is a form, and a form's answer is held to a size as
	// every other body here is.
	t.Run("and the answer is not read without end", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())

		for _, path := range []string{"/redeem", "/confirm", "/leave"} {
			res, err := http.DefaultClient.Do(mustRequest(t, d, path+"?at="+d.fabrikam.String(),
				"token="+strings.Repeat("a", 64<<10)))
			x.NoError(err)
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			x.Contains([]int{http.StatusBadRequest, http.StatusRequestEntityTooLarge}, res.StatusCode, path)
		}
	})
}

func jsonUnmarshal(body string, v any) error { return json.Unmarshal([]byte(body), v) }

func mustRequest(t *testing.T, d *deployment, path, body string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.app.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Host = "fabrikam.test"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return req
}
