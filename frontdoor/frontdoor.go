// Package frontdoor is the sign-in an app would otherwise write itself.
//
// D22 says what this is and why it is a package rather than a service: *the
// wish behind this is right -- somebody building an app wants to put up their
// brand and write their business logic, not to learn what a second factor
// costs. Handing them a store and a list of Rpcs leaves the hardest, most
// security-shaped part of the job on their desk. The answer is to write that
// part and ship it as something they import. The answer is not to serve it.*
//
// # It is not roster, and the name is the point
//
// D22 again: *a login flow is not a list of people. Needing a second name is
// the signal that this is a second product in one repository rather than roster
// growing.* Everything here runs in the app's process, sets the app's cookie on
// the app's domain, and issues nothing roster does not.
//
// # What it carries, and what it deliberately does not
//
// D21 draws the line and this package is on both sides of it, once each:
//
//   - *which browser is mid-sign-in* is the **app's**, so this holds a cookie
//     and the string roster gave it, in the app's process;
//   - *what has been proved about this person* is **roster's**, so this holds
//     none of it -- no attempt state, no list of factors, no idea how many
//     steps there are.
//
// And the screen is nobody's business here. This answers what a second form
// needs to draw itself -- what is satisfied, what is available -- and never
// what to call it, which order to offer, or how many are enough. That is D21's
// three answers and three refusals, and the failure mode to watch for is a
// field that describes what to render: D22 says to refuse it however small it
// looks.
//
// # Why it was written last
//
// D24 §6, and its reason: *extracting first means guessing what to extract.
// What 4 and 5 turn out to need is the specification, and it is not knowable in
// advance.* So this is `examples/sso` after the screens were written, with the
// half that is the same in every app lifted out and the half that is that app's
// -- its provider, its pages, its enrolment policy -- left where it was.
package frontdoor

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
	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// ErrNotSignedIn is a request from a browser that has not finished.
//
// One error for a session that never existed, one that is half way, and one
// from a sign-in this package did not do -- because to a page they are one
// thing: there is no credential here to act with.
var ErrNotSignedIn = errors.New("frontdoor: this session cannot act for anybody")

// ErrUnknownHost is [Config.Tenant] saying this deployment serves nobody under
// the name the browser arrived at.
var ErrUnknownHost = errors.New("frontdoor: no operator here serves this name")

// Config is what an app has to say.
type Config struct {
	// Sessions is the app's own, on the app's domain. This package never makes
	// one: a cookie's name, its lifetime and whether it is `Secure` are
	// decisions about a deployment, not about signing in.
	Sessions *authsession.Sessions

	// Vouch is roster's sign-in flow, as this app authenticates to it: the two
	// forms that prove somebody and mint a delegation for them (`Delegate`,
	// `Accept`).
	Vouch rstr.VouchServiceClient

	// Delegation is roster's delegation surface, which is where a sign-out ends
	// one: `Revoke` moved onto the entity it was always about, off the sign-in
	// flow that mints them.
	Delegation rstr.DelegationServiceClient

	// Methods is what the delegation is minted with, and it is the list this
	// app's own screens need rather than the list the person holds. A
	// delegation narrows to the intersection, so asking for less than they may
	// do is free and asking for more buys nothing.
	//
	// Required. An empty one mints a credential that opens no door, which
	// roster refuses -- and refusing here says so before a person has typed a
	// password.
	Methods []string

	// Tenant answers which operator a request arrived at, from the host it
	// arrived under.
	//
	// Supplied rather than done here, because a deployment that serves one
	// operator knows the answer at build time and one that serves many asks
	// roster -- `FrontService.WhoseHost` -- and both are a line the app writes.
	//
	// A name this deployment serves nobody under is [ErrUnknownHost], and that
	// is the only error a person is ever told "no" for. Everything else --
	// a roster that cannot be reached, a lookup that failed -- is this
	// deployment being broken, and is answered as broken. The distinction is
	// asked of the app because only the app knows which of its two answers is
	// which; getting it wrong in the safe direction costs a page saying
	// something is down, and in the other it costs somebody being told their
	// correct password was wrong.
	Tenant func(ctx context.Context, host string) (string, error)

	// Half is how long a browser has to answer a second form.
	//
	// Shorter than roster's own hold on the attempt, so that this app is the
	// one that gives up first and the browser is told rather than finding out
	// from a refusal it cannot explain. Zero takes [HalfLife].
	Half time.Duration
}

// HalfLife is how long a half-signed-in browser has, when nothing says
// otherwise.
const HalfLife = 4 * time.Minute

// Door is the two forms and the credential they end in.
type Door struct {
	c    Config
	held held
}

// New checks what an app said and answers with the door.
func New(c Config) (*Door, error) {
	switch {
	case c.Sessions == nil:
		return nil, errors.New("frontdoor: Sessions: the cookie is the app's, so the app makes it")
	case c.Vouch == nil:
		return nil, errors.New("frontdoor: Vouch: which roster this signs in against")
	case c.Delegation == nil:
		return nil, errors.New("frontdoor: Delegation: where a sign-out ends the delegation it minted")
	case len(c.Methods) == 0:
		return nil, errors.New("frontdoor: Methods: a delegation that allows nothing opens no door")
	case c.Tenant == nil:
		return nil, errors.New("frontdoor: Tenant: which operator a request arrived at")
	}

	if c.Half <= 0 {
		c.Half = HalfLife
	}

	return &Door{c: c}, nil
}

// Handler is `POST /session`, `POST /session/continue` and `DELETE /session`.
//
// Mounted on the app's mux, beside the app's own pages. It is a handler and not
// a server, which is D22's whole shape: this runs where the browser already is.
func (d *Door) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /session", d.signIn)
	m.HandleFunc("POST /session/continue", d.finish)
	m.HandleFunc("DELETE /session", d.SignOut)

	return m
}

// Acting is the context a call to roster is made in, for this browser.
//
// The pair `roster-as` carries: this app's own credential, which the connection
// already attaches, and the person it is acting for. A browser that signed in
// some other way -- through a provider, where roster never checked a secret --
// has no delegation and gets [ErrNotSignedIn], which is a refusal rather than a
// quiet fall back to the app's own credential. That fall back is the mistake
// D23 exists to prevent, and it is the one an app makes by accident.
func (d *Door) Acting(ctx context.Context, r *http.Request) (context.Context, error) {
	v, ok := d.held.get(d.keyOf(r))
	if !ok || v.token == "" {
		return nil, ErrNotSignedIn
	}

	return metadata.AppendToOutgoingContext(ctx, keys.HeaderActing, v.token), nil
}

// Who is the person this browser is, or nothing.
//
// For a page that draws a name before it makes a call. It reads the session and
// not the delegation, so a browser half way through gets nothing -- which is
// the same answer [Door.Acting] gives and for the same reason.
func (d *Door) Who(ctx context.Context, r *http.Request) (pdid.Id, bool) {
	v, ok := d.held.get(d.keyOf(r))
	if !ok || v.token == "" {
		return pdid.Nil, false
	}

	return v.who, true
}

// keyOf is the session key this request carries.
//
// Through `Sessions.KeyOf` and never `r.Cookie`, and the difference is not
// pedantry: the cookie's **name** is unexported state on the `*Sessions`
// (`Insecure()` renames it), and `KeyOf` takes the last value of that name
// while `r.Cookie` takes the first. Two cookies of one name and the two halves
// resolve different sessions -- roster authenticating one browser while this
// app hands over another's credential.
func (d *Door) keyOf(r *http.Request) string {
	return d.c.Sessions.KeyOf(r.Header.Values("Cookie"))
}

// signIn is the first form.
//
// Three answers and they are three status codes, which is what lets a page read
// this without a boolean in a body: **204** signed in, **200** one factor
// proved and more to prove, **401** everything else.
//
// The last one is one answer for a wrong password, an unknown person and
// somebody with no password at all -- which roster already took care to make
// one, and which this must not undo. A lockout is the exception roster reports
// and this does not pass on: a page saying "try again in fifteen minutes" would
// re-expose what the 401 is hiding.
func (d *Door) signIn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Alias    string `json:"alias"`
		Address  string `json:"address"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	tenant, err := d.c.Tenant(ctx, r.Host)
	if err != nil {
		if errors.Is(err, ErrUnknownHost) {
			// Reached under a name this deployment does not serve. Same answer
			// as a wrong password: which of the two it was is not this
			// browser's to learn.
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}

		// Anything else is this deployment's, and saying "no" to it is the one
		// mistake `frontdoor.js` documents in as many words: *a proxy answering
		// 502 is not a wrong password, and a page that said 'no' to it sends
		// somebody to type their password again at a server that is down.* A
		// deployment that asks roster which operator serves a name -- which is
		// the deployment `FrontService` exists for -- turned its own outage
		// into a wrong password here, while the identical outage one call later
		// at `Delegate` answered 500 and read as broken.
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	who := rstr.VouchWho_builder{Tenant: tenant}
	if body.Address != "" {
		who.Address = body.Address
	} else {
		who.Alias = body.Alias
	}

	res, err := d.c.Vouch.Delegate(ctx, rstr.VouchDelegateRequest_builder{
		Who:     who.Build(),
		Secret:  []byte(body.Password),
		Methods: d.c.Methods,
	}.Build())
	if err != nil {
		// A refusal roster made about the **request** rather than about the
		// person -- a method this app may not hand out, a tenant it may not
		// reach. Said to the log and not to the browser.
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	d.answer(w, r, res)
}

// finish is the second form.
func (d *Door) finish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	key := d.keyOf(r)

	// Taken rather than read: this browser gets one attempt at the second form
	// per first form, which is the app's half of a rule roster keeps too -- a
	// continuation is single-use there.
	//
	// Taken **only if it is a continuation**, and that qualifier is the whole of
	// a defect worth stating. A signed-in browser's delegation lives in the same
	// map under the same key, so an unconditional take removed it here: one
	// stray POST -- a retry, a page that fires twice, anybody who can make that
	// browser send one -- and the delegation was gone without being revoked,
	// leaving somebody whose cookie still resolves and whose every call answers
	// that this session cannot act. The alternative was to put it back after
	// looking, which is the same race written twice; deciding under the one lock
	// is what makes single-use and leave-alone the same statement.
	v, ok := d.held.takeHalf(key)
	if !ok {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}

	res, err := d.c.Vouch.Delegate(ctx, rstr.VouchDelegateRequest_builder{
		Continuation: v.continuation,
		Kind:         body.Kind,
		Name:         body.Name,
		Secret:       []byte(body.Secret),
		Methods:      d.c.Methods,
	}.Build())
	if err != nil {
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	if !res.GetVerified().GetOk() && res.GetVerified().GetContinuation() == "" {
		// A wrong code, an attempt that expired, one somebody else spent: one
		// answer, and the half-session is gone with it -- so a wrong code costs
		// the first form again, which is where the lockout counts.
		_ = d.c.Sessions.End(ctx, key)
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}

	// The old one is ended whatever comes next: a session's grant is written
	// when it is minted and nothing widens one, which is the right direction
	// for the one thing a session carries.
	_ = d.c.Sessions.End(ctx, key)

	d.answer(w, r, res)
}

// SignOut ends both halves.
//
// Exported as well as routed, because an app has its own reasons to end a
// session -- signing out everywhere is one, and it is the one that would
// otherwise leave this app's own cookie alive after roster had been told the
// opposite.
//
// The delegation is **revoked** and not merely forgotten. Forgetting it leaves
// a live credential for that person in roster's table until its own clock runs
// out, which is what D23 said *revoking it is a delete* about.
func (d *Door) SignOut(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := d.keyOf(r)

	if v, ok := d.held.take(key); ok && v.token != "" {
		// Best effort, and after the local drop. A roster that cannot be
		// reached must not stop somebody signing out of this app; what is lost
		// is a row that expires on its own.
		_, _ = d.c.Delegation.Revoke(ctx, rstr.DelegationRevokeRequest_builder{Token: v.token}.Build())
	}

	http.SetCookie(w, d.c.Sessions.End(ctx, key))
	w.WriteHeader(http.StatusNoContent)
}

// answer mints the cookie the last call earned, and says which of the three
// this was.
func (d *Door) answer(w http.ResponseWriter, r *http.Request, res *rstr.VouchDelegateResponse) {
	ctx := r.Context()
	v := res.GetVerified()

	who, tenant, err := ids(v.GetHolder(), v.GetTenant())
	if err != nil {
		if c := v.GetContinuation(); c == "" && !v.GetOk() {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}

		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	if c := v.GetContinuation(); c != "" {
		// Half way, which is a third answer and not a refusal.
		//
		// A session with an **empty grant**: the browser carries a cookie that
		// names nobody it may act as, and the continuation stays here. payday
		// anticipated the shape -- `Session.Expires` may be set by a `Verify`,
		// *which is how an app gives a short session to somebody who has not
		// finished a second factor.*
		s, cookie, err := d.c.Sessions.Mint(ctx, authsession.Session{
			Id:       who.String(),
			TenantId: tenant.String(),
			Grant:    frame.Grant{},
			Expires:  time.Now().Add(d.c.Half),
		})
		if err != nil {
			http.Error(w, "cannot sign in", http.StatusInternalServerError)
			return
		}

		d.held.put(s.Key, one{who: who, continuation: c, expires: time.Now().Add(d.c.Half)})

		http.SetCookie(w, cookie)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(http.StatusOK)

		// What the second form needs to draw itself, and nothing else. Not how
		// many steps there are, not which to offer, not what to call them --
		// D21's three refusals, and D22 says to refuse the field that describes
		// what to render however small it looks.
		_ = json.NewEncoder(w).Encode(struct {
			Satisfied []string `json:"satisfied"`
			Available []string `json:"available"`
		}{
			Satisfied: v.GetSatisfied(),
			Available: kindsOf(v.GetAvailable()),
		})

		return
	}

	if !v.GetOk() || res.GetToken() == "" {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}

	s, cookie, err := d.c.Sessions.Mint(ctx, authsession.Session{
		Id:       who.String(),
		TenantId: tenant.String(),

		// Everything this person may do, which is what signing in at this app's
		// own page means. The delegation is narrower on purpose and they are
		// not the same thing: this grant is what the **session** allows against
		// this app, and the delegation is what this app may ask roster.
		Grant: frame.Whole(),

		// Never outliving what this app was given to act with: a session that
		// outlived its delegation would be somebody signed in to a page that
		// cannot be drawn.
		Expires: res.GetExpires().AsTime(),
	})
	if err != nil {
		http.Error(w, "cannot sign in", http.StatusInternalServerError)
		return
	}

	d.held.put(s.Key, one{who: who, token: res.GetToken(), expires: res.GetExpires().AsTime()})

	http.SetCookie(w, cookie)
	w.WriteHeader(http.StatusNoContent)
}

// Accept signs somebody in on a claim this app has already checked, and holds
// the delegation beside the session exactly as a password sign-in does.
//
// # The seam D23 left, from the other side
//
// A sign-in through a provider never calls `Vouch`, so there was nothing for a
// delegation to ride back on -- and an app that minted its own session anyway
// ended up with a browser that is signed in and a `/me` that answers a refusal.
// `examples/sso` had exactly that, and its own comment said the exchange *is a
// decision nobody has taken*.
//
// It is taken: `Vouch.Accept`, D49. roster does not check the token -- being
// the relying party is what `connection.proto` decided roster is not -- so the
// app checks it and this hands the claim over.
//
// # Why it is here and not in the app
//
// Because everything after the claim is the part that is the same in every app
// that does this: the delegation, the session that must not outlive it, and the
// pair of headers a later call carries. That is what `frontdoor` is, and an app
// doing it itself is an app that gets the *must not outlive it* wrong once.
//
// # What the app still owns
//
// The exchange, the state parameter, and which tenant the browser arrived in.
// This takes the claim and nothing about the browser's journey to it.
func (d *Door) Accept(ctx context.Context, w http.ResponseWriter, tenant, provider, subject string) error {
	if provider == "" || subject == "" {
		return ErrNotSignedIn
	}

	id, err := pdid.Parse(tenant)
	if err != nil {
		return err
	}

	res, err := d.c.Vouch.Accept(ctx, rstr.VouchAcceptRequest_builder{
		Claim: rstr.VouchClaim_builder{
			Tenant:   id.Bytes(),
			Provider: provider,
			Subject:  subject,
		}.Build(),
		Methods: d.c.Methods,
	}.Build())
	if err != nil {
		return err
	}

	who, err := pdid.From(res.GetVerified().GetHolder())
	if err != nil {
		return err
	}

	s, cookie, err := d.c.Sessions.Mint(ctx, authsession.Session{
		Id:       who.String(),
		TenantId: id.String(),

		// The same two as a password sign-in, and the same reasons: everything
		// this person may do against **this app**, and never outliving the
		// delegation this app was given to act with.
		Grant:   frame.Whole(),
		Expires: res.GetExpires().AsTime(),
	})
	if err != nil {
		return err
	}

	d.held.put(s.Key, one{who: who, token: res.GetToken(), expires: res.GetExpires().AsTime()})

	http.SetCookie(w, cookie)

	return nil
}

// one is what this app holds for one browser, and it is one of two things.
//
// A **delegation** for somebody who has finished, and a **continuation** for
// somebody half way. They live in one map because they are the same lifecycle
// from the app's side -- one string, held for one browser, dropped when its own
// clock runs out -- and because a browser has exactly one at a time: finishing
// swaps the second for the first.
type one struct {
	who          pdid.Id
	token        string
	continuation string
	expires      time.Time
}

// held is the side of a session that is not payday's.
//
// `authsession.Session` has an actor, a tenant, a grant and two clocks, and
// nowhere to put an opaque string an app holds on somebody's behalf. That is
// not an omission to route around: a session is deliberately not a place to
// keep a copy of anything, so that it cannot become a stale one.
//
// A map and a mutex, matching the store beside it. An app that put this in
// Redis and left its sessions in memory would have two answers to how many
// replicas it runs -- so a deployment that moves one moves both, and this is
// the seam where it would.
//
// **The leak worth knowing about**: nothing in `authsession` tells anybody a
// session has died, so a map keyed by session would grow one entry per expired
// session forever. What keeps this one bounded is that the thing it holds
// carries its own expiry, and a pass on each write is enough.
type held struct {
	mu sync.Mutex
	by map[string]one
}

func (d *held) put(key string, v one) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.by == nil {
		d.by = map[string]one{}
	}

	now := time.Now()
	for k, v := range d.by {
		if !now.Before(v.expires) {
			delete(d.by, k)
		}
	}

	d.by[key] = v
}

func (d *held) get(key string) (one, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.by[key]
	if !ok || !time.Now().Before(v.expires) {
		return one{}, false
	}

	return v, true
}

// take is [held.get] and a delete in one, and it does **not** read the clock.
//
// Its one caller is [Door.SignOut], which wants the entry in order to revoke
// what is in it -- and an expired entry is exactly the one that most needs
// revoking. `expires` is this app's hold on the browser, not roster's on the
// credential: a delegation whose entry timed out here is still a live
// credential in roster's table until somebody says otherwise, so answering
// "absent" for it would drop the reference and leave that credential to run out
// on its own. See [Door.SignOut], which says the same thing from the other end.
//
// Which is why the second form does not use this. See [held.takeHalf].
func (d *held) take(key string) (one, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.by[key]
	delete(d.by, key)

	return v, ok
}

// takeHalf is take, of a continuation and never of a delegation.
//
// Two things at once, and they are one decision because they are decided under
// one lock.
//
// **Never of a delegation.** A signed-in browser's delegation lives in the same
// map under the same key -- a browser has one or the other, see [one] -- so an
// unconditional take removed it here: one stray POST to the second form, a
// retry, a page that fires twice, anybody who can make that browser send one,
// and the delegation was gone without being revoked. What is left behind is
// somebody whose cookie still resolves and whose every call answers that this
// session cannot act, and a credential still live in roster with nothing
// holding the reference. Putting it back after looking is the same race written
// twice; deciding here is what makes it one statement.
//
// **And only while it is live**, which is how [Config.Half] became a number
// something enforces. `finish` authenticates a second form from this and
// nothing else, so before this the only thing ending a half-session was the
// cookie's own `Expires` -- the browser's good manners. Anything that is not a
// browser had roster's hold on the attempt instead: five minutes, whatever the
// app asked for.
//
// An expired continuation is deleted on the way past, because there is nothing
// in one to revoke: roster spends it or lets it expire, and this end of it is a
// string. That is the whole reason the clock lives here and not in [held.take].
func (d *held) takeHalf(key string) (one, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.by[key]
	if !ok || v.continuation == "" {
		// Absent, or somebody's delegation -- which is not this call's to
		// spend, whether or not this app's hold on it has run out.
		return one{}, false
	}

	delete(d.by, key)
	if !time.Now().Before(v.expires) {
		return one{}, false
	}

	return v, true
}

func ids(holder, tenant []byte) (pdid.Id, pdid.Id, error) {
	who, err := pdid.From(holder)
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	at, err := pdid.From(tenant)
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	return who, at, nil
}

func kindsOf(vs []*rstr.VouchFactor) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.GetKind())
	}

	return out
}

// Redeem spends a recovery link and signs the browser in as the person it was
// minted for, the way [Door.Accept] signs a browser in on a provider's word.
//
// The same shape and the same reason: roster proves the person (`Vouch.Redeem`
// checks the link and mints the delegation) and this package does the one
// thing that has to be done right once -- the session that must not outlive
// the delegation, and the pair of headers a later call carries. An app that
// did it itself gets the *must not outlive* wrong once.
//
// What the app owns is what the link was for: `Redeem` proves a mailbox, and
// whether that is enough to sign in, or only enough to hand the person a new
// password (`Vouch.Reset`), is the app's policy and not this package's.
func (d *Door) Redeem(ctx context.Context, w http.ResponseWriter, token string) error {
	if token == "" {
		return ErrNotSignedIn
	}
	res, err := d.c.Vouch.Redeem(ctx, rstr.VouchRedeemRequest_builder{
		Token:   token,
		Methods: d.c.Methods,
	}.Build())
	if err != nil {
		return err
	}
	v := res.GetVerified()
	if !v.GetOk() || res.GetToken() == "" {
		return ErrNotSignedIn
	}
	who, tenant, err := ids(v.GetHolder(), v.GetTenant())
	if err != nil {
		return err
	}

	s, cookie, err := d.c.Sessions.Mint(ctx, authsession.Session{
		Id:       who.String(),
		TenantId: tenant.String(),
		Grant:    frame.Whole(),
		Expires:  res.GetExpires().AsTime(),
	})
	if err != nil {
		return err
	}
	d.held.put(s.Key, one{who: who, token: res.GetToken(), expires: res.GetExpires().AsTime()})
	http.SetCookie(w, cookie)

	return nil
}
