// Package arrives is a `Connection` row turned into a relying party.
//
// # Why it is not in `frontdoor`
//
// `frontdoor`'s package comment says what it lifted out and what it left: *the
// half that is the same in every app, and the half that is that app's -- its
// provider, its pages, its enrolment policy -- left where it was.* That is
// still right about `examples/sso`, whose provider is three lines of its own
// configuration and has nothing to do with roster.
//
// It is not right about roster's own two front doors. Both read the operator's
// providers out of roster -- `Connection`, added on the admin console's *arrives
// through* panel -- and a `Connection` is roster's vocabulary, not an app's.
// `frontdoor` knows nothing about roster's entities on purpose, so a package
// that turns one into an `oauth2.Config` does not belong inside it.
//
// # Why it is not two copies
//
// Because it was, for an afternoon, and the repository has the same lesson
// written down once already: the Login App's page began as a worse copy of the
// account page's sign-in and the fix was `ts/lib/signin.tsx`. The relying party
// is the larger and more dangerous version of that copy -- the discovery cache,
// the client secret, the nonce, the token verification and the enrolment policy
// -- and a second one drifts silently, because both compile.
//
// # What is still the app's
//
// The state parameter and the cookie that binds a browser to it, where the
// browser is sent afterwards, and which tenant it arrived in. This takes a
// tenant it is given and never works one out: `account/` reads the host and the
// Login App reads Hydra's challenge, and neither answer is available here.
//
// And the route the answer comes back to, `GET /callback/{tenant}/{connection}`
// on the app's own mux: [Providers.Begin] tells each provider that path for
// that connection, and [Providers.Claim] reads which connection it names.
package arrives

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// Caller is who a provider said signed in, before this deployment has decided
// whether that is anybody here.
type Caller struct {
	// Which operator they arrived at -- the fact the app established and
	// roster cannot read off a token, carried by the round trip the app began
	// for that tenant ([Trip]).
	Tenant      pdid.Id
	TenantAlias string

	// Provider is the `Connection.name`, which is what `Identity.provider`
	// stores; Subject is the provider's immutable identifier for them, never an
	// address or a username -- the claim the connection names
	// (`Connection.subject_claim`), which is `sub` unless it says otherwise.
	Provider string
	Subject  string

	// sub is the ID token's own `sub`, kept when the connection names another
	// claim as the subject. It is what the person's identity was keyed by
	// before the connection moved to that claim, so [Providers.Known] can find
	// them by it once, and it is who userinfo answers about (OpenID Connect
	// Core 5.3.2). Empty when Subject is the `sub`.
	sub string

	// What else the token carried, for a policy that names people by it.
	Email    string
	Verified bool
	Name     string

	// Picture is the token's `picture` claim: where the provider says a
	// picture of them is, when it said so in the token. Entra never does; its
	// userinfo does, and [Providers.Fill] reads that only when a picture is
	// wanted. GitHub's is the account's `avatar_url`.
	Picture string

	// token is what the exchange handed over, kept for [Providers.Fill] to ask
	// the provider with and for nothing else. Unexported, so it is never in
	// anything an app logs or passes on by accident.
	token *oauth2.Token
}

// tokenSub is the ID token's `sub`, whichever claim Subject is.
func (c Caller) tokenSub() string {
	if c.sub != "" {
		return c.sub
	}

	return c.Subject
}

// Enrol decides what happens when somebody signs in at a provider and roster
// has never seen them.
//
// roster cannot answer that: whether a stranger with a valid Google account
// gets an account here, and as whom, is a policy and every deployment has a
// different one. A policy answers with the holder they are; [Providers.Known]
// links the identity to that holder itself, so a policy cannot forget it or
// write it a second way (`server/core/identity.go` has the rules, and they only
// work if everything goes through them). [ErrUninvited] is the answer that says
// no.
type Enrol func(ctx context.Context, c rstr.Client, who Caller) (pdid.Id, error)

// ErrUninvited is what an [Enrol] answers when this person gets no account.
var ErrUninvited = errors.New("arrives: nobody here")

// Invited refuses everybody roster has not been told about, where *told about*
// means an `Identity` row: this is only reached when there is none, so it is
// the policy that never makes one.
//
// Note what it does **not** do, because it reads as though it should: it does
// not let in somebody an operator entered by name and address. It cannot -- the
// operator does not know the subject a directory will assert, which is an
// opaque identifier issued at that directory and not an address. [Expected] is
// that policy.
func Invited() Enrol {
	return func(context.Context, rstr.Client, Caller) (pdid.Id, error) {
		return pdid.Nil, ErrUninvited
	}
}

// Expected is the operator's invitation: somebody they entered, and nobody
// else. The `Holder` is theirs to make, with an `Email` row carrying the
// address that person will arrive under; the first sign-in finds it and links
// the identity, and every one after that finds the identity.
//
// The address is the only identifier an operator has before the first sign-in.
// A directory's subject is issued at the directory and is not knowable in
// advance, so a policy that matched on nothing else could never admit anybody.
//
// And the row has to be the operator's word: written by somebody other than
// the person, for somebody with no way in yet -- or proved since. An address a
// person wrote onto their own account is their claim, and admitting by it
// would admit the next person to prove that mailbox **as them**; see
// [invitation].
func Expected() Enrol {
	return func(ctx context.Context, c rstr.Client, who Caller) (pdid.Id, error) {
		id, ok, err := invitation(ctx, c, who)
		if err != nil || !ok {
			if err != nil {
				return pdid.Nil, err
			}

			return pdid.Nil, ErrUninvited
		}

		return id, nil
	}
}

// Enrolling makes an account for anybody the provider vouches for, named by the
// local part of their address -- **after** looking for an invitation, so an
// operator may enter the people they know and let the rest arrive. For a
// deployment where signing in at the operator's own directory *is* the
// invitation, which is a decision about the provider and one to take knowing
// the tenant's key has to hold `HolderService.Add` for it.
//
// The lookup is not an optimisation. Without it, entering somebody in advance
// **breaks** their sign-in: the alias an operator chose is the alias this
// derives, and `Holder.Add` answers AlreadyExists.
func Enrolling() Enrol {
	return func(ctx context.Context, c rstr.Client, who Caller) (pdid.Id, error) {
		id, ok, err := invitation(ctx, c, who)
		if err != nil {
			return pdid.Nil, err
		}
		if ok {
			return id, nil
		}

		alias := aliasOf(who.Email)

		v, err := c.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Alias: proto.String(who.TenantAlias)}.Build(),
			Alias:  alias,
			Name:   who.Name,
		}.Build())
		if status.Code(err) == codes.AlreadyExists {
			// Two addresses whose local parts are the same word -- `alice` at
			// two domains, or two people the derivation folded together. Both
			// belong here and one of them cannot have the plain name, so it
			// gets a name nobody chose rather than a refusal: a sign-in that
			// fails because somebody else signed up first is not something the
			// person at the form can do anything about.
			v, err = c.Holder().Add(ctx, rstr.HolderAddRequest_builder{
				Tenant: rstr.TenantRef_builder{Alias: proto.String(who.TenantAlias)}.Build(),
				Alias:  alias + "-" + slug.RandomAliasN(4),
				Name:   who.Name,
			}.Build())
		}
		if err != nil {
			return pdid.Nil, fmt.Errorf("enrol %s: %w", who.Email, err)
		}

		return pdid.From(v.GetId())
	}
}

// AliasOf is what to call somebody a directory sent, from their address: the
// rule a sign-in that enrols somebody names them by, for a directory that
// provisions them instead (`scim`).
func AliasOf(address string) string { return aliasOf(address) }

// aliasOf is what to call somebody a directory sent, from the address it sent.
//
// # Why the local part is not the answer on its own
//
// It is not an alias, and the common forms are exactly the ones that are not:
// an alias begins with a lowercase letter and holds lowercase letters, digits
// and single hyphens, so `first.last` -- which is what a corporate directory
// hands out -- is refused, and so are `first+tag`, `first_last` and `3rin`.
// This derived the local part unchanged and the first person with a dot in
// their address got a 500 at the end of an otherwise complete sign-in.
//
// So every run of anything else becomes one hyphen, the ends are trimmed, and
// what is left has to start with a letter. `Erin.Hart@contoso.example` is
// `erin-hart`.
//
// # And why it can still answer with a name nobody chose
//
// Because an address may hold nothing an alias can be made of -- `123@`, or a
// local part that is not Latin at all. payday's answer to *a row that needs a
// name before anybody has an opinion about it* is [slug.RandomAlias], which is
// what a server does with an `Add` that named nothing, and it is the right
// answer here for the same reason: a name is changed afterwards and a refused
// sign-in is not.
func aliasOf(address string) string {
	local, _, _ := strings.Cut(address, "@")

	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(local)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			// An alias begins with a letter, so digits before the first one
			// are dropped rather than carried to a name that will be refused.
			if b.Len() == 0 && !(r >= 'a' && r <= 'z') {
				continue
			}
			if dash {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)

		default:
			// A run of anything else is one hyphen, and only once something has
			// been written -- which is what keeps it off both ends.
			dash = b.Len() > 0
		}
	}

	v := b.String()
	if len(v) > slug.AliasMaxLen {
		v = strings.TrimRight(v[:slug.AliasMaxLen], "-")
	}
	if slug.Validate(v) != nil {
		return slug.RandomAlias()
	}

	return v
}

// invitation is the `Holder` a verified address names to somebody arriving at
// it, if it names one.
//
// # Why this is not a new grant
//
// `CLAUDE.md` already says what an `Email` row is: *`Identity.Add` and
// `Email.Add` sound like keeping a directory tidy and each is a way to sign in
// as whoever the row is about.* This is that sentence carried out. Writing the
// row is gated where every grant is (`server/core/escalate.go`, *nobody writes
// a way into an account wider than their own*), an address is unique within a
// tenant so it cannot be claimed twice, and the wall means the row read is one
// this caller could already see.
//
// # The provider must say the address is verified
//
// Without that, a directory that lets somebody type an arbitrary address into
// their own profile is a directory that hands out whichever account carries
// it. `email_verified` is the claim -- or `xms_edov`, which is how Microsoft
// says it (`microsoft.go`) -- and an unverified one is not an answer here: it
// falls through to whatever the policy does with a stranger.
//
// # And the row must be somebody's word that it is them
//
// Which is the half that was missing. This matched whatever row held the
// address, and two of the roads to a row are the holder's own: an address they
// typed onto their account, and one a directory handed over unchecked at their
// sign-in. Each is a claim nobody has checked, and matched by address each
// handed whoever arrived verified at that mailbox to the account that made it.
// Somebody signs in first through a connection that lets its users say any
// address -- any directory's administrator, through Microsoft's sign-in for
// any account -- the address lands on their own row, and the rightful holder's
// first sign-in through GitHub is linked to that account and signed in as it.
// `vouch.byAddress` refuses an unproved row for the same reason.
//
// So a row names somebody arriving in two cases and in no other:
//
//   - **It is proved** (`date_verified`). Somebody showed they read that
//     mailbox, and a verified arrival at it is them.
//   - **It is an invitation.** Somebody other than its holder wrote it
//     (`entered_by`), no directory vouched for it (`vouched_by`), and the
//     holder has no way in yet -- no identity, no credential, no key
//     (`HolderService.SignsIn`). That is an operator entering somebody before
//     they arrive, which is what [Expected] is for, and it lasts until they
//     do: their first sign-in stamps the address, and from then it is proved.
//
// Anything else is a stranger to this lookup, and the policy decides about them
// as about anybody. The address then follows the proof the way it does
// everywhere: [Providers.attest] hands it to `Email.Attest`, which takes an
// address nobody proved from whoever held it.
func invitation(ctx context.Context, c rstr.Client, who Caller) (pdid.Id, bool, error) {
	if who.Email == "" || !who.Verified {
		return pdid.Nil, false, nil
	}

	v, err := c.Email().Get(ctx, rstr.EmailGetRequest_builder{
		Ref: rstr.EmailRef_builder{
			At: rstr.EmailRefByAt_builder{
				TenantId: who.Tenant.Bytes(),
				// The same normalisation the write is held to, from the same
				// function: a lookup that lowers against a column that does not
				// is an index comparing strings this never compares.
				Address: proto.String(front.Address(who.Email)),
			}.Build(),
		}.Build(),
		Select: rstr.EmailSelect_builder{
			DateVerified: proto.Bool(true),
			EnteredBy:    proto.Bool(true),
			VouchedBy:    rstr.IdentitySelect_builder{}.Build(),
			Holder:       rstr.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return pdid.Nil, false, err
		}

		return pdid.Nil, false, nil
	}

	h := v.GetHolder()
	if h == nil || len(h.GetId()) == 0 {
		return pdid.Nil, false, nil
	}
	id, err := pdid.From(h.GetId())
	if err != nil {
		return pdid.Nil, false, err
	}

	switch {
	case v.GetDateVerified() != nil:
		return id, true, nil
	case len(v.GetVouchedBy().GetId()) > 0:
		// A directory's word that it did not check.
		return pdid.Nil, false, nil
	case bytes.Equal(v.GetEnteredBy(), h.GetId()):
		// Their own claim about a mailbox.
		return pdid.Nil, false, nil
	}

	ways, err := c.Holder().SignsIn(ctx, rstr.HolderSignsInRequest_builder{
		Ref: rstr.HolderRef_builder{Id: id.Bytes()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, false, err
	}
	if len(ways.GetIdentities()) > 0 || len(ways.GetCredentials()) > 0 || len(ways.GetKeys()) > 0 {
		// Somebody who has arrived already: whatever wrote the address, a way
		// into their account is not handed to whoever proves a mailbox now.
		return pdid.Nil, false, nil
	}

	return id, true, nil
}

// Providers is one app's relying-party half.
//
// Every call takes a context the app has already put its own tenant key on --
// the wall is what narrows a `Connection` list to one operator, and this holds
// no key of its own so that it cannot narrow it wrongly.
type Providers struct {
	roster rstr.Client
	secret func(ref string) (string, error)

	// discovery is the document per (tenant, connection), fetched once.
	discovery sync.Map // tenantId + "\x00" + name -> *oidc.Provider

	// own and anywhere fetch a picture: from the provider's own API, with
	// the token it handed over, and from anywhere else, without it and only
	// on the internet. `fill.go` says why there are two.
	own, anywhere *http.Client

	// SlackAPI is where Slack's Web API is, for a tenant whose profiles are
	// filled from Slack (`TenantProfile.slack_secret_ref`). Empty is Slack's
	// own; a test, or a deployment on another Slack, names its own.
	SlackAPI string

	// GitHub is the issuer a connection names to be GitHub's sign-in rather
	// than an OpenID provider's (`github.go`). Empty is github.com's own,
	// `https://github.com`; a test, or a deployment on GitHub Enterprise
	// Server, names its own, whose REST API is then `/api/v3` beside it.
	GitHub string

	// Microsoft is the origin of the Microsoft identity platform a token's
	// `xms_edov` is believed from (`microsoft.go`). Empty is Microsoft's own:
	// its global cloud and its national ones. A test names its own.
	Microsoft string
}

// New is the relying party for whatever `Connection` rows the given client can
// see. `secret` turns a `Connection.secret_ref` into the client secret it
// names; roster stores that reference and never reads it.
func New(c rstr.Client, secret func(ref string) (string, error)) *Providers {
	return &Providers{roster: c, secret: secret, own: ownClient(), anywhere: publicClient(public)}
}

// Connections is one tenant's providers, in the order roster lists them.
func (p *Providers) Connections(ctx context.Context, tenant pdid.Id) ([]*rstr.Connection, error) {
	vs, err := p.roster.Connection().List(ctx, rstr.ConnectionListRequest_builder{
		Filters: []*rstr.ConnectionFilter{rstr.ConnectionFilter_builder{
			Tenant: rstr.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return nil, err
	}

	return vs.GetItems(), nil
}

// A Trip is one round trip to a connection, started and not yet finished: the
// authorization request to send the browser with, and what the answer that
// comes back is held to.
//
// The app keeps it beside its own flow, by the state, and hands it back to
// [Providers.Claim] at the callback. Nothing in it but [Trip.URL] is the app's
// to read. The PKCE verifier leaves this process only at the exchange, to the
// provider, and the nonce comes back only inside a token the provider signed:
// they are what prove the answer is this trip's, and the redirect is what the
// exchange has to repeat.
//
// # What it holds the answer to, and why each
//
//   - **The connection.** Every connection has a redirect of its own,
//     `<origin>/callback/<tenant>/<connection>`, and the answer has to come
//     back to the one this trip was sent with. One redirect for every tenant
//     and connection was a mix-up waiting to happen: a tenant's administrator
//     may add a connection to a server of their own, and that server could send
//     the browser on to another tenant's provider, whose code then came back to
//     the one URL every flow shares -- and was exchanged at the first server's
//     token endpoint, on the state of the flow that began there. A redirect per
//     connection is a code that can only arrive at its own connection's door,
//     which is the defence RFC 9700 names; PKCE alone does not give it.
//   - **The PKCE verifier**, S256, on every connection. A code that leaks is
//     worth nothing without it, so a code injected into somebody else's
//     browser -- the other half of the same attack -- is refused at the
//     exchange by the provider itself. GitHub and Microsoft accept it, and a
//     provider that does not ignores it (RFC 7636 § 5).
//   - **The nonce**, on an OpenID connection, held against the ID token's own.
//     GitHub issues no ID token, so there is nothing for one to be in.
type Trip struct {
	// URL is where to send the browser.
	URL string

	tenant     pdid.Id
	alias      string
	connection string
	redirect   string
	verifier   string
	nonce      string
}

// callbackOf is where a connection's answer comes back: one path per tenant and
// connection under the app's origin, which is what the provider is told and
// what an operator registers there.
func callbackOf(origin, alias, connection string) string {
	return strings.TrimSuffix(origin, "/") + "/callback/" + url.PathEscape(alias) + "/" + url.PathEscape(connection)
}

// Begin starts a round trip to one connection of one tenant. `origin` is where
// the browser reaches this app -- `https://login.example.com` -- and `state`
// is the app's own, which the answer carries back and the app binds to the
// browser.
func (p *Providers) Begin(ctx context.Context, tenant pdid.Id, alias, connection, origin, state string) (*Trip, error) {
	t := &Trip{tenant: tenant, alias: alias, connection: connection, redirect: callbackOf(origin, alias, connection)}

	cfg, v, err := p.relying(ctx, t)
	if err != nil {
		return nil, err
	}

	t.verifier = oauth2.GenerateVerifier()
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(t.verifier)}
	if v.gitHub == "" {
		if t.nonce, err = Nonce(); err != nil {
			return nil, err
		}
		opts = append(opts, oidc.Nonce(t.nonce))
	}
	t.URL = cfg.AuthCodeURL(state, opts...)

	return t, nil
}

// verifier is a connection's check of what came back, and which claim of a
// token is the person's subject there (`Connection.subject_claim`).
//
// One value rather than a second return, so that what a subject is read by
// cannot come from a different connection than the check did -- and unexported,
// with the token check inside it unexported too. For an authority of more than
// one directory the check go-oidc makes is told to skip the issuer, and what
// stands in for it is [inDirectory]; a caller holding the check alone could run
// the half that skips, so nothing outside this package holds it.
//
// A GitHub connection has no token to check: GitHub issues none, and is asked
// who signed in instead, at the API in `gitHub` (`github.go`). A connection to
// an authority of more than one directory holds each token to the directory
// it names, by the template in `directories` (`microsoft.go`).
type verifier struct {
	id *oidc.IDTokenVerifier

	subject     string
	gitHub      string
	directories string

	// microsoft is whether the issuer is Microsoft's, which is the one place
	// `xms_edov` means anything ([Providers.microsoft]).
	microsoft bool
}

// relying is this app as the relying party for the connection a trip is to:
// the discovery done, the secret resolved, the redirect fixed.
func (p *Providers) relying(ctx context.Context, t *Trip) (*oauth2.Config, *verifier, error) {
	c, v, err := p.discover(ctx, t.tenant, t.connection)
	if err != nil {
		return nil, nil, err
	}

	secret := ""
	if ref := c.GetSecretRef(); ref != "" && p.secret != nil {
		secret, err = p.secret(ref)
		if err != nil {
			return nil, nil, err
		}
	}

	if api := p.gitHub(c.GetIssuer()); api != "" {
		// A claim of an ID token names nobody at a provider that issues none,
		// and reading it as `id` would be a second rule for what the field
		// says.
		if named := c.GetSubjectClaim(); named != "" && named != "sub" {
			return nil, nil, fmt.Errorf("connection %s: GitHub names its people by their account's id, and %q is a claim of an ID token, which GitHub does not issue", t.connection, named)
		}

		return gitHubRelying(c.GetIssuer(), c.GetClientId(), secret, t.redirect, c.GetScopes()), &verifier{gitHub: api}, nil
	}

	// The issuer is checked here, against the token's own directory, for an
	// authority whose tokens each name a different one; everywhere else it is
	// checked against the issuer, by the verifier.
	every := directoriesOf(c.GetIssuer(), v)

	return &oauth2.Config{
		ClientID:     c.GetClientId(),
		ClientSecret: secret,
		Endpoint:     v.Endpoint(),
		RedirectURL:  t.redirect,
		Scopes:       append([]string{oidc.ScopeOpenID}, c.GetScopes()...),
	}, &verifier{
		id:          v.Verifier(&oidc.Config{ClientID: c.GetClientId(), SkipIssuerCheck: every != ""}),
		subject:     c.GetSubjectClaim(),
		directories: every,
		microsoft:   p.microsoft(c.GetIssuer()),
	}, nil
}

// discover is one connection of one tenant and what its issuer publishes, the
// second fetched once -- and nil for GitHub's, which publishes nothing.
func (p *Providers) discover(ctx context.Context, tenant pdid.Id, name string) (*rstr.Connection, *oidc.Provider, error) {
	c, err := p.roster.Connection().Get(ctx, rstr.ConnectionGetRequest_builder{
		Ref: rstr.ConnectionRef_builder{
			At: rstr.ConnectionRefByAt_builder{
				Tenant: rstr.TenantRef_builder{Id: tenant.Bytes()}.Build(),
				Name:   proto.String(name),
			}.Build(),
		}.Build(),
		Select: rstr.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, nil, err
	}
	if p.gitHub(c.GetIssuer()) != "" {
		return c, nil, nil
	}

	k := tenant.String() + "\x00" + name
	if had, ok := p.discovery.Load(k); ok {
		return c, had.(*oidc.Provider), nil
	}
	v, err := discoverAt(ctx, c.GetIssuer())
	if err != nil {
		return nil, nil, fmt.Errorf("discovery at %s: %w", c.GetIssuer(), err)
	}
	p.discovery.Store(k, v)

	return c, v, nil
}

// ErrUnfinished is a round trip [Providers.Claim] could not finish for a reason
// of this side's -- the connection or the provider's document could not be
// read, a secret could not be resolved -- rather than an answer it refused. An
// app answers the two differently: one is its own failure, and the other is
// the browser's.
var ErrUnfinished = errors.New("arrives: the round trip could not be finished")

// Claim finishes a round trip [Providers.Begin] started: what the provider
// said, verified -- the exchange and the token check that make the app the
// relying party -- and named for the tenant and connection the trip was to.
//
// `back` is the request the browser came back with, served at
// `GET /callback/{tenant}/{connection}`: the two it names have to be the trip's
// own, which is what makes a redirect per connection a defence rather than a
// spelling ([Trip]). Its context carries whatever the app calls roster with for
// that tenant, as [Providers.Begin]'s did.
//
// The subject is the claim the connection names. A token without it is
// refused rather than read by `sub`: the person would be somebody else under
// the other claim, and a second person is what that makes.
func (p *Providers) Claim(ctx context.Context, t *Trip, back *http.Request) (Caller, error) {
	if t == nil {
		return Caller{}, errors.New("no round trip")
	}
	if back.PathValue("tenant") != t.alias || back.PathValue("connection") != t.connection {
		return Caller{}, fmt.Errorf("the answer came back to the callback of %q at %q, and this round trip was to %q at %q",
			back.PathValue("connection"), back.PathValue("tenant"), t.connection, t.alias)
	}

	cfg, v, err := p.relying(ctx, t)
	if err != nil {
		return Caller{}, fmt.Errorf("%w: %w", ErrUnfinished, err)
	}
	who, err := p.exchange(ctx, cfg, v, t, back.URL.Query().Get("code"))
	if err != nil {
		return Caller{}, err
	}
	who.Tenant, who.TenantAlias, who.Provider = t.tenant, t.alias, t.connection

	return who, nil
}

// exchange is the code turned into who signed in: the token, with the trip's
// verifier, and then what it says -- or, for GitHub, what its API says.
func (p *Providers) exchange(ctx context.Context, cfg *oauth2.Config, v *verifier, t *Trip, code string) (Caller, error) {
	if code == "" {
		return Caller{}, errors.New("no code")
	}
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(t.verifier))
	if err != nil {
		return Caller{}, err
	}
	if v.gitHub != "" {
		return p.fromGitHub(ctx, v.gitHub, tok)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return Caller{}, errors.New("no id_token")
	}
	id, err := v.id.Verify(ctx, raw)
	if err != nil {
		return Caller{}, err
	}
	if v.directories != "" {
		if err := inDirectory(id, v.directories); err != nil {
			return Caller{}, err
		}
	}
	// The token is this trip's, which is what a nonce is for. A trip with no
	// nonce is one this package did not begin, and gets no token either.
	if t.nonce == "" || id.Nonce != t.nonce {
		return Caller{}, errors.New("the id_token is for another sign-in: its nonce is not this one's")
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
		Picture  string `json:"picture"`

		// Microsoft's word for `email_verified`, which it does not send.
		DomainVerified any `json:"xms_edov"`
	}
	if err := id.Claims(&claims); err != nil {
		return Caller{}, err
	}

	who := Caller{
		Subject: id.Subject,
		Email:   claims.Email,
		// `xms_edov` is Microsoft's word, read only in a token Microsoft
		// signed: anywhere else it is a claim like any other somebody may
		// have put there, and an address is verified on the provider's own
		// word and nothing else.
		Verified: claims.Verified || (v.microsoft && domainVerified(claims.DomainVerified)),
		Name:     claims.Name,
		Picture:  claims.Picture,
		token:    tok,
	}
	if named := v.subject; named != "" && named != "sub" {
		var all map[string]any
		if err := id.Claims(&all); err != nil {
			return Caller{}, err
		}
		s, _ := all[named].(string)
		if s == "" {
			// Entra leaves `oid` out without the `profile` scope, which is the
			// usual reason, and the one worth saying.
			return Caller{}, fmt.Errorf("the token carries no %q, which this connection names its people by; "+
				"Entra sends `oid` only to a client asking for the `profile` scope", named)
		}
		who.sub, who.Subject = id.Subject, s
	}

	return who, nil
}

// Known makes sure the claim names somebody here, enrolling them if the
// deployment's policy says so, and links the identity itself so a policy cannot
// forget to or do it a second way. What it answers is the `Holder.id`, which is
// the `sub` of every token minted for them afterwards.
//
// # And it may answer by the subject they had
//
// A connection that names another claim than `sub` (`Connection.subject_claim`)
// finds people who signed in before it did by the `sub` their identity was
// keyed by, and moves that identity to the claim -- once, with the token that
// carries both. When the move is refused -- somebody wider than this front door,
// whom [Core.mayWriteAWayIn] keeps it from writing a way into -- they are
// signed in by the row they had, which is what happened before the connection
// moved, and an operator moves them. `who.Subject` is then that row's subject,
// because it is what `Door.Accept` resolves them by next.
//
// # And it attests on every sign-in, not only the first
//
// Somebody already linked used to be answered before [Providers.attest] was
// reached, so only the sign-in that made the identity ever wrote an address. A
// directory that starts saying an address is verified after people have
// arrived -- an Entra app registration given `xms_edov` one day -- was then
// believed about nobody who had signed in before, and their addresses stayed
// unproved for good. `Email.Attest` is idempotent for exactly this, so the
// returning half calls it too.
func (p *Providers) Known(ctx context.Context, enrol Enrol, who *Caller) (pdid.Id, error) {
	v, err := p.identity(ctx, who, who.Subject)
	switch status.Code(err) {
	case codes.OK:
		id, err := pdid.From(v.GetHolder().GetId())
		if err != nil {
			return pdid.Nil, err
		}
		p.attest(ctx, id, v, *who)

		return id, nil
	case codes.NotFound:
	default:
		return pdid.Nil, err
	}

	if who.sub != "" && who.sub != who.Subject {
		id, ok, err := p.moved(ctx, who)
		if err != nil {
			return pdid.Nil, err
		}
		if ok {
			return id, nil
		}
	}

	if enrol == nil {
		enrol = Invited()
	}
	id, err := enrol(ctx, p.roster, *who)
	if err != nil {
		return pdid.Nil, err
	}
	link, err := p.roster.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: id.Bytes()}.Build(),
		Provider: who.Provider,
		Subject:  who.Subject,
	}.Build())
	if err != nil {
		return pdid.Nil, fmt.Errorf("link %s/%s: %w", who.Provider, who.Subject, err)
	}

	p.attest(ctx, id, link, *who)

	return id, nil
}

// identity is the row a subject at this provider names in this tenant.
func (p *Providers) identity(ctx context.Context, who *Caller, subject string) (*rstr.Identity, error) {
	return p.roster.Identity().Get(ctx, rstr.IdentityGetRequest_builder{
		Ref: rstr.IdentityRef_builder{
			Subject: rstr.IdentityRefBySubject_builder{
				TenantId: who.Tenant.Bytes(),
				Provider: proto.String(who.Provider),
				Subject:  proto.String(subject),
			}.Build(),
		}.Build(),
		Select: rstr.IdentitySelect_builder{
			Holder: rstr.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
}

// moved finds somebody by the `sub` their identity was keyed by before the
// connection named another claim, and moves it to that claim.
//
// Answers false when there is no such row: they are new under either claim,
// and enrolment decides. A refused move is said and not refused -- they signed
// in by a row the token's own `sub` names, exactly as they did before the
// connection moved -- and `who.Subject` goes back to that row's subject.
func (p *Providers) moved(ctx context.Context, who *Caller) (pdid.Id, bool, error) {
	v, err := p.identity(ctx, who, who.sub)
	switch status.Code(err) {
	case codes.OK:
	case codes.NotFound:
		return pdid.Nil, false, nil
	default:
		return pdid.Nil, false, err
	}

	id, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return pdid.Nil, false, err
	}

	if _, err := p.roster.Identity().Resubject(ctx, rstr.IdentityResubjectRequest_builder{
		Ref:     rstr.IdentityRef_builder{Id: v.GetId()}.Build(),
		Subject: proto.String(who.Subject),
	}.Build()); err != nil {
		slog.WarnContext(ctx, "arrives: signed in by the subject they had; an operator moves them",
			"provider", who.Provider, "holder", id.String(), "err", err)
		who.Subject = who.sub

		return id, true, nil
	}

	return id, true, nil
}

// attest writes down the address the directory just handed over, on the word of
// the identity that carried it.
//
// # Why this is not optional and was missing
//
// The address arrives inside a token the directory signed, names the person
// (`Enrolling` derives an alias from it), and was then **thrown away**. Nothing
// else can put it back: the account page's own route mints a link and mails it,
// and a deployment with no mail has no route at all. So a person who arrived
// through a directory had an account roster could not say the address of, and
// every token minted for them afterwards was missing the claim a product asked
// for.
//
// The schema had been waiting for this. `Email.vouched_by` is *which identity
// vouched for it, if one did*, and it says an address in a provider's claims is
// *only as good as that provider's own check* -- and nothing had ever written
// it.
//
// # Why a failure here is not a failure
//
// Signing somebody in worked. The identity is linked, the `sub` is theirs, and
// what is missing is a convenience -- so refusing the sign-in over it would
// trade the thing that matters for the thing that does not. A tenant whose key
// does not allow `Email.Attest` is the ordinary case of that, and it is a
// deployment choosing not to store addresses rather than a fault.
func (p *Providers) attest(ctx context.Context, who pdid.Id, link *rstr.Identity, from Caller) {
	if from.Email == "" {
		return
	}

	_, _ = p.roster.Email().Attest(ctx, rstr.EmailAttestRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		Address: proto.String(front.Address(from.Email)),

		// Whose word it was, which is the whole of what makes this different
		// from an address somebody typed.
		VouchedBy: rstr.IdentityRef_builder{Id: link.GetId()}.Build(),

		// What the **provider** said, and not what this app thinks. A directory
		// that does not send `email_verified` gets a row with the address and
		// the evidence and no stamp, which is exactly right: the address is
		// kept, and nothing claims a check that did not happen.
		Verified: proto.Bool(from.Verified),
	}.Build())
}

// Nonce is a state parameter: 24 bytes, and nothing else.
func Nonce() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// States is every round trip to a provider started and not yet finished.
//
// Held here rather than in the state parameter, because the callback arrives
// under the app's own name and not the tenant's, so the tenant has to be
// remembered and the state has to be a nonce and nothing else. A cookie binds
// the browser to it: a callback carrying a state this browser did not start is
// refused, whoever else's it was -- and that cookie is the app's, because this
// package writes no headers.
type States[T any] struct {
	mu sync.Mutex
	by map[string]held[T]
}

type held[T any] struct {
	v       T
	expires time.Time
}

// Held is an empty store.
func Held[T any]() *States[T] { return &States[T]{by: map[string]held[T]{}} }

// Put remembers one flow, and sweeps whatever has run out while it is here.
func (s *States[T]) Put(state string, v T, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, w := range s.by {
		if now.After(w.expires) {
			delete(s.by, k)
		}
	}
	s.by[state] = held[T]{v: v, expires: now.Add(ttl)}
}

// Take answers one flow once. A state that names none, or one that has run out,
// is the same answer.
func (s *States[T]) Take(state string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.by[state]
	delete(s.by, state)
	if !ok || time.Now().After(w.expires) {
		var zero T

		return zero, false
	}

	return w.v, true
}
