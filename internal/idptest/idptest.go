// Package idptest is a provider of synthetic identities for relying-party tests.
//
// Discovery, a key, an authorize endpoint that redirects back at once, and a
// token endpoint that signs whatever [Idp.Subject] and [Idp.Claims] say. Both
// of roster's front doors are relying parties against roster's own `Connection`
// rows, so both need one of these, and a second copy would be the thing
// `arrives` exists to prevent one directory down.
//
// It is `internal` and it is only ever imported by a `_test` package: the
// consumer rule in `scripts/test.sh` reads a package's own imports, and what it
// is about is an app reaching roster around the wire rather than a test
// standing up a fake directory.
package idptest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

// Idp is the fake, and its server.
type Idp struct {
	*httptest.Server

	// Audience is the client id it signs tokens for.
	Audience string

	// Subject is who the next sign-in is, and Claims is whatever else the token
	// should carry -- `email`, `email_verified`, `name`. A claim here wins over
	// the one the fake would have written, `iss`, `tid` and `nonce` included.
	Subject string
	Claims  map[string]any

	// Answer adds claims derived from this authorization request, after Claims.
	// It lets a regression model a provider whose token depends on a parameter,
	// rather than issuing the same token for ordinary login and reauthentication.
	Answer func(url.Values) map[string]any

	// Tid is the directory the next sign-in is in, for the fake as Microsoft's
	// authority of more than one: each token's `iss` is `<URL>/<Tid>/v2.0`,
	// and it carries `tid`. What it publishes for that is at the paths
	// Microsoft's is -- `/common/v2.0` and `/organizations/v2.0` name
	// `{tenantid}` in their issuer, `/consumers/v2.0` names the one directory
	// personal accounts are in, and `/<guid>/v2.0` is that directory's own.
	Tid string

	// UserInfo is what `/userinfo` answers beside `sub`, to a caller holding
	// [AccessToken]. A `picture` here and none in [Claims] is Entra's shape.
	UserInfo map[string]any

	// Photo is what `/photo` answers to a caller holding [AccessToken], and a
	// 404 when it is nil: a picture only the token can fetch, as Entra's is.
	// Photos counts the fetches that got one.
	Photo  []byte
	Photos atomic.Int32

	// Exchanges counts the requests its token endpoint was sent, answered or
	// not: a code that reached it reached it, whatever it then said.
	Exchanges atomic.Int32

	key *rsa.PrivateKey

	mu        sync.Mutex
	asked     url.Values
	redirects map[string]struct{}
}

// RegisterRedirects replaces this client's preregistered callback allowlist.
// Call it before the browser starts the round trip, using literal expected
// paths, never the redirect_uri the app sent. An empty list refuses every URI;
// leaving it unset keeps the permissive fake used by unrelated protocol tests.
// A provider that simply echoes the requested URI cannot catch a deployment
// whose callback changed before its external app registration did.
func (p *Idp) RegisterRedirects(uris ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.redirects = make(map[string]struct{}, len(uris))
	for _, uri := range uris {
		p.redirects[uri] = struct{}{}
	}
}

// Asked is what the last authorization request carried: `redirect_uri`,
// `nonce`, `code_challenge`.
func (p *Idp) Asked() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.asked
}

// heldTo is what a provider holds its one code to, from the authorization
// request that it answered: the redirect the exchange has to repeat, the PKCE
// challenge its verifier has to meet, and the nonce the token carries. It is
// what a real provider does and what a relying party that skipped any of it
// would be caught by.
//
// There is one code, so the last authorization request is what it is held to.
func heldTo(asked url.Values, form url.Values) string {
	if asked == nil {
		return ""
	}
	if want := asked.Get("redirect_uri"); want != "" && form.Get("redirect_uri") != want {
		return "the redirect_uri is not the one the code was issued for"
	}
	if challenge := asked.Get("code_challenge"); challenge != "" {
		if asked.Get("code_challenge_method") != "S256" {
			return "the challenge was not S256"
		}
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			return "the code_verifier does not meet the code_challenge"
		}
	}

	return ""
}

// AccessToken is the one the token endpoint hands out, which `/userinfo` and
// `/photo` want back.
const AccessToken = "the-access-token"

// Consumers is the directory every personal Microsoft account is in.
const Consumers = "9188040d-6c67-4c5b-b112-36a304b66dad"

// New stands one up, closed when the test ends.
func New(t *testing.T, audience string) *Idp {
	t.Helper()
	x := require.New(t)

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)

	p := &Idp{key: k, Audience: audience}
	m := http.NewServeMux()

	document := func(w http.ResponseWriter, issuer string) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": p.URL + "/authorize",
			"token_endpoint":         p.URL + "/token",
			"jwks_uri":               p.URL + "/keys",
			"userinfo_endpoint":      p.URL + "/userinfo",
			// The one an app needs to ask the issuer to forget a browser.
			// Discovery is where it is found, and `oidc.Provider` does not
			// model it -- so an app reads it off the raw document and a fake
			// has to publish it.
			"end_session_endpoint":                  p.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}
	m.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		document(w, p.URL)
	})
	m.HandleFunc("GET /{directory}/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		switch d := r.PathValue("directory"); d {
		case "common", "organizations":
			document(w, p.URL+"/{tenantid}/v2.0")
		case "consumers":
			document(w, p.URL+"/"+Consumers+"/v2.0")
		default:
			document(w, p.URL+"/"+d+"/v2.0")
		}
	})
	m.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &k.PublicKey, Algorithm: "RS256", Use: "sig"},
		}})
	})
	m.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p.mu.Lock()
		if p.redirects != nil {
			_, registered := p.redirects[q.Get("redirect_uri")]
			if !registered || len(q["redirect_uri"]) != 1 || len(q["client_id"]) != 1 || q.Get("client_id") != p.Audience {
				p.mu.Unlock()
				// In particular, do not redirect an error to an unregistered URI.
				http.Error(w, "client or redirect_uri is not registered", http.StatusBadRequest)

				return
			}
		}
		p.asked = q
		p.mu.Unlock()

		to, _ := url.Parse(q.Get("redirect_uri"))
		back := to.Query()
		back.Set("code", "the-code")
		back.Set("state", q.Get("state"))
		to.RawQuery = back.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	m.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		to := r.URL.Query().Get("post_logout_redirect_uri")
		if to == "" {
			to = "/"
		}
		http.Redirect(w, r, to, http.StatusFound)
	})
	m.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		p.Exchanges.Add(1)
		_ = r.ParseForm()
		p.mu.Lock()
		asked := p.asked
		unissued := p.redirects != nil && asked == nil
		p.mu.Unlock()
		why := heldTo(asked, r.PostForm)
		if unissued {
			why = "no authorization code was issued"
		}
		if why != "" {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": why})

			return
		}

		claims := map[string]any{
			"iss": p.URL,
			"aud": p.Audience,
			"sub": p.Subject,
			"exp": 4102444800,
			"iat": 1700000000,
		}
		if nonce := asked.Get("nonce"); nonce != "" {
			claims["nonce"] = nonce
		}
		if p.Tid != "" {
			claims["iss"] = p.URL + "/" + p.Tid + "/v2.0"
			claims["tid"] = p.Tid
		}
		for k, v := range p.Claims {
			claims[k] = v
		}
		if p.Answer != nil {
			for k, v := range p.Answer(asked) {
				claims[k] = v
			}
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": AccessToken,
			"token_type":   "Bearer",
			"id_token":     p.Sign(t, claims),
		})
	})
	m.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+AccessToken {
			http.Error(w, "no", http.StatusUnauthorized)

			return
		}
		claims := map[string]any{"sub": p.Subject}
		for k, v := range p.UserInfo {
			claims[k] = v
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(claims)
	})
	m.HandleFunc("/photo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+AccessToken {
			http.Error(w, "no", http.StatusUnauthorized)

			return
		}
		if p.Photo == nil {
			http.NotFound(w, r)

			return
		}
		p.Photos.Add(1)
		w.Header().Set("content-type", "image/png")
		_, _ = w.Write(p.Photo)
	})

	p.Server = httptest.NewServer(m)
	t.Cleanup(p.Close)

	return p
}

// Sign is one token, for a test that wants to hand a bad one over.
func (p *Idp) Sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	x := require.New(t)

	b, err := json.Marshal(claims)
	x.NoError(err)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, nil)
	x.NoError(err)
	o, err := s.Sign(b)
	x.NoError(err)
	v, err := o.CompactSerialize()
	x.NoError(err)

	return v
}
