package sso

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// The password half of the front door, and the page that spends what it
// answers with.
//
// # Why this file exists at all, given the rest of the package
//
// `examples/sso` signs people in with a provider, and that path never calls
// `Vouch` -- the secret is somebody else's to check. So there is nothing for a
// delegation to ride back on, which PLAN.md D23 already recorded and left:
// *a deployment with Hydra in front does not call `Vouch` at all ... anything
// built on this should assume the `Vouch` case first and leave the seam.*
//
// So this is the `Vouch` case, and it is a **specimen** rather than the app's
// main flow: it exists so that the delegation has a page, which is what D24
// says this app is for -- *not to demonstrate, to specify*. Exchanging an
// `id_token` for a delegation is the seam, and it is not designed here: it is
// roster accepting somebody else's assertion as proof, which is a D19 question
// and takes its own entry.
//
// What that means for a reader: `/me` works after `POST /session` and not after
// `/callback`, and the handler says so rather than pretending.
//
// # Where the delegation lives, and why it is the app's problem
//
// `authsession.Session` has an actor, a tenant, a grant and two clocks, and
// nowhere to put an opaque string an app is holding on somebody's behalf. That
// is not an omission to route around: a session is deliberately not a place to
// keep a copy of anything, so that it cannot become a stale one.
//
// So the app keeps it, beside the session, keyed by the session's own key. It
// is ten lines and it is the app's data. The reason this is not extracted into
// something roster ships is D24 §6: extracting first means guessing what to
// extract, and one consumer is not enough to know.
//
// **The leak worth knowing about**: nothing in `authsession` tells anybody a
// session has died -- expiry is checked when one is read, and a store may
// forget one at any time. So a map keyed by session would grow one entry per
// expired session forever. What keeps this one bounded is that the thing it
// holds carries its own expiry, so a pass over it on every write is enough.

// held is the delegation this app is holding for one signed-in browser.
type held struct {
	token   string
	expires time.Time
}

// delegations is the side of a session that is not payday's.
//
// A map and a mutex, because that is what the store beside it is
// (`authsession.MemStore`) and matching it is the honest thing: an app that put
// this in Redis and left its sessions in memory would have two answers to how
// many replicas it runs.
type delegations struct {
	mu sync.Mutex
	by map[string]held
}

func (d *delegations) put(key string, v held) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.by == nil {
		d.by = map[string]held{}
	}

	// Nothing says when a session died, so this is where the dead are found:
	// every entry carries its own expiry, and a pass on each write is enough to
	// keep the map the size of who is actually signed in.
	now := time.Now()
	for k, v := range d.by {
		if !now.Before(v.expires) {
			delete(d.by, k)
		}
	}

	d.by[key] = v
}

func (d *delegations) get(key string) (held, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.by[key]
	if !ok || !time.Now().Before(v.expires) {
		return held{}, false
	}

	return v, true
}

func (d *delegations) take(key string) (held, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.by[key]
	delete(d.by, key)

	return v, ok
}

// keyOf is the session key this request carries.
//
// Through `Sessions.KeyOf` and never `r.Cookie`, and the difference is not
// pedantry: the cookie's **name** is unexported state on the `*Sessions`
// (`Insecure()` renames it), and `KeyOf` takes the last value of that name
// while `r.Cookie` takes the first. Two cookies of one name and the two halves
// resolve different sessions -- roster authenticating one browser while this
// app hands over another's delegation.
func (a *App) keyOf(r *http.Request) string {
	return a.sessions.KeyOf(r.Header.Values("Cookie"))
}

// signIn is `POST /session`: a password, checked by roster, answered with a
// cookie and nothing else.
//
// The 204 with no body is `authsession.Serve`'s shape and this keeps it: what a
// page needs *about* the person is a request it should make, against this same
// server, behind this same session.
func (a *App) signIn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Alias    string `json:"alias"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	tenant, err := a.tenantOf(ctx, r.Host)
	if err != nil {
		// Reached under a name this deployment does not serve. Same answer as a
		// wrong password: which of the two it was is not this browser's to
		// learn.
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}

	res, err := a.vouch.Delegate(ctx, rstr.VouchDelegateRequest_builder{
		Who: rstr.VouchWho_builder{Tenant: tenant, Alias: body.Alias}.Build(),

		Secret: []byte(body.Password),

		// What this app will do on their behalf, and nothing else. It is the
		// list a screen needs, not the list the person holds -- the delegation
		// narrows to the intersection, so asking for less than they may do is
		// free and asking for more buys nothing.
		Methods: []string{rstr.MeService_Get_FullMethodName},
	}.Build())
	if err != nil {
		// A refusal roster made about the *request* rather than about the
		// person -- a method this app may not hand out, a tenant it may not
		// reach. Said to the log and not to the browser.
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}
	if !res.GetVerified().GetOk() {
		// One answer for a wrong password, an unknown person and somebody with
		// no password at all, which is the answer roster already took care to
		// make one. A lockout is the exception roster reports and this does not
		// pass on, because a page that said "try again in fifteen minutes"
		// would be re-exposing what the 401 is hiding.
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}

	who, tenantId, err := ids(res.GetVerified().GetHolder(), res.GetVerified().GetTenant())
	if err != nil {
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	v, cookie, err := a.sessions.Mint(ctx, authsession.Session{
		Id:       who.String(),
		TenantId: tenantId.String(),

		// Everything this person may do, which is what signing in at this app's
		// own page means. The delegation below is narrower on purpose and they
		// are not the same thing: this grant is what the **session** allows
		// against this app, and the delegation is what this app may ask roster.
		Grant: frame.Whole(),

		// Never outliving what this app was given to act with. `Mint` fills
		// this from the configured lifetime when it is zero, and a session that
		// outlived its delegation would be somebody signed in to a page that
		// cannot be drawn.
		Expires: res.GetExpires().AsTime(),
	})
	if err != nil {
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	a.held.put(v.Key, held{token: res.GetToken(), expires: res.GetExpires().AsTime()})

	http.SetCookie(w, cookie)
	w.WriteHeader(http.StatusNoContent)
}

// signOut is `DELETE /session`, and it ends both halves.
//
// The delegation is revoked and not merely forgotten. Forgetting it leaves a
// live credential for that person in roster's table until its own clock runs
// out -- which is exactly the case D23 said *revoking it is a delete* about, and
// which nothing could do until `VouchService.Revoke` existed.
func (a *App) signOut(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := a.keyOf(r)

	if v, ok := a.held.take(key); ok {
		// Best effort, and after the local drop. A roster that cannot be
		// reached must not stop somebody signing out of this app; what is lost
		// is a row that expires on its own.
		_, _ = a.vouch.Revoke(ctx, rstr.VouchRevokeRequest_builder{Token: v.token}.Build())
	}

	cookie := a.sessions.End(ctx, key)
	http.SetCookie(w, cookie)
	w.WriteHeader(http.StatusNoContent)
}

// errNotDelegated is what a page gets when this browser signed in the other
// way.
var errNotDelegated = errors.New("sso: this session has no delegation")

// acting is the pair a delegated call carries: this app's own credential,
// which the connection already attaches, and the person it is acting for.
func (a *App) acting(ctx context.Context, r *http.Request) (context.Context, error) {
	v, ok := a.held.get(a.keyOf(r))
	if !ok {
		return nil, errNotDelegated
	}

	return metadata.AppendToOutgoingContext(ctx, keys.HeaderActing, v.token), nil
}

// me is `GET /me`: the person's own record, read **as them**.
//
// This is the whole reason the delegation exists, so it is worth saying what
// the alternatives were. This app holds a credential that can read every tenant
// it serves; drawing somebody's own page with it means filtering in this
// process, and D17 already named what that costs -- *the kind of thing that
// leaks by being forgotten* -- with one bug here exposing everybody, on reads
// roster answered correctly.
//
// So the call goes out narrowed to one person and to one method, and if this
// code asked for anything else it would be refused by roster rather than by
// remembering.
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	ctx, err := a.acting(r.Context(), r)
	if err != nil {
		// Signed in through the provider, which does not produce one. See the
		// note at the top of this file: the exchange that would is a decision
		// nobody has taken.
		http.Error(w, "this session cannot read its own record; sign in with a password", http.StatusForbidden)
		return
	}

	v, err := a.me_.Get(ctx, rstr.MeGetRequest_builder{}.Build())
	if err != nil {
		http.Error(w, "cannot read", http.StatusBadGateway)
		return
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Alias      string   `json:"alias"`
		Name       string   `json:"name"`
		Emails     []string `json:"emails"`
		Providers  []string `json:"providers"`
		SignsInBy  []string `json:"signs_in_by"`
		MayCallAll []string `json:"may_call"`
	}{
		Alias:      v.GetAlias(),
		Name:       v.GetName(),
		Emails:     addresses(v),
		Providers:  providers(v),
		SignsInBy:  kinds(v),
		MayCallAll: v.GetMethods(),
	})
}

func addresses(v *rstr.MeGetResponse) []string {
	out := make([]string, 0, len(v.GetEmails()))
	for _, e := range v.GetEmails() {
		out = append(out, e.GetAddress())
	}

	return out
}

func providers(v *rstr.MeGetResponse) []string {
	out := make([]string, 0, len(v.GetIdentities()))
	for _, i := range v.GetIdentities() {
		out = append(out, i.GetProvider())
	}

	return out
}

func kinds(v *rstr.MeGetResponse) []string {
	out := make([]string, 0, len(v.GetCredentials()))
	for _, c := range v.GetCredentials() {
		out = append(out, c.GetKind())
	}

	return out
}
