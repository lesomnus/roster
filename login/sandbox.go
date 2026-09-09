package login

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/lesomnus/roster/frontdoor"
)

// The Login App with nothing behind it.
//
// # What it is for
//
// Looking at the flow. Every page here is the **real** one -- the same
// `login.html`, the same consent template, the same `frontdoor.js` -- and what
// is made up is the server: there is no roster to ask, no Hydra to be
// redirected by, and no database. So a change to the form can be seen in a
// browser in one command, and somebody deciding whether they want this app at
// all can walk it without standing four containers up.
//
//	roster login sandbox
//
// The console's sandbox does the opposite and compiles the **real** server into
// the page (`wasm/`), which is right for it: what the console is is calls, and
// a fake server would be a fake answer to every question the console exists to
// ask. What this app is, is four pages and the order they come in -- the calls
// behind them are two lines each -- so the thing worth keeping real is the
// pages.
//
// # It is not a test double
//
// Nothing here is imported by anything that runs in a deployment, and no test
// asserts against it. A fake that tests pass against is a fake that has to stay
// true, and this one does not: it says `ok` to a password it has in a map. What
// keeps the real flow honest is `login/login_test.go` against a fake **Hydra**
// with a real roster, and `scripts/hydra.sh` against both.
type Sandbox struct {
	mu    sync.Mutex
	flows map[string]*flow
}

// flow is one browser's place in the walk.
type flow struct {
	who      string
	proved   []string
	consent  bool
	finished bool
}

// A person to sign in as, and what it takes.
type person struct {
	Alias    string
	Password string
	Factor   string // the code their authenticator would be showing, or none
	Name     string
	Email    string
}

// Two people, because one of them is the interesting one: the second form only
// exists for somebody who has a factor, and a sandbox with nobody like that
// shows half the flow.
var sandboxPeople = []person{
	{Alias: "erin", Password: "correct horse battery staple", Name: "Erin Example", Email: "erin@contoso.example"},
	{Alias: "frank", Password: "correct horse battery staple", Factor: "123456", Name: "Frank Example", Email: "frank@contoso.example"},
}

// NewSandbox is the app with nothing behind it.
func NewSandbox() *Sandbox { return &Sandbox{flows: map[string]*flow{}} }

// Handler is the same routes the real app serves, and one more: an index that
// says what this is and who there is to be.
func (s *Sandbox) Handler() http.Handler {
	m := http.NewServeMux()

	m.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		s.open(w)
		form(w)
	})
	m.HandleFunc("POST /session", s.signIn)
	m.HandleFunc("POST /session/continue", s.proceed)
	m.HandleFunc("DELETE /session", s.signOut)
	m.HandleFunc("POST /accept", s.accept)
	m.HandleFunc("GET /consent", s.consent)
	m.HandleFunc("POST /consent", s.decide)
	m.HandleFunc("GET /done", s.done)
	m.HandleFunc("GET /frontdoor.js", frontdoor.Script)
	m.HandleFunc("/", s.index)

	return m
}

// open starts a browser's flow and gives it a cookie to be known by.
//
// The same shape as the real one -- `HttpOnly`, `SameSite=Lax`, no `Secure`
// because this is served over plain http on a desk and nothing else.
func (s *Sandbox) open(w http.ResponseWriter) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	key := hex.EncodeToString(b)

	s.mu.Lock()
	s.flows[key] = &flow{}
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name: "sandbox", Value: key, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Sandbox) at(r *http.Request) *flow {
	c, err := r.Cookie("sandbox")
	if err != nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.flows[c.Value]
}

// signIn is the first form, answered the three ways the real one answers: 204
// for finished, 200 and what is left for half way, 401 for no.
func (s *Sandbox) signIn(w http.ResponseWriter, r *http.Request) {
	f := s.at(r)
	if f == nil {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	var body struct {
		Alias    string `json:"alias"`
		Address  string `json:"address"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)

	who := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(body.Alias+body.Address)), "@contoso.example")
	i := slices.IndexFunc(sandboxPeople, func(p person) bool { return p.Alias == who })
	if i < 0 || sandboxPeople[i].Password != body.Password {
		// One answer for an unknown person and a wrong password, which is the
		// rule the real one keeps and the reason it takes the same time.
		http.Error(w, "no", http.StatusUnauthorized)

		return
	}

	s.mu.Lock()
	f.who, f.proved = sandboxPeople[i].Alias, []string{"password"}
	s.mu.Unlock()

	if sandboxPeople[i].Factor == "" {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	writeJson(w, map[string]any{"satisfied": []string{"password"}, "available": []string{"totp"}})
}

// proceed is the second form.
func (s *Sandbox) proceed(w http.ResponseWriter, r *http.Request) {
	f := s.at(r)
	if f == nil || f.who == "" {
		http.Error(w, "no", http.StatusUnauthorized)

		return
	}

	var body struct {
		Kind   string `json:"kind"`
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)

	i := slices.IndexFunc(sandboxPeople, func(p person) bool { return p.Alias == f.who })
	if i < 0 || sandboxPeople[i].Factor != strings.TrimSpace(body.Secret) {
		http.Error(w, "no", http.StatusUnauthorized)

		return
	}

	s.mu.Lock()
	f.proved = append(f.proved, "totp")
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (s *Sandbox) signOut(w http.ResponseWriter, r *http.Request) {
	if f := s.at(r); f != nil {
		s.mu.Lock()
		*f = flow{}
		s.mu.Unlock()
	}

	w.WriteHeader(http.StatusNoContent)
}

// accept is where the real one tells Hydra a subject. There is nobody to tell,
// so it answers where the browser would have been sent.
func (s *Sandbox) accept(w http.ResponseWriter, r *http.Request) {
	f := s.at(r)
	if f == nil || f.who == "" || !s.whole(f) {
		http.Error(w, "no", http.StatusUnauthorized)

		return
	}

	writeJson(w, map[string]string{"redirect_to": "/consent?consent_challenge=sandbox"})
}

func (s *Sandbox) whole(f *flow) bool {
	i := slices.IndexFunc(sandboxPeople, func(p person) bool { return p.Alias == f.who })

	return i >= 0 && (sandboxPeople[i].Factor == "" || slices.Contains(f.proved, "totp"))
}

// consent draws the screen, which is the whole reason it is worth having a
// sandbox at all: `skip` is a redirect nobody sees.
func (s *Sandbox) consent(w http.ResponseWriter, r *http.Request) {
	f := s.at(r)
	if f == nil || f.who == "" || !s.whole(f) {
		http.Error(w, "no", http.StatusUnauthorized)

		return
	}

	_ = ask(w, &consentRequest{
		Challenge: "sandbox",
		Scope:     []string{"openid", "profile", "email"},
		Client:    client{Id: "demo", Name: "the demo product"},
	})
}

func (s *Sandbox) decide(w http.ResponseWriter, r *http.Request) {
	f := s.at(r)
	if f == nil {
		http.Error(w, "no", http.StatusBadRequest)

		return
	}

	s.mu.Lock()
	f.consent, f.finished = r.FormValue("allow") != "", true
	s.mu.Unlock()

	http.Redirect(w, r, "/done", http.StatusSeeOther)
}

//go:embed done.html
var donePage string

var doneTemplate = template.Must(template.New("done").Parse(donePage))

// done is what a product would have been given, spelled out.
//
// The real app redirects to Hydra here and the browser never sees a page of
// this app again. What it is worth showing instead is the **claims** -- because
// what goes in a token, and what does not, is a decision `login/claims.go`
// makes and a thing somebody looking at this app wants to see.
func (s *Sandbox) done(w http.ResponseWriter, r *http.Request) {
	f := s.at(r)
	if f == nil || !f.finished {
		http.Redirect(w, r, "/", http.StatusSeeOther)

		return
	}

	i := slices.IndexFunc(sandboxPeople, func(p person) bool { return p.Alias == f.who })
	claims := map[string]any{}
	if f.consent && i >= 0 {
		claims = map[string]any{
			"sub":                base64.RawURLEncoding.EncodeToString([]byte("a-holder-id")),
			"preferred_username": sandboxPeople[i].Alias,
			"name":               sandboxPeople[i].Name,
			"email":              sandboxPeople[i].Email,
			"email_verified":     true,
		}
	}
	pretty, _ := json.MarshalIndent(claims, "", "  ")

	b := &bytes.Buffer{}
	_ = doneTemplate.Execute(b, struct {
		Allowed bool
		Proved  []string
		Claims  string
	}{Allowed: f.consent, Proved: f.proved, Claims: string(pretty)})

	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	_, _ = w.Write(b.Bytes())
}

//go:embed sandbox.html
var indexPage string

var indexTemplate = template.Must(template.New("index").Parse(indexPage))

func (s *Sandbox) index(w http.ResponseWriter, r *http.Request) {
	b := &bytes.Buffer{}
	_ = indexTemplate.Execute(b, sandboxPeople)

	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	_, _ = w.Write(b.Bytes())
}
