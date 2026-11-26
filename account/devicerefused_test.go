package account_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/cli"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestATerminalIsToldWhenRosterWouldNotMakeItsKey: a yes that roster then
// refuses -- a method the person does not hold, a name they already use, an
// app that is nobody -- finishes the code, and the terminal's next poll is
// told so, in the sentence the approval page showed.
//
// # What it was
//
// The approval page was answered the refusal and the poll went on answering
// `authorization_pending` until the code lapsed, a quarter of an hour later:
// an app's page that began it drew *waiting* all that time, to somebody who
// had already read the refusal. Right is `refused`, with the sentence as
// `error_description`, and no second yes to the same code.
//
// A person's own no is `access_denied`, with no sentence, as it was: the
// program tells *you said no* from *roster would not make it* by the error
// alone.
func TestATerminalIsToldWhenRosterWouldNotMakeItsKey(t *testing.T) {
	d := serve(t, account.Invited(), terminal)
	mints(t, d)

	// A key of hers already called `taken`.
	_, sum, err := keys.Mint(keys.PrefixTenant)
	require.NoError(t, err)
	_, err = d.ungated.ApiKey().Add(context.Background(), rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: d.erin}.Build(), Alias: "taken", Secret: sum,
		Methods: []string{rstr.MeService_Get_FullMethodName},
	}.Build())
	require.NoError(t, err)

	term := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	require.Equal(t, http.StatusNoContent, code, body)

	for name, c := range map[string]struct {
		begin  string
		status int
		says   string
	}{
		"a method she does not hold": {
			`{"name":"laptop","allow":["/roster.RoleService/List"]}`,
			http.StatusForbidden, "a terminal cannot be allowed more than you hold",
		},
		"a name she already uses": {
			`{"name":"taken","allow":["/roster.MeService/Get"]}`,
			http.StatusConflict, "you already have a key called that",
		},
		"an app that is nobody": {
			`{"name":"laptop","app":"nobody","allow":["/nobody.ThingService/Get"]}`,
			http.StatusNotFound, "no operator here serves this name",
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			device, user := scopedTerminal(t, term, c.begin)
			code, body := her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
			x.Equal(c.status, code, body)
			x.Equal(c.says+"\n", body, "the approval page is shown roster's sentence")

			// The code is finished: a second yes is to a code nobody is
			// waiting on, and the page is told there is none.
			code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
			x.Equal(http.StatusNotFound, code, body)
			x.Contains(body, "that is not a code this is waiting for")
			code, body = her.do(t, http.MethodGet, "/device/pending?user_code="+user, "", nil)
			x.Equal(http.StatusNotFound, code, body)

			// The next poll is told, whole: the status and the sentence, and
			// nothing of who she is or what she holds.
			code, body = asked(t, term, device)
			x.Equal(http.StatusBadRequest, code, body)
			var got map[string]any
			x.NoError(json.Unmarshal([]byte(body), &got), body)
			x.Equal(map[string]any{"error": "refused", "error_description": c.says}, got)

			// Once, as a key is: the row went with the answer.
			x.Equal("expired_token", polls(t, term, device))
		})
	}

	t.Run("her own no", func(t *testing.T) {
		x := require.New(t)

		device, user := scopedTerminal(t, term, `{"name":"laptop","allow":["/roster.MeService/Get"]}`)
		code, body := her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`","deny":true}`, nil)
		x.Equal(http.StatusNoContent, code, body)

		code, body = asked(t, term, device)
		x.Equal(http.StatusBadRequest, code, body)
		var got map[string]any
		x.NoError(json.Unmarshal([]byte(body), &got), body)
		x.Equal(map[string]any{"error": "access_denied"}, got, "a no is told apart from a refusal by its error, and carries no sentence")
		x.Equal("expired_token", polls(t, term, device))
	})
}

// TestTheCommandSaysWhenRosterWouldNotMakeItsKey is `roster sign-in` told
// that the yes was refused: it says so, in roster's sentence, and exits 1
// rather than waiting. It waited, until the code ran out a quarter of an hour
// later, and then said *the code ran out before anybody approved it*.
func TestTheCommandSaysWhenRosterWouldNotMakeItsKey(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited(), terminal)
	mints(t, d)
	her := d.browser(t, "contoso.test")
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	codes := make(chan string, 4)
	said := regexp.MustCompile(`and type\s+([A-Z]{4}-[A-Z]{4})`)
	say := func(f string, a ...any) (int, error) {
		if m := said.FindStringSubmatch(fmt.Sprintf(f, a...)); m != nil {
			codes <- m[1]
		}

		return 0, nil
	}
	go func() {
		select {
		case user := <-codes:
			_, _ = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
		case <-time.After(20 * time.Second):
		}
	}()

	// Bounded well short of the code's life, which is what it waited out.
	ctx, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	key, err := cli.SignIn(ctx, say, &http.Client{Transport: hostIs{name: "contoso.test"}}, d.app.URL, "laptop", "", []string{"/roster.RoleService/List"})
	x.Empty(key)
	x.EqualError(err, "roster would not make the key: a terminal cannot be allowed more than you hold")
	x.Equal(1, cli.Status(err), "the command's exit status")
}
