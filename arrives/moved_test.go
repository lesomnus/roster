package arrives

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// TestWhatAnIssuerHandedOverIsSentToNoOther: a connection moved to another
// issuer in the middle of somebody's sign-in is asked nothing with what the
// first issuer handed over -- not its token endpoint with the code, and not
// its userinfo with the token.
//
// # What it was
//
// Not this, while a connection went on being its first issuer until the
// process restarted (`account`'s `TestAConnectionWhoseIssuerMovedIsTheNewIssuer`).
// What mends that is a row believed at every step, and a row believed at every
// step may move between two steps of one sign-in. The callback would then post
// the first issuer's code, with the verifier that spends it, to the second
// one's token endpoint; and a fill would ask the second one's userinfo with the
// first one's token. A connection with no secret is its tenant's
// administrator's to move, so that is whoever moved it being handed both
// (#277).
func TestWhatAnIssuerHandedOverIsSentToNoOther(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	tenant := pdid.New(1)

	// The second issuer: its document is there to be read, and whatever it is
	// sent is counted.
	var sent atomic.Int32
	taking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(taking.Close)
	second := scatter(t)
	second.authorize = byName(taking.URL) + "/authorize"
	second.token = byName(taking.URL) + "/token"
	second.keys = byName(taking.URL) + "/keys"
	second.userinfo = byName(taking.URL) + "/userinfo"

	first, _ := working(t)
	row := func(s *scattered) *rstr.Connection {
		return rstr.Connection_builder{Name: "example", Issuer: byName(s.URL), ClientId: "app"}.Build()
	}
	r := &roster{connection: row(first), profile: rstr.TenantProfile_builder{Fill: true}.Build()}
	p := New(r, nil)
	p.PrivateHosts = localhost(t)

	begin := func() (*Trip, *http.Request) {
		trip, err := p.Begin(ctx, tenant, "contoso", "example", "https://account.example", "the-state", false)
		x.NoError(err)

		return trip, comesBack(t, trip)
	}

	// Moved while the browser was at the first issuer: the code that comes
	// back is not exchanged, and the person starts again.
	trip, back := begin()
	r.connection = row(second)
	_, err := p.Claim(ctx, trip, back)
	x.ErrorIs(err, ErrUnfinished)
	x.Zero(sent.Load(), "the first issuer's code and its verifier were posted to the second")

	// Moved between the exchange and the fill: nobody is asked with the token.
	r.connection = row(first)
	trip, back = begin()
	who, err := p.Claim(ctx, trip, back)
	x.NoError(err)
	r.connection = row(second)
	x.ErrorContains(p.Fill(ctx, pdid.New(2), who), "moved to another issuer")
	x.Zero(sent.Load(), "the first issuer's token was sent to the second one's userinfo")

	// And where the row stayed, the fill is the first issuer's as it was.
	r.connection = row(first)
	x.NoError(p.Fill(ctx, pdid.New(2), who))
}
