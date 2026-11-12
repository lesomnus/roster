package reach

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
// next one to begin from the shortest wait again -- [standing]'s, so that the
// two streams an app holds behave alike.
const (
	reopenFirst  = time.Second
	reopenAtMost = 30 * time.Second
	reopenKept   = time.Minute
)

// Follow holds roster's sync stream open and drops what is kept of each holder
// it speaks of, so that the next [Holders.Reach] about them asks: a role bound
// to them, widened, or taken away -- which roster says as `SYNC_REASON_REACH`
// -- and anything else it says of them, since being suspended, erased or in a
// closed tenant is reaching nothing. It is how a change at roster reaches a
// call here at once, rather than once [standing.HeldTtl] is over.
//
// It returns once ctx is done, and not before.
//
// # What a stream promises, and what it does not
//
// [standing]'s stream, and the same promises: what changed while it was
// open, and nothing else. So opening it again forgets what was kept of
// everybody, rather than trusting answers that may have changed unheard. It
// is opened again until ctx ends, waiting longer each time it will not open.
//
// A stream roster refuses is said once, as an error, and asked for again only
// after [Config.Refused]: a role without `SyncService/Watch`, or a roster with
// no broker -- the one an app runs in its own process, which has no stream and
// needs none, since every change to its roles is the app's own and it calls
// [Holders.Recheck]. Nothing stops working meanwhile: what roster said is still
// asked again within [standing.HeldTtl].
func (h *Holders) Follow(ctx context.Context) error {
	wait := reopenFirst
	for {
		began := time.Now()
		err := h.follow(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(began) > reopenKept {
			wait = reopenFirst
		}

		pause := wait
		switch status.Code(err) {
		case codes.PermissionDenied:
			h.log(ctx).ErrorContext(ctx, "reach: roster refuses this app its sync stream. "+
				"The role it is installed with wants /roster.SyncService/Watch, which `roster role patch` adds "+
				"and installing again does not. Until then a change to what somebody may call is heard when they are next asked about",
				slog.Any("err", err))
			pause = h.refused
		case codes.Unimplemented:
			h.log(ctx).ErrorContext(ctx, "reach: this roster publishes no changes (its watch.broker), so there is no stream to hold. "+
				"A change to what somebody may call is heard when they are next asked about",
				slog.Any("err", err))
			pause = h.refused
		default:
			h.log(ctx).WarnContext(ctx, "reach: roster's sync stream ended; opening it again", "in", wait.String(), "err", fmt.Sprint(err))
			wait = min(wait*2, reopenAtMost)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pause):
		}
	}
}

// Follower is [Holders.Follow] as a loop for payday's `spin.Run`, called
// `name` -- the app's, and prefixed with it as payday asks (`loft.roster-reach`).
//
// Mark it to run in every process, with `spin.InEveryProcess`, for
// [standing.Accounts.Follower]'s reason: what the stream drops is what **this**
// process kept, and a stream held by the one process that holds the lease
// leaves every other replica answering from what it was told, for up to
// [standing.HeldTtl], after the role was taken away.
func (h *Holders) Follower(name string) spin.Spinner {
	return spin.Called(name, spin.Func(func(ctx context.Context) error {
		return h.Follow(ctx)
	}))
}

// follow is one stream, from opening to the error that ended it.
func (h *Holders) follow(ctx context.Context) error {
	as, err := h.as(ctx)
	if err != nil {
		return err
	}
	stream, err := rstr.NewSyncServiceClient(h.conn).Watch(as, rstr.SyncWatchRequest_builder{}.Build())
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	// Whatever was kept before this opened may have changed unheard.
	h.mu.Lock()
	clear(h.known)
	h.dropped++
	h.mu.Unlock()

	for {
		ev, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		k, err := pdid.From(ev.GetHolder())
		if err != nil {
			h.log(ctx).WarnContext(ctx, "reach: a sync event about nobody this reads", slog.Any("err", err))
			continue
		}
		h.Recheck(k)
	}
}
