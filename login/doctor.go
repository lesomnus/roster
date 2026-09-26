package login

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lesomnus/roster/server/front"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// The check a deployment runs, and the reason it is here rather than in a
// runbook.
//
// # What this app needs from Hydra, and cannot ask for
//
// The Login App fronts a list of tenants, each with a list of OAuth clients,
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

// PublicAuthMethod is how a **public** client is registered: a page with no
// server of its own, which cannot keep a secret and so has none, and proves it
// is the one that asked for the code with PKCE instead.
//
// It is not the relying parties above with the secret left out. Nothing sends a
// secret for it -- the browser posts the code and the verifier -- so "this stack
// sends the secret in the header" is not a statement about it, and reading
// `none` as a mistake refused the one registration a page can have. What a
// public client costs is that PKCE is the whole of its proof, which is Hydra's
// to enforce (`oauth2.pkce.enforced_for_public_clients`) and not something the
// admin API says.
const PublicAuthMethod = "none"

// DeviceGrant is RFC 8628's grant type, as Hydra spells it.
//
// A client that carries this one and **not** `authorization_code` is a device --
// a television, a shell, anything with no browser to redirect -- and half the
// questions below do not apply to it. See [deviceOnly].
const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// deviceOnly is whether a client can do the device grant and nothing else.
//
// Which changes what is wrong with it, and the checks below were written before
// there was such a thing. A device client registered exactly as it should be would
// have been reported **Broken** twice -- no `authorization_code`, no
// `post_logout_redirect_uris` -- and a check that is loud about a correct
// registration is a check a deployment learns to ignore.
//
// Deliberately *only*: a client that does both is asked every question, because it
// has browser flows to get wrong as well.
func deviceOnly(v *hydraClient) bool {
	return slices.Contains(v.Grants, DeviceGrant) && !slices.Contains(v.Grants, "authorization_code")
}

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
// `clients` is `login.clients`: an tenant's alias against the client ids that
// are theirs. What comes back is every finding, worst first, and an empty slice
// is a deployment with nothing wrong that this can see.
func Doctor(ctx context.Context, hydra, public string, header http.Header, whose Whose) ([]Finding, error) {
	a := admin{base: strings.TrimSuffix(hydra, "/"), header: header, client: http.DefaultClient}
	public = strings.TrimSuffix(public, "/")

	// One registered client, for the probes below: what Hydra was **told** can
	// only be found out by asking it to do something, and asking it to start a
	// flow needs a client and one of its own redirect URIs.
	var sample *hydraClient

	// Every client Hydra holds, and no list to compare against.
	//
	// It was `login.clients`, a map of alias to client id that this app was
	// configured with -- and the two halves of that were *does this client work*
	// and *does anybody claim it*. The second is gone with the map (#36): which
	// tenant a flow is about comes from the redirect it names, so what used to be
	// an unclaimed client is now [ambiguous]'s question, asked of roster's rows
	// rather than of a list somebody maintains.
	ids, err := a.listClients(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)

	out := []Finding{}
	for _, id := range ids {
		v, err := a.getClient(ctx, id)
		if err != nil {
			return nil, err
		}

		if sample == nil && v != nil && len(v.Redirects) > 0 {
			sample = v
		}

		out = append(out, check(id, v)...)
		out = append(out, ambiguous(ctx, whose, id, v)...)
	}

	out = append(out, told(ctx, public, sample)...)

	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity < out[j].Severity })

	return out, nil
}

// Whose is what a hostname resolves to, or an error where nothing claims it.
//
// The seam this file needs and does not want to own: the answer is roster's
// (`FrontService.WhoseHost` over `Host` rows), and every other question here is
// answered by Hydra alone. Nil is a run that could not ask, which is **said**
// rather than passed over -- the same choice `told` makes about `--public`.
type Whose func(ctx context.Context, host string) (alias string, err error)

// ambiguous is the check that replaces `strays`, and it is the one this file
// gained for #36.
//
// A flow resolves to a tenant through the **redirect** the authorization request
// named. So what breaks a client is not being unclaimed -- there is no list to be
// absent from any more -- but having redirects that answer differently:
//
//	none of them resolves    every flow raised for it reaches a page saying the
//	                         login is not working; the tenant has no `Host` row
//	more than one tenant     which tenant a flow is about depends on which
//	                         redirect the browser asked for, and the browser
//	                         chooses from the registered set
//
// The second is a **determinism** finding and not a security one, which is worth
// being exact about because it looks like the other thing. A browser can only
// pick a redirect its client registered, and picking another tenant's means its
// password is checked against that tenant's holders -- whose passwords it does
// not have, at a front door that is public anyway. What is wrong with it is that
// one client then means two customers, which nobody meant to write down.
func ambiguous(ctx context.Context, whose Whose, id string, v *hydraClient) []Finding {
	if v == nil || len(v.Redirects) == 0 {
		// Both already said by `check`, and saying them twice is a run somebody
		// reads twice.
		return nil
	}
	if whose == nil {
		return []Finding{{
			Severity: Fragile,
			About:    id,
			What:     "not asked whether its redirects resolve to a tenant",
			Costs:    "this run cannot reach roster, so which tenant its flows are about is unchecked",
		}}
	}

	seen := map[string]bool{}
	for _, u := range v.Redirects {
		h := front.Hostname(hostOf(u))
		if h == "" {
			continue
		}
		alias, err := whose(ctx, h)
		if err != nil {
			continue
		}
		seen[alias] = true
	}

	switch len(seen) {
	case 0:
		return []Finding{{
			Severity: Broken,
			About:    id,
			What:     "no tenant answers at any of its redirect_uris",
			Costs:    "every flow raised for it reaches a page saying the login is not working; a `Host` row is how a tenant says a name is theirs",
		}}
	case 1:
		return nil
	}

	names := make([]string, 0, len(seen))
	for alias := range seen {
		names = append(names, alias)
	}
	sort.Strings(names)

	return []Finding{{
		Severity: Broken,
		About:    id,
		What:     fmt.Sprintf("its redirect_uris are %s's", strings.Join(names, " and ")),
		Costs:    "which tenant a flow is about then depends on which redirect the browser asked for, and the browser chooses",
	}}
}

// check is every question asked of one client, and it is the whole of what this
// knows. A `nil` document is a client Hydra has never heard of.
func check(id string, v *hydraClient) []Finding {
	if v == nil {
		// **Not broken, and the first cut of this had it the wrong way round.**
		//
		// Hydra raises a challenge for a client it has. One it has never heard
		// of is one no flow can name, so nothing is refused and nobody sees
		// anything -- the row here is a product that has not been registered
		// yet, or a typo that has never been reached. The direction that does
		// cost somebody is the other one, and [strays] is where it is found.
		return []Finding{{
			Severity: Fragile,
			About:    id,
			What:     "hydra has no such client",
			Costs:    "no flow can be raised for it, so nothing is broken -- but if it was meant to be live, it is not",
		}}
	}

	out := []Finding{}
	add := func(s Severity, what, costs string) {
		out = append(out, Finding{Severity: s, About: id, What: what, Costs: costs})
	}

	switch v.AuthMethod {
	case AuthMethod, PublicAuthMethod:
	case "":
		add(Broken, "it names no token_endpoint_auth_method",
			"hydra picks its own default, which is a thing to find out about on the day it changes")
	default:
		add(Broken, fmt.Sprintf("token_endpoint_auth_method is %s, and this stack sends the secret in the header", v.AuthMethod),
			"every sign-in answers 400 -- or works until the app restarts, which is worse")
	}

	device := deviceOnly(v)

	if len(v.Redirects) == 0 {
		// One question, two reasons, and for a device client the reason is the
		// surprising one. An ordinary client needs a redirect because Hydra sends
		// a browser to it; a device client never redirects anywhere and still
		// needs one, because it is the only thing that says **which tenant** a
		// device flow is about (`login/at.go`, `login/device.go`).
		//
		// Said differently for each, because a message about a form would send
		// somebody looking for a form that is not in their product.
		if device {
			add(Broken, "it can do the device grant and has no redirect_uris",
				"a device flow carries no redirect, so nothing says which tenant it is about and the sign-in refuses the right password")
		} else {
			add(Broken, "it has no redirect_uris",
				"hydra refuses the flow before anybody sees a form")
		}
	}

	if !device && !slices.Contains(v.Grants, "authorization_code") {
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

	// And the one question a device is exempt from rather than answered
	// differently. Signing out is a browser ending a session it holds, and a
	// device holds none: what it has is a token that runs out. A row here would
	// be advice to register a URL nothing will ever redirect to.
	if !device && len(v.AfterOut) == 0 {
		add(Fragile, "it has no post_logout_redirect_uris",
			"an app that sends an id_token_hint to come back here is refused; one that does not lands on urls.post_logout_redirect")
	}

	return out
}

// told is what Hydra was configured with, which it will not say.
//
// Its settings are not on the admin API and not in the discovery document --
// `urls.login`, `urls.consent`, `urls.logout`, `urls.post_logout_redirect` are
// a file and environment variables on the other side of a network. What is
// answerable is what Hydra **does**, so these ask it to do the two things whose
// answer is the setting:
//
//   - end a session for a browser that has none. Hydra redirects to
//     `urls.post_logout_redirect`, or, unset, to a fallback page of its own
//     whose text tells whoever clicked sign out to contact an administrator.
//     That page is the end of a **successful** sign-out and it was reported as
//     the sign-out being broken.
//   - start a flow. Hydra redirects to `urls.login`, or to a fallback.
//
// Both are on Hydra's **public** port; its admin port serves neither. Nothing
// here is followed: what is being read is where it points.
func told(ctx context.Context, public string, sample *hydraClient) []Finding {
	if public == "" {
		return nil
	}

	// A client that does not follow anything: the Location **is** the answer.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	at := func(u string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		res, err := c.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()

		return res.Header.Get("location"), nil
	}

	out := []Finding{}
	add := func(s Severity, what, costs string) {
		out = append(out, Finding{Severity: s, What: what, Costs: costs})
	}

	// Discovery first, because it is also the thing that says the rest of this
	// is worth trying.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, public+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil
	}
	res, err := c.Do(req)
	if err != nil {
		add(Fragile, fmt.Sprintf("its public endpoints did not answer at %s", public),
			"nothing here could be checked about what hydra was told; pass --public if it is somewhere else")

		return out
	}
	defer res.Body.Close()

	var doc struct {
		Issuer     string   `json:"issuer"`
		EndSession string   `json:"end_session_endpoint"`
		Methods    []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil || doc.Issuer == "" {
		add(Fragile, fmt.Sprintf("%s answered something that is not a discovery document", public),
			"nothing here could be checked about what hydra was told")

		return out
	}

	if !slices.Contains(doc.Methods, AuthMethod) {
		add(Broken, fmt.Sprintf("it does not offer %s, which is how every relying party here sends its secret", AuthMethod),
			"every sign-in answers 400 at the exchange, whatever the clients say")
	}

	if doc.EndSession == "" {
		add(Broken, "it publishes no end_session_endpoint",
			"an app can end its own session and nothing else, so the next page signs the person straight back in")
	} else if to, err := at(doc.EndSession); err == nil && isFallback(to) {
		add(Broken, "urls.post_logout_redirect is not set",
			"a sign-out that asked to come back nowhere ends on hydra's own page, which tells the person who clicked it to contact an administrator")
	}

	// And where a browser is sent to sign in. It needs a client and one of its
	// own redirect URIs, because Hydra refuses the request before it decides
	// where to send anybody.
	if sample != nil {
		u := fmt.Sprintf("%s/oauth2/auth?client_id=%s&response_type=code&scope=openid&state=%s&redirect_uri=%s",
			public, url.QueryEscape(sample.Id), "roster-login-doctor-probe", url.QueryEscape(sample.Redirects[0]))
		if to, err := at(u); err == nil && isFallback(to) {
			add(Broken, "urls.login does not point at this app",
				"a browser sent here to sign in lands on hydra's fallback page instead of a form")
		}
	}

	return out
}

// isFallback is Hydra answering with a page of its own because it was told
// nowhere to send the browser.
func isFallback(to string) bool { return strings.Contains(to, "/oauth2/fallbacks/") }
