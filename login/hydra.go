package login

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Hydra's admin API, as much of it as a Login App uses.
//
// # Why this is not the Ory SDK
//
// It is six endpoints, JSON in and JSON out, and the client that speaks them
// is `net/http` and `encoding/json` -- both of which are already here. Taking
// the generated SDK would put a versioned module in `go.mod` for the first
// time in this repository, and what it would buy is types this file writes in
// fifty lines.
//
// What the absence does **not** buy is independence from Hydra: the field names
// below are its wire contract and a change to them breaks this at run time
// rather than at compile time. That is the real cost of the glue and it is
// contained here, in one file, rather than spread through the flow.
//
// # And what it is not authenticated with
//
// Nothing, by default. Hydra's admin port is private by construction -- anybody
// who reaches it can sign anybody in as anybody -- so what protects it is that
// it is not routable, not a credential. A deployment that puts a proxy in front
// of it says so with [Config.HydraHeader], because that is the deployment's
// arrangement and not this app's.

// admin is a client for Hydra's admin API.
type admin struct {
	base   string
	header http.Header
	client *http.Client
}

// client is the OAuth client a challenge was raised for.
//
// The `client_id` used to be what said **which tenant** a flow belonged to,
// through a map this app was configured with. It is a **name on a screen** now
// and nothing else: which tenant comes from the redirect the authorization
// request named, because one product serving a hundred tenants is one client and
// a discriminator that forced a client per customer forced a Hydra registration
// per customer (`login/at.go`).
type client struct {
	Id   string `json:"client_id"`
	Name string `json:"client_name"`
}

// registration is what Hydra holds about a client, which is more than a
// challenge says.
//
// Only `redirect_uris`, and only for the fallback in `arrivedAt`: a client that
// registered exactly one may leave `redirect_uri` out of the authorization
// request, so the URL Hydra recorded has nothing to read. `login/doctor.go` reads
// the same field off the same endpoint for its own audit, with its own struct --
// two narrow shapes over one document rather than one wide one, because what each
// needs is two fields and neither wants the other's.
type registration struct {
	Redirects []string `json:"redirect_uris"`
}

// loginRequest is what Hydra says about a login challenge.
type loginRequest struct {
	Challenge string   `json:"challenge"`
	Subject   string   `json:"subject"`
	Skip      bool     `json:"skip"`
	Scope     []string `json:"requested_scope"`
	Client    client   `json:"client"`
	Url       string   `json:"request_url"`
}

// consentRequest is what Hydra says about a consent challenge.
type consentRequest struct {
	Challenge string   `json:"challenge"`
	Subject   string   `json:"subject"`
	Skip      bool     `json:"skip"`
	Scope     []string `json:"requested_scope"`
	Audience  []string `json:"requested_access_token_audience"`
	Client    client   `json:"client"`

	// The same field the login request carries, and read for the same reason:
	// which name this flow is about (`login/at.go`). Read rather than assumed --
	// where Hydra leaves it empty, `arrivedAt` falls back to the client's
	// registration, which is the path a client that registered one redirect
	// needs anyway.
	Url string `json:"request_url"`
}

// session is the claims a consent puts in the tokens.
type session struct {
	IdToken     map[string]any `json:"id_token,omitempty"`
	AccessToken map[string]any `json:"access_token,omitempty"`
}

// acceptLogin is the body of `PUT .../login/accept`.
type acceptLogin struct {
	Subject     string `json:"subject"`
	Remember    bool   `json:"remember"`
	RememberFor int64  `json:"remember_for"`
}

// acceptConsent is the body of `PUT .../consent/accept`.
type acceptConsent struct {
	Scope       []string `json:"grant_scope"`
	Audience    []string `json:"grant_access_token_audience"`
	Session     session  `json:"session"`
	Remember    bool     `json:"remember"`
	RememberFor int64    `json:"remember_for"`
}

// redirect is what every accept answers with.
type redirect struct {
	To string `json:"redirect_to"`
}

func (a admin) login(ctx context.Context, challenge string) (*loginRequest, error) {
	v := &loginRequest{}

	return v, a.do(ctx, http.MethodGet, "login", "login_challenge", challenge, nil, v)
}

func (a admin) acceptLogin(ctx context.Context, challenge, subject string, remember time.Duration) (string, error) {
	v := &redirect{}
	body := acceptLogin{
		Subject:     subject,
		Remember:    remember > 0,
		RememberFor: int64(remember.Seconds()),
	}
	if err := a.do(ctx, http.MethodPut, "login/accept", "login_challenge", challenge, body, v); err != nil {
		return "", err
	}

	return v.To, nil
}

// registered is what Hydra holds about a client, by id.
//
// Not `.../auth/requests/…`, so it does not go through `do`: that one takes a
// challenge and this takes an id, and a `do` that took either would be a
// function with two shapes.
func (a admin) registered(ctx context.Context, id string) (*registration, error) {
	if id == "" {
		return nil, fmt.Errorf("login: which client")
	}

	u := fmt.Sprintf("%s/admin/clients/%s", a.base, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range a.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("accept", "application/json")

	res, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login: hydra: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))

		return nil, fmt.Errorf("login: hydra: GET clients/%s: %s: %s", id, res.Status, bytes.TrimSpace(b))
	}

	v := &registration{}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return nil, fmt.Errorf("login: hydra: GET clients/%s: %w", id, err)
	}

	return v, nil
}

// acceptDeviceCode hands Hydra the short code somebody typed, and answers with
// where to send them next.
//
// The device grant's one admin call, and it is one rather than two because Hydra
// offers no getter for a device challenge -- there is no
// `.../requests/device`, only `.../requests/device/accept`. So unlike a login, a
// consent or a logout there is nothing to ask **about** this challenge before
// answering it: the screen draws a field, and this is the answer.
//
// What `redirect_to` leads to is the ordinary flow. Hydra binds the code to the
// device that is polling, then raises a `login_challenge` and redirects here
// again -- so everything after this point is `/login` and `/consent`, unchanged,
// and the device half of this app is two handlers wide.
//
// A wrong code is Hydra's refusal and not this app's: *The 'user_code' session
// could not be found or has expired or is otherwise malformed*, which is one
// answer for a code never issued, one already used and one that ran out. Passed
// back as it arrives rather than sorted into cases, for `Vouch.Redeem`'s reason
// said about somebody else's store.
func (a admin) acceptDeviceCode(ctx context.Context, challenge, code string) (string, error) {
	v := &redirect{}
	body := map[string]string{"user_code": code}
	if err := a.do(ctx, http.MethodPut, "device/accept", deviceChallenge, challenge, body, v); err != nil {
		return "", err
	}

	return v.To, nil
}

func (a admin) consent(ctx context.Context, challenge string) (*consentRequest, error) {
	v := &consentRequest{}

	return v, a.do(ctx, http.MethodGet, "consent", "consent_challenge", challenge, nil, v)
}

func (a admin) acceptConsent(ctx context.Context, challenge string, req *consentRequest, claims map[string]any, remember time.Duration) (string, error) {
	v := &redirect{}
	body := acceptConsent{
		// What was asked for, in full, and never more: this is the request's
		// list and not a configured one. Granting **less** would be a screen
		// that lets somebody pick, and neither mode draws that -- `Ask` is a
		// yes or a no about what the client asked for, which is what a person
		// can actually answer.
		Scope:       req.Scope,
		Audience:    req.Audience,
		Session:     session{IdToken: claims},
		Remember:    remember > 0,
		RememberFor: int64(remember.Seconds()),
	}
	if err := a.do(ctx, http.MethodPut, "consent/accept", "consent_challenge", challenge, body, v); err != nil {
		return "", err
	}

	return v.To, nil
}

// logout is the third challenge, and the one that was missing.
//
// A person who clicks *sign out* in a product ends **that product's** session,
// and the issuer was never asked -- so the next page starts a flow Hydra
// answers without a form, and they land signed in. Nothing leaks and it is
// correct, and it is also the most convincing bug report a deployment can
// produce.
//
// So an app that means it sends the browser to Hydra's `end_session_endpoint`,
// Hydra redirects here with a `logout_challenge`, and this says yes.
func (a admin) logout(ctx context.Context, challenge string) (*logoutRequest, error) {
	v := &logoutRequest{}

	return v, a.do(ctx, http.MethodGet, "logout", "logout_challenge", challenge, nil, v)
}

// acceptLogout ends the session Hydra holds for that browser.
//
// The body is empty and there is nothing to say: unlike a login or a consent,
// there is no subject to name and no scope to grant. What is being answered is
// whether the person meant it.
// getClient reads one registered OAuth client, and answers `nil` for one Hydra
// has never heard of.
//
// Not through [admin.do], which builds the path every challenge shares --
// `requests/<thing>?<param>=<challenge>`. A client is addressed by its id in the
// path and there is no challenge, so it is the one call here that is shaped
// differently.
func (a admin) getClient(ctx context.Context, id string) (*hydraClient, error) {
	u := fmt.Sprintf("%s/admin/clients/%s", a.base, url.PathEscape(id))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range a.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("accept", "application/json")

	res, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login: hydra: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))

		return nil, fmt.Errorf("login: hydra: GET clients/%s: %s: %s", id, res.Status, bytes.TrimSpace(b))
	}

	v := &hydraClient{}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return nil, err
	}

	return v, nil
}

// listClients is every client id Hydra holds, following its pages.
//
// Paged because Hydra pages: the default is small and a deployment with a
// product per team runs past it, and a check that silently saw the first
// twenty would be a check that passes for the wrong reason.
func (a admin) listClients(ctx context.Context) ([]string, error) {
	out := []string{}
	next := "/admin/clients?page_size=100"

	for range 100 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+next, nil)
		if err != nil {
			return nil, err
		}
		for k, vs := range a.header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		req.Header.Set("accept", "application/json")

		res, err := a.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("login: hydra: %w", err)
		}

		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
			res.Body.Close()

			return nil, fmt.Errorf("login: hydra: GET clients: %s: %s", res.Status, bytes.TrimSpace(b))
		}

		var page []struct {
			Id string `json:"client_id"`
		}
		err = json.NewDecoder(res.Body).Decode(&page)
		link := res.Header.Get("link")
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, v := range page {
			out = append(out, v.Id)
		}

		// `Link: </admin/clients?…>; rel="next", …`, and a `next` that repeats
		// the page just read is the last one -- Hydra answers that rather than
		// leaving the relation out.
		to := relNext(link)
		if to == "" || to == next {
			break
		}
		next = to
	}

	sort.Strings(out)

	return out, nil
}

// relNext is the `next` target of an RFC 8288 `Link` header, or empty.
func relNext(v string) string {
	for _, part := range strings.Split(v, ",") {
		i, j := strings.Index(part, "<"), strings.Index(part, ">")
		if i < 0 || j < i {
			continue
		}
		if strings.Contains(part[j:], `rel="next"`) {
			return part[i+1 : j]
		}
	}

	return ""
}

// rejectLogout is somebody answering the confirmation with no.
//
// It answers nothing to redirect to, and Hydra's own API has no body for it:
// there is no relying party waiting to be told, because the whole reason a
// person was asked is that nobody proved there was one. The browser stays
// where it is, signed in, which is what the answer meant.
func (a admin) rejectLogout(ctx context.Context, challenge string) error {
	return a.do(ctx, http.MethodPut, "logout/reject", "logout_challenge", challenge, struct{}{}, nil)
}

func (a admin) acceptLogout(ctx context.Context, challenge string) (string, error) {
	v := &redirect{}
	if err := a.do(ctx, http.MethodPut, "logout/accept", "logout_challenge", challenge, struct{}{}, v); err != nil {
		return "", err
	}

	return v.To, nil
}

// logoutRequest is what Hydra says about one.
type logoutRequest struct {
	Challenge string `json:"challenge"`
	Subject   string `json:"subject"`
	Sid       string `json:"sid"`

	// Whether a relying party asked, or somebody typed the URL. It is the one
	// field that decides whether this needs a person's answer -- see
	// `login.go`, `logout`.
	RpInitiated bool `json:"rp_initiated"`

	Client client `json:"client"`
}

// rejectConsent is somebody saying no, which is an answer and not an error.
//
// `access_denied` is the OAuth code for it, so the client is told what happened
// rather than being left at a redirect that never comes.
func (a admin) rejectConsent(ctx context.Context, challenge string) (string, error) {
	v := &redirect{}
	body := map[string]string{
		"error":             "access_denied",
		"error_description": "the person was asked and said no",
	}
	if err := a.do(ctx, http.MethodPut, "consent/reject", "consent_challenge", challenge, body, v); err != nil {
		return "", err
	}

	return v.To, nil
}

// revokeSessions tells Hydra to forget a browser it remembers as somebody.
//
// The other half of `remember`: with it, Hydra skips the form for a browser
// that has already signed in, and this is what makes "signed out everywhere"
// mean it. Idempotent, and 404 for somebody Hydra never remembered is not an
// error worth having -- there is nothing to do about it and nothing was left
// wrong.
//
// Not under `auth/requests/`, which is why it does not go through [admin.do]:
// that path is challenges, and this is the session behind them.
func (a admin) revokeSessions(ctx context.Context, subject string) error {
	u := fmt.Sprintf("%s/admin/oauth2/auth/sessions/login?subject=%s", a.base, url.QueryEscape(subject))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	for k, vs := range a.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	res, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("login: hydra: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("login: hydra: DELETE sessions/login: %s", res.Status)
	}

	return nil
}

// refusal is an answer Hydra gave that was not a success, with the status it gave
// it with.
//
// A type rather than a formatted string because one caller has to tell two things
// apart that every other caller is right to treat as one: `POST /device` is
// answering a person who **typed** something, so *Hydra refused this code* is a
// 400 about what they typed and *Hydra could not be reached* is a 502 about the
// deployment. Everything else here goes to `App.broken`, which is 502 for both --
// correctly, because no other endpoint takes a value a person composed.
//
// The message is unchanged from what this used to format, so a log written before
// this type existed reads the same.
type refusal struct {
	Method string
	Path   string

	// Status is the code, for a caller deciding what to answer. `Says` is the
	// same thing as Hydra phrased it, which is what belongs in a log.
	Status int
	Says   string

	Body string
}

func (e *refusal) Error() string {
	return fmt.Sprintf("login: hydra: %s %s: %s: %s", e.Method, e.Path, e.Says, e.Body)
}

// Refused is whether this is Hydra saying no rather than Hydra being unreachable.
//
// A 4xx and not simply *not 2xx*: a 500 from Hydra is a deployment problem wearing
// a request's clothes, and answering it as *you typed that wrong* would send
// somebody back to retype a code that was correct.
func (e *refusal) Refused() bool { return e.Status/100 == 4 }

// do is the one request shape the five challenge calls share.
func (a admin) do(ctx context.Context, method, path, param, challenge string, in, out any) error {
	if challenge == "" {
		return fmt.Errorf("login: %s: no challenge", path)
	}

	u := fmt.Sprintf("%s/admin/oauth2/auth/requests/%s?%s=%s",
		a.base, path, param, url.QueryEscape(challenge))

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	for k, vs := range a.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	req.Header.Set("accept", "application/json")

	res, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("login: hydra: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode/100 != 2 {
		// The body is Hydra's own error document and it is worth keeping: a
		// challenge that was already used and one that never existed are the
		// two mistakes this app makes, and they read differently there.
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))

		return &refusal{
			Method: method,
			Path:   path,
			Status: res.StatusCode,
			Says:   res.Status,
			Body:   string(bytes.TrimSpace(b)),
		}
	}

	// Nothing to read, for the one call that answers nothing: Hydra's
	// `logout/reject` is a 204, and decoding into a nil is an error about this
	// code rather than about the request.
	if out == nil {
		return nil
	}

	return json.NewDecoder(res.Body).Decode(out)
}
