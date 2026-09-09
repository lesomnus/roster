// Package idptest is a provider that agrees with everybody.
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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	// should carry -- `email`, `email_verified`, `name`.
	Subject string
	Claims  map[string]any

	key *rsa.PrivateKey
}

// New stands one up, closed when the test ends.
func New(t *testing.T, audience string) *Idp {
	t.Helper()
	x := require.New(t)

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)

	p := &Idp{key: k, Audience: audience}
	m := http.NewServeMux()

	m.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.URL,
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"jwks_uri":                              p.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	m.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &k.PublicKey, Algorithm: "RS256", Use: "sig"},
		}})
	})
	m.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		to, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := to.Query()
		q.Set("code", "the-code")
		q.Set("state", r.URL.Query().Get("state"))
		to.RawQuery = q.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	m.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		claims := map[string]any{
			"iss": p.URL,
			"aud": p.Audience,
			"sub": p.Subject,
			"exp": 4102444800,
			"iat": 1700000000,
		}
		for k, v := range p.Claims {
			claims[k] = v
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "unused",
			"token_type":   "Bearer",
			"id_token":     p.Sign(t, claims),
		})
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
