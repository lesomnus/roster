package frontdoor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// vouch is roster as this package talks to it, with only the two calls a sign
// in makes.
//
// Embedded rather than implemented: everything else panics if it is ever
// reached, which is what a test of a sign-in wants -- a second form that
// quietly called `Verify` would otherwise pass.
type vouch struct {
	rstr.VouchServiceClient

	delegate  func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error)
	delegates int
	revokes   int
}

func (f *vouch) Delegate(ctx context.Context, in *rstr.VouchDelegateRequest, _ ...grpc.CallOption) (*rstr.VouchDelegateResponse, error) {
	f.delegates++
	return f.delegate(in)
}

func (f *vouch) Revoke(ctx context.Context, in *rstr.VouchRevokeRequest, _ ...grpc.CallOption) (*rstr.VouchRevokeResponse, error) {
	f.revokes++
	return rstr.VouchRevokeResponse_builder{}.Build(), nil
}

var (
	someone  = pdid.New(2)
	somehere = pdid.New(3)
)

// halfway is what roster answers a password that was right and is not enough.
func halfway() *rstr.VouchDelegateResponse {
	return rstr.VouchDelegateResponse_builder{
		Verified: rstr.VouchVerifyResponse_builder{
			Holder:       someone.Bytes(),
			Tenant:       somehere.Bytes(),
			Satisfied:    []string{"password"},
			Continuation: "vc_half",
		}.Build(),
	}.Build()
}

// whole is what roster answers when there is nothing left to prove.
func whole() *rstr.VouchDelegateResponse {
	return rstr.VouchDelegateResponse_builder{
		Verified: rstr.VouchVerifyResponse_builder{
			Ok:     true,
			Holder: someone.Bytes(),
			Tenant: somehere.Bytes(),
		}.Build(),
		Token:   "rd_whole",
		Expires: timestamppb.New(time.Now().Add(time.Hour)),
	}.Build()
}

func doorFor(t *testing.T, f *vouch, with func(*Config)) *Door {
	t.Helper()

	c := Config{
		Sessions: authsession.New(authsession.NewMemStore()),
		Vouch:    f,
		Methods:  []string{rstr.MeService_Get_FullMethodName},
		Tenant:   func(ctx context.Context, host string) (string, error) { return "contoso", nil },
	}
	if with != nil {
		with(&c)
	}

	d, err := New(c)
	require.NoError(t, err)

	return d
}

func post(d *Door, path string, body string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://contoso.example.com"+path, strings.NewReader(body))
	if c != nil {
		r.Header.Set("Cookie", c.Name+"="+c.Value)
	}

	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, r)

	return w
}

func firstForm(t *testing.T, d *Door, want int) *http.Cookie {
	t.Helper()

	w := post(d, "/session", `{"alias":"somebody","password":"correct horse battery staple"}`, nil)
	require.Equal(t, want, w.Code, w.Body.String())

	cs := w.Result().Cookies()
	require.Len(t, cs, 1)

	return cs[0]
}

// TestAHalfSessionIsOverWhenTheAppSaidItWas is [Config.Half] being a promise
// rather than a note: *this app is the one that gives up first*.
//
// It was written down and never read. The window was the browser's cookie
// expiry and nothing else, so anything that is not a browser -- curl, or
// somebody holding a cookie they captured -- had until roster's own hold on the
// attempt, five minutes, whatever the app had configured.
func TestAHalfSessionIsOverWhenTheAppSaidItWas(t *testing.T) {
	x := require.New(t)

	f := &vouch{delegate: func(in *rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
		// Roster would still take the continuation: its own hold on the attempt
		// is minutes long, which is the whole reason the shorter number here
		// has to be the one that ends it.
		if in.GetContinuation() != "" {
			return whole(), nil
		}

		return halfway(), nil
	}}
	d := doorFor(t, f, func(c *Config) { c.Half = 25 * time.Millisecond })

	c := firstForm(t, d, http.StatusOK)
	time.Sleep(50 * time.Millisecond)

	w := post(d, "/session/continue", `{"kind":"totp","secret":"123456"}`, c)
	x.Equal(http.StatusUnauthorized, w.Code, "the half session is over and this app is the one that says so")
	x.Equal(1, f.delegates, "and roster is never asked, because the app already knows the answer")

	// And the other half of the same statement, which is what says the clock
	// is being read rather than the door being shut: inside the window the
	// second form is answered.
	f.delegates = 0
	d = doorFor(t, f, nil)

	c = firstForm(t, d, http.StatusOK)

	w = post(d, "/session/continue", `{"kind":"totp","secret":"123456"}`, c)
	x.Equal(http.StatusNoContent, w.Code, w.Body.String())
	x.Equal(2, f.delegates)
}

// TestASignedInBrowserSurvivesAStraySecondForm is the single-use rule reading
// the entry it was about.
//
// `take` removed whatever the cookie named, and a signed-in browser's entry is
// named by the same cookie. So one duplicate POST -- a retry, a page bug,
// anybody who can make that browser send one -- stripped it of its delegation:
// every call afterwards answered *this session cannot act for anybody* while
// the cookie went on resolving, and the delegation roster minted was left
// un-revoked until its own clock ran out.
func TestASignedInBrowserSurvivesAStraySecondForm(t *testing.T) {
	x := require.New(t)

	f := &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
		return whole(), nil
	}}
	d := doorFor(t, f, nil)

	c := firstForm(t, d, http.StatusNoContent)

	w := post(d, "/session/continue", `{"kind":"totp","secret":"123456"}`, c)
	x.Equal(http.StatusUnauthorized, w.Code, "there is no second form to answer")
	x.Equal(1, f.delegates, "and nothing is spent asking")
	x.Equal(0, f.revokes)

	r := httptest.NewRequest(http.MethodGet, "http://contoso.example.com/me", nil)
	r.Header.Set("Cookie", c.Name+"="+c.Value)

	ctx, err := d.Acting(context.Background(), r)
	x.NoError(err, "the browser that was signed in still is")
	x.NotNil(ctx)
}

// TestARosterThatIsDownIsNotAWrongPassword is `frontdoor.js`'s own rule applied
// to the call it was skipped for: *a proxy answering 502 is not a wrong
// password, and a page that said 'no' to it sends somebody to type their
// password again at a server that is down.*
//
// Every error from [Config.Tenant] was a 401. In the deployment that asks
// roster which operator serves a name -- `FrontService.WhoseHost`, which is the
// whole point of not writing the map into the app -- a roster that is
// unreachable made a correct password read as a wrong one, while the identical
// outage one call later, at `Delegate`, correctly answered 500.
func TestARosterThatIsDownIsNotAWrongPassword(t *testing.T) {
	f := &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
		return whole(), nil
	}}

	t.Run("an outage is broken", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, f, func(c *Config) {
			c.Tenant = func(ctx context.Context, host string) (string, error) {
				return "", errors.New("connection refused")
			}
		})

		w := post(d, "/session", `{"alias":"somebody","password":"correct horse battery staple"}`, nil)
		x.Equal(http.StatusInternalServerError, w.Code)
		x.Equal(0, f.delegates)
	})

	t.Run("a name nobody serves is no", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, f, func(c *Config) {
			c.Tenant = func(ctx context.Context, host string) (string, error) {
				return "", ErrUnknownHost
			}
		})

		w := post(d, "/session", `{"alias":"somebody","password":"correct horse battery staple"}`, nil)
		x.Equal(http.StatusUnauthorized, w.Code)
		x.Equal(0, f.delegates)
	})
}

// TestSigningOutRevokesEvenAWindowThatHasPassed.
//
// `expires` on an entry is **this app's** hold on the browser, not roster's on
// the credential -- and only one of the two is a credential. A delegation whose
// entry timed out here is still a live row in roster's table, so an entry
// dropped for being old is a reference thrown away with the thing it referred
// to still working.
//
// Which is why [held.take] does not read the clock and [held.takeHalf] does.
// The clock belongs to the second form, where what is being spent is a string
// roster is holding anyway; it does not belong on the path that exists to tell
// roster to stop holding something.
func TestSigningOutRevokesEvenAWindowThatHasPassed(t *testing.T) {
	x := require.New(t)

	f := &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
		return whole(), nil
	}}
	d := doorFor(t, f, nil)

	c := firstForm(t, d, http.StatusNoContent)

	// Age the entry past whatever this app's window was, the way sitting on a
	// tab overnight does.
	key := c.Value
	d.held.mu.Lock()
	v := d.held.by[key]
	v.expires = time.Now().Add(-time.Hour)
	d.held.by[key] = v
	d.held.mu.Unlock()

	r := httptest.NewRequest(http.MethodDelete, "http://contoso.example.com/session", nil)
	r.Header.Set("Cookie", c.Name+"="+c.Value)
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, r)

	x.Equal(http.StatusNoContent, w.Code)
	x.Equal(1, f.revokes,
		"a delegation this app had stopped holding was left live in roster")
}
