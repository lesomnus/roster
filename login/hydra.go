package login

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Hydra's admin API, as much of it as a Login App uses.
//
// # Why this is not the Ory SDK
//
// It is four endpoints, JSON in and JSON out, and the client that speaks them
// is `net/http` and `encoding/json` -- both of which are already here. Taking
// the generated SDK would put a versioned module in `go.mod` for the first
// time in this repository, and what it would buy is types this file writes in
// forty lines.
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
// The `client_id` is what says **which operator** this flow belongs to, and it
// is the whole reason this app needs no hostname: it comes from Hydra over the
// admin API rather than from the browser or from this app's own guess.
type client struct {
	Id   string `json:"client_id"`
	Name string `json:"client_name"`
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

func (a admin) consent(ctx context.Context, challenge string) (*consentRequest, error) {
	v := &consentRequest{}

	return v, a.do(ctx, http.MethodGet, "consent", "consent_challenge", challenge, nil, v)
}

func (a admin) acceptConsent(ctx context.Context, challenge string, req *consentRequest, claims map[string]any) (string, error) {
	v := &redirect{}
	body := acceptConsent{
		// What was asked for, in full. A screen that lets somebody grant less
		// than a client asked for is a screen, and this app does not draw one
		// yet -- what it must not do meanwhile is grant **more**, which is why
		// this is the request's list and not a configured one.
		Scope:    req.Scope,
		Audience: req.Audience,
		Session:  session{IdToken: claims},
	}
	if err := a.do(ctx, http.MethodPut, "consent/accept", "consent_challenge", challenge, body, v); err != nil {
		return "", err
	}

	return v.To, nil
}

// do is the one request shape all four share.
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

		return fmt.Errorf("login: hydra: %s %s: %s: %s", method, path, res.Status, bytes.TrimSpace(b))
	}

	return json.NewDecoder(res.Body).Decode(out)
}
