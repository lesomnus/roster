package idptest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// GitHub is a GitHub that agrees with everybody, at the paths GitHub
// Enterprise Server serves: the authorize page that redirects back at once,
// the token endpoint answering the way GitHub's does, and the two REST calls
// who somebody is comes from, under `/api/v3`.
//
// The token endpoint is the part worth faking exactly. GitHub answers it with
// a form rather than JSON unless asked otherwise, and a code it did not hand
// out is an `error` **in a 200**, not a 4xx -- so a relying party that read
// the status alone would sign somebody in with no token.
type GitHub struct {
	*httptest.Server

	// ClientId and Secret are what the token endpoint wants, in the form.
	ClientId string
	Secret   string

	// User is what `/user` answers; Emails what `/user/emails` does, and a
	// 404 when nil -- what GitHub answers a token without `user:email`.
	User   map[string]any
	Emails []map[string]any

	// Avatar is what `/avatars/<anything>` answers with, and a 404 when nil.
	Avatar []byte

	mu    sync.Mutex
	asked url.Values
	sent  url.Values
	auth  []string
}

// GitHubToken is the token the fake hands out, and the one its API wants back.
const GitHubToken = "gho_the-access-token"

// NewGitHub stands one up, closed when the test ends.
func NewGitHub(t *testing.T, clientId, secret string) *GitHub {
	t.Helper()

	g := &GitHub{ClientId: clientId, Secret: secret}
	m := http.NewServeMux()

	m.HandleFunc("GET /login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.asked = r.URL.Query()
		g.mu.Unlock()

		to, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := to.Query()
		q.Set("code", "the-code")
		q.Set("state", r.URL.Query().Get("state"))
		to.RawQuery = q.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	m.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		g.sent = r.PostForm
		asked := g.asked
		g.mu.Unlock()

		w.Header().Set("content-type", "application/x-www-form-urlencoded; charset=utf-8")
		switch why := heldTo(asked, r.PostForm); {
		case r.PostForm.Get("client_id") != g.ClientId || r.PostForm.Get("client_secret") != g.Secret:
			_, _ = w.Write([]byte(url.Values{
				"error":             {"incorrect_client_credentials"},
				"error_description": {"The client_id and/or client_secret passed are incorrect."},
			}.Encode()))
		case r.PostForm.Get("code") != "the-code" || why != "":
			// One error for a code GitHub did not hand out and one handed out
			// for another redirect or another verifier, which is what GitHub
			// answers to both.
			_, _ = w.Write([]byte(url.Values{
				"error":             {"bad_verification_code"},
				"error_description": {"The code passed is incorrect or expired."},
			}.Encode()))
		default:
			_, _ = w.Write([]byte(url.Values{
				"access_token": {GitHubToken},
				"scope":        {"user:email"},
				"token_type":   {"bearer"},
			}.Encode()))
		}
	})
	api := func(w http.ResponseWriter, r *http.Request) bool {
		g.mu.Lock()
		g.auth = append(g.auth, r.URL.Path+" "+r.Header.Get("authorization"))
		g.mu.Unlock()
		if r.Header.Get("authorization") != "Bearer "+GitHubToken {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)

			return false
		}

		return true
	}
	m.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		if !api(w, r) {
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(g.User)
	})
	m.HandleFunc("GET /api/v3/user/emails", func(w http.ResponseWriter, r *http.Request) {
		if !api(w, r) {
			return
		}
		if g.Emails == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)

			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(g.Emails)
	})
	m.HandleFunc("GET /avatars/", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.auth = append(g.auth, r.URL.Path+" "+r.Header.Get("authorization"))
		g.mu.Unlock()
		if g.Avatar == nil {
			http.NotFound(w, r)

			return
		}
		w.Header().Set("content-type", "image/png")
		_, _ = w.Write(g.Avatar)
	})

	g.Server = httptest.NewServer(m)
	t.Cleanup(g.Close)

	return g
}

// Asked is what the last authorize was asked with: `scope`, `client_id`,
// `code_challenge`.
func (g *GitHub) Asked() url.Values {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.asked
}

// Sent is the form the last token request carried.
func (g *GitHub) Sent() url.Values {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.sent
}

// Calls is every call to its API and its avatars, as the path and the
// `authorization` it carried.
func (g *GitHub) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), g.auth...)
}
