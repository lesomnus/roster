package account

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/roster/mail"
)

// How much this app sends, and how much of it at once.
//
// [Config.Mail] is the one door every mailed link leaves by. Three of the
// routes to it are a person signed in, and the fourth -- `POST /recover` -- is a
// form anybody may fill in. Unlimited, that form is a stranger filling
// somebody's mailbox from this deployment's address. It is also a way to have
// the deployment's sending account suspended, because a provider counts the
// complaints and not who asked. Nothing in front of this app limits it:
// `server.limit` is roster's, off unless set, and counts per tenant.
//
// So three limits:
//
//   - **An address** is sent [addressBurst] of one kind of message, then one
//     more every [addressEvery]. This is what keeps a mailbox from being
//     filled, by a stranger at the recovery form or by anybody signed in.
//   - **A person signed in** may have [accountBurst] sent, of every kind
//     together, then one more every [accountEvery]. `verify` reaches whatever
//     address somebody writes onto their own row, so an address's limit cannot
//     hold a script on one account; this does.
//   - **At most [inFlight] messages are on their way at once.** One past that is
//     dropped and logged rather than queued: the recovery form still answers
//     202, and a person signed in is told 503.
//
// The first two count messages that went out, and never requests refused
// before one would, so that one person's refusals never spend another's
// allowance: a request about an address that is not the asker's is refused by
// roster before anything is counted against that address.
//
// # What it holds, and for how long
//
// In memory, as [devices] is, with the same compromise. A restart forgets what
// was sent, and a second replica does not know what the first sent, so **N
// replicas allow N times as much** -- the compromise `server.limit` makes for
// the same reason (operating.md, "Running more than one"). Exact across
// replicas would be state they share, and this app has none of its own: it
// reaches roster over the wire and holds no database, so that would be roster
// growing a kind for a front door's counters, or a store the deployment does
// not otherwise run.
//
// One time per address and per account, the one GCRA keeps: when its allowance
// is whole again. An entry goes once it is, so what is held is what was sent
// in the last hour and no more. The recovery form counts only what it sends, to
// an address somebody proved, so a stranger typing addresses by the thousand
// adds nothing here.
const (
	// addressBurst and addressEvery are three messages of one kind to one
	// address, then one every twenty minutes.
	//
	// A link lasts fifteen minutes (`vouch.LinkFor`), so one every twenty is a
	// fresh link about as often as the last one runs out. Three is somebody
	// pressing again because the first is slow to arrive, which greylisting can
	// make it for minutes. A stranger repeating the recovery form at one address
	// gets it three messages, then three an hour.
	addressBurst = 3
	addressEvery = 20 * time.Minute

	// accountBurst and accountEvery are five messages for one person signed
	// in, of every kind together, then one every ten minutes.
	//
	// Five is somebody confirming a few addresses and asking once more. Six an
	// hour after that is more than a person asks for, and is what a script on
	// one account is held to whatever addresses it names.
	accountBurst = 5
	accountEvery = 10 * time.Minute

	// inFlight is how many messages may be on their way at once.
	//
	// A front door's mail is a trickle, and a provider answers in well under a
	// second, so eight at once is many times its busiest. A server that stops
	// answering then holds eight connections, each for `account.mail.timeout`,
	// rather than one per request.
	inFlight = 8
)

// What a message is for, which is what an address's allowance is counted by.
// `verify` and `claim` send the same message -- *confirm your address* -- and
// so count as one.
const (
	forRecovery = "recover"
	forConfirm  = "confirm"
	forLeaving  = "leave"
)

// allowance is a bucket of `burst` messages that fills by one every `every`.
type allowance struct {
	burst int
	every time.Duration
}

// wait is how long until one more message fits, in a bucket that is whole at
// `full`: zero or less is now.
func (a allowance) wait(full, now time.Time) time.Duration {
	if full.Before(now) {
		full = now
	}

	return full.Add(a.every).Sub(now) - time.Duration(a.burst)*a.every
}

// spend is when the bucket is whole again once one more message has gone.
func (a allowance) spend(full, now time.Time) time.Time {
	if full.Before(now) {
		full = now
	}

	return full.Add(a.every)
}

// outbox is what this app has sent lately, and what is on its way.
type outbox struct {
	mu  sync.Mutex
	now func() time.Time

	to map[string]time.Time // by kind and address: when its allowance is whole
	by map[string]time.Time // by account, the same

	toAddress allowance
	byAccount allowance

	flying chan struct{}
}

func newOutbox() *outbox {
	return &outbox{
		now:       time.Now,
		to:        map[string]time.Time{},
		by:        map[string]time.Time{},
		toAddress: allowance{addressBurst, addressEvery},
		byAccount: allowance{accountBurst, accountEvery},
		flying:    make(chan struct{}, inFlight),
	}
}

func addressKey(kind, to string) string {
	return kind + " " + strings.ToLower(strings.TrimSpace(to))
}

// may answers how long until a message of `kind` to `to` would fit, for `by`
// -- an account, or nobody for the recovery form -- spending nothing. A route
// asks it before minting a link, so that a refusal mints nothing.
func (o *outbox) may(kind, to, by string) (time.Duration, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	wait := o.waitFor(kind, to, by, o.now())

	return wait, wait <= 0
}

// take spends one message of `kind` to `to` for `by`: both allowances, or
// neither when either is spent. It answers how long until the message would
// fit when it does not.
func (o *outbox) take(kind, to, by string) (time.Duration, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := o.now()
	for k, full := range o.to {
		if !full.After(now) {
			delete(o.to, k)
		}
	}
	for k, full := range o.by {
		if !full.After(now) {
			delete(o.by, k)
		}
	}

	if wait := o.waitFor(kind, to, by, now); wait > 0 {
		return wait, false
	}

	key := addressKey(kind, to)
	o.to[key] = o.toAddress.spend(o.to[key], now)
	if by != "" {
		o.by[by] = o.byAccount.spend(o.by[by], now)
	}

	return 0, true
}

// waitFor is the longer of the two waits, the address's and the account's.
func (o *outbox) waitFor(kind, to, by string, now time.Time) time.Duration {
	wait := o.toAddress.wait(o.to[addressKey(kind, to)], now)
	if by != "" {
		wait = max(wait, o.byAccount.wait(o.by[by], now))
	}

	return wait
}

// slot holds one of the [inFlight] messages that may be on their way, and
// answers false when they all are. The caller releases it once the message has
// gone or failed.
func (o *outbox) slot() (func(), bool) {
	select {
	case o.flying <- struct{}{}:
		return func() { <-o.flying }, true
	default:
		return nil, false
	}
}

// tooSoon answers somebody signed in who has asked for more than an allowance
// holds: 429, when to ask again, and that in words for the page to show.
func tooSoon(w http.ResponseWriter, wait time.Duration) {
	minutes := int((wait + time.Minute - 1) / time.Minute)
	w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
	when := "in a minute"
	if minutes > 1 {
		when = fmt.Sprintf("in %d minutes", minutes)
	}
	http.Error(w, "a link went out a moment ago, and more would be too many; ask again "+when, http.StatusTooManyRequests)
}

// busy answers somebody signed in whose message was dropped because as many as
// may be were already on their way.
func busy(w http.ResponseWriter, to string) {
	dropped(to)
	http.Error(w, "too many messages are on their way just now; ask again in a moment", http.StatusServiceUnavailable)
}

// dropped says a message did not go because [inFlight] were already on their
// way, as [unsent] says one that failed: the address cut to its domain.
func dropped(to string) {
	fmt.Fprintf(os.Stderr, "account: mail to %s: dropped, %d messages are already on their way\n", mail.Mask(to), inFlight)
}

// spent says a recovery message did not go because its address has had its
// allowance. Nobody else is told: the form answered 202 already.
func spent(to string, wait time.Duration) {
	fmt.Fprintf(os.Stderr, "account: mail to %s: not sent, its recovery messages are spent for %s\n",
		mail.Mask(to), wait.Round(time.Second))
}
