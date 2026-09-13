package account_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/cli"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestATerminalIsSignedInFromABrowser walks the device grant end to end, which is
// the only way to see the thing it exists for: the two ends of it never meet.
//
// A terminal that holds nothing asks; a person who holds a session approves; and
// the key that comes out is the person's, held to everything a key of theirs is
// held to. `cli/signin.go` is the other side of these three requests and
// `account/device.go` says why they are here rather than in roster.
func TestATerminalIsSignedInFromABrowser(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited())
	mints(t, d)
	term := d.browser(t, "contoso.test") // no session, and never gets one
	her := d.browser(t, "contoso.test")

	// She is at the account page.
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	// The terminal begins, holding no credential of any kind.
	code, body = term.do(t, http.MethodPost, "/device/begin",
		`{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
	x.Equal(http.StatusOK, code, body)

	var began struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		At         string `json:"verification_uri"`
		Complete   string `json:"verification_uri_complete"`
		Interval   int    `json:"interval"`
		ExpiresIn  int    `json:"expires_in"`
	}
	x.NoError(json.Unmarshal([]byte(body), &began))
	x.NotEmpty(began.DeviceCode)
	x.Len(strings.ReplaceAll(began.UserCode, "-", ""), 8, "a code somebody has to read out: %q", began.UserCode)
	x.NotContains(began.UserCode, "0", "RFC 8628 §6.1: no glyph anybody could misread")
	x.NotContains(began.UserCode, "O")
	x.Contains(began.At, "/device")
	x.Contains(began.Complete, began.UserCode, "the one-click form carries the code")
	x.Positive(began.Interval)
	x.Positive(began.ExpiresIn)

	// Nobody has said anything yet, and the answer is RFC 8628's word for it.
	x.Equal("authorization_pending", polls(t, term, began.DeviceCode))

	// The page asks what it is being asked to allow, which is what makes the
	// screen worth drawing rather than a yes/no with no subject.
	code, body = her.do(t, http.MethodGet, "/device/pending?user_code="+began.UserCode, "", nil)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, "laptop")
	x.Contains(body, "/roster.MeService/Get")

	// A code that is not one is the same answer as a code of somebody else's.
	code, _ = her.do(t, http.MethodGet, "/device/pending?user_code=BBBB-CCCC", "", nil)
	x.Equal(http.StatusNotFound, code)

	// And a terminal cannot approve itself: there is nobody there.
	code, _ = term.do(t, http.MethodPost, "/device/approve",
		`{"user_code":"`+began.UserCode+`"}`, nil)
	x.Equal(http.StatusForbidden, code, "a browser with no session approved a device")

	// She says yes.
	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+began.UserCode+`"}`, nil)
	x.Equal(http.StatusOK, code, body)

	// The terminal's next poll carries the key away, once.
	key := ""
	code, body = term.do(t, http.MethodGet, "/device/poll?device_code="+began.DeviceCode, "", nil)
	x.Equal(http.StatusOK, code, body)
	var got struct{ Token string }
	x.NoError(json.Unmarshal([]byte(body), &got))
	key = got.Token
	x.True(strings.HasPrefix(key, "rt_"), "a tenant key, which is what a terminal wants: %q", key)

	// Once: the row went with the answer, so the flow cannot be replayed by
	// anybody who reads the poll out of a log.
	x.Equal("expired_token", polls(t, term, began.DeviceCode))

	// And it is hers, with the reach the terminal asked for and nothing else:
	// `Me.Get` through the page lists the keys that resolve to her.
	code, body = her.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, "laptop", "the key is not on her row: %s", body)
	x.Contains(body, "/roster.MeService/Get")
}

// TestATerminalNobodyApprovesGetsNothing is the other three endings, because a
// flow that only works when somebody says yes is half tested.
func TestATerminalNobodyApprovesGetsNothing(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited())
	mints(t, d)
	term := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")

	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	// A terminal that asks for nothing is refused at the start rather than handed
	// a key that does not work -- `roster key add`'s rule, at the other end.
	code, _ = term.do(t, http.MethodPost, "/device/begin", `{"name":"laptop","allow":[]}`, nil)
	x.Equal(http.StatusBadRequest, code)

	// A `device_code` nobody ever minted is over, not pending: a client that is
	// told to keep waiting for one would wait fifteen minutes for nothing.
	x.Equal("expired_token", polls(t, term, "not-a-device-code"))

	// She says no, and the terminal is told so rather than left waiting.
	code, body = term.do(t, http.MethodPost, "/device/begin",
		`{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
	x.Equal(http.StatusOK, code, body)

	var began struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	x.NoError(json.Unmarshal([]byte(body), &began))

	code, body = her.do(t, http.MethodPost, "/device/approve",
		`{"user_code":"`+began.UserCode+`","deny":true}`, nil)
	x.Equal(http.StatusNoContent, code, body)
	x.Equal("access_denied", polls(t, term, began.DeviceCode))

	// And a code that was answered is not waiting any more, whichever way it was
	// answered -- so a second browser cannot approve what somebody refused.
	code, _ = her.do(t, http.MethodGet, "/device/pending?user_code="+began.UserCode, "", nil)
	x.Equal(http.StatusNotFound, code)
}

// TestATerminalIsNeverAllowedMoreThanWhoeverApprovesIt is the escalation rule
// arriving through the front door.
//
// Nothing about the flow re-implements it: the key is minted **as** the person by
// `ApiKey.Issue`, so *nobody hands out a method they do not hold* is enforced
// where it always was. What this pins is that the refusal reaches the screen as a
// refusal rather than as a terminal that waits for ever.
func TestATerminalIsNeverAllowedMoreThanWhoeverApprovesIt(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited())
	password(t, d) // a password, and **not** the role that may mint a key

	term := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")

	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	code, body = term.do(t, http.MethodPost, "/device/begin",
		`{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
	x.Equal(http.StatusOK, code, body)

	var began struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	x.NoError(json.Unmarshal([]byte(body), &began))

	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+began.UserCode+`"}`, nil)
	x.Equal(http.StatusForbidden, code, body)
	x.Contains(body, "more than you hold")

	// And the terminal is still waiting rather than holding half a flow: an
	// approval that was refused did not spend the code, so she can be given the
	// method and try again.
	x.Equal("authorization_pending", polls(t, term, began.DeviceCode))
}

// TestTheCommandWalksTheFlowItPrints runs `cli`'s half of this against the app's
// half, because the two were written from the same document and that is exactly
// when halves drift: one renames `user_code`, or stops sending
// `authorization_pending`, and nothing else in the suite would notice.
//
// The command's loop is what is under test -- begin, print, poll, take the key --
// so the browser's half is driven by hand from the code the command **printed**,
// which is also the only place a test can get it, exactly as a person can.
func TestTheCommandWalksTheFlowItPrints(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited())
	mints(t, d)

	her := d.browser(t, "contoso.test")
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	// The command dials an address; the app resolves the operator from the host,
	// which a test server's address is not. So the client puts the name back, the
	// way `browser` does -- and counts the polls, because when the approval lands
	// decides which half of the loop is exercised.
	polled := make(chan struct{}, 8)
	cl := &http.Client{Transport: hostIs{name: "contoso.test", polled: polled}}

	// Only the line somebody reads the code off. Taking it from anywhere in the
	// output passes on a renamed `user_code`, because the complete URL carries the
	// same characters -- which is how this test first passed against an app that
	// had stopped sending the field.
	codes := make(chan string, 4)
	said := regexp.MustCompile(`and type\s+([A-Z]{4}-[A-Z]{4})`)
	say := func(f string, a ...any) (int, error) {
		if m := said.FindStringSubmatch(fmt.Sprintf(f, a...)); m != nil {
			codes <- m[1]
		}

		return 0, nil
	}

	go func() {
		var user string
		select {
		case user = <-codes:
		case <-time.After(20 * time.Second):
			return
		}

		// **After** the first poll, so that the command has to survive an
		// `authorization_pending` before it is given anything. Approving straight
		// away let the first poll answer with the key, and the loop's other half
		// was never run.
		select {
		case <-polled:
		case <-time.After(20 * time.Second):
			return
		}

		_, _ = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
	}()

	// Bounded, because the failure this is for is a renamed field: the command
	// would print no code, nobody would approve, and it would poll happily until
	// the fifteen minutes ran out. A test that takes a quarter of an hour to say
	// *these two disagree* is one somebody turns off.
	ctx, stop := context.WithTimeout(t.Context(), 45*time.Second)
	defer stop()

	key, err := cli.SignIn(ctx, say, cl, d.app.URL, "laptop", []string{"/roster.MeService/Get"})
	x.NoError(err)
	x.True(strings.HasPrefix(key, "rt_"), "the command came back with something that is not a key: %q", key)
	x.GreaterOrEqual(len(polled)+2, 2, "the loop did not poll twice, so a pending answer was never read")
}

// hostIs makes every request arrive under a name this app serves, whatever
// address the test server is actually on, and says when a poll has gone by.
type hostIs struct {
	name   string
	polled chan struct{}
}

func (h hostIs) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Host = h.name
	res, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && strings.Contains(r.URL.Path, "/device/poll") && h.polled != nil {
		select {
		case h.polled <- struct{}{}:
		default:
		}
	}

	return res, err
}

// polls is one poll, answering the reason it came back with.
// password gives erin one, since the harness signs her in through a provider.
func password(t *testing.T, d *deployment) {
	t.Helper()

	_, err := d.ungated.Credential().Set(context.Background(), rstr.CredentialSetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: d.erin}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	require.NoError(t, err)
}

// mints is that, and the one method a person needs to be able to sign a terminal
// in: the same one *mint an app password* needs, because it is the same verb.
func mints(t *testing.T, d *deployment) {
	t.Helper()
	password(t, d)

	ctx := context.Background()
	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		Alias:  "terminals",
		// Both, and the second is the finding: `Me.Get` is **waived** -- anybody
		// may call it with no role at all -- and a waiver is not a *holding*, so
		// the grant rule refuses a key naming it unless the person's own role
		// does. Which is the same constraint *mint an app password* is under, and
		// it is the rule working rather than a hole: a key is a credential that
		// acts as them, so what it may call is what they may.
		Methods: []string{"/roster.ApiKeyService/Issue", "/roster.MeService/Get"},
	}.Build())
	require.NoError(t, err)

	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: d.erin}.Build(),
	}.Build())
	require.NoError(t, err)
}

func polls(t *testing.T, b *browser, code string) string {
	t.Helper()

	_, body := b.do(t, http.MethodGet, "/device/poll?device_code="+code, "", nil)

	var out struct{ Error string }
	require.NoError(t, json.Unmarshal([]byte(body), &out), body)

	return out.Error
}
