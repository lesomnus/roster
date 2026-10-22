package account_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
)

// stderr is what this process writes to its standard error from here on, as it
// has been so far: the account app's log.
func stderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	was := os.Stderr
	os.Stderr = w

	var mu sync.Mutex
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			mu.Lock()
			buf.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		os.Stderr = was
		w.Close()
		<-done
		r.Close()
	})

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// TestALetterHasOneThingToFollow: what a link goes out in says what it is for,
// and the link is the one thing in it to open.
func TestALetterHasOneThingToFollow(t *testing.T) {
	x := require.New(t)
	link := "https://login.contoso.example/redeem?token=rl_x&at=y"
	v := account.Letter("Recover your account", link)
	x.True(strings.HasPrefix(v, "Recover your account\n"))
	x.Equal(1, strings.Count(v, "://"), "a letter with something to follow besides the link: %s", v)
	x.Contains(v, "\n"+link+"\n")
}

// TestAMailThatFailsLeavesNoLinkInTheLog: a deliverer that fails says why, and
// what reaches the log is the address cut to its domain and nothing of the
// link or its token -- for the recovery form, which answers the browser the
// same whatever happened, and for a confirmation, which says it could not
// send.
//
// Red with the address logged as it was, and with the error logged as it was.
func TestAMailThatFailsLeavesNoLinkInTheLog(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited(), func(c *account.Config) {
		c.Mail = func(ctx context.Context, to, subject, link string) error {
			// What a server that refuses a recipient says, and more.
			return fmt.Errorf("550 <%s> rejected: %s", to, link)
		}
	})
	log := stderr(t)

	// Recovery: accepted, as for anybody, and the failure is the log's.
	b := d.browser(t, "fabrikam.test")
	code, _ := b.do(t, http.MethodPost, "/recover", `{"address":"bob@fabrikam.com"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusAccepted, code, "a failure to send reached the recovery form's answer")
	x.Eventually(func() bool { return strings.Contains(log(), "account: mail to") }, 5*time.Second, 20*time.Millisecond,
		"a failure to send was not logged")

	// A confirmation, as somebody signed in: it says it could not send.
	d.idp.Subject = "3001"
	d.idp.Claims = map[string]any{}
	e := d.browser(t, "contoso.test")
	code, _ = e.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code)
	code, body := e.rpc(t, "/roster.EmailService/Add", `{"holder":{"id":"`+std(d.erin)+`"},"address":"erin@contoso.com"}`)
	x.Equal(http.StatusOK, code, body)
	var added struct {
		Id string `json:"id"`
	}
	x.NoError(json.Unmarshal([]byte(body), &added))
	id, err := base64.StdEncoding.DecodeString(added.Id)
	x.NoError(err)
	code, _ = e.do(t, http.MethodPost, "/verify", `{"id":"`+base64.RawURLEncoding.EncodeToString(id)+`"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusBadGateway, code)

	x.Eventually(func() bool { return strings.Contains(log(), "account: mail to …@contoso.com") }, 5*time.Second, 20*time.Millisecond,
		"a confirmation that could not be sent was not logged")
	got := log()
	x.Contains(got, "account: mail to …@fabrikam.com")
	for _, leak := range []string{"bob@", "erin@contoso.com", "token=", "rl_", "/redeem", "/confirm", "://"} {
		x.NotContains(got, leak, "the log carries what it must not:\n%s", got)
	}
}
