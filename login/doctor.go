package login

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// The check a deployment runs, and the reason it is here rather than in a
// runbook.
//
// # What this app needs from Hydra, and cannot ask for
//
// The Login App fronts a list of operators, each with a list of OAuth clients,
// and every one of those clients is registered **at Hydra** by somebody else --
// a `hydra import client`, a Job, a console. Nothing in this repository writes
// them and nothing here can: a client is the product's, and its secret is the
// product's.
//
// So the contract between them is written twice and checked nowhere, which is a
// shape with a known ending. It has ended four times:
//
//   - a client registered with no `post_logout_redirect_uris`, so signing out
//     of it ended on the issuer's error page
//   - `urls.post_logout_redirect` unset, so a sign-out with no hint ended on a
//     page addressed to an administrator
//   - a client registered for `client_secret_post` while every relying party
//     here sends the secret in the header, so **every sign-in answered 400**
//   - a client registered at Hydra and left out of `login.clients`, so a flow
//     for it reached a page saying the login is not working
//
// Each was found by a person clicking something in a browser. Each is a
// question this can ask in a second, from inside the deployment, before
// anybody clicks anything.
//
// # What it does not do
//
// It changes nothing. A deployment that registers clients declaratively fixes
// what this finds by editing the declaration; one that does not fixes it by
// hand. Either way the fix is somewhere this app cannot reach, which is why
// this reports rather than repairs.

// Severity is whether a finding stops somebody signing in.
type Severity int

const (
	// Broken is a thing that does not work now. Somebody clicking sign in gets
	// an error.
	Broken Severity = iota

	// Fragile is a thing that works and will stop, or works and does half of
	// what it looks like it does.
	Fragile
)

func (s Severity) String() string {
	if s == Broken {
		return "broken"
	}

	return "fragile"
}

// Finding is one thing wrong, and what it does to a person.
type Finding struct {
	Severity Severity

	// About is the client, or empty for a finding about the deployment.
	About string

	// What is the fact, in the vocabulary of whoever has to fix it.
	What string

	// Costs is what a person sees because of it. Every finding has one; a
	// finding that cannot say what it costs is a preference and does not
	// belong here.
	Costs string
}

func (f Finding) String() string {
	at := f.About
	if at == "" {
		at = "this deployment"
	}

	return fmt.Sprintf("%-8s %-24s %s\n%*s↳ %s", f.Severity, at, f.What, 34, "", f.Costs)
}

// AuthMethod is how every relying party of this stack sends its secret.
//
// **Said rather than discovered, and this is the other half of saying it.**
// `golang.org/x/oauth2` probes for the method -- the header first, the body if
// that fails -- and then caches what worked for the life of the process. So a
// client registered for one method and changed to the other is one a running
// app keeps addressing the old way, with **no second try**: the sign-ins stop
// when the registration changes rather than when anything is deployed, and no
// test sees it, because a test starts a fresh process and the probe finds the
// right answer first time. That is exactly how it went.
//
// `examples/product` pins this in code. This is what makes the registration
// answerable for the same choice.
const AuthMethod = "client_secret_basic"

// hydraClient is what the admin API says about a registered client. It is the
// whole document rather than the two fields the flow needs, because what this
// checks is the fields nothing else reads.
type hydraClient struct {
	Id         string   `json:"client_id"`
	Name       string   `json:"client_name"`
	Grants     []string `json:"grant_types"`
	Responses  []string `json:"response_types"`
	Scope      string   `json:"scope"`
	Redirects  []string `json:"redirect_uris"`
	AfterOut   []string `json:"post_logout_redirect_uris"`
	AuthMethod string   `json:"token_endpoint_auth_method"`
}

// Doctor asks Hydra whether the clients this app fronts are registered in a way
// this stack works with.
//
// `clients` is `login.clients`: an operator's alias against the client ids that
// are theirs. What comes back is every finding, worst first, and an empty slice
// is a deployment with nothing wrong that this can see.
func Doctor(ctx context.Context, hydra string, header http.Header, clients map[string][]string) ([]Finding, error) {
	a := admin{base: strings.TrimSuffix(hydra, "/"), header: header, client: http.DefaultClient}

	out := []Finding{}
	for _, alias := range sorted(clients) {
		for _, id := range clients[alias] {
			v, err := a.getClient(ctx, id)
			if err != nil {
				return nil, err
			}

			out = append(out, check(alias, id, v)...)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity < out[j].Severity })

	return out, nil
}

func sorted(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)

	return out
}

// check is every question asked of one client, and it is the whole of what this
// knows. A `nil` document is a client Hydra has never heard of.
func check(alias, id string, v *hydraClient) []Finding {
	if v == nil {
		return []Finding{{
			Severity: Broken,
			About:    id,
			What:     fmt.Sprintf("%s names it and hydra has no such client", alias),
			Costs:    "every flow raised for it reaches a page saying the login is not working",
		}}
	}

	out := []Finding{}
	add := func(s Severity, what, costs string) {
		out = append(out, Finding{Severity: s, About: id, What: what, Costs: costs})
	}

	switch v.AuthMethod {
	case AuthMethod:
	case "":
		add(Broken, "it names no token_endpoint_auth_method",
			"hydra picks its own default, which is a thing to find out about on the day it changes")
	default:
		add(Broken, fmt.Sprintf("token_endpoint_auth_method is %s, and this stack sends the secret in the header", v.AuthMethod),
			"every sign-in answers 400 -- or works until the app restarts, which is worse")
	}

	if len(v.Redirects) == 0 {
		add(Broken, "it has no redirect_uris",
			"hydra refuses the flow before anybody sees a form")
	}

	if !slices.Contains(v.Grants, "authorization_code") {
		add(Broken, "grant_types does not carry authorization_code",
			"there is no flow this app can complete for it")
	}
	if !slices.Contains(v.Responses, "code") {
		add(Broken, "response_types does not carry code",
			"hydra refuses the flow before anybody sees a form")
	}
	if !slices.Contains(strings.Fields(v.Scope), "openid") {
		add(Broken, "scope does not carry openid",
			"what comes back is an access token and no id_token, so the app learns nobody's name")
	}

	if len(v.AfterOut) == 0 {
		add(Fragile, "it has no post_logout_redirect_uris",
			"an app that sends an id_token_hint to come back here is refused; one that does not lands on urls.post_logout_redirect")
	}

	return out
}
