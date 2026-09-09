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
// providers out of roster -- `Connection`, added on the console's *arrives
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
package arrives

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
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
	// roster cannot read off a token.
	Tenant      pdid.Id
	TenantAlias string

	// Provider is the `Connection.name`, which is what `Identity.provider`
	// stores; Subject is the provider's immutable identifier for them, never an
	// address or a username.
	Provider string
	Subject  string

	// What else the token carried, for a policy that names people by it.
	Email    string
	Verified bool
	Name     string
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
// what is left has to start with a letter. `Seunghyun.Hwang@hday.dev` is
// `seunghyun-hwang`.
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

// invitation is the `Holder` an operator entered for the address a directory
// just vouched for, if there is one.
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
// # What it does add, and it is one condition
//
// **The provider must say the address is verified.** Without that, a directory
// that lets somebody type an arbitrary address into their own profile is a
// directory that hands out whichever account carries it. `email_verified` is
// the claim, and an unverified one is not an answer here -- it falls through to
// whatever the policy does with a stranger.
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
			Holder: rstr.HolderSelect_builder{}.Build(),
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

	return id, err == nil, err
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
}

// New is the relying party for whatever `Connection` rows the given client can
// see. `secret` turns a `Connection.secret_ref` into the client secret it
// names; roster stores that reference and never reads it.
func New(c rstr.Client, secret func(ref string) (string, error)) *Providers {
	return &Providers{roster: c, secret: secret}
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

// Relying is this app as the relying party for one connection of one tenant:
// the discovery done, the secret resolved, the redirect fixed.
func (p *Providers) Relying(ctx context.Context, tenant pdid.Id, name, redirect string) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
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

	k := tenant.String() + "\x00" + name
	var v *oidc.Provider
	if had, ok := p.discovery.Load(k); ok {
		v = had.(*oidc.Provider)
	} else {
		v, err = oidc.NewProvider(ctx, c.GetIssuer())
		if err != nil {
			return nil, nil, fmt.Errorf("discovery at %s: %w", c.GetIssuer(), err)
		}
		p.discovery.Store(k, v)
	}

	secret := ""
	if ref := c.GetSecretRef(); ref != "" && p.secret != nil {
		secret, err = p.secret(ref)
		if err != nil {
			return nil, nil, err
		}
	}

	return &oauth2.Config{
		ClientID:     c.GetClientId(),
		ClientSecret: secret,
		Endpoint:     v.Endpoint(),
		RedirectURL:  redirect,
		Scopes:       append([]string{oidc.ScopeOpenID}, c.GetScopes()...),
	}, v.Verifier(&oidc.Config{ClientID: c.GetClientId()}), nil
}

// Claim is what the provider said, verified: the exchange and the token check
// that make the app the relying party.
func (p *Providers) Claim(ctx context.Context, cfg *oauth2.Config, verifier *oidc.IDTokenVerifier, code string) (Caller, error) {
	if code == "" {
		return Caller{}, errors.New("no code")
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return Caller{}, err
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return Caller{}, errors.New("no id_token")
	}
	id, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Caller{}, err
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if err := id.Claims(&claims); err != nil {
		return Caller{}, err
	}

	return Caller{Subject: id.Subject, Email: claims.Email, Verified: claims.Verified, Name: claims.Name}, nil
}

// Known makes sure the claim names somebody here, enrolling them if the
// deployment's policy says so, and links the identity itself so a policy cannot
// forget to or do it a second way. What it answers is the `Holder.id`, which is
// the `sub` of every token minted for them afterwards.
func (p *Providers) Known(ctx context.Context, enrol Enrol, who Caller) (pdid.Id, error) {
	v, err := p.roster.Identity().Get(ctx, rstr.IdentityGetRequest_builder{
		Ref: rstr.IdentityRef_builder{
			Subject: rstr.IdentityRefBySubject_builder{
				TenantId: who.Tenant.Bytes(),
				Provider: proto.String(who.Provider),
				Subject:  proto.String(who.Subject),
			}.Build(),
		}.Build(),
		Select: rstr.IdentitySelect_builder{
			Holder: rstr.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	switch status.Code(err) {
	case codes.OK:
		return pdid.From(v.GetHolder().GetId())
	case codes.NotFound:
	default:
		return pdid.Nil, err
	}

	if enrol == nil {
		enrol = Invited()
	}
	id, err := enrol(ctx, p.roster, who)
	if err != nil {
		return pdid.Nil, err
	}
	if _, err := p.roster.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: id.Bytes()}.Build(),
		Provider: who.Provider,
		Subject:  who.Subject,
	}.Build()); err != nil {
		return pdid.Nil, fmt.Errorf("link %s/%s: %w", who.Provider, who.Subject, err)
	}

	return id, nil
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
