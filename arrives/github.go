package arrives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
)

// GitHub, which signs people in with OAuth 2.0 and no OpenID Connect.
//
// # Why it is a provider and not a protocol
//
// Every other connection is an OpenID provider: a discovery document at its
// issuer, an ID token signed with a key it publishes, and the claims in it.
// GitHub's sign-in for people has none of the three. There is no document to
// discover, the token response carries no `id_token`, and who somebody is
// comes from its REST API -- `GET /user` for the account, `GET /user/emails`
// for its addresses. So GitHub's half is code, the way Slack's is
// (`slack.go`), rather than a generic "OAuth 2.0 and a userinfo URL" whose
// one user would be GitHub and whose second call would be GitHub's anyway.
//
// # Which connection is GitHub's
//
// The one whose issuer is: `https://github.com`. A host is a fact about whose
// sign-in this is, which is the sentence the sign-in page already draws
// GitHub's mark by (`ts/lib/signin.tsx`), and a connection's name is the
// operator's word and decides nothing. The issuer is still where the secret
// goes -- the token endpoint is on it -- so what `server/core/connection.go`
// holds a tenant to about moving one holds for GitHub unchanged.
//
// # What it is held to
//
// The subject is `id`, the account's number, which never changes -- and never
// `login`, a username its owner may change and somebody else may then take,
// which is the rule `Identity.subject` states. An address is the account's
// primary one, and verified only when GitHub says it is, because that is the
// condition an invitation by address is read under ([Expected]). And the
// token is spent here and dropped: roster needs who somebody is, not a
// credential for GitHub's API.

// gitHubIssuer is GitHub's when [Providers.GitHub] says nothing.
const gitHubIssuer = "https://github.com"

// gitHubAPI is where github.com's REST API is. GitHub Enterprise Server serves
// its own under the host it signs people in at, at `/api/v3`.
const gitHubAPI = "https://api.github.com"

// gitHubScope is what a GitHub connection asks for whatever else it names, as
// an OpenID connection asks for `openid`: the account's addresses, which
// `/user` leaves out for somebody who keeps theirs private. The account
// itself needs no scope.
const gitHubScope = "user:email"

// gitHub is the REST API of the GitHub an issuer names, or empty for an issuer
// that is not GitHub's.
func (p *Providers) gitHub(issuer string) string {
	is := gitHubIssuer
	if p.GitHub != "" {
		is = strings.TrimSuffix(p.GitHub, "/")
	}
	if strings.TrimSuffix(issuer, "/") != is {
		return ""
	}
	if is == gitHubIssuer {
		return gitHubAPI
	}

	return is + "/api/v3"
}

// gitHubRelying is [Providers.Relying] for a GitHub connection.
//
// The client's credentials go in the form, which is how GitHub says to send
// them. Said here rather than probed for: `x/oauth2` caches what a probe
// found, and a probe is a request GitHub answers with an error first.
func gitHubRelying(issuer, clientId, secret, redirect string, scopes []string) *oauth2.Config {
	at := strings.TrimSuffix(issuer, "/")
	if !slices.Contains(scopes, gitHubScope) {
		scopes = append([]string{gitHubScope}, scopes...)
	}

	return &oauth2.Config{
		ClientID:     clientId,
		ClientSecret: secret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   at + "/login/oauth/authorize",
			TokenURL:  at + "/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		RedirectURL: redirect,
		Scopes:      scopes,
	}
}

// fromGitHub is who GitHub says signed in, asked with the token the exchange
// handed over.
//
// An account whose addresses cannot be read -- a GitHub App not given the
// *Email addresses* permission, which answers 403, or a token without
// `user:email`, which answers 404 -- signs in with no address: the subject is
// who they are, and an address is what an invitation or a name is read from,
// which is said and not refused, as [Providers.attest] says of storing one.
func (p *Providers) fromGitHub(ctx context.Context, api string, tok *oauth2.Token) (Caller, error) {
	var user struct {
		Id     int64  `json:"id"`
		Login  string `json:"login"`
		Name   string `json:"name"`
		Avatar string `json:"avatar_url"`
	}
	if _, err := p.askGitHub(ctx, api, "/user", tok, &user); err != nil {
		return Caller{}, err
	}
	if user.Id <= 0 {
		return Caller{}, errors.New("github: /user answered with no id")
	}

	who := Caller{
		Subject: strconv.FormatInt(user.Id, 10),
		Name:    strings.TrimSpace(user.Name),
		Picture: user.Avatar,
		token:   tok,
	}
	if who.Name == "" {
		who.Name = user.Login
	}

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	code, err := p.askGitHub(ctx, api, "/user/emails", tok, &emails)
	switch {
	case code == http.StatusForbidden || code == http.StatusNotFound:
		slog.WarnContext(ctx, "arrives: GitHub would not say this account's addresses; it signs in with none",
			"subject", who.Subject, "err", err)

		return who, nil
	case err != nil:
		return Caller{}, err
	}
	for _, e := range emails {
		if e.Primary {
			who.Email, who.Verified = e.Email, e.Verified

			break
		}
	}

	return who, nil
}

// askGitHub is one call to GitHub's REST API, and the status it answered with.
func (p *Providers) askGitHub(ctx context.Context, api, path string, tok *oauth2.Token, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(api, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "roster")
	tok.SetAuthHeader(req)

	// GitHub's API is the provider's own, which is the client that carries
	// its token and follows no redirect off its host.
	res, err := p.own.Do(req)
	if err != nil {
		return 0, fmt.Errorf("github: %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, fmt.Errorf("github: %s: %s", path, res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v); err != nil {
		return res.StatusCode, fmt.Errorf("github: %s: %w", path, err)
	}

	return res.StatusCode, nil
}
