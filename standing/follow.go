package standing

import (
	"context"
	"fmt"
	"log/slog"
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
// Each account it hears of is dropped from what this process knows first
// ([Accounts.Recheck]), so the next [Accounts.Held] asks. `on` may be nil, and
// the stream then does only that. What `on` answers with is said and does not
// stop the stream: the next time the account is held to roster it is asked
// anyway, and the next event about it is the whole truth.
//
// It returns once ctx is done, and not before.
//
// # What a stream promises, and what it does not
//
// What changed while it was open, and nothing else: it replays nothing, and an
// event carries no position to begin again from. So a stream that ended is one
// that missed things, and opening it again forgets what was known of everybody
// rather than trusting answers that may have changed unheard -- and what the
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
	wait := reopenFirst
	for {
		began := time.Now()
		err := a.follow(ctx, on)
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
// The cost is `on`, which every process then runs for every event: what it does
// has to be safe to do once per process, as writing down state is -- a session
// ended twice is ended, an account marked twice is marked.
func (a *Accounts) Follower(name string, on func(ctx context.Context, v Standing) error) spin.Spinner {
	return spin.Called(name, spin.Func(func(ctx context.Context) error {
		return a.Follow(ctx, on)
	}))
}

// follow is one stream, from opening to the error that ended it.
func (a *Accounts) follow(ctx context.Context, on func(ctx context.Context, v Standing) error) error {
	as, err := a.as(ctx)
	if err != nil {
		return err
	}
	stream, err := rstr.NewSyncServiceClient(a.conn).Watch(as, rstr.SyncWatchRequest_builder{}.Build())
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	// Whatever was known before this opened may have changed unheard.
	a.mu.Lock()
	clear(a.held)
	a.dropped++
	a.mu.Unlock()

	for {
		ev, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		st, err := standingOf(ev)
		if err != nil {
			a.log(ctx).WarnContext(ctx, "standing: a sync event about nobody this reads", slog.Any("err", err))
			continue
		}
		a.Recheck(st.Holder)
		if on == nil {
			continue
		}
		if err := on(ctx, st); err != nil {
			a.log(ctx).WarnContext(ctx, "standing: a sync event was not carried out here", "holder", st.Holder.String(), slog.Any("err", err))
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
