package account_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// The numbers in `outbox.go`, as a person meets them: three of a kind to one
// address, five for one person signed in, eight on their way at once.

// count is how many messages went to `to`.
func (d *deployment) count(to string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, m := range d.mail {
		if m.to == to {
			n++
		}
	}

	return n
}

// links is how many links roster holds, which is how many were minted.
func (d *deployment) links(t *testing.T) int {
	t.Helper()
	n, err := d.s.Ent.Link.Query().Count(t.Context())
	require.NoError(t, err)

	return n
}

// asks posts JSON, and answers with the header as well as the code and body.
func (b *browser) asks(t *testing.T, path, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, b.base+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Host = b.host
	req.Header.Set("Content-Type", "application/json")
	res, err := b.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)

	return res.StatusCode, res.Header, string(out)
}

// erinSignsIn is erin at contoso's front door, through the directory, which
// hands over no address.
func erinSignsIn(t *testing.T, d *deployment) *browser {
	t.Helper()
	d.idp.Subject = "3001"
	d.idp.Claims = map[string]any{}
	b := d.browser(t, "contoso.test")
	code, body := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	require.Equal(t, http.StatusOK, code, body)

	return b
}

// added is an address erin writes onto her own row from the page, as `/verify`
// names it.
func added(t *testing.T, b *browser, d *deployment, address string) string {
	t.Helper()
	code, body := b.rpc(t, "/roster.EmailService/Add", `{"holder":{"id":"`+std(d.erin)+`"},"address":"`+address+`"}`)
	require.Equal(t, http.StatusOK, code, body)
	var v struct {
		Id string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &v))
	id, err := base64.StdEncoding.DecodeString(v.Id)
	require.NoError(t, err)

	return `{"id":"` + base64.RawURLEncoding.EncodeToString(id) + `"}`
}

// TestTheRecoveryFormSendsAnAddressItsShareAndNoMore: the form asked five times
// about one person answers five times alike, and the mailbox gets three. The
// two held back are the log's, without the address, and mint nothing.
//
// Red with the recovery form's allowance gone, with the link minted before the
// allowance is asked, and with the address logged as it was.
func TestTheRecoveryFormSendsAnAddressItsShareAndNoMore(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	log := stderr(t)
	b := d.browser(t, "fabrikam.test")
	before := d.links(t)

	for range 5 {
		code, _, body := b.asks(t, "/recover", `{"address":"bob@fabrikam.com"}`)
		x.Equal(http.StatusAccepted, code, "the form answered differently once the address had had its share: %s", body)
	}
	x.Eventually(func() bool {
		return strings.Count(log(), "its recovery messages are spent") == 2 && d.count("bob@fabrikam.com") == 3
	}, 5*time.Second, 20*time.Millisecond, "not three sent and two held back: %d sent, log:\n%s", d.count("bob@fabrikam.com"), log())
	x.Equal(before+3, d.links(t), "a link was minted for a message that was not sent")

	got := log()
	x.Contains(got, "account: mail to …@fabrikam.com: not sent")
	x.NotContains(got, "bob@", "the log carries the address")
}

// TestAConfirmationPastItsShareIsRefusedAndMintsNothing: the fourth
// *confirm your address* to one address in a moment is 429, with when to ask
// again in the header and in words, and no link is minted for it.
//
// Red with `/verify` not held to the allowance, and with the allowance asked
// only after the link is minted.
func TestAConfirmationPastItsShareIsRefusedAndMintsNothing(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	b := erinSignsIn(t, d)
	id := added(t, b, d, "erin@contoso.com")

	for range 3 {
		code, _, body := b.asks(t, "/verify", id)
		x.Equal(http.StatusAccepted, code, body)
	}
	before := d.links(t)
	code, h, body := b.asks(t, "/verify", id)
	x.Equal(http.StatusTooManyRequests, code, body)
	x.Contains(body, "ask again in 20 minutes")
	secs, err := strconv.Atoi(h.Get("Retry-After"))
	x.NoError(err, "no Retry-After: %v", h)
	// Twenty minutes from the first of the three, less however long the
	// four took to ask.
	x.LessOrEqual(secs, 20*60)
	x.Greater(secs, 19*60)
	x.Equal(before, d.links(t), "a link was minted for a message that was refused")
	x.Equal(3, d.count("erin@contoso.com"))
}

// TestClaimAndTheDeletionLinkPastTheirShareAreRefused: the two other routes a
// person signed in asks for mail by are held to the same allowance.
//
// Red with `/claim` not held to it, and with `/leave/link` not held to it.
func TestClaimAndTheDeletionLinkPastTheirShareAreRefused(t *testing.T) {
	t.Run("claim", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		const theirs = "shared@fabrikam.com"
		alice, err := d.ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(), Alias: "alice",
		}.Build())
		x.NoError(err)
		_, err = d.ungated.Email().Add(t.Context(), rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: alice.GetId()}.Build(), Address: theirs,
		}.Build())
		x.NoError(err)

		b := d.browser(t, "fabrikam.test")
		code, _, body := b.asks(t, "/session", `{"alias":"bob","password":"correct horse battery staple"}`)
		x.Equal(http.StatusNoContent, code, body)
		for range 3 {
			code, _, body = b.asks(t, "/claim", `{"address":"`+theirs+`"}`)
			x.Equal(http.StatusAccepted, code, body)
		}
		code, _, body = b.asks(t, "/claim", `{"address":"`+theirs+`"}`)
		x.Equal(http.StatusTooManyRequests, code, body)
		x.Equal(3, d.count(theirs))
	})

	t.Run("a deletion link", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		_, err := d.ungated.Tenant().Patch(t.Context(), rstr.TenantPatchRequest_builder{
			Ref:              rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
			Config:           rstr.TenantConfig_builder{Leave: true, Password: proto.Bool(false)}.Build(),
			DateUpdatedForce: proto.Bool(true),
		}.Build())
		x.NoError(err)
		proved(t, d, d.erin, "erin@contoso.com")

		b := erinSignsIn(t, d)
		for range 3 {
			code, _, body := b.asks(t, "/leave/link", `{"address":"erin@contoso.com"}`)
			x.Equal(http.StatusAccepted, code, body)
		}
		code, _, body := b.asks(t, "/leave/link", `{"address":"Erin@Contoso.com"}`)
		x.Equal(http.StatusTooManyRequests, code, "a fourth, written differently: %s", body)
		x.Equal(3, d.count("erin@contoso.com"))
	})
}

// TestOnePersonIsHeldToTheirShareWhateverTheAddress: five messages for one
// person signed in, however many addresses they spread them over -- because
// `/verify` mails whatever address somebody writes onto their own row, and an
// address's allowance alone would let one account mail the world.
//
// Red with the account's allowance gone.
func TestOnePersonIsHeldToTheirShareWhateverTheAddress(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	b := erinSignsIn(t, d)

	for i := range 5 {
		code, _, body := b.asks(t, "/verify", added(t, b, d, fmt.Sprintf("erin%d@contoso.com", i)))
		x.Equal(http.StatusAccepted, code, body)
	}
	code, _, body := b.asks(t, "/verify", added(t, b, d, "erin5@contoso.com"))
	x.Equal(http.StatusTooManyRequests, code, body)
	x.Contains(body, "ask again in 10 minutes")
	x.Zero(d.count("erin5@contoso.com"))
}

// TestOnePastAsManyAsMayBeOnTheirWayIsDropped: with eight messages on their
// way, a ninth is dropped and logged -- the recovery form answering 202 as
// ever, somebody signed in told 503 -- and the eight still go once the server
// answers.
//
// Red with no cap on what is on its way.
func TestOnePastAsManyAsMayBeOnTheirWayIsDropped(t *testing.T) {
	x := require.New(t)
	release := make(chan struct{})
	d := serve(t, account.Invited(), func(c *account.Config) {
		record := c.Mail
		c.Mail = func(ctx context.Context, to, subject, link string) error {
			// A server that has stopped answering, until the test says.
			<-release
			return record(ctx, to, subject, link)
		}
	})
	log := stderr(t)
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)

	// erin, signed in with an address to confirm, before anything is on its
	// way.
	e := erinSignsIn(t, d)
	id := added(t, e, d, "erin@contoso.com")

	// Two more people at fabrikam with proved addresses, so that eight can be
	// on their way without any address passing its share.
	for _, who := range []string{"carol", "dave"} {
		h, err := d.ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(), Alias: who,
		}.Build())
		x.NoError(err)
		proved(t, d, h.GetId(), who+"@fabrikam.com")
	}

	b := d.browser(t, "fabrikam.test")
	recovers := func(to string) {
		code, _, body := b.asks(t, "/recover", `{"address":"`+to+`"}`)
		x.Equal(http.StatusAccepted, code, body)
	}
	for _, to := range []string{"bob", "bob", "bob", "carol", "carol", "carol", "dave", "dave"} {
		recovers(to + "@fabrikam.com")
	}

	// The ninth, to an address with room left.
	recovers("dave@fabrikam.com")
	x.Eventually(func() bool { return strings.Contains(log(), "account: mail to …@fabrikam.com: dropped") }, 5*time.Second, 20*time.Millisecond,
		"a ninth went on its way: %s", log())

	code, _, body := e.asks(t, "/verify", id)
	x.Equal(http.StatusServiceUnavailable, code, body)

	free()
	x.Eventually(func() bool {
		return d.count("bob@fabrikam.com") == 3 && d.count("carol@fabrikam.com") == 3 && d.count("dave@fabrikam.com") == 2
	}, 5*time.Second, 20*time.Millisecond, "the eight on their way did not all go")
	x.Never(func() bool { return d.count("dave@fabrikam.com") > 2 }, 300*time.Millisecond, 20*time.Millisecond,
		"the one dropped went anyway")
	x.Zero(d.count("erin@contoso.com"))
	x.NotContains(log(), "dave@")
}
