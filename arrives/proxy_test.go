package arrives

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// wayOut is as much of an egress proxy as a front door asks of one: a tunnel
// for `CONNECT`, and a request written in absolute form sent on. Where a host
// is, is its to say (`at`) -- which is how a provider in these tests has a
// name nothing resolves -- and a host it was told nothing of is refused, as a
// proxy refuses what its deployment told it to.
type wayOut struct {
	*httptest.Server

	// at is where each host it sends on to is, by the `host:port` asked for.
	at map[string]string

	mu    sync.Mutex
	asked []string
}

// heard is what it was asked, in order: `CONNECT host:port`, or a method and
// the URL in absolute form.
func (p *wayOut) heard() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.asked)
}

func proxyOnThisMachine(t *testing.T) *wayOut {
	t.Helper()

	p := &wayOut{at: map[string]string{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.asked = append(p.asked, r.Method+" "+r.RequestURI)
		at, known := p.at[r.Host]
		p.mu.Unlock()
		if !known {
			http.Error(w, "this proxy sends nothing there", http.StatusForbidden)

			return
		}

		if r.Method == http.MethodConnect {
			up, err := net.Dial("tcp", at)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)

				return
			}
			defer up.Close()
			down, buffered, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer down.Close()
			_, _ = io.WriteString(down, "HTTP/1.1 200 Connection established\r\n\r\n")
			go func() { _, _ = io.Copy(up, buffered) }()
			_, _ = io.Copy(down, up)

			return
		}

		// The provider's own name stays in `Host`, as it does through a proxy.
		out := r.Clone(r.Context())
		out.RequestURI = ""
		out.URL.Host = at
		res, err := (&http.Transport{DisableKeepAlives: true}).RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(p.Close)

	return p
}

// environment is the proxy variables as a deployment's environment has them,
// in both the spellings Go reads, so that whatever this machine's own says is
// not what a test hears.
func environment(t *testing.T, https, http, no string) {
	t.Helper()
	for k, v := range map[string]string{"HTTPS_PROXY": https, "HTTP_PROXY": http, "NO_PROXY": no} {
		t.Setenv(k, v)
		t.Setenv(strings.ToLower(k), v)
	}
	t.Setenv("REQUEST_METHOD", "")
}

// TestAProviderIsAskedThroughTheProxyTheEnvironmentNames: a front door whose
// deployment names a proxy in its environment asks a provider through it --
// connecting to the proxy wherever the proxy is -- and what is not sent
// through it is still held to an address on the internet, or to a host the
// deployment named.
//
// # What it was
//
// For the length of one review, a way to lock a deployment out. A provider
// used to be asked with Go's default client, which reads `HTTPS_PROXY`,
// `HTTP_PROXY` and `NO_PROXY`. The client that replaced it checks the address
// it connects to, and a proxy's address tells it nothing of where the provider
// is, so it was given no proxy at all: a deployment whose only way out is an
// egress proxy could then reach no provider, the public ones included, and
// nobody of it could sign in through one. And had the variables only been
// read again, the connection to the proxy would have been refused for being
// to an address inside, which is where a deployment's proxy is (#277).
//
// So the proxy is read, and connected to as the deployment's own host. Where
// the provider is, is then the proxy's to refuse: this process never sees
// that address.
func TestAProviderIsAskedThroughTheProxyTheEnvironmentNames(t *testing.T) {
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
	begin := func(p *Providers) error {
		_, err := p.Begin(ctx, tenant, "contoso", "example", "https://account.example", "the-state", false)

		return err
	}

	// Two providers on the internet, as far as anybody here can tell: names
	// only the proxy knows the whereabouts of. One is written `https`, which a
	// proxy is asked to tunnel to, and one `http`, which it is asked to fetch.
	secure := scatterOver(t, httptest.NewTLSServer)
	plain := scatter(t)
	const (
		secureHost = "idp.example.com:8443"
		plainHost  = "login.provider.example"
	)
	trusting := func(p *Providers) *Providers {
		pool := x509.NewCertPool()
		pool.AddCert(secure.Certificate())
		p.own.Transport.(*asks).secure.TLSClientConfig = &tls.Config{RootCAs: pool}

		return p
	}

	t.Run("through the proxy, which is on this machine", func(t *testing.T) {
		x := require.New(t)
		tunnels, fetches := proxyOnThisMachine(t), proxyOnThisMachine(t)
		tunnels.at[secureHost] = secure.Listener.Addr().String()
		fetches.at[plainHost] = plain.Listener.Addr().String()
		environment(t, tunnels.URL, fetches.URL, "")

		x.NoError(begin(trusting(relying("https://"+secureHost, PrivateHosts{}))),
			"a provider was not reached through the proxy `HTTPS_PROXY` names")
		x.Equal([]string{"CONNECT " + secureHost}, tunnels.heard())
		x.EqualValues(1, secure.documents.Load())

		// One written `http` is the deployment's own, and named: in the clear
		// a provider is asked nowhere else, through a proxy or not
		// ([TestAProviderIsAskedInTheClearOnlyWhereTheDeploymentNamedIt]).
		own, err := ParsePrivateHosts([]string{plainHost})
		x.NoError(err)
		x.NoError(begin(relying("http://"+plainHost, own)),
			"a provider was not reached through the proxy `HTTP_PROXY` names")
		x.Equal([]string{"GET http://" + plainHost + "/.well-known/openid-configuration"}, fetches.heard())
		x.EqualValues(1, plain.documents.Load())

		// And where a provider is, is the proxy's to refuse: an address inside
		// is asked of it like any other, and it says no.
		err = begin(trusting(relying("https://10.255.255.1:8443", PrivateHosts{})))
		x.ErrorContains(err, "Forbidden")
		x.Contains(tunnels.heard(), "CONNECT 10.255.255.1:8443")
	})

	t.Run("and with none named, an address on this machine is refused as it was", func(t *testing.T) {
		x := require.New(t)
		proxy := proxyOnThisMachine(t)
		environment(t, "", "", "")

		// The proxy's own address, which nothing now says is one.
		err := begin(relying(proxy.URL, PrivateHosts{}))
		x.ErrorContains(err, "is not an address on the internet")
		x.Empty(proxy.heard())
	})

	t.Run("and what is not sent through it is held to the rule", func(t *testing.T) {
		x := require.New(t)
		proxy := proxyOnThisMachine(t)
		inside := scatter(t)

		// `0.0.0.0` is this machine by an address Go does not know to be it,
		// so what keeps a request for it from the proxy is `NO_PROXY` alone:
		// a host inside, sent direct.
		direct := "http://0.0.0.0:" + must(url.Parse(inside.URL)).Port()
		environment(t, proxy.URL, proxy.URL, "0.0.0.0")

		err := begin(relying(direct, PrivateHosts{}))
		x.ErrorContains(err, "is not an address on the internet")
		x.Zero(inside.documents.Load(), "a host `NO_PROXY` sends direct was asked at an address inside")

		// Unless the deployment named it, and then it is asked directly.
		hosts, err := ParsePrivateHosts([]string{"0.0.0.0"})
		x.NoError(err)
		x.NoError(begin(relying(direct, hosts)))
		x.EqualValues(1, inside.documents.Load())

		// And this machine by its own address, which Go sends nothing for
		// through a proxy whatever `NO_PROXY` says.
		err = begin(relying(inside.URL, PrivateHosts{}))
		x.ErrorContains(err, "is not an address on the internet")
		x.EqualValues(1, inside.documents.Load())

		x.Empty(proxy.heard(), "the proxy was asked for what was to go direct")
	})

	// What a deployment writes there is a URL or a bare `host:port`, and the
	// address a transport connects to for it is that host and port -- the
	// scheme's own where none is written.
	for written, dialled := range map[string]string{
		"http://proxy.corp.example:3128": "proxy.corp.example:3128",
		"http://proxy.corp.example":      "proxy.corp.example:80",
		"https://proxy.corp.example":     "proxy.corp.example:443",
		"socks5://proxy.corp.example":    "proxy.corp.example:1080",
		"http://[fd00::1]:3128":          "[fd00::1]:3128",
	} {
		require.Equal(t, dialled, proxyAddr(must(url.Parse(written))), written)
	}
}
