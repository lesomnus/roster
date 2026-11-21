package arrives

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAProviderIsAskedInTheClearOnlyWhereTheDeploymentNamedIt: whatever a
// front door asks a provider in plain `http` -- its document, and the token
// endpoint, the keys and the userinfo that document names -- it asks only at a
// host its deployment named (`private_hosts`).
//
// # What it was
//
// A connection's issuer was held to no scheme, at the server or here, and a
// connection with no secret is its tenant's administrator's to write. One
// written `http://` was asked like any other: the code and the client's
// secret posted, and the keys every token of that connection is believed by
// fetched, where anybody on the way reads the first and rewrites the second
// -- and whoever answers for those keys signs in as anybody of that
// connection. So was an issuer written `https` whose document names an `http`
// endpoint (#278).
//
// # Why it is refused here and not where the row is written
//
// A directory on the premises is often plain `http` on a network its
// deployment trusts, and which hosts those are is said to the front door and
// to nothing else. So the server takes the row, and the front door asks in
// the clear only where it was told it may: by name, by address or by network,
// exactly as it is told where a provider off the internet is.
func TestAProviderIsAskedInTheClearOnlyWhereTheDeploymentNamedIt(t *testing.T) {
	ctx := t.Context()
	tenant := pdid.New(1)

	relying := func(issuer string, hosts PrivateHosts) *Providers {
		p := New(&roster{
			connection: rstr.Connection_builder{Name: "example", Issuer: issuer, ClientId: "app"}.Build(),
		}, nil)
		p.PrivateHosts = hosts
		t.Cleanup(p.own.CloseIdleConnections)

		return p
	}
	naming := func(t *testing.T, hosts ...string) PrivateHosts {
		t.Helper()
		h, err := ParsePrivateHosts(hosts)
		require.NoError(t, err)

		return h
	}
	// refused is that nothing was asked, and that the refusal says what a
	// deployment would write.
	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "it was asked in the clear")
		require.ErrorContains(t, err, "plain `http`")
		require.ErrorContains(t, err, "private_hosts", "the refusal does not say what a deployment would write")
	}

	// The rule where a connection is made, with this machine standing in for
	// the internet: an address a front door may reach, which nobody named.
	t.Run("at an address it may reach, where it connects", func(t *testing.T) {
		x := require.New(t)
		s := scatter(t)
		at := must(url.Parse(s.URL))
		anywhere := func(string, netip.Addr) bool { return true }

		ask := func(hosts PrivateHosts, to string) error {
			c := ownClient(anywhere, hosts.holds)
			t.Cleanup(c.CloseIdleConnections)
			res, err := c.Get(to + "/.well-known/openid-configuration")
			if err == nil {
				res.Body.Close()
			}

			return err
		}

		refused(t, ask(PrivateHosts{}, s.URL))
		refused(t, ask(naming(t, "idp.corp.example", "10.20.0.0/16"), s.URL))
		x.Zero(s.documents.Load(), "a host nobody named was asked in the clear")

		// By its name, by its address, or by a network it is in.
		x.NoError(ask(naming(t, "localhost"), byName(s.URL)))
		x.NoError(ask(naming(t, at.Hostname()), s.URL))
		x.NoError(ask(naming(t, "127.0.0.0/8"), byName(s.URL)))
		x.EqualValues(3, s.documents.Load())
	})

	// Two providers on the internet, as far as anybody here can tell: names
	// only a proxy knows the whereabouts of, as in
	// [TestAProviderIsAskedThroughTheProxyTheEnvironmentNames]. Through a
	// proxy the address is never seen here, and the scheme still is.
	const (
		secureHost = "idp.example.com:8443"
		plainHost  = "login.provider.example"
	)

	t.Run("an issuer written http, through a proxy", func(t *testing.T) {
		x := require.New(t)
		plain := scatter(t)
		fetches := proxyOnThisMachine(t)
		fetches.at[plainHost] = plain.Listener.Addr().String()
		environment(t, "", fetches.URL, "")

		begin := func(hosts PrivateHosts) error {
			_, err := relying("http://"+plainHost, hosts).Begin(ctx, tenant, "contoso", "example", "https://account.example", "the-state", false)

			return err
		}

		refused(t, begin(PrivateHosts{}))
		// A network names an address, and through a proxy there is none to
		// hold it against.
		refused(t, begin(naming(t, "127.0.0.0/8")))
		x.Empty(fetches.heard(), "the proxy was asked for a provider nobody named, in the clear")
		x.Zero(plain.documents.Load())

		x.NoError(begin(naming(t, plainHost)))
		x.Equal([]string{"GET http://" + plainHost + "/.well-known/openid-configuration"}, fetches.heard())
	})

	t.Run("and what an https issuer's document names in http", func(t *testing.T) {
		x := require.New(t)

		// The document is at an `https` issuer, and says its token endpoint
		// and its keys are somewhere written `http`.
		idp := idptest.New(t, "app")
		idp.Subject = "erin-at-the-directory"
		idp.Claims = map[string]any{"iss": "https://" + secureHost}
		secure := scatterOver(t, httptest.NewTLSServer)
		secure.authorize = idp.URL + "/authorize"
		secure.token = "http://" + plainHost + "/token"
		secure.keys = "http://" + plainHost + "/keys"
		secure.userinfo = "http://" + plainHost + "/userinfo"

		tunnels, fetches := proxyOnThisMachine(t), proxyOnThisMachine(t)
		tunnels.at[secureHost] = secure.Listener.Addr().String()
		fetches.at[plainHost] = must(url.Parse(idp.URL)).Host
		environment(t, tunnels.URL, fetches.URL, "")

		arrive := func(hosts PrivateHosts) (Caller, error) {
			p := relying("https://"+secureHost, hosts)
			p.own.Transport.(*asks).secure.TLSClientConfig = believing(secure.Server)

			trip, err := p.Begin(ctx, tenant, "contoso", "example", "https://account.example", "the-state", false)
			require.NoError(t, err, "an https issuer was not reached through the proxy")

			return p.Claim(ctx, trip, comesBack(t, trip))
		}

		_, err := arrive(PrivateHosts{})
		refused(t, err)
		x.Empty(fetches.heard(), "a code and its verifier were posted in the clear to a host nobody named")
		x.Zero(idp.Exchanges.Load())

		who, err := arrive(naming(t, plainHost))
		x.NoError(err)
		x.Equal("erin-at-the-directory", who.Subject)
		x.Contains(fetches.heard(), http.MethodPost+" http://"+plainHost+"/token")
	})
}

// believing is a TLS configuration that trusts one test server's certificate
// and no other.
func believing(s *httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())

	return &tls.Config{RootCAs: pool}
}
