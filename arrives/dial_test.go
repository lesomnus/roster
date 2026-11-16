package arrives

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// byName is a test server's URL with `localhost` where its address was: the
// same listener, as a host somebody can name. Which of the two a row or a
// document writes is what these tests are about -- a deployment that names
// `localhost` has said nothing of `127.0.0.1`.
func byName(at string) string {
	return "http://localhost:" + must(url.Parse(at)).Port()
}

// scattered is a provider as its document describes it, which is all a relying
// party has to go on: the document is at one host, and its token endpoint, its
// keys and its userinfo are wherever it says they are.
type scattered struct {
	*httptest.Server

	// What the document names. Its issuer is the name it was asked under.
	authorize, token, keys, userinfo string

	// documents counts the times it was asked for.
	documents atomic.Int32
}

func scatter(t *testing.T) *scattered { return scatterOver(t, httptest.NewServer) }

// scatterOver is [scatter] on a server of the caller's choosing: one that
// speaks TLS, for a provider written `https`.
func scatterOver(t *testing.T, serve func(http.Handler) *httptest.Server) *scattered {
	t.Helper()

	s := &scattered{}
	m := http.NewServeMux()
	m.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		s.documents.Add(1)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 scheme + "://" + r.Host,
			"authorization_endpoint": s.authorize,
			"token_endpoint":         s.token,
			"jwks_uri":               s.keys,
			"userinfo_endpoint":      s.userinfo,

			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	s.Server = serve(m)
	t.Cleanup(s.Close)

	return s
}

// working is a provider the deployment named as `localhost`, all of it: the
// document, and the fake that signs, by that name. A test moves what it is
// about somewhere else.
func working(t *testing.T) (*scattered, *idptest.Idp) {
	t.Helper()

	idp := idptest.New(t, "app")
	idp.Subject = "erin-at-the-directory"

	s := scatter(t)
	s.authorize = byName(idp.URL) + "/authorize"
	s.token = byName(idp.URL) + "/token"
	s.keys = byName(idp.URL) + "/keys"
	s.userinfo = byName(idp.URL) + "/userinfo"
	idp.Claims = map[string]any{"iss": byName(s.URL)}

	return s, idp
}

// localhost is what a deployment says that names `localhost` and nothing
// else: nothing of the address under it.
func localhost(t *testing.T) PrivateHosts {
	t.Helper()
	hosts, err := ParsePrivateHosts([]string{"localhost"})
	require.NoError(t, err)

	return hosts
}

// comesBack is a browser's part of a round trip: out to the provider's
// authorization endpoint, and back at the callback with the code it was given.
func comesBack(t *testing.T, trip *Trip) *http.Request {
	t.Helper()

	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := browser.Get(trip.URL)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusFound, res.StatusCode, "the provider did not send the browser back")

	back := httptest.NewRequest(http.MethodGet, res.Header.Get("Location"), nil)
	back.SetPathValue("tenant", "contoso")
	back.SetPathValue("connection", "example")

	return back
}

// TestAProviderATenantNamedIsAskedFromOutside: whatever a front door asks for a
// provider's sake -- its document, its token endpoint, its keys, its userinfo
// -- it asks only at an address on the internet or at a host its deployment
// named, follows no redirect off the host it asked, waits no longer than it
// says, and repeats no more of the answer than a log line holds.
//
// # What it was
//
// A connection with no secret is its tenant's administrator's to write, and
// nothing holds its issuer to anything. The front door fetched that issuer's
// document with Go's default client, at a `GET /login` nobody had signed in
// for; posted the code, the verifier and the client's secret to whatever token
// endpoint the document named; fetched the keys from wherever it said they
// were; and asked userinfo, with the person's token, through a client that
// was left unrestricted *because the deployment named it* -- which a tenant
// had. Only a `picture` hosted somewhere else was held to an address on the
// internet.
//
// So a tenant's row sent requests from where the front door stands: to this
// machine, where the deployment's own listeners are, to its private network,
// to its cloud's metadata service. With no timeout, so an issuer that
// accepted a connection and said nothing held a request for as long as the
// browser waited. And go-oidc puts an answer it cannot read into its error
// whole, which an app writes to its log -- the body of whatever answered
// inside, as long as it was (#277).
func TestAProviderATenantNamedIsAskedFromOutside(t *testing.T) {
	ctx := t.Context()
	tenant := pdid.New(1)

	// Something on this machine that nobody named: an admin listener, a
	// metadata service. It counts what asks it, and what it answers is what
	// is not to be read.
	const kept = "the-deployments-own"
	var asked atomic.Int32
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = io.WriteString(w, kept)
	}))
	t.Cleanup(inside.Close)

	// A provider the deployment named, as `localhost`, with nothing said of
	// the address under it.
	named := localhost(t)
	provider := func(t *testing.T) (*scattered, *idptest.Idp) {
		t.Helper()
		asked.Store(0)

		return working(t)
	}
	relying := func(issuer string, hosts PrivateHosts) *Providers {
		p := New(&roster{
			connection: rstr.Connection_builder{Name: "example", Issuer: issuer, ClientId: "app"}.Build(),
			profile:    rstr.TenantProfile_builder{Fill: true}.Build(),
		}, nil)
		p.PrivateHosts = hosts

		return p
	}

	// arrive is one whole round trip.
	arrive := func(t *testing.T, p *Providers) (Caller, error) {
		t.Helper()

		trip, err := p.Begin(ctx, tenant, "contoso", "example", "https://account.example", "the-state", false)
		if err != nil {
			return Caller{}, err
		}

		return p.Claim(ctx, trip, comesBack(t, trip))
	}

	// refused is that a request was not made, and said to be why.
	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "it was asked")
		require.Zero(t, asked.Load(), "an address nobody named was asked: %v", err)
		require.ErrorContains(t, err, "is not an address on the internet")
		require.ErrorContains(t, err, "private_hosts", "the refusal does not say what a deployment would write")
		require.NotContains(t, err.Error(), kept, "what the address answered is in the error")
	}

	t.Run("the control: a provider the deployment named, all of it", func(t *testing.T) {
		x := require.New(t)
		s, _ := provider(t)
		p := relying(byName(s.URL), named)

		who, err := arrive(t, p)
		x.NoError(err)
		x.Equal("erin-at-the-directory", who.Subject)
		x.NoError(p.Fill(ctx, pdid.New(2), who))
	})

	t.Run("its document", func(t *testing.T) {
		x := require.New(t)
		s, _ := provider(t)

		// The same provider by its address, which the deployment did not name.
		_, err := arrive(t, relying(s.URL, named))
		x.Zero(s.documents.Load(), "the document was fetched from an address nobody named")
		refused(t, err)

		// And by a name, for a deployment that named none.
		_, err = arrive(t, relying(byName(s.URL), PrivateHosts{}))
		x.Zero(s.documents.Load(), "the document was fetched from this machine with nothing named")
		refused(t, err)
	})

	t.Run("its token endpoint", func(t *testing.T) {
		s, idp := provider(t)
		s.token = inside.URL + "/token"

		_, err := arrive(t, relying(byName(s.URL), named))
		refused(t, err)
		require.Zero(t, idp.Exchanges.Load())
	})

	t.Run("its keys", func(t *testing.T) {
		s, _ := provider(t)
		s.keys = inside.URL + "/keys"

		_, err := arrive(t, relying(byName(s.URL), named))
		refused(t, err)
	})

	t.Run("its userinfo, which is handed the token", func(t *testing.T) {
		x := require.New(t)
		s, _ := provider(t)
		s.userinfo = inside.URL + "/userinfo"
		p := relying(byName(s.URL), named)

		// Signing in asks userinfo nothing; filling a profile does.
		who, err := arrive(t, p)
		x.NoError(err)
		refused(t, p.Fill(ctx, pdid.New(2), who))
	})

	t.Run("and a redirect is not followed off the host that was asked", func(t *testing.T) {
		x := require.New(t)
		var redirected atomic.Int32
		away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirected.Add(1)
			http.Redirect(w, r, inside.URL+"/document", http.StatusFound)
		}))
		t.Cleanup(away.Close)
		asked.Store(0)

		_, err := arrive(t, relying(byName(away.URL), named))
		x.ErrorContains(err, "a redirect off the provider's own host")
		x.EqualValues(1, redirected.Load())
		x.Zero(asked.Load(), "a redirect was followed to an address nobody named")
	})

	t.Run("and a provider that says nothing is not waited for", func(t *testing.T) {
		x := require.New(t)
		silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		t.Cleanup(silent.Close)

		// The bound is this package's own, shortened so that the test is.
		// What stops the wait otherwise is whoever is asking, and a browser
		// waits a long time: five seconds stands for that here.
		p := relying(byName(silent.URL), named)
		p.own.Timeout = 100 * time.Millisecond

		patient, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		began := time.Now()
		_, err := p.Begin(patient, tenant, "contoso", "example", "https://account.example", "the-state", false)
		x.Error(err)
		x.Less(time.Since(began), 3*time.Second, "the provider was waited for as long as the browser would wait: %v", err)
	})

	t.Run("and what answered is cut before it is a line in a log", func(t *testing.T) {
		x := require.New(t)
		const end = "the-end-of-a-long-answer"
		loud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "line one\nline two\n"+strings.Repeat("x", 1<<20)+end)
		}))
		t.Cleanup(loud.Close)

		_, err := arrive(t, relying(byName(loud.URL), named))
		x.Error(err)
		x.ErrorContains(err, "500", "what went wrong is no longer said")
		x.Less(len(err.Error()), 2048, "an answer was repeated at length")
		x.NotContains(err.Error(), end)
		x.NotContains(err.Error(), "\n", "an answer's line breaks are the log's")
	})
}
