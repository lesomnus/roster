package account_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
)

// begins is a terminal asking to be signed in, and the two codes it is given.
func begins(t *testing.T, d *deployment, host string) (device, user string) {
	t.Helper()

	code, body := d.browser(t, host).do(t, http.MethodPost, "/device/begin",
		`{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
	require.Equal(t, http.StatusOK, code, body)

	var began struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &began))

	return began.DeviceCode, began.UserCode
}

// asked is one poll, as a terminal makes it: the code in the body.
func asked(t *testing.T, b *browser, device string) (int, string) {
	t.Helper()

	return b.do(t, http.MethodPost, "/device/poll", `{"device_code":"`+device+`"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
}

// TestATerminalsCodesAreHeldToWhatTheySay: what `account/device.go` says of a
// terminal's two codes is what happens to them.
//
// # What it was
//
// Five things the file said, or a reader would take from it, that were not so
// (#277):
//
//   - *A wrong code costs somebody something*: the count was asked only after
//     the code had been looked up and found wrong, so past ten misses a right
//     code was still answered, and approved. And it never ran down, so *wait a
//     while* meant until the process restarted.
//   - *The key is handed over once*: the row was marked answered outside the
//     lock it is read under, and two approvals of one code each minted a key.
//   - *The row cannot be replayed by anybody who reads the poll out of a log*:
//     the device code was in the URL of every poll, which is what a log keeps;
//     and a `HEAD` -- which a mux answers on a `GET` pattern -- took the key
//     and delivered it to nobody.
//   - A terminal is asked for by anybody, and each asking was held for a
//     quarter of an hour with no ceiling on how many.
//   - The page a terminal is told to open, `/device`, was not served.
func TestATerminalsCodesAreHeldToWhatTheySay(t *testing.T) {
	asJSON := func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }

	signedIn := func(t *testing.T, d *deployment) *browser {
		t.Helper()

		b := d.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`, asJSON)
		require.Equal(t, http.StatusNoContent, code, body)

		return b
	}

	t.Run("wrong codes cost the right one", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), terminal)
		mints(t, d)
		her := signedIn(t, d)
		_, user := begins(t, d, "contoso.test")

		refused := 0
		for i := range 14 {
			code, _ := her.do(t, http.MethodGet, fmt.Sprintf("/device/pending?user_code=BBBB-%04d", i), "", nil)
			if code == http.StatusTooManyRequests {
				refused++
			}
		}
		x.Positive(refused, "fourteen wrong codes and none was refused for it")

		code, body := her.do(t, http.MethodGet, "/device/pending?user_code="+user, "", nil)
		x.Equal(http.StatusTooManyRequests, code, "after them, a right code was answered: %s", body)

		code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, asJSON)
		x.Equal(http.StatusTooManyRequests, code, "and approved: %s", body)
	})

	t.Run("one code is one key", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), terminal)
		mints(t, d)
		her := signedIn(t, d)
		device, user := begins(t, d, "contoso.test")

		var (
			wg  sync.WaitGroup
			mu  sync.Mutex
			yes int
		)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()

				code, _ := her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, asJSON)
				if code == http.StatusOK {
					mu.Lock()
					yes++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		x.Equal(1, yes, "one code was approved %d times", yes)

		code, body := her.rpc(t, "/roster.ApiKeyService/List", `{}`)
		x.Equal(http.StatusOK, code, body)
		var listed struct {
			Items []struct {
				Alias string `json:"alias"`
			} `json:"items"`
		}
		x.NoError(json.Unmarshal([]byte(body), &listed))
		x.Len(listed.Items, 1, "one code minted %d keys", len(listed.Items))

		code, body = asked(t, d.browser(t, "contoso.test"), device)
		x.Equal(http.StatusOK, code, body)
	})

	t.Run("the device code is in no address", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), terminal)
		mints(t, d)
		her := signedIn(t, d)
		device, user := begins(t, d, "contoso.test")
		term := d.browser(t, "contoso.test")

		code, body := asked(t, term, device)
		x.Equal(http.StatusBadRequest, code, body)
		x.Contains(body, "authorization_pending")

		code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, asJSON)
		x.Equal(http.StatusOK, code, body)

		// Asked as it was, by its address: not answered, and nothing taken.
		for _, method := range []string{http.MethodHead, http.MethodGet} {
			code, _ := term.do(t, method, "/device/poll?device_code="+url.QueryEscape(device), "", nil)
			x.Equal(http.StatusMethodNotAllowed, code, method)
		}

		code, body = asked(t, term, device)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"token":"rt_`, "a fetch by its address took the key: %s", body)
	})

	t.Run("what is held for terminals nobody approved has a ceiling", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), terminal)

		term := d.browser(t, "contoso.test")
		full := 0
		for range account.TerminalsWaiting + 8 {
			code, _ := term.do(t, http.MethodPost, "/device/begin", `{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
			if code == http.StatusServiceUnavailable {
				full++
			}
		}
		x.Equal(8, full, "terminals were held past the ceiling")
	})

	t.Run("and the page a terminal is told to open is one", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), terminal)

		b := d.browser(t, "contoso.test")
		b.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.app.URL+"/device?user_code=WDJB-MJHT", nil)
		x.NoError(err)
		req.Host = "contoso.test"
		res, err := b.Do(req)
		x.NoError(err)
		res.Body.Close()

		x.Equal(http.StatusFound, res.StatusCode)
		x.Equal("/?user_code=WDJB-MJHT", res.Header.Get("Location"))
	})
}
