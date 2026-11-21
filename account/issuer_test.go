package account_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAConnectionWhoseIssuerMovedIsTheNewIssuer: a connection is the issuer its
// row names now -- from the next request on, with no restart -- and a round
// trip begun before the row moved is not finished at the issuer it moved to.
//
// # What it was
//
// What an issuer publishes is fetched once and kept. It was kept by the
// connection's name alone, for the life of the process, and never held against
// the row again -- while the client's id and its secret are read from the row
// at every request. So a connection moved to another issuer went on being the
// first one until the front door restarted: browsers were sent to the issuer
// the row had left, that issuer's keys were believed, and a whole sign-in
// finished there, with the secret the row names now posted to its token
// endpoint. A connection erased and added again under the same name was the
// erased one's issuer likewise (#277).
//
// An operator who moves a connection off an issuer has usually stopped
// trusting it, which is the case this was worst in.
func TestAConnectionWhoseIssuerMovedIsTheNewIssuer(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	row := func(name string) *rstr.Connection {
		v, err := d.ungated.Connection().Get(ctx, rstr.ConnectionGetRequest_builder{
			Ref: rstr.ConnectionRef_builder{
				At: rstr.ConnectionRefByAt_builder{Tenant: at, Name: proto.String(name)}.Build(),
			}.Build(),
			Select: rstr.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)

		return v
	}

	// leaves is a browser that asked to sign in and stopped where it was sent:
	// the provider's authorization endpoint, with the state in its cookie.
	leaves := func(name string) (*http.Client, string) {
		jar, err := cookiejar.New(nil)
		x.NoError(err)
		b := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.app.URL+"/login?connection="+name, nil)
		x.NoError(err)
		req.Host = "contoso.test"
		res, err := b.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusFound, res.StatusCode)

		return b, res.Header.Get("Location")
	}

	// returns is that browser going on: to the provider it was sent to, and
	// back to the callback with the code it was given there.
	returns := func(b *http.Client, away string) (int, string) {
		res, err := b.Get(away)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusFound, res.StatusCode)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, res.Header.Get("Location"), nil)
		x.NoError(err)
		req.Host = "contoso.test"
		res, err = b.Do(req)
		x.NoError(err)
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(body)
	}

	// erin signs in, so what the first issuer publishes is held.
	first := d.idp
	first.Subject = "3001"
	code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code, body)
	x.EqualValues(1, first.Exchanges.Load())

	// And somebody else is half way through a round trip to it.
	half, toFirst := leaves("example")
	x.True(strings.HasPrefix(toFirst, first.URL+"/authorize?"), toFirst)

	// The row is moved, with the front door up.
	second := idptest.New(t, clientId)
	second.Subject = "3001"
	was := row("example")
	_, err := d.ungated.Connection().Patch(ctx, rstr.ConnectionPatchRequest_builder{
		Ref:         rstr.ConnectionRef_builder{Id: was.GetId()}.Build(),
		Issuer:      proto.String(second.URL),
		DateUpdated: was.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	_, away := leaves("example")
	x.True(strings.HasPrefix(away, second.URL+"/authorize?"),
		"a browser was sent to the issuer the connection left: %s", away)

	// A whole sign-in is the second issuer's: its token endpoint, its keys.
	b := d.browser(t, "contoso.test")
	code, body = b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code, body)
	x.EqualValues(1, first.Exchanges.Load(), "a code and the client's secret were posted to the issuer the connection left")
	x.EqualValues(1, second.Exchanges.Load())
	code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"erin"`)

	// The round trip left half way comes back with the first issuer's code,
	// which is nobody's to send to the second: it is not finished, and the
	// person starts again.
	code, body = returns(half, toFirst)
	x.Equal(http.StatusInternalServerError, code, body)
	x.EqualValues(1, first.Exchanges.Load())
	x.EqualValues(1, second.Exchanges.Load(), "the first issuer's code, and the verifier that spends it, were posted to the second")

	// Erased and added again under the same name is another provider too.
	_, toFirst = leaves("other")
	x.True(strings.HasPrefix(toFirst, first.URL+"/authorize?"), toFirst)

	_, err = d.ungated.Connection().Erase(ctx, rstr.ConnectionRef_builder{Id: row("other").GetId()}.Build())
	x.NoError(err)
	third := idptest.New(t, clientId)
	_, err = d.ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
		Tenant: at, Name: "other", Issuer: third.URL, ClientId: clientId,
		Scopes: []string{"email"}, SecretRef: "env:EXAMPLE_SECRET",
	}.Build())
	x.NoError(err)

	_, away = leaves("other")
	x.True(strings.HasPrefix(away, third.URL+"/authorize?"),
		"a connection added again was the erased one's issuer: %s", away)
}

// TestAFrontDoorAsksAProviderInsideOnlyWhereItsDeploymentSaid: a front door
// connects to a provider off the internet only at a host its deployment named
// (`account.private_hosts`) -- whoever wrote the row, a tenant's administrator
// or the deployment's own file.
//
// # What it was
//
// A connection with no secret is its tenant's administrator's to write, issuer
// and all, and the sign-in link is a `GET /login` anybody may follow. The
// front door fetched whatever document the issuer's address answered with,
// with nothing asked about the address: this machine, where the deployment's
// own listeners are; its private network; its cloud's metadata service.
// `arrives`'s own test holds each further request -- the token endpoint, the
// keys, userinfo -- to the same rule; this is the rule as a deployment meets
// it, off by default and the deployment's alone to turn (#277).
func TestAFrontDoorAsksAProviderInsideOnlyWhereItsDeploymentSaid(t *testing.T) {
	// Something on this machine that is no provider, counting what asks it.
	const kept = "the-deployments-own"
	var asked atomic.Int32
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, kept)
	}))
	t.Cleanup(inside.Close)

	// Two rows that say a provider is there. One is as a tenant's
	// administrator may write it: no secret, so the row is theirs. The other
	// is as the deployment's file declares one, and is held to the same rule:
	// what a deployment says of an address, it says by naming the host.
	connect := func(t *testing.T, d *deployment) {
		t.Helper()
		for name, labels := range map[string]map[string]string{
			"inside":   nil,
			"declared": {cmd.Declared: "Connection"},
		} {
			_, err := d.ungated.Connection().Add(t.Context(), rstr.ConnectionAddRequest_builder{
				Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
				Name:   name, Labels: labels, Issuer: inside.URL, ClientId: clientId,
			}.Build())
			require.NoError(t, err)
		}
	}

	t.Run("a deployment that named none", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited(), func(c *account.Config) { c.PrivateHosts = arrives.PrivateHosts{} })
		connect(t, d)
		said := stderr(t)
		asked.Store(0)

		for _, name := range []string{"inside", "declared"} {
			// Nobody is signed in: this is the link on the sign-in form.
			code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection="+name, "", nil)
			x.Equal(http.StatusInternalServerError, code, name)
			x.Equal("cannot start\n", body, name)
			x.Zero(asked.Load(), "%s: an address on this machine was asked for a provider's document", name)
		}

		// And whoever runs it is told what to write, where they read.
		x.Eventually(func() bool { return strings.Count(said(), "private_hosts") == 2 }, 5*time.Second, 20*time.Millisecond,
			"the refusals were not logged with what a deployment would write")
		x.Contains(said(), "is not an address on the internet")
	})

	t.Run("and one that named this machine", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		connect(t, d)
		said := stderr(t)
		asked.Store(0)

		// It is asked, and is no provider: which is the log's to say, at no
		// more length than a line, and never the browser's.
		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=inside", "", nil)
		x.Equal(http.StatusInternalServerError, code)
		x.Equal("cannot start\n", body)
		x.EqualValues(1, asked.Load(), "a host the deployment named was not asked")
		x.Eventually(func() bool { return strings.Contains(said(), "418") }, 5*time.Second, 20*time.Millisecond,
			"what the host answered was not logged")
	})
}
