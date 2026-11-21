package standing

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"

	rstr "github.com/lesomnus/roster/rstr"
)

// How long a stream that ended is waited for before it is opened again, the
// most that wait grows to, and how long a stream has to have been open for the
// next one to begin from the shortest wait again.
const (
	reopenFirst  = time.Second
	reopenAtMost = 30 * time.Second
	reopenKept   = time.Minute
)

// Follow holds roster's sync stream open and hands `on` each account it speaks
// of, with what roster says of it now: signed out everywhere, suspended,
// erased, a directory closed, or none of them any more (roster's `SyncService`).
// It is how something already open ends when roster ends it, rather than at
// the next time it is held to roster, and how an app hears an account leave.
//
// Each account it hears of is dropped from what this process knows as the
// event is read ([Accounts.Recheck]), so the next [Accounts.Held] asks. `on`
// may be nil, and the stream then does only that. What `on` answers with is
// said and does not stop the stream: the next time the account is held to
// roster it is asked anyway, and the next event about it is the whole truth.
//
// `on` runs beside the stream and not in the loop that reads it: in one
// goroutine of the follower's, one call at a time, never for two accounts at
// once, and with ctx -- the follower's own. While it is busy, what the stream
// says of others is still dropped as it is read, and waits for `on`; and what
// it says of an account again replaces what waited, since it is state and not
// a history: `on` is handed the latest [Standing] heard of each account. Run
// in the loop, one slow call held every later drop, and sixty-four events
// behind roster cuts the stream. When ctx ends, what was not handed on is let
// go, and Follow returns once `on` has.
//
// An event about what somebody may call (`SYNC_REASON_REACH`) is passed by:
// it is `reach`'s, and none of the four facts on it is news.
//
// It returns once ctx is done, and not before.
//
// # What a stream promises, and what it does not
//
// What changed while it was open, and nothing else: it replays nothing, and an
// event carries no position to begin again from. So a stream that ended is one
// that missed things, and opening it again forgets what was known of everybody
// rather than trusting answers that may have changed unheard -- forgotten when
// roster says the stream listens, which is from when a write is heard -- and what the
// app marked from what it heard is put right by [Accounts.Reconcile], which is
// how a restart is met too. It is opened again until ctx ends, waiting longer
// each time it will not open.
//
// A stream roster refuses is said once, as an error, in so many words, and
// asked for again only after [Config.Refused]: no retry changes a role without
// `SyncService/Watch` in it, or a roster with no broker to publish from -- the
// one an app runs in its own process, which has no stream and needs none,
// since every change to its people is the app's own. Nothing stops working
// meanwhile: [Accounts.Held] still asks within [HeldTtl]. But an erased account
// is not heard leaving until a reconcile finds it.
func (a *Accounts) Follow(ctx context.Context, on func(ctx context.Context, v Standing) error) error {
	var next *handing
	if on != nil {
		next = &handing{latest: map[pdid.Id]Standing{}, ready: make(chan struct{}, 1)}
		done := make(chan struct{})
		go func() {
			defer close(done)
			next.run(ctx, on, a.log(ctx))
		}()
		defer func() { <-done }()
	}

	wait := reopenFirst
	for {
		began := time.Now()
		err := a.follow(ctx, next)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(began) > reopenKept {
			wait = reopenFirst
		}

		pause := wait
		switch status.Code(err) {
		case codes.PermissionDenied:
			a.log(ctx).ErrorContext(ctx, "standing: roster refuses this app its sync stream. "+
				"The role it is installed with wants /roster.SyncService/Watch, which `roster role patch` adds "+
				"and installing again does not. Until then what roster says of an account is heard when the account is next held to it, "+
				"and an erased account is not heard leaving", slog.Any("err", err))
			pause = a.refused
		case codes.Unimplemented:
			a.log(ctx).ErrorContext(ctx, "standing: this roster publishes no changes (its watch.broker), so there is no stream to hold. "+
				"What roster says of an account is heard when the account is next held to it, "+
				"and an erased account is not heard leaving", slog.Any("err", err))
			pause = a.refused
		default:
			a.log(ctx).WarnContext(ctx, "standing: roster's sync stream ended; opening it again", "in", wait.String(), "err", fmt.Sprint(err))
			wait = min(wait*2, reopenAtMost)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pause):
		}
	}
}

// Follower is [Accounts.Follow] as a loop for payday's `spin.Run`, called
// `name` -- the app's, and prefixed with it as payday asks
// (`loft.roster-stream`).
//
// Mark it to run in every process, with `spin.InEveryProcess`. What the stream
// says drops what **this** process knows of an account, so a stream held by the
// one process that holds the deployment's lease leaves every other process
// believing what it was told for up to [HeldTtl] -- which is a session that
// outlives a suspension, in all but one replica. Left unmarked under a lease,
// it is that, and nothing says so.
//
// The cost is `on`, which every process then runs for every account the stream
// speaks of: what it does
// has to be safe to do once per process, as writing down state is -- a session
// ended twice is ended, an account marked twice is marked.
func (a *Accounts) Follower(name string, on func(ctx context.Context, v Standing) error) spin.Spinner {
	return spin.Called(name, spin.Func(func(ctx context.Context) error {
		return a.Follow(ctx, on)
	}))
}

// follow is one stream, from opening to the error that ended it.
func (a *Accounts) follow(ctx context.Context, next *handing) error {
	as, err := a.as(ctx)
	if err != nil {
		return err
	}
	stream, err := rstr.NewSyncServiceClient(a.conn).Watch(as, rstr.SyncWatchRequest_builder{}.Build())
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	// roster sends the stream's header once it has subscribed it, and nothing
	// with it: a write made from then on is heard here. So whatever was known
	// is forgotten then, and not when Watch returned, which is before roster
	// has subscribed anything -- an answer asked for in between was believed
	// for HeldTtl over a write made in between, which nothing heard. A stream
	// whose header does not come is one that failed, and Recv says how.
	if md, err := stream.Header(); err != nil || md == nil {
		if err == nil {
			_, err = stream.Recv()
		}
		return fmt.Errorf("roster: %w", err)
	}
	a.mu.Lock()
	clear(a.held)
	a.dropped++
	a.mu.Unlock()

	for {
		ev, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		if len(ev.GetHolder()) == 0 {
			// About nobody, which roster sends none of: it is kept for a sign
			// of life on a stream that is quiet, and a follower of this
			// release does not complain of one.
			continue
		}
		if ev.GetReason() == rstr.SyncReason_SYNC_REASON_REACH {
			// About what they may call, which `reach` keeps and this does
			// not. None of the facts on it is news (`sync.proto`), so nothing
			// known is dropped for it and `on` is not handed it -- once for
			// every person a role edit reaches.
			continue
		}
		st, err := standingOf(ev)
		if err != nil {
			a.log(ctx).WarnContext(ctx, "standing: a sync event about nobody this reads", slog.Any("err", err))
			continue
		}
		a.Recheck(st.Holder)
		if next != nil {
			next.put(st)
		}
	}
}

// handing is what the stream said and `on` has not been handed yet: the latest
// of each account, in the order they were first heard.
type handing struct {
	mu     sync.Mutex
	latest map[pdid.Id]Standing
	order  []pdid.Id

	// ready says there is something to hand on, and holds one word at most.
	ready chan struct{}
}

// put keeps what the stream said of an account for `on`, over anything said
// of them before that is still waiting.
func (h *handing) put(v Standing) {
	h.mu.Lock()
	if _, ok := h.latest[v.Holder]; !ok {
		h.order = append(h.order, v.Holder)
	}
	h.latest[v.Holder] = v
	h.mu.Unlock()

	select {
	case h.ready <- struct{}{}:
	default:
	}
}

func (h *handing) take() (Standing, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.order) == 0 {
		return Standing{}, false
	}
	k := h.order[0]
	h.order = h.order[1:]
	v := h.latest[k]
	delete(h.latest, k)

	return v, true
}

// run hands `on` what waits, one at a time, until ctx ends.
func (h *handing) run(ctx context.Context, on func(ctx context.Context, v Standing) error, logger *slog.Logger) {
	for {
		for {
			if ctx.Err() != nil {
				return
			}
			v, ok := h.take()
			if !ok {
				break
			}
			if err := on(ctx, v); err != nil {
				logger.WarnContext(ctx, "standing: a sync event was not carried out here", "holder", v.Holder.String(), slog.Any("err", err))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-h.ready:
		}
	}
}

// standingOf is a sync event as the four facts it carries. Its reason is left
// behind: it is commentary on them, and best effort (`proto/app/sync.proto`).
func standingOf(ev *rstr.SyncEvent) (Standing, error) {
	h, err := pdid.From(ev.GetHolder())
	if err != nil {
		return Standing{}, err
	}
	t, _ := pdid.From(ev.GetTenant())

	return Standing{
		Holder:      h,
		Tenant:      t,
		Invalidated: at(ev.GetDateInvalidated()),
		Disabled:    at(ev.GetDateDisabled()),
		Erased:      at(ev.GetDateErased()),
		Closed:      at(ev.GetDateClosed()),
	}, nil
}
