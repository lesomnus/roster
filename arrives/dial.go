package arrives

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/idna"
)

// Where a front door connects when a row tells it to.
//
// # Why there is a rule
//
// A `Connection` with no secret is its tenant's administrator's to write
// (`server/core/connection.go`), and its `issuer` is held to being an address
// and to nothing about where that address is.
// A front door then asks that issuer for its document, and asks whatever the
// document names for the rest: the token endpoint is sent the code, the
// verifier and the client's secret, the keys are believed, userinfo is handed
// the token. Each of those is an address somebody outside the deployment
// chose, asked from where this process stands -- beside the deployment's own
// listeners, inside its network, next to its cloud's metadata service -- and
// the first of them is asked at a `GET /login` nobody has signed in for.
//
// So a provider is asked from outside. Only at an address on the internet
// ([public]), checked where the connection is made; following no redirect off
// the host that was asked; and waited for no longer than this package says
// ([connectTimeout], [askTimeout]). One client does all of it ([ownClient]), so
// there is no request to a provider the rule can be left off.
//
// # And not in the clear
//
// The same row says how its provider is asked, and nothing held that to
// anything: an issuer written `http://`, or an `https` one whose
// document names an `http` token endpoint or `http` keys, was asked as
// written. What goes to a token endpoint is the code, the verifier that makes
// the code worth something, and the client's secret; what comes back from the
// keys is what every token of that connection is believed by. In plain `http`
// anybody on the way reads the first and rewrites the second, and whoever
// answers for a connection's keys signs in as anybody who arrives through it.
//
// So a request written `http` is made only to a host the deployment named
// ([Providers.inTheClear]), and on the internet a provider is written `https`.
// It is this package that refuses and not `server/core`, which takes the row:
// a directory on the premises is often plain `http` over a network its
// deployment trusts, and which hosts those are is said to a front door and to
// nothing else -- the same setting, for the same reason, as the exception
// below.
//
// # Why the exception is the deployment's
//
// A directory on the premises is not on the internet, and neither is a test's
// on this machine. Which addresses inside are a provider's is something only
// whoever runs the network knows, so it is the deployment's to say
// ([PrivateHosts]) and it says none by default.
//
// Not the row's to say: the row is what the rule is about. And not a declared
// row's either, though the deployment's own file wrote it (`roster.declared`):
// the rule is about addresses, an address in a file is as reachable as one a
// tenant typed, and a deployment that means it names the host.
//
// # Where the way out is a proxy
//
// A deployment whose only way out is a proxy says so in its environment
// (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`), and that is read as Go reads it.
// A front door that did not read it could reach no provider at all, the
// public ones included, and nobody of that deployment would sign in through
// one -- which is worse than what the rule keeps out. The proxy is the
// deployment's own host, named by the deployment, exactly as a `private_hosts`
// entry is its word about one; so a connection to it is made wherever it is
// ([proxies]).
//
// And for a request that goes out through it, **where the provider is, is the
// proxy's to refuse**. This process connects to the proxy and never sees the
// address the proxy then connects to, so there is nothing here to hold to
// anything. A deployment that sends its way out through a proxy has put the
// proxy in charge of which addresses a tenant's issuer may be at, and
// configures it to refuse its own network. What `NO_PROXY` sends direct is
// dialled here and held to the rule as before, and that is all `private_hosts`
// is then about where a provider is.
//
// How a provider is asked is still seen here: a request written `http` is in
// the clear to the proxy and beyond it. So through a proxy it is made only for
// a host the deployment named as the URL writes it -- its name, or its address
// -- since the address a name comes to is the one thing the proxy keeps to
// itself.

// How long a provider is waited for. `fill.go`'s numbers, which were a
// picture's alone while everything else a provider was asked had no bound at
// all: five seconds for a connection and five more for its TLS handshake, and
// ten for a whole request -- what [fillTimeout] gives the whole of a fill.
const (
	// connectTimeout is one connection made, and the TLS handshake after it.
	connectTimeout = 5 * time.Second

	// askTimeout is one request, from the dial to the last byte of the answer.
	askTimeout = 10 * time.Second
)

// PrivateHosts is the hosts off the internet a deployment says its tenants'
// providers may be at: by name, by address, or a network of them by prefix.
// They are its own, so they are also the only hosts a provider is asked at in
// plain `http`. The zero value names none, which is what a deployment that
// said nothing has.
type PrivateHosts struct {
	names    map[string]struct{}
	prefixes []netip.Prefix
}

// ParsePrivateHosts is [PrivateHosts] as a deployment writes it
// (`private_hosts`): `idp.corp.example`, `10.20.0.5`, `10.20.0.0/16`.
//
// A name is a host as an issuer's URL writes it -- no scheme, no port, no
// path -- and it names that host and no other, on whatever port: what it
// resolves to is the deployment's word about a name of its own. An address or
// a prefix is held against the address a connection is actually made to.
//
// Refused here rather than read loosely, so that a deployment learns of a
// mistake when it starts and not at somebody's sign-in.
func ParsePrivateHosts(named []string) (PrivateHosts, error) {
	var h PrivateHosts
	for _, v := range named {
		v = strings.TrimSpace(v)

		if p, err := netip.ParsePrefix(v); err == nil {
			h.prefixes = append(h.prefixes, p.Masked())

			continue
		}
		if ip, err := netip.ParseAddr(v); err == nil {
			ip = ip.Unmap().WithZone("")
			h.prefixes = append(h.prefixes, netip.PrefixFrom(ip, ip.BitLen()))

			continue
		}

		name := hostname(v)
		if name == "" || strings.ContainsAny(name, "/:@?#*[] \t") {
			return PrivateHosts{}, fmt.Errorf("%q is not a host, an address or a network: "+
				"a name with no scheme, port or path, as idp.corp.example; or 10.20.0.5; or 10.20.0.0/16", v)
		}
		if h.names == nil {
			h.names = map[string]struct{}{}
		}
		h.names[name] = struct{}{}
	}

	return h, nil
}

// holds is whether the deployment named where a connection is about to be
// made: the host as the URL wrote it, or the address it came to.
func (h PrivateHosts) holds(host string, ip netip.Addr) bool {
	if _, ok := h.names[hostname(host)]; ok {
		return true
	}

	ip = ip.Unmap().WithZone("")
	for _, p := range h.prefixes {
		if p.Contains(ip) {
			return true
		}
	}

	return false
}

// hostname is a host as two spellings of it compare: lowered, and without the
// dot a fully qualified name may end in.
func hostname(v string) string {
	return strings.TrimSuffix(strings.ToLower(v), ".")
}

// reaches is whether this front door connects to an address for a provider's
// sake: one on the internet, or one its deployment named.
func (p *Providers) reaches(host string, ip netip.Addr) bool {
	return public(ip) || p.PrivateHosts.holds(host, ip)
}

// inTheClear is whether this front door asks a provider there in plain
// `http`: only at a host its deployment named, by name, by address or by a
// network it is in -- which is how the deployment says a host is its own. `ip`
// is the address a connection is being made to, and no address at all for a
// request that leaves through a proxy, where only the host as it was written
// is known here.
func (p *Providers) inTheClear(host string, ip netip.Addr) bool {
	return p.PrivateHosts.holds(host, ip)
}

// Why a connection for a provider's sake is not made, each beginning after the
// address or the host it is about. Both name the setting, since both are a
// deployment's to answer.
const (
	notOutside = "is not an address on the internet, " +
		"and is not one the deployment said a provider may be at (`private_hosts`)"

	notInTheClear = "would be asked in plain `http`, where what a provider is sent and what it answers are anybody's on the way to read and to rewrite; " +
		"a front door asks so only at a host the deployment named (`private_hosts`), and a provider anywhere else is written `https`"
)

// dialTo connects only where `refuses` says nothing, asked about what a
// transport set out to reach -- a host and its port, as a URL named them --
// and the address that came to. What it says is why not, after the address.
//
// The address is checked where the connection is made, so it is the one
// actually dialled -- after every redirect, and after whatever DNS answered,
// including an answer that changed since anybody last looked.
func dialTo(refuses func(to string, ip netip.Addr) string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{
			Timeout: connectTimeout,
			Control: func(_, address string, _ syscall.RawConn) error {
				at, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip, err := netip.ParseAddr(at)
				if err != nil {
					return fmt.Errorf("%s is not an address", at)
				}
				if why := refuses(addr, ip); why != "" {
					return fmt.Errorf("%s %s", at, why)
				}

				return nil
			},
		}

		return d.DialContext(ctx, network, addr)
	}
}

// ownClient is how a provider is asked anything: its document, its token
// endpoint, its keys, its userinfo, its API, and a picture on that API's host.
//
// It connects only where `reaches` says a front door may
// ([Providers.reaches]), or to the proxy the environment names, through which
// a request goes wherever the proxy lets it; and for a request written `http`,
// only where `inTheClear` says it may ([Providers.inTheClear]). The file
// comment says why of each. It follows no redirect off the host it asked, nor
// from `https` to `http`, since what it carries goes with every hop: the token
// to the provider's API, and the code and the client's secret to its token
// endpoint.
func ownClient(reaches, inTheClear func(host string, ip netip.Addr) bool) *http.Client {
	out := fromEnvironment()

	// way is a transport for one of the two ways a request is written, which
	// is what its connections are held to: `refuses` is asked of each one made
	// for a provider, and never of one to the proxy.
	way := func(proxy func(*http.Request) (*url.URL, error), refuses func(host string, ip netip.Addr) string) *http.Transport {
		return &http.Transport{
			Proxy: proxy,
			DialContext: dialTo(func(to string, ip netip.Addr) string {
				if out.is(to) {
					return ""
				}
				host, _, err := net.SplitHostPort(to)
				if err != nil {
					return notOutside
				}

				return refuses(host, ip)
			}),
			TLSHandshakeTimeout: connectTimeout,
			ForceAttemptHTTP2:   true,

			// A connection to a provider nobody is signing in through is let
			// go, as `http.DefaultTransport` lets one go -- which is what
			// asked a provider for its document until this did.
			MaxIdleConns:    100,
			IdleConnTimeout: 90 * time.Second,
		}
	}

	return &http.Client{
		Timeout: askTimeout,
		Transport: &asks{
			secure: way(out.proxy, func(host string, ip netip.Addr) string {
				if !reaches(host, ip) {
					return notOutside
				}

				return ""
			}),
			plain: way(func(r *http.Request) (*url.URL, error) {
				u, err := out.proxy(r)
				if err != nil || u == nil {
					return u, err
				}
				// Through a proxy the provider's address is the proxy's to
				// see, and to refuse. That it would be asked in the clear is
				// known here all the same, so the host is held to what the
				// deployment named as the URL wrote it: by name, or as an
				// address. A network names addresses, and there is none here
				// to hold it against.
				host := r.URL.Hostname()
				ip, _ := netip.ParseAddr(host)
				if !inTheClear(host, ip) {
					return nil, fmt.Errorf("%s %s", host, notInTheClear)
				}

				return u, nil
			}, func(host string, ip netip.Addr) string {
				switch {
				case !reaches(host, ip):
					return notOutside
				case !inTheClear(host, ip):
					return notInTheClear
				}

				return ""
			}),
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 3 || r.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(r.URL.Host, via[0].URL.Host) {
				return errors.New("a redirect off the provider's own host")
			}

			return nil
		},
	}
}

// asks is a provider asked either way a URL is written, each by a transport
// of its own.
//
// Two, because the rule about a request in the clear is held where a
// connection is made -- the one place the address it came to is known -- and a
// dial is told a host and a port and nothing of the scheme they were written
// under. A transport for each is a dial that knows which it is for, and a
// connection made for one is never handed to the other.
type asks struct {
	secure, plain *http.Transport
}

func (a *asks) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme == "http" {
		return a.plain.RoundTrip(r)
	}

	return a.secure.RoundTrip(r)
}

// CloseIdleConnections is both transports', which is what
// [http.Client.CloseIdleConnections] asks its transport for.
func (a *asks) CloseIdleConnections() {
	a.secure.CloseIdleConnections()
	a.plain.CloseIdleConnections()
}

// proxies is the way out a deployment's environment names, and where a
// transport connects for it.
//
// # How a connection is known to be the proxy's
//
// A transport asks which proxy a request goes through, and then dials: the
// proxy's host and port for a request that has one, and the provider's own
// for one that has not. Nothing at the dial says which of the two it is. So
// each proxy the environment answers with is kept by the address a transport
// dials for it ([proxyAddr]), and a dial to one of those is the proxy's --
// two at most, `HTTPS_PROXY`'s and `HTTP_PROXY`'s, and known before the dial
// they are asked about, since the question comes first.
//
// By host **and port**: the deployment named a listener, and the same host on
// another port is whatever else runs there. A row that names the proxy's own
// listener as its issuer reaches it, which is no more than every request
// through the proxy does.
type proxies struct {
	of func(*url.URL) (*url.URL, error)

	mu sync.Mutex
	at map[string]struct{}
}

// fromEnvironment is [proxies] as the environment has them when a front door
// starts, which is when Go reads them too. Through `x/net`'s reading rather
// than [http.ProxyFromEnvironment]: they are one function, and the second
// keeps what it read the first time anything in the process asked.
func fromEnvironment() *proxies {
	return &proxies{of: httpproxy.FromEnvironment().ProxyFunc(), at: map[string]struct{}{}}
}

// proxy is which proxy a request goes out through, and none for one
// `NO_PROXY` sends direct -- or to this machine, which Go sends nothing for
// through a proxy.
func (p *proxies) proxy(r *http.Request) (*url.URL, error) {
	u, err := p.of(r.URL)
	if err != nil || u == nil {
		return nil, err
	}

	p.mu.Lock()
	p.at[proxyAddr(u)] = struct{}{}
	p.mu.Unlock()

	return u, nil
}

// is is whether a transport dialling `to` is connecting to a proxy the
// environment named.
func (p *proxies) is(to string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.at[to]

	return ok
}

// proxyAddr is the address a transport dials for a proxy: its host and its
// port, the scheme's own where the environment wrote none. `net/http` keeps
// this to itself (`canonicalAddr`), so it is said again here, and a proxy this
// spelled another way would be refused like any address inside.
func proxyAddr(u *url.URL) string {
	host, port := u.Hostname(), u.Port()
	if !isASCII(host) {
		if v, err := idna.Lookup.ToASCII(host); err == nil {
			host = v
		}
	}
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		}
	}

	return net.JoinHostPort(host, port)
}

func isASCII(v string) bool {
	return strings.IndexFunc(v, func(r rune) bool { return r >= utf8.RuneSelf }) < 0
}

// through is a context whose requests go out by [ownClient], for the two
// libraries that ask a provider on this package's behalf: go-oidc and
// `x/oauth2` both take their client from the context, and with none there they
// take `http.DefaultClient`, which connects anywhere and waits for ever.
func (p *Providers) through(ctx context.Context) context.Context {
	// One key for both: go-oidc keeps its client under the one `x/oauth2`
	// reads, and says so.
	return oidc.ClientContext(ctx, p.own)
}

// maxSaid is the most an error carries of what was said to this package: a
// kilobyte, which holds a refusal whole -- where was asked, and why not -- and
// is still a line.
const maxSaid = 1024

// heard is an error with somebody else's server's words in it, as a log may
// carry it.
//
// go-oidc and `x/oauth2` put the body of an answer they could not use into the
// error they return -- the whole of it, whatever answered: a megabyte, or
// another service's page. An app writes that error to its log. So what is kept
// is the start of it, on one line; the error itself is still under it, for
// whoever asks what it is rather than what it says.
type heard struct {
	said string
	err  error
}

func (e *heard) Error() string { return e.said }
func (e *heard) Unwrap() error { return e.err }

// said is an error a provider's answer may be in, cut to what a log line
// holds. Nil is nil.
func said(err error) error {
	if err == nil {
		return nil
	}

	s := err.Error()
	cut := len(s) > maxSaid
	if cut {
		s = s[:maxSaid]
	}
	// Whatever is not text goes, the half of a character the cut left
	// included; and a line break is a space, since a log is read by the line.
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}

		return r
	}, strings.ToValidUTF8(s, ""))
	if cut {
		s += "…"
	}

	return &heard{said: s, err: err}
}
