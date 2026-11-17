package login

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// Watch carries roster's sign-outs across to Hydra, and blocks until ctx is
// done.
//
// # Why this is here and not in roster
//
// Because roster does not know Hydra exists, and this is the whole of what
// keeps that true. `SyncService` says what has stopped being good about
// somebody -- signed out everywhere, suspended, erased -- to **any** app that
// holds a credential, in roster's own vocabulary and nobody else's. Turning one
// of those into `DELETE /admin/oauth2/auth/sessions/{consent,login}` is a fact
// about Hydra, so it lives in the package that already knows about Hydra.
//
// Without it there is a real hole and it is quiet: an tenant signs somebody
// out everywhere, roster's own credentials stop working, and Hydra goes on
// remembering them -- so the next product they open gets a fresh token with no
// form in between.
//
// # One stream, and no filter
//
// `SyncWatchRequest` is empty on purpose: what an app hears is narrowed by the
// **wall**, exactly as a read is. There is nothing to filter and nothing that
// could be filtered wrong.
//
// It was one stream per tenant, because this app held one `rt_` per tenant and
// each key heard its own. It holds one `rk_` now, and the one call in this app
// that goes out **without** `roster-at` is this one: a deployment key hears every
// tenant, which is what a deployment key is for. So a customer added after this
// process started is heard without anything being restarted -- which the old
// shape could not do, and which is most of why the list of tenants is gone.
//
// What that widens is what this hears, and it is worth being exact about: every
// tenant's sign-outs rather than the fronted ones'. What it does with one is
// `DELETE` a subject's Hydra grants and login session, and a subject Hydra has
// never seen is two deletes of nothing. So the wider stream costs calls that do
// nothing, and buys not having to know who this app fronts.
//
// # What was heard is the process's to remember
//
// The stream replays nothing -- "a stream that ended is a stream that missed
// things" -- and the first time a connection hears of somebody it is sent the
// facts as they stand, whatever the write was: a profile edit, the `Fill` this
// app's own callback makes, a directory's sync. `date_invalidated` is never
// cleared, so that first event carries the stamp of a sign-out however old.
//
// What this holds -- the last moment heard, per person -- was the connection's,
// dropped when the stream was, on the argument that revoking once more is the
// safe direction. It was not safe. After a restart or a dropped stream, the
// first write of any kind to somebody's row had Hydra forget them for a
// sign-out acted on months before: every client's grant, the browser, and a
// sign-in in progress. So it is the process's ([signOuts]), and a stream that
// comes back says nothing new about anybody it had spoken of.
//
// A process cannot remember what it never heard. So the first stamp it hears
// about somebody is held against the moment it started, less [watchGrace]:
// an older one is remembered and not acted on, and a newer one is news, since
// nobody may have been listening then. A stamp that moves past what is
// remembered is always news.
//
// **What that gives up**: a sign-out made while no instance of this app was
// listening for longer than the grace is not carried to Hydra. It was, before
// -- whenever that person's row was next written, which is no promise of when
// -- at the price above. Hydra goes on remembering that browser, which this
// app asks roster about at every flow all the same, and a client's refresh
// token for them lives until its lifetime.
//
// # Heard, and then carried
//
// The loop that reads the stream writes down *this person, this moment* and
// reads on; a worker tells Hydra ([signOuts.carry]). They were one loop, on a
// client that waited for ever. A Hydra slow to answer put the stream behind,
// and roster cuts a subscriber that falls `watch.Backlog` events behind; a
// revocation Hydra refused was logged and never tried again, on the argument
// that the reconnect was the retry -- and a reconnect replays nothing. What is
// written down is tried until Hydra takes it or this process stops, and a
// sign-in of the same person waits for it ([signOuts.settle]).
//
// # A stream roster refuses
//
// A deployment with no broker cannot serve this at all, and a key whose list
// does not name `SyncService/Watch` is refused it. Either is **logged and not
// fatal**: an app that refused to sign anybody in because it could not sign
// anybody out is worse than one that says so loudly and goes on working. It
// returned on hearing so, for the life of the process, and the role put right
// an hour later was noticed at the next restart. The stream is asked for again
// every [watchRefused], and what is said is said when the refusing begins and
// when it ends.
func (a *App) Watch(ctx context.Context) error {
	// The worker lives exactly as long as the stream is asked for, and is
	// waited for: once this has returned, nothing tells Hydra anything on its
	// behalf.
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.outs.carry(ctx, a.admin.revokeSessions)
	}()
	defer func() { <-done }()

	return a.watch(ctx)
}

// How long a dropped stream waits before it is asked for again, the most that
// wait doubles to, and how long a stream has to have lasted for the next wait
// to begin from the shortest again.
//
// The last was missing: the wait only ever grew, so a process that had seen
// five drops in a month waited thirty seconds after every one that followed,
// hearing nothing meanwhile.
const (
	watchBackoff = 1 * time.Second
	watchAtMost  = 30 * time.Second
	watchKept    = time.Minute
)

// watchRefused is how long a stream roster refused is left before it is asked
// for again, where [Config.Refused] does not say. It is
// `standing.RefusedRetry`, for its reason: long enough not to fill a log, short
// enough that the role change which fixes it is noticed without a restart.
const watchRefused = 10 * time.Minute

// watchServed is how long a stream has to have been open, unrefused, to be
// taken as one roster serves.
//
// roster refuses a stream as it is opened, and says nothing of one it serves
// until it has something to say about somebody -- which in a quiet deployment
// is hours. So that the refusing has ended is read off the first event, or off
// this much silence, whichever is first: an operator who has just patched a
// role is told it worked.
const watchServed = 10 * time.Second

// watchGrace is how far before this process started a sign-out first heard
// may have been made and still be news ([signOuts.hear]): how long a restart
// with nobody listening is allowed to take.
//
// A sign-out made after one instance stopped and before the next subscribed
// was heard by nobody, and its stamp is all that is left of it. Ten minutes is
// a rollout that waited on an image and a slow start, and the difference
// between roster's clock, which wrote the stamp, and this process's, which it
// is held against.
//
// What it costs is the same ten minutes read the other way: somebody signed
// out everywhere inside them, which a process before this one heard and
// carried, is forgotten by Hydra once more when their row is next written.
const watchGrace = 10 * time.Minute

func (a *App) watch(ctx context.Context) error {
	log := slog.Default()
	wait := watchBackoff

	every := a.c.Refused
	if every <= 0 {
		every = watchRefused
	}

	// Whether roster is refusing the stream, which is said when it begins and
	// when it ends and at no ask between: a line every ten minutes is one
	// nobody reads by the second day.
	var refused atomic.Bool
	served := func() {
		if refused.CompareAndSwap(true, false) {
			log.InfoContext(ctx, "login: roster serves the sync stream again; a sign-out made while it would not was heard by nobody")
		}
	}

	for ctx.Err() == nil {
		began := time.Now()
		err := a.stream(ctx, served)
		if ctx.Err() != nil {
			// A stream that ended because this app is stopping is not a stream
			// that dropped, and saying so at every shutdown is how a log stops
			// being read.
			return nil
		}
		if time.Since(began) > watchKept {
			wait = watchBackoff
		}

		pause := wait
		switch status.Code(err) {
		case codes.Unimplemented, codes.PermissionDenied:
			// Not something a second's wait fixes: a deployment with no
			// broker, or a key whose list does not name this. Loudly, and this
			// app goes on signing people in -- what it cannot do is sign them
			// out when roster says to, which an tenant has to know.
			if refused.CompareAndSwap(false, true) {
				log.ErrorContext(ctx, "login: roster will not say when somebody is signed out; hydra will keep remembering them",
					"err", err, "asked_again_every", every)
			}
			pause = every

		default:
			log.WarnContext(ctx, "login: the sync stream dropped", "err", err, "in", wait)
			wait = min(wait*2, watchAtMost)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pause):
		}
	}

	return nil
}

// errEnded is a stream roster closed without saying why. It is a stream that
// ended like any other, and is asked for again: read as *nothing more to
// hear*, it stopped this app listening for the life of the process.
var errEnded = errors.New("roster ended the stream")

// stream is one connection, from asking for it to the error that ended it.
// `served` is told when roster is seen to be serving it.
//
// Nothing here waits on Hydra, and nothing it remembers is its own: what an
// event says is written down in [signOuts], which outlives it.
func (a *App) stream(ctx context.Context, served func()) error {
	// No `roster-at`, which is the whole of what makes this one stream; see the
	// paragraph above.
	s, err := a.sync.Watch(ctx, rstr.SyncWatchRequest_builder{}.Build())
	if err != nil {
		return err
	}

	// See [watchServed]. Asked for and not yet answered is all that is known
	// here; a refusal is the answer to that, and comes at once.
	quiet := time.AfterFunc(watchServed, served)
	defer quiet.Stop()

	for {
		v, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return errEnded
		}
		if err != nil {
			return err
		}
		served()

		who, err := pdid.From(v.GetHolder())
		if err != nil {
			continue
		}
		if v.GetReason() == rstr.SyncReason_SYNC_REASON_REACH {
			// What they may call, which is no session of Hydra's. None of the
			// facts on it is news (`sync.proto`): a stamp that moved was an
			// event of its own. Read as if they were, one role edit would be
			// the first this process hears of everybody it reaches, and
			// Hydra forgetting each of them whose last sign-out it was not
			// there for.
			continue
		}

		// Their tenant closing is the fourth way, and is heard on each of its
		// people for exactly this: a subject is what Hydra forgets.
		at := latest(v.GetDateInvalidated().AsTime(), v.GetDateDisabled().AsTime(), v.GetDateErased().AsTime(), v.GetDateClosed().AsTime())
		if at.IsZero() {
			// Nothing is set: somebody in good standing, including somebody
			// who **was** suspended and is not any more. Nothing to revoke, and
			// nothing to remember either -- if they are suspended again that
			// is a later moment and it will be newer than what is here.
			continue
		}

		// The subject and not the tenant: one stream hears every tenant, and a
		// `Holder.id` is globally unique -- which is the whole of what `sub`
		// is for.
		a.outs.hear(who, at)
	}
}

// signOuts is what this process has heard of who stopped being good, and what
// of it Hydra has not taken yet.
//
// One of them for the life of the process, whatever becomes of the stream:
// see [App.Watch], *What was heard is the process's to remember*.
type signOuts struct {
	// since is the moment a stamp first heard has to be at or after to be
	// news: when this process started, less [watchGrace].
	since time.Time

	mu sync.Mutex

	// seen is the newest moment heard, per person, acted on or not.
	//
	// What the stream sends is **state and not a delta**, and
	// `date_invalidated` is monotonic and never cleared -- so without this,
	// every later event about somebody would revoke again, including the one
	// that says they are back in good standing, which would sign them out the
	// moment after they signed in.
	//
	// It holds one moment for everybody the stream has spoken of with a stamp
	// set, for as long as the process runs, and nothing takes one out:
	// somebody erased is heard of again when they are restored and written to,
	// and their old stamp is what keeps that from being news. An identifier
	// and a time each, which for a hundred thousand people heard of is a few
	// megabytes.
	seen map[pdid.Id]time.Time

	// owed is whom Hydra is still to be told to forget, by subject -- which is
	// what Hydra is told, and what a login is accepted as.
	owed map[string]*owing

	// said counts what was written down, which is the order it is carried in.
	said uint64

	// woken is poked when somebody is owed who was not.
	woken chan struct{}
}

// owing is one person Hydra has not yet been told to forget.
type owing struct {
	subject string

	// at is the newest moment heard for them. One entry however many arrive:
	// forgetting somebody is by subject and takes everything they hold, so the
	// last moment is the only one there is anything to do about.
	at time.Time

	// n is when this was written down among the rest, and due when it is next
	// tried: zero is now, and later is a Hydra that refused it.
	n   uint64
	due time.Time

	// wait is how long the next refusal puts it off, and tries how many there
	// have been.
	wait  time.Duration
	tries int

	// done is closed when Hydra has taken it.
	done chan struct{}
}

// How long what Hydra refused waits before it is tried again, and the most
// that doubles to. It is tried until Hydra takes it: there is no number of
// refusals after which somebody signed out everywhere is left signed in.
const (
	carryFirst  = 1 * time.Second
	carryAtMost = 30 * time.Second
)

// settleWaits is how long a sign-in waits for Hydra to take the sign-out
// before it ([signOuts.settle]), which is two `DELETE`s to a port on the same
// network. A Hydra that needs longer is one the sign-in is about to be slow at
// too, and somebody at a form is not kept there for it.
const settleWaits = 5 * time.Second

func newSignOuts(started time.Time) *signOuts {
	return &signOuts{
		since: started.Add(-watchGrace),
		seen:  map[pdid.Id]time.Time{},
		owed:  map[string]*owing{},
		woken: make(chan struct{}, 1),
	}
}

// hear is the stream saying when somebody last stopped being good, and writes
// down that Hydra is to forget them where that is news. It never waits.
func (o *signOuts) hear(who pdid.Id, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()

	was, heard := o.seen[who]
	if heard && !at.After(was) {
		// What was heard already, said again: every event about somebody
		// carries the stamps still set, and a connection that has just opened
		// says them of whoever is written to next.
		return
	}
	o.seen[who] = at

	if !heard && at.Before(o.since) {
		// The first this process hears of them, and from before it may have
		// missed anything: a process that was listening then carried it, or
		// nobody did and it is past carrying ([App.Watch]). Remembered, so
		// that the next stamp is held against this one and not against when
		// the process started.
		return
	}

	subject := who.String()
	if v, ok := o.owed[subject]; ok {
		// Still owed from a moment before this one. It stays where it is in
		// the order and is tried when it was going to be; what moves is the
		// moment, which a `DELETE` already on its way does not answer for
		// ([signOuts.answered]).
		v.at = at

		return
	}

	o.said++
	o.owed[subject] = &owing{
		subject: subject,
		at:      at,
		n:       o.said,
		wait:    carryFirst,
		done:    make(chan struct{}),
	}
	select {
	case o.woken <- struct{}{}:
	default:
	}
}

// carry tells Hydra to forget whoever it is owed, until ctx is done.
//
// One at a time, in the order they were written down, and each until Hydra
// takes it: a refusal puts that one off -- a second, doubling to half a minute
// -- and the rest are not kept waiting behind it. One worker, because what it
// is slow at is Hydra, and a second would ask the same Hydra.
//
// What is still owed when the process stops is not carried by the next one,
// which hears nothing of it until that person's row is written again; see
// [watchGrace] for what it does then.
func (o *signOuts) carry(ctx context.Context, forget func(ctx context.Context, subject string) error) {
	for {
		due, next := o.due(time.Now())
		for _, v := range due {
			err := forget(ctx, v.subject)
			if ctx.Err() != nil {
				return
			}
			o.answered(ctx, v, err)
		}
		if len(due) > 0 {
			// What was written down meanwhile, before anything is waited for.
			continue
		}

		var later <-chan time.Time
		if next > 0 {
			later = time.After(next)
		}
		select {
		case <-ctx.Done():
			return
		case <-o.woken:
		case <-later:
		}
	}
}

// due is who is to be tried now, as each stood when this was asked, in the
// order they were written down; and how long until the first of the rest is,
// or zero when nobody else is owed.
func (o *signOuts) due(now time.Time) ([]owing, time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()

	var (
		out  []owing
		next time.Duration
	)
	for _, v := range o.owed {
		if in := v.due.Sub(now); in > 0 {
			if next == 0 || in < next {
				next = in
			}

			continue
		}
		out = append(out, *v)
	}
	slices.SortFunc(out, func(a, b owing) int {
		switch {
		case a.n < b.n:
			return -1
		case a.n > b.n:
			return 1
		}

		return 0
	})

	return out, next
}

// answered is what Hydra said to being told to forget somebody, as they stood
// when it was told (`tried`).
func (o *signOuts) answered(ctx context.Context, tried owing, err error) {
	o.mu.Lock()
	v, ok := o.owed[tried.subject]
	if !ok {
		o.mu.Unlock()

		return
	}

	if err != nil {
		v.tries++
		first, in := v.tries == 1, v.wait
		v.due = time.Now().Add(in)
		v.wait = min(v.wait*2, carryAtMost)
		o.mu.Unlock()

		// Said once for each person, when it begins: a Hydra that is down for
		// an hour is one line for everybody it owes, not one each half minute.
		if first {
			slog.ErrorContext(ctx, "login: could not tell hydra to forget somebody; trying until it takes it",
				"subject", tried.subject, "err", err, "in", in)
		}

		return
	}

	if v.at.After(tried.at) {
		// A newer moment was heard while Hydra was being told of this one, and
		// whether the `DELETE` was read before or after it is nothing this can
		// know: a sign-in between the two would have survived. So Hydra is
		// told once more, at once, and whoever waits on this goes on waiting.
		v.due, v.wait = time.Time{}, carryFirst
		o.mu.Unlock()

		return
	}

	delete(o.owed, tried.subject)
	close(v.done)
	tries := v.tries
	o.mu.Unlock()

	if tries > 0 {
		slog.InfoContext(ctx, "login: hydra was told to forget somebody it had refused to",
			"subject", tried.subject, "refused", tries)
	}
}

// settle waits until Hydra is owed nothing about a subject, and is what every
// login passes before Hydra is told of it ([admin.settle]).
//
// # Why a sign-in waits for a sign-out
//
// Hydra forgets somebody by subject: every client's grant, every browser. A
// sign-in made after the sign-out is not what was signed out, and nothing told
// the two apart. Somebody signed out everywhere who signs in again at once had
// the form answered and Hydra told who while the forgetting was still on its
// way; it landed after, and took the sign-in just made -- on Hydra v26.2.0 the
// code their product was handed answers `invalid_grant`. Told to Hydra in the
// order they happened, the sign-in survives.
//
// For [settleWaits] at most, and then the sign-in goes ahead and says that it
// did: a Hydra that will not take a `DELETE` is no reason to keep somebody at a
// form, and what it costs them is signing in once more when it does.
//
// # What this does not put in order
//
// A sign-out roster has made and this app has not heard yet -- the time an
// event takes to arrive. Nothing is owed then, and nothing here can know that
// something is about to be.
//
// And a challenge Hydra raised while it still remembered the browser. The
// form is drawn on that challenge when roster no longer reads the person
// (`begin`), and Hydra v26.2.0 refuses the code of a challenge it raised
// against a browser it was told to forget before the answer came, whichever
// of the two it was told last. That sign-in wants a new challenge, which is
// the flow's to ask for and not this wait's to give: the product is answered
// `invalid_grant`, and its next flow is raised against a Hydra that has
// forgotten.
func (o *signOuts) settle(ctx context.Context, subject string) {
	o.mu.Lock()
	v, ok := o.owed[subject]
	o.mu.Unlock()
	if !ok {
		return
	}

	select {
	case <-v.done:
	case <-ctx.Done():
	case <-time.After(settleWaits):
		slog.WarnContext(ctx, "login: hydra is told of a sign-in before it took the sign-out that came first; when it does, this sign-in goes with it",
			"subject", subject, "waited", settleWaits)
	}
}

// latest is the newest of what a [rstr.SyncEvent] carries, or zero when none of
// it is set.
//
// Newest rather than any one of them, because they are four ways of having
// stopped being good and an app holding a session cares only that one of them
// happened -- and, for the comparison above, **when**.
func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	if !out.After(time.Unix(0, 0)) {
		// An unset `Timestamp` reads back as the first instant of 1970 rather
		// than as Go's zero `Time`, and `IsZero` is false for it.
		return time.Time{}
	}

	return out
}
