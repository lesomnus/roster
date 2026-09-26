package login

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// Which tenant a flow is about, and where that comes from.
//
// # It is the domain, and which domain took finding
//
// Not the one the browser arrived at. Hydra has **one** `urls.login` for the
// whole issuer, so every flow lands on this app under one name and the arrival
// host says nothing about anybody. `Config.Base` has said so all along -- *Hydra
// sends every browser here under one name.*
//
// The domain that does say is the **redirect** the authorization request named:
//
//	1. contoso.app.example          the product, which is contoso's
//	2. hydra.example/oauth2/auth?client_id=product
//	                              &redirect_uri=https://contoso.app.example/callback
//	3. login.example/login?login_challenge=…       always this host
//	4. Hydra, asked about the challenge, answers with `request_url` -- step 2,
//	   whole -- and `redirect_uri` is in it
//
// So the host at step 1 reaches this app at step 4, and `FrontService.WhoseHost`
// turns it into a tenant. Which means a tenant is served because it registered a
// `Host` row, and not because a roster operator wrote it into this app's
// configuration.
//
// # What it replaces, and why that had to go
//
// The OAuth **client**, through a map of client to tenant. It works and it does
// not scale: one product serving a hundred tenants is one client, so the
// discriminator forced a client per tenant -- a Hydra registration per customer,
// for a fact roster already holds.
//
// # Can a browser lie about it
//
// Only within the set its client registered; Hydra refuses a `redirect_uri` that
// is not one of them, before any of this. So the question is what picking
// another tenant's registered redirect buys, and the answer is **nothing**: the
// password is then checked against that tenant's holders, whose passwords this
// person does not have -- and that tenant's own front door is public anyway, so
// there was never anything to reach that could not be reached.
//
// It is worth saying because it looks like the thing `auth.proto` refuses a
// tenant field for. That refusal is about a caller **holding a key**, which can
// reach a port it could not reach a front door through; here the caller is an
// anonymous browser and both doors are public, so there is no asymmetry to
// exploit. The lockout and `grpcx.Limit` count against the named tenant either
// way, reachable directly too.
//
// What is left is one narrow case: a tenant whose product is **not** public
// sharing an OAuth client with one that is. That is registration hygiene, and
// `roster login doctor` is where a deployment is told about it -- as an
// ambiguity rather than as an attack, because a client whose redirects span
// tenants is a client whose flows resolve differently depending on what the
// browser sent.

// hosts is what a name resolved to.
//
// A cache and not state: every entry can be recomputed from two reads, so
// dropping the whole thing costs round trips and nothing else. Nothing negative
// is kept, so a `Host` row written a moment ago is honoured on the next attempt
// rather than after a restart.
type hosts struct {
	mu sync.RWMutex
	at map[string]*tenant
}

func (h *hosts) get(name string) (*tenant, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	o, ok := h.at[name]

	return o, ok
}

func (h *hosts) put(name string, o *tenant) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.at[name] = o
}

// at is the tenant that claims a name.
//
// Two reads, and both go out as the deployment key with **no** `roster-at`,
// which is the one place in this app that is deliberate rather than incidental:
// these are the reads that decide what the narrowing will be, so they cannot be
// narrowed by it. That is what an `rk_` is for, and `keys.At` says so from the
// other side.
func (a *App) at(ctx context.Context, name string) (*tenant, error) {
	if name == "" {
		return nil, fmt.Errorf("login: nothing in this flow says which name it is about")
	}
	if o, ok := a.known.get(name); ok {
		return o, nil
	}

	res, err := a.front.WhoseHost(ctx, rstr.FrontWhoseHostRequest_builder{Host: name}.Build())
	if err != nil {
		// Unchanged, `NotFound` and all: a name nothing claims is a deployment
		// that has not written a `Host` row, and `broken` is what a browser is
		// told either way.
		return nil, fmt.Errorf("login: no tenant answers at %q: %w", name, err)
	}

	id, err := pdid.From(res.GetTenant())
	if err != nil {
		return nil, err
	}

	v, err := a.roster.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Id: id.Bytes()}.Build(),
		// The alias as well as the name, and the alias is the one that is
		// load-bearing: it is what `arrives` names a tenant by when it enrols
		// somebody, and a `Select` that asked for the name alone answered an
		// empty one -- which reached `Tenant not found` two hops later, on a
		// tenant that is plainly there. The old shape read the alias off the
		// configuration's map key and so never asked for it.
		Select: rstr.TenantSelect_builder{Alias: proto.Bool(true), Name: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("login: %q is %s and this key cannot read it: %w", name, id, err)
	}

	alias := v.GetAlias()
	who := &tenant{id: id, alias: alias, name: v.GetName(), at: name}
	if who.name == "" {
		who.name = alias
	}
	a.known.put(name, who)

	return who, nil
}

// arrivedAt is the name a flow is about, from the authorization request or from
// the client that made it.
//
// `request_url` is the whole of step 2 above, so the ordinary answer is the
// `redirect_uri` in it. It can be absent and legitimately so: RFC 6749 §3.1.2.3
// makes the parameter optional for a client that registered exactly one, and
// Hydra then uses the registered one without writing it into the URL it
// recorded. The device grant has no redirect at all.
//
// So the fallback is the client's own registration, and the condition on it is
// **one host**: several that resolve to one tenant would be fine and several
// that do not are a flow with two answers, which is not something to pick
// between. Refused, naming them, and `roster login doctor` is where a deployment
// finds out before somebody tries to sign in.
func (a *App) arrivedAt(ctx context.Context, requested, client string) (string, error) {
	if h := redirectHost(requested); h != "" {
		return h, nil
	}

	v, err := a.admin.registered(ctx, client)
	if err != nil {
		return "", fmt.Errorf("login: %q named no redirect and its client could not be read: %w", client, err)
	}

	var one string
	for _, u := range v.Redirects {
		h := front.Hostname(hostOf(u))
		if h == "" || h == one {
			continue
		}
		if one != "" {
			return "", fmt.Errorf(
				"login: %q named no redirect, and its client is registered across %s and %s, "+
					"so which tenant this flow is about has two answers (roster login doctor)",
				client, one, h)
		}
		one = h
	}
	if one == "" {
		return "", fmt.Errorf("login: %q named no redirect and its client has none registered", client)
	}

	return one, nil
}

// redirectHost is the host of the `redirect_uri` in an authorization URL, or
// empty where there is none to read.
//
// Empty for anything malformed as well, deliberately: what follows is the
// fallback above, which asks Hydra rather than this app's parsing.
func redirectHost(requested string) string {
	if requested == "" {
		return ""
	}

	u, err := url.Parse(requested)
	if err != nil {
		return ""
	}

	return front.Hostname(hostOf(u.Query().Get("redirect_uri")))
}

// hostOf is the host of a URL, without the port, or empty.
func hostOf(raw string) string {
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return u.Host
}
