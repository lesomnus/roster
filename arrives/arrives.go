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

	rstr "github.com/lesomnus/roster/rstr"
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

// Invited refuses everybody roster has not been told about. The default, and
// the right one for a deployment where people are put in by an operator.
func Invited() Enrol {
	return func(context.Context, rstr.Client, Caller) (pdid.Id, error) {
		return pdid.Nil, ErrUninvited
	}
}

// Enrolling makes an account for anybody the provider vouches for, named by the
// local part of their address. For a deployment where signing in at the
// operator's own directory *is* the invitation -- which is a decision about the
// provider, and one to take knowing the tenant's key has to hold
// `HolderService.Add` for it.
func Enrolling() Enrol {
	return func(ctx context.Context, c rstr.Client, who Caller) (pdid.Id, error) {
		alias, _, ok := strings.Cut(who.Email, "@")
		if !ok || alias == "" {
			return pdid.Nil, fmt.Errorf("enrol %s/%s: no email to name them by", who.Provider, who.Subject)
		}

		v, err := c.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Alias: proto.String(who.TenantAlias)}.Build(),
			Alias:  alias,
			Name:   who.Name,
		}.Build())
		if err != nil {
			return pdid.Nil, fmt.Errorf("enrol %s: %w", who.Email, err)
		}

		return pdid.From(v.GetId())
	}
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
