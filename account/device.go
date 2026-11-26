package account

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/arrives"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// A terminal that signs in without a clipboard: RFC 8628, the device grant.
//
// `roster sign-in` prints a URL and eight characters, somebody types them into
// this app in a browser they are already signed in at, and the command -- which
// has been polling -- writes the key it was given. What that replaces is a person
// copying a long-lived credential out of a web page, through a paste buffer and a
// scrollback, onto the machine they are actually working on.
//
// # Why this is here and not in roster
//
// Because the first draft of it was going to be two unauthenticated RPCs on
// roster's data plane, and `cmd/serve.go`'s `Public` had already refused that
// shape for two reasons that land here harder than where they were written:
//
//   - **No frame means no rate limit.** `grpcx.Limit` counts per tenant off the
//     frame, and a public call has none -- so a public poll is an unmetered
//     guessing surface, which is exactly what RFC 8628 §5.1 says a `user_code`
//     must not be.
//   - **The trail could not say who asked.** Every device grant would be
//     recorded as a stranger.
//
// And what makes the standard flow safe is the thing that draft left out: in RFC
// 8628 the device is **not anonymous**, it presents a `client_id`. A bare
// terminal talking to roster has no such thing, and a secret compiled into a
// distributed binary is not one.
//
// This app has what the flow needs and roster does not have to grow any of it: a
// key per tenant, the tenant from the host a browser arrived at, a person
// signed in at a page, and `ApiKey.Issue` reached **as that person** -- which is
// the same verb *mint an app password* already uses, held to both escalation
// rules without anything new being taught about grants.
//
// # What it holds, and for how long
//
// In memory, for fifteen minutes, exactly as the provider round trip's `state`
// is held (`arrives.States`) -- and the same compromise: a restart loses a code
// somebody is half way through, and a second replica does not know about it. For
// a credential that is minted in the next quarter of an hour or not at all, that
// is honest; a table would make a row that outlives the reason for it.
//
// The token is held on the row between the approval and the poll that carries it
// away, which is the one moment this app holds a credential it did not mint for
// itself. It is handed over once and the row goes with it.
const (
	// deviceAlphabet is RFC 8628 §6.1: a set with no two glyphs somebody could
	// confuse reading them off a terminal, which rules out `0O1IlS5U V2Z`.
	deviceAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

	// deviceCodeLen is eight, written `XXXX-XXXX`. Twenty to the eighth is
	// 2.56e10, and the guess has to be made by somebody **already signed in** at
	// this front door, which is the rate limit the RFC asks for in the form this
	// app can actually enforce.
	deviceCodeLen = 8

	// deviceFor is how long somebody has to type it.
	deviceFor = 15 * time.Minute

	// deviceInterval is what a client is told to wait between polls, and what it
	// is told `slow_down` for not waiting.
	deviceInterval = 5 * time.Second

	// deviceMisses is how many codes one person may get wrong before this stops
	// answering them for a while -- [deviceFor], the life of a code -- about any
	// code, a right one too. The code space makes guessing hopeless; this is
	// for the other thing, which is somebody pointing a script at the form.
	deviceMisses = 10

	// TerminalsWaiting is how many terminals may be waiting to be approved at
	// once, across every tenant this app fronts.
	//
	// Beginning asks for no session, so it is anybody's to ask, and each asking
	// is held for [deviceFor] with up to eight kilobytes of what it said. It
	// was held without number: the one thing in this app a stranger could grow
	// (#277). Past this a terminal is told to come back, which is what a
	// front door that cannot hold another row has to say.
	TerminalsWaiting = 1024
)

// device is one terminal waiting to be signed in.
type device struct {
	tenant  string   // whose front door began it, by alias
	name    string   // what the terminal calls itself, for the key's own name
	app     string   // the app the key is for, by alias in the tenant; empty is roster's
	methods []string // what it asked to be allowed

	// scope is the app's own word about the key, as it will be stored
	// (`ApiKey.scope`), and ends is when the key stops working, zero for a
	// key that does not. Each is what the person approving is shown.
	scope string
	ends  time.Time

	// delegate is that what is asked for is a delegation issued to `app`,
	// for it to act as the person at roster, and not a key.
	delegate bool

	// token is the key, once somebody approved it, and is handed over once.
	token  string
	denied bool

	// answering is that somebody's answer is being acted on: the row is taken
	// for it under the lock and given back, answered or not, under it.
	answering bool

	last    time.Time // the previous poll, for `slow_down`
	expires time.Time
}

// devices is every device flow begun and not yet finished.
//
// Two indexes over one row, because the two ends arrive with different halves:
// a poll carries the `device_code` and an approval carries the `user_code`
// somebody read out. `arrives.States` is take-once and cannot be either of them
// -- a poll that took the row would end the flow it is waiting on.
//
// # Everything about a row is read and written under the lock
//
// A row is answered by one request and read by another, and it was marked
// answered after the lock was let go: two approvals of one code each found it
// waiting and each minted a key, and a poll read what an approval was writing
// (#277). So an answer **takes** the row first ([devices.claim]) and gives it
// back when it is done, and a poll is told what the row says rather than
// handed the row.
type devices struct {
	mu       sync.Mutex
	byDevice map[string]*device
	byUser   map[string]*device
	misses   map[string]miss // by holder, so a wrong code costs somebody something
}

// miss is how many codes somebody got wrong, and since when.
type miss struct {
	n     int
	since time.Time
}

func held() *devices {
	return &devices{
		byDevice: map[string]*device{},
		byUser:   map[string]*device{},
		misses:   map[string]miss{},
	}
}

// put remembers one, and sweeps what has run out while it is here. It answers
// whether it did, and if not whether that is for want of room: the other
// reason is a `user` already waiting, which a caller answers with another code
// -- kept, the later terminal's code would have named the earlier one's row.
func (d *devices) put(code, user string, v *device) (ok, full bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	for k, w := range d.byDevice {
		if now.After(w.expires) {
			delete(d.byDevice, k)
		}
	}
	for k, w := range d.byUser {
		if now.After(w.expires) {
			delete(d.byUser, k)
		}
	}
	for k, w := range d.misses {
		if now.Sub(w.since) > deviceFor {
			delete(d.misses, k)
		}
	}

	if len(d.byDevice) >= TerminalsWaiting {
		return false, true
	}
	if _, taken := d.byUser[user]; taken {
		return false, false
	}

	d.byDevice[code] = v
	d.byUser[user] = v

	return true, false
}

// row is what a `user_code` names in a tenant, live and not yet answered, or
// nothing. Called with the lock held.
func (d *devices) row(user, tenant string) (*device, bool) {
	v, ok := d.byUser[strings.ToUpper(strings.TrimSpace(user))]
	if !ok || v.tenant != tenant || time.Now().After(v.expires) || v.token != "" || v.denied || v.answering {
		return nil, false
	}

	return v, true
}

// asking is what a terminal asked for, as the page is told it.
type asking struct {
	name     string
	app      string
	methods  []string
	scope    string
	ends     time.Time
	delegate bool
	expires  time.Time
}

// waiting is what the terminal a `user_code` names asked for: its name, the
// app, the methods, whether it is a delegation, and when the code runs out.
func (d *devices) waiting(user, tenant string) (asking, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.row(user, tenant)
	if !ok {
		return asking{}, false
	}

	return asking{name: v.name, app: v.app, methods: v.methods, scope: v.scope, ends: v.ends, delegate: v.delegate, expires: v.expires}, true
}

// claim takes the row a `user_code` names for one answer, so that a second
// answer finds none; [devices.settle] gives it back.
func (d *devices) claim(user, tenant string) (*device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.row(user, tenant)
	if ok {
		v.answering = true
	}

	return v, ok
}

// settle gives a claimed row back: with the key somebody's yes minted, with
// their no, or with neither, as it was -- an answer that failed leaves the
// code to be answered again.
func (d *devices) settle(v *device, token string, denied bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v.answering = false
	v.token, v.denied = token, denied
}

// polled answers a poll for the row a `device_code` names: the key, once, and
// the row gone with it; or the RFC's word for why not.
func (d *devices) polled(code string) (token, why string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.byDevice[code]
	if !ok || time.Now().After(v.expires) {
		return "", "expired_token"
	}

	soon := time.Since(v.last) < deviceInterval
	v.last = time.Now()

	switch {
	case v.denied:
		d.gone(code, v)

		return "", "access_denied"
	case v.token == "":
		if soon {
			return "", "slow_down"
		}

		return "", "authorization_pending"
	}

	// Once. The row goes with the answer, so a second poll with the same
	// `device_code` finds nothing -- and a credential this app is holding for
	// somebody else stops being held the moment it is delivered.
	d.gone(code, v)

	return v.token, ""
}

// gone takes a row away. Called with the lock held.
func (d *devices) gone(code string, v *device) {
	delete(d.byDevice, code)
	for k, w := range d.byUser {
		if w == v {
			delete(d.byUser, k)
		}
	}
}

// blocked is whether somebody has got too many codes wrong, lately, to be
// answered about another.
//
// Asked **before** a code is looked up. It was asked after, of a code already
// found wrong -- so past [deviceMisses] a right code was still answered and
// approved, and what the count bounded was nothing. And it never ran down:
// *wait a while* was until the process restarted (#277).
func (d *devices) blocked(who string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	m, ok := d.misses[who]
	if !ok {
		return false
	}
	if time.Since(m.since) > deviceFor {
		delete(d.misses, who)

		return false
	}

	return m.n >= deviceMisses
}

// missed counts a code somebody got wrong.
func (d *devices) missed(who string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	m := d.misses[who]
	if m.n == 0 || time.Since(m.since) > deviceFor {
		m = miss{since: time.Now()}
	}
	m.n++
	d.misses[who] = m
}

// userCode is eight characters of [deviceAlphabet], written `XXXX-XXXX`.
func userCode() (string, error) {
	b := make([]byte, deviceCodeLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	out := make([]byte, 0, deviceCodeLen+1)
	for i, v := range b {
		if i == deviceCodeLen/2 {
			out = append(out, '-')
		}
		// Modulo, and **slightly biased**: 256 is 12×20 + 16, so the first
		// sixteen letters come up 13 times in 256 and the last four 12. That is
		// worth saying rather than hiding behind a rejection loop -- it costs
		// about a tenth of a bit against a code that a signed-in person may get
		// wrong ten times, and the number that matters is `deviceMisses`.
		out = append(out, deviceAlphabet[int(v)%len(deviceAlphabet)])
	}

	return string(out), nil
}

// offered is whether this deployment signs terminals in at all.
//
// A 501 and not a 404, which is the difference between *this deployment does not
// do that* and *you asked for the wrong thing* -- the same answer the mail flows
// give when nothing can deliver. A terminal that gets it says so and stops,
// rather than polling for fifteen minutes on a code nobody can approve.
func (a *App) offered(w http.ResponseWriter) bool {
	if a.c.Terminal {
		return true
	}

	http.Error(w, "this deployment does not sign terminals in", http.StatusNotImplemented)

	return false
}

// begin is a terminal asking to be signed in. No session: there is nobody yet,
// which is the whole of the flow.
//
// The request is this app's own shape rather than RFC 8628's form encoding --
// there is no `client_id` to send -- and the **answer** is the RFC's, so a
// client written against the standard reads what it expects. It is what a
// client of another app's writes against, `roster sign-in` beside it: an app's
// page beginning one for the person in front of it, `foo auth login`. Its
// fields and its refusals are a table in `docs/usage/ways-in.md`, held to this
// function by `TestTheTerminalsBeginningIsWhatTheManualSays`.
//
// # It names the app
//
// `app` is the alias, in the tenant the request arrived at, of the app the
// key is for (`ApiKey.app`); none is a key of roster's own, as every key
// was. Anybody may begin one, as before: nothing is minted without the
// person's approval, which is held to what they hold at that app.
//
// # Or it asks to act for them
//
// With `delegate`, what is asked for is a delegation issued to `app`, for that
// app to act **as the person** at roster (`DelegationService/Issue`), and
// `allow` is roster's methods: an app's page that writes for its
// administrator asks this way, and presents what it is handed beside its own
// key in `roster-as`. So `app` is required, and refused at once without one.
//
// # A key's scope and its end
//
// A key for an app may carry the app's own word about it, `scope` -- an
// identifier of a row of the app's that lists what the key reaches there
// (`ApiKey.scope`) -- and an end, `date_expired`, as `ApiKeyService/Issue`
// takes them. Both are refused here as `Issue` refuses them, before anybody is
// asked to approve what roster would refuse, and both are shown to the person
// approving. A key asked for without an end never ends, as before.
//
// # A field it does not know is refused
//
// It was dropped: a body naming `scopes` was a key with no scope, minted as
// though it were what was asked for, which is a key wider than its client
// meant at the app that reads it. So a field that is none of these is refused,
// naming it. A delegation takes neither a scope nor an end: what it is narrowed
// to is roster's methods, and it lasts `delegation.life`, which is what the
// person approving is told.
func (a *App) deviceBegin(w http.ResponseWriter, r *http.Request) {
	if !a.offered(w) {
		return
	}

	t, ok := tenantFrom(r.Context())
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}

	var in struct {
		Name        string   `json:"name"`
		App         string   `json:"app"`
		Allow       []string `json:"allow"`
		Scope       string   `json:"scope"`
		DateExpired string   `json:"date_expired"`
		Delegate    bool     `json:"delegate"`
	}
	if err := strictly(http.MaxBytesReader(w, r.Body, 8<<10), &in); err != nil {
		if name, ok := unknownField(err); ok {
			http.Error(w, fmt.Sprintf("%s: this request has no such field", name), http.StatusBadRequest)
			return
		}
		http.Error(w, "a name and what to allow", http.StatusBadRequest)
		return
	}

	// Empty is refused rather than defaulted in either direction, which is
	// `roster key add`'s decision: everything hands out more than anybody asked
	// for, and nothing mints a credential that silently does not work.
	if len(in.Allow) == 0 {
		http.Error(w, "allow: which methods this terminal may call", http.StatusBadRequest)
		return
	}
	// A front door that was not told how long a delegation lives cannot tell
	// the person approving one, and it has no default of its own to say: it
	// is a consumer, and the bound is the deployment's file's. So it offers
	// none, in the words the switch being off has.
	if in.Delegate && a.delegationLife() <= 0 {
		http.Error(w, "this deployment does not let an app act for somebody here: delegation.life is not set for this front door", http.StatusNotImplemented)
		return
	}
	if in.Delegate && strings.TrimSpace(in.App) == "" {
		http.Error(w, "app: a delegation is issued to an app; name the one that is to act for you", http.StatusBadRequest)
		return
	}
	if in.Delegate && in.Scope != "" {
		http.Error(w, "scope: a delegation carries none; a scope is the word of the app a key is for", http.StatusBadRequest)
		return
	}
	if in.Delegate && in.DateExpired != "" {
		http.Error(w, "date_expired: a delegation lasts delegation.life, which is what the person approving is told", http.StatusBadRequest)
		return
	}

	// The scope as `Issue` holds it, said again here because this is a
	// consumer and reaches roster over the wire (`server/core`, `keyScoped`):
	// so a terminal is refused before somebody approves what would be.
	if in.Scope != "" && strings.TrimSpace(in.App) == "" {
		http.Error(w, "scope: this key is roster's own and for no app, so no app is there to read a scope; a scope is the word of the app a key is for, and nothing here reads it", http.StatusBadRequest)
		return
	}
	if n := len(in.Scope); n > maxKeyScope {
		http.Error(w, fmt.Sprintf("scope: at most %d bytes, and this is %d", maxKeyScope, n), http.StatusBadRequest)
		return
	}

	// And the end as `Issue` holds it. Nothing in a deployment's file bounds
	// how long a key lives, so a key may end whenever it is asked to after
	// now; roster asks it again when the key is minted.
	var ends time.Time
	if in.DateExpired != "" {
		v, err := time.Parse(time.RFC3339, in.DateExpired)
		if err != nil {
			http.Error(w, "date_expired: when the key stops working, as RFC 3339 writes a time", http.StatusBadRequest)
			return
		}
		if !v.After(time.Now()) {
			http.Error(w, "date_expired: a key cannot have already expired", http.StatusBadRequest)
			return
		}
		ends = v
	}

	code, err := arrives.Nonce()
	if err != nil {
		http.Error(w, "cannot begin", http.StatusInternalServerError)
		return
	}

	// A code somebody else is already waiting on is not handed out twice: a
	// few tries at another, which among a thousand rows and 2.56e10 codes is
	// one try.
	user, kept := "", false
	for range 4 {
		if user, err = userCode(); err != nil {
			http.Error(w, "cannot begin", http.StatusInternalServerError)
			return
		}

		ok, full := a.devices.put(code, user, &device{
			tenant: t.alias,

			// What the key will be called, in the spelling a key's name is
			// kept in: it is what somebody typed, or a machine's own name,
			// and roster refuses a name written otherwise than it is read
			// rather than folding it unsaid. So the page that asks somebody
			// to approve it shows the name they will then find.
			name: front.Alias(in.Name),

			// Taken as it is written, and not looked up: a request nobody
			// signed would be told whether a name is somebody of the tenant,
			// and roster does not tell an app's holder from a person's. The
			// person approving is shown the name that was asked for, and one
			// that is nobody is refused there ([App.deviceApprove]).
			app:     strings.TrimSpace(in.App),
			methods: in.Allow,

			// As it will be stored, and read by nothing here: the app's own
			// word, shown to whoever approves it as it was sent.
			scope:    in.Scope,
			ends:     ends,
			delegate: in.Delegate,
			expires:  time.Now().Add(deviceFor),
		})
		if full {
			w.Header().Set("retry-after", "60")
			http.Error(w, "too many terminals are waiting to be approved here; try again in a minute", http.StatusServiceUnavailable)
			return
		}
		if kept = ok; kept {
			break
		}
	}
	if !kept {
		http.Error(w, "cannot begin", http.StatusInternalServerError)
		return
	}

	// `Base` if the deployment named one, else the origin this was asked at,
	// as every other address this app hands out is ([App.redirectBase]). It
	// read `Base` alone, and a deployment that turned terminals on and named
	// no base -- one that fronts several tenants' names has none to name --
	// dereferenced nothing on every request to a route that asks for no
	// session (#277).
	at := strings.TrimSuffix(a.redirectBase(r).String(), "/") + "/device"
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device_code":               code,
		"user_code":                 user,
		"verification_uri":          at,
		"verification_uri_complete": at + "?user_code=" + user,
		"expires_in":                int(deviceFor.Seconds()),
		"interval":                  int(deviceInterval.Seconds()),
	})
}

// devicePoll is the terminal asking whether anybody has said yes yet.
//
// The errors are RFC 8628 §3.5's, by name, because that is what a client is
// written to read: `authorization_pending` means keep going, `slow_down` means
// keep going more slowly, and the other two are over.
//
// # A `POST`, with the code in its body
//
// It was a `GET` with the device code in the query, and the device code is the
// whole of what a poll is held to: whoever has it is answered the key. An
// address is what a proxy's log keeps, what a browser's history keeps, and
// what a `HEAD` asks for -- and a mux answers `HEAD` on a `GET` pattern, so a
// fetch of the address took the key and delivered it to nobody (#277). The RFC
// has it posted, for that reason.
func (a *App) devicePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("allow", http.MethodPost)
		http.Error(w, "a poll is posted, with the device code in its body", http.StatusMethodNotAllowed)
		return
	}
	if !a.offered(w) {
		return
	}

	var in struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || in.DeviceCode == "" {
		http.Error(w, "device_code", http.StatusBadRequest)
		return
	}

	token, why := a.devices.polled(in.DeviceCode)
	if why != "" {
		deviceErr(w, why)
		return
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"token": token})
}

func deviceErr(w http.ResponseWriter, why string) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": why})
}

// devicePending is the page asking what a code it has been given is for, so that
// somebody approving one is told what they are allowing.
func (a *App) devicePending(w http.ResponseWriter, r *http.Request) {
	if !a.offered(w) {
		return
	}

	t, ok := tenantFrom(r.Context())
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}

	// Somebody roster says is there, and not whoever a cookie names
	// ([App.present]).
	me, ok := a.present(r, t)
	if !ok {
		http.Error(w, "no", http.StatusForbidden)
		return
	}
	who, err := pdid.From(me.GetId())
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	if a.devices.blocked(who.String()) {
		http.Error(w, "too many codes that were not one; wait a while", http.StatusTooManyRequests)
		return
	}

	v, ok := a.devices.waiting(r.URL.Query().Get("user_code"), t.alias)
	if !ok {
		// A code of another operator's is *no such code* here, which is the same
		// answer as one that never existed: this front door serves several, and
		// which of them a code belongs to is not somebody else's to learn.
		a.devices.missed(who.String())
		http.Error(w, "that is not a code this is waiting for", http.StatusNotFound)

		return
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	// And the app the key is for, which is part of what they approve: empty
	// is a key of roster's. And whether it is that app acting as them, which
	// is not a key at all: a page draws it in words of its own.
	out := map[string]any{
		"name":     v.name,
		"app":      v.app,
		"methods":  v.methods,
		"delegate": v.delegate,
		"expires":  v.expires.UTC().Format(time.RFC3339),
	}
	if !v.delegate {
		// The app's word about the key as it will be stored, empty for none,
		// and when the key ends, absent for one that does not: each is part
		// of what they approve.
		out["scope"] = v.scope
		if !v.ends.IsZero() {
			out["date_expired"] = v.ends.UTC().Format(time.RFC3339)
		}
	}
	if v.delegate {
		// How long it would last, in seconds, which is how long the yes
		// mints it for: the page says for how long, and until when from the
		// moment it shows it.
		out["life"] = int(a.delegationLife().Seconds())
	}
	_ = json.NewEncoder(w).Encode(out)
}

// devicePage is where a terminal is told to go: `/device`, with the code in
// the query for anybody who followed the complete address.
//
// The page that asks is this app's one page, at `/`, which reads
// `?user_code=` wherever it is drawn. `/device` itself was served by nothing --
// a build is a document and its bundles, and a path that is neither is not
// found -- so the address a terminal printed opened a 404 (#277).
func (a *App) devicePage(w http.ResponseWriter, r *http.Request) {
	to := "/"
	if v := r.URL.Query().Get("user_code"); v != "" {
		to += "?" + url.Values{"user_code": {v}}.Encode()
	}

	http.Redirect(w, r, to, http.StatusFound)
}

// deviceApprove is the person saying yes, and it is where the key is minted.
//
// As **them**, through the delegation in their session -- so the methods are held
// to what they hold, the trail says they did it, and this app's own credential
// buys nothing here that the person could not have asked for themselves. Which is
// the whole reason the flow is in this app rather than in roster.
func (a *App) deviceApprove(w http.ResponseWriter, r *http.Request) {
	if !a.offered(w) {
		return
	}

	ctx := r.Context()
	t, ok := tenantFrom(ctx)
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}

	// Somebody roster says is there, for the refusal as for the yes: saying no
	// to a terminal is this app's own doing, and was any cookie's.
	me, ok := a.present(r, t)
	if !ok {
		http.Error(w, "no", http.StatusForbidden)
		return
	}
	who, err := pdid.From(me.GetId())
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	// A field it does not know is refused, as at the beginning: `denied`
	// for `deny` was a yes, and minted a key somebody meant to refuse.
	var in struct {
		UserCode string `json:"user_code"`
		Deny     bool   `json:"deny"`
	}
	if err := strictly(http.MaxBytesReader(w, r.Body, 4<<10), &in); err != nil {
		if name, ok := unknownField(err); ok {
			http.Error(w, fmt.Sprintf("%s: this request has no such field", name), http.StatusBadRequest)
			return
		}
		http.Error(w, "user_code", http.StatusBadRequest)
		return
	}

	if a.devices.blocked(who.String()) {
		http.Error(w, "too many codes that were not one; wait a while", http.StatusTooManyRequests)
		return
	}

	// Taken for this answer, so that a second one finds no code waiting: two
	// approvals of one code each minted a key ([devices]).
	v, ok := a.devices.claim(in.UserCode, t.alias)
	if !ok {
		a.devices.missed(who.String())
		http.Error(w, "that is not a code this is waiting for", http.StatusNotFound)

		return
	}

	if in.Deny {
		a.devices.settle(v, "", true)
		w.WriteHeader(http.StatusNoContent)

		return
	}

	as, err := a.door.Acting(ctx, r)
	if err != nil {
		a.devices.settle(v, "", false)
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	name := v.name
	if name == "" {
		name = "terminal"
	}

	if v.delegate {
		a.deviceDelegate(w, t, as, who, v, name)
		return
	}

	req := rstr.ApiKeyIssueRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   name,
		Methods: v.methods,
		Scope:   v.scope,
	}
	if !v.ends.IsZero() {
		req.DateExpired = timestamppb.New(v.ends)
	}
	if v.app != "" {
		// For the app the terminal named, by its alias in this tenant: the
		// methods are held to what she holds there (`ApiKey.app`).
		req.App = rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias:  &v.app,
			Tenant: rstr.TenantRef_builder{Id: t.id.Bytes()}.Build(),
		}.Build()}.Build()
	}

	res, err := a.roster.ApiKey().Issue(t.on(as), req.Build())
	if err != nil {
		// Not answered: the code waits for another try, or for its clock.
		a.devices.settle(v, "", false)
		fmt.Fprintf(os.Stderr, "account: device approve at %s: %v\n", t.alias, err)

		// The two refusals worth telling apart are the rules a person can do
		// something about: a method they do not hold themselves, and a name they
		// have already used. Everything else is this app's problem and says so.
		switch status.Code(err) {
		case codes.PermissionDenied:
			http.Error(w, "a terminal cannot be allowed more than you hold", http.StatusForbidden)
		case codes.AlreadyExists:
			http.Error(w, "you already have a key called that", http.StatusConflict)
		case codes.NotFound:
			// The app the terminal named is nobody here: the words a name
			// nobody serves is refused in.
			http.Error(w, "no operator here serves this name", http.StatusNotFound)
		case codes.InvalidArgument:
			// What the terminal asked for, refused as `Issue` refuses it: an
			// end that has passed while the code waited, say. In roster's
			// words, which name the field.
			http.Error(w, status.Convert(err).Message(), http.StatusBadRequest)
		default:
			http.Error(w, "the key could not be made", http.StatusInternalServerError)
		}

		return
	}

	// The terminal's next poll carries this away and the row goes with it.
	a.devices.settle(v, res.GetToken(), false)

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name})
}

// maxKeyScope is the most bytes a key's scope is, roster's bound
// (`server/core`, `MaxKeyScope`) said again by a consumer that cannot import
// it, so that a terminal is refused before anybody approves it. roster holds
// it to its own when the key is minted, so the two disagreeing is a refusal
// after the yes and never a key past the bound.
const maxKeyScope = 256

// strictly reads a JSON body into `v` and refuses a field `v` does not have,
// which the decoder would otherwise drop ([App.deviceBegin]).
func strictly(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()

	return d.Decode(v)
}

// unknownField is the name of the field a [strictly] read refused, as the
// body wrote it. The decoder says it in a sentence and in no type of its own.
func unknownField(err error) (string, bool) {
	rest, ok := strings.CutPrefix(err.Error(), "json: unknown field ")
	if !ok {
		return "", false
	}
	name, uerr := strconv.Unquote(rest)
	if uerr != nil {
		return "", false
	}

	return name, true
}

// delegationLife is how long a delegation this app asks for lives, and
// zero where it was not told: then it offers none ([App.deviceBegin]).
func (a *App) delegationLife() time.Duration { return a.c.DelegationLife }

// deviceDelegate is the yes to an app asking to act for the person: a
// delegation issued to that app, minted with `DelegationService/Issue` as
// them, through the delegation in their session, as a key is minted. Its
// methods are held to what they may call at roster, and the poll carries the
// token away once, as it carries a key.
//
// # It ends when they were told
//
// The page said how long (`life`, `delegation.life` as this app read it), so
// what is minted says its end -- this yes and that long -- and is not left to
// roster to choose. Where roster's own bound is shorter, two files that
// disagree, roster refuses it, and the person is told so here: nothing is
// minted that lasts other than what they approved.
func (a *App) deviceDelegate(w http.ResponseWriter, t *tenant, as context.Context, who pdid.Id, v *device, name string) {
	res, err := a.roster.Delegation().Issue(t.on(as), rstr.DelegationIssueRequest_builder{
		Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		App: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias:  &v.app,
			Tenant: rstr.TenantRef_builder{Id: t.id.Bytes()}.Build(),
		}.Build()}.Build(),
		Methods:     v.methods,
		DateExpired: timestamppb.New(time.Now().Add(a.delegationLife())),
	}.Build())
	if err != nil {
		a.devices.settle(v, "", false)
		fmt.Fprintf(os.Stderr, "account: device approve (a delegation) at %s: %v\n", t.alias, err)

		switch status.Code(err) {
		case codes.InvalidArgument:
			if strings.HasPrefix(status.Convert(err).Message(), "date_expired") {
				http.Error(w, "this front door would let the app act as you for longer than roster allows, so nothing was made; tell whoever runs it", http.StatusConflict)
				return
			}
			if strings.Contains(status.Convert(err).Message()+fmt.Sprint(status.Convert(err).Details()), "came through") {
				http.Error(w, "the app named is this front door, which does not hand itself a delegation", http.StatusBadRequest)
				return
			}
			http.Error(w, "the app named is you; an app acts for you, and you are not one", http.StatusBadRequest)
		case codes.PermissionDenied:
			http.Error(w, "an app cannot be allowed to act as you for more than you may call", http.StatusForbidden)
		case codes.NotFound:
			http.Error(w, "no operator here serves this name", http.StatusNotFound)
		default:
			http.Error(w, "the delegation could not be made", http.StatusInternalServerError)
		}

		return
	}

	a.devices.settle(v, res.GetToken(), false)

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name})
}
