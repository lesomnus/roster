package account

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/arrives"
	rstr "github.com/lesomnus/roster/rstr"
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
// key per operator, the tenant from the host a browser arrived at, a person
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
	// answering them for a while. The code space makes guessing hopeless; this is
	// for the other thing, which is somebody pointing a script at the form.
	deviceMisses = 10
)

// device is one terminal waiting to be signed in.
type device struct {
	tenant  string   // whose front door began it, by alias
	name    string   // what the terminal calls itself, for the key's own name
	methods []string // what it asked to be allowed

	// token is the key, once somebody approved it, and is handed over once.
	token  string
	denied bool

	last    time.Time // the previous poll, for `slow_down`
	expires time.Time
}

// devices is every device flow begun and not yet finished.
//
// Two indexes over one row, because the two ends arrive with different halves:
// a poll carries the `device_code` and an approval carries the `user_code`
// somebody read out. `arrives.States` is take-once and cannot be either of them
// -- a poll that took the row would end the flow it is waiting on.
type devices struct {
	mu       sync.Mutex
	byDevice map[string]*device
	byUser   map[string]*device
	misses   map[string]int // by holder, so a wrong code costs somebody something
}

func held() *devices {
	return &devices{
		byDevice: map[string]*device{},
		byUser:   map[string]*device{},
		misses:   map[string]int{},
	}
}

// put remembers one, and sweeps what has run out while it is here.
func (d *devices) put(code, user string, v *device) {
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

	d.byDevice[code] = v
	d.byUser[user] = v
}

// waiting answers the row a `user_code` names, live and not yet answered.
func (d *devices) waiting(user string) (*device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.byUser[strings.ToUpper(strings.TrimSpace(user))]
	if !ok || time.Now().After(v.expires) || v.token != "" || v.denied {
		return nil, false
	}

	return v, true
}

// polled answers the row a `device_code` names, and whether this poll arrived
// too soon after the last one.
func (d *devices) polled(code string) (*device, bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.byDevice[code]
	if !ok || time.Now().After(v.expires) {
		return nil, false, false
	}

	soon := time.Since(v.last) < deviceInterval
	v.last = time.Now()

	return v, true, soon
}

// done takes the row away, which is what handing the key over does.
func (d *devices) done(code string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.byDevice[code]
	if !ok {
		return
	}
	delete(d.byDevice, code)
	for k, w := range d.byUser {
		if w == v {
			delete(d.byUser, k)
		}
	}
}

// missed counts a code somebody got wrong, and answers whether they have run out
// of tries.
func (d *devices) missed(who string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.misses[who]++

	return d.misses[who] > deviceMisses
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

// begin is a terminal asking to be signed in. No session: there is nobody yet,
// which is the whole of the flow.
//
// The request is this app's own shape rather than RFC 8628's form encoding --
// nothing but `roster sign-in` speaks to it, and there is no `client_id` to send
// -- and the **answer** is the RFC's, so a client written against the standard
// reads what it expects.
func (a *App) deviceBegin(w http.ResponseWriter, r *http.Request) {
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
	user, err := userCode()
	if err != nil {
		http.Error(w, "cannot begin", http.StatusInternalServerError)
		return
	}

	a.devices.put(code, user, &device{
		tenant:  t.alias,
		name:    in.Name,
		methods: in.Allow,
		expires: time.Now().Add(deviceFor),
	})

	at := strings.TrimSuffix(a.c.Base.String(), "/") + "/device"
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
func (a *App) devicePoll(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("device_code")
	if code == "" {
		http.Error(w, "device_code", http.StatusBadRequest)
		return
	}

	v, ok, soon := a.devices.polled(code)
	if !ok {
		deviceErr(w, "expired_token")
		return
	}

	switch {
	case v.denied:
		a.devices.done(code)
		deviceErr(w, "access_denied")
	case v.token == "":
		if soon {
			deviceErr(w, "slow_down")

			return
		}
		deviceErr(w, "authorization_pending")
	default:
		// Once. The row goes with the answer, so a second poll with the same
		// `device_code` finds nothing -- and a credential this app is holding for
		// somebody else stops being held the moment it is delivered.
		token := v.token
		a.devices.done(code)

		w.Header().Set("content-type", "application/json")
		w.Header().Set("cache-control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": token})
	}
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
	who, ok := a.door.Who(r.Context(), r)
	if !ok {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	t, ok := tenantFrom(r.Context())
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
		return
	}

	v, ok := a.devices.waiting(r.URL.Query().Get("user_code"))
	if !ok || v.tenant != t.alias {
		// A code of another operator's is *no such code* here, which is the same
		// answer as one that never existed: this front door serves several, and
		// which of them a code belongs to is not somebody else's to learn.
		if a.devices.missed(who.String()) {
			http.Error(w, "too many codes that were not one; wait a while", http.StatusTooManyRequests)

			return
		}
		http.Error(w, "that is not a code this is waiting for", http.StatusNotFound)

		return
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":    v.name,
		"methods": v.methods,
		"expires": v.expires.UTC().Format(time.RFC3339),
	})
}

// deviceApprove is the person saying yes, and it is where the key is minted.
//
// As **them**, through the delegation in their session -- so the methods are held
// to what they hold, the trail says they did it, and this app's own credential
// buys nothing here that the person could not have asked for themselves. Which is
// the whole reason the flow is in this app rather than in roster.
func (a *App) deviceApprove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	who, ok := a.door.Who(ctx, r)
	if !ok {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	t, ok := tenantFrom(ctx)
	if !ok {
		http.Error(w, "no operator here serves this name", http.StatusNotFound)
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

	v, ok := a.devices.waiting(in.UserCode)
	if !ok || v.tenant != t.alias {
		if a.devices.missed(who.String()) {
			http.Error(w, "too many codes that were not one; wait a while", http.StatusTooManyRequests)

			return
		}
		http.Error(w, "that is not a code this is waiting for", http.StatusNotFound)

		return
	}

	if in.Deny {
		v.denied = true
		w.WriteHeader(http.StatusNoContent)

		return
	}

	as, err := a.door.Acting(ctx, r)
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	name := v.name
	if name == "" {
		name = "terminal"
	}

	res, err := a.roster.ApiKey().Issue(withKey(as, t.key), rstr.ApiKeyIssueRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   name,
		Methods: v.methods,
	}.Build())
	if err != nil {
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
	v.token = res.GetToken()

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name})
}
