package account

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	methods []string // what it asked to be allowed

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

// waiting is what the terminal a `user_code` names asked for: its name, the
// methods, and when the code runs out.
func (d *devices) waiting(user, tenant string) (string, []string, time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.row(user, tenant)
	if !ok {
		return "", nil, time.Time{}, false
	}

	return v.name, v.methods, v.expires, true
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
// nothing but `roster sign-in` speaks to it, and there is no `client_id` to send
// -- and the **answer** is the RFC's, so a client written against the standard
// reads what it expects.
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
		Name  string   `json:"name"`
		Allow []string `json:"allow"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
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
			name:    front.Alias(in.Name),
			methods: in.Allow,
			expires: time.Now().Add(deviceFor),
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

	name, methods, expires, ok := a.devices.waiting(r.URL.Query().Get("user_code"), t.alias)
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
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":    name,
		"methods": methods,
		"expires": expires.UTC().Format(time.RFC3339),
	})
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

	var in struct {
		UserCode string `json:"user_code"`
		Deny     bool   `json:"deny"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
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

	res, err := a.roster.ApiKey().Issue(t.on(as), rstr.ApiKeyIssueRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   name,
		Methods: v.methods,
	}.Build())
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
