package account_test

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

// clientId is what the fake provider knows this app as.
const clientId = "account-app"

// idp is a provider that agrees with everybody: discovery, a key, an authorize
// endpoint that redirects back at once, and a token endpoint that signs whatever
// `subject` and `claims` say. The same fake `examples/sso` tests against.
type idp struct {
	*httptest.Server

	key     *rsa.PrivateKey
	subject string
	claims  map[string]any
}

func newIdp(t *testing.T) *idp {
	t.Helper()
	x := require.New(t)

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)

	p := &idp{key: k}
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
			"aud": clientId,
			"sub": p.subject,
			"exp": 4102444800,
			"iat": 1700000000,
		}
		for k, v := range p.claims {
			claims[k] = v
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "unused",
			"token_type":   "Bearer",
			"id_token":     p.sign(t, claims),
		})
	})

	p.Server = httptest.NewServer(m)
	t.Cleanup(p.Close)

	return p
}

func (p *idp) sign(t *testing.T, claims map[string]any) string {
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
