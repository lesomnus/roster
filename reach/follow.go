package reach

import (
	"context"
	"errors"
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
// everybody, rather than trusting answers that may have changed unheard --
// once roster says the stream listens, which is from when a write is heard. It
// is opened again until ctx ends, waiting longer each time it will not open.
//
// A stream roster refuses is said once, as an error, and asked for again only
// after [Config.Refused]: a role without `SyncService/Watch`, or a roster with
// no broker -- the one an app runs in its own process, which has no stream and
// needs none, since every change to its roles is the app's own and it calls
// [Holders.Recheck]. Nothing stops working meanwhile: what roster said is still
// asked again within [standing.HeldTtl].
//
// What it says in its log is what [standing.Accounts.Follow] says: a stream
// that listens is a line at the level of information, each time; an ending or
// a refusal is said once, with roster's own words beside the package's, and
// not again until a stream has listened in between; a refusal of the stream
// is either the app's role or the key's own list of methods, and an
// [Config.As] that failed is said as the app not being able to be somebody at
// roster.
func (h *Holders) Follow(ctx context.Context) error {
	wait := reopenFirst
	// How the last stream ended, as it was said, and whether one has ever
	// listened: an ending is said once, and the same one again is no line
	// until a stream has listened in between.
	said, ever := "", false
	listened := func() {
		if ever {
			h.log(ctx).InfoContext(ctx, "reach: listening to roster's sync stream again; what was kept before it is forgotten")
		} else {
			h.log(ctx).InfoContext(ctx, "reach: listening to roster's sync stream")
		}
		ever, said = true, ""
	}
	for {
		began := time.Now()
		err := h.follow(ctx, listened)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(began) > reopenKept {
			wait = reopenFirst
		}

		code := status.Code(err)
		var self *notSomebody
		mine := errors.As(err, &self)
		ending := fmt.Sprint(mine, code)
		say := ending != said
		said = ending

		pause, level := wait, slog.LevelWarn
		if code == codes.PermissionDenied || code == codes.Unimplemented {
			pause, level = h.refused, slog.LevelError
		} else {
			wait = min(wait*2, reopenAtMost)
		}
		if !say {
			// Said already, and nothing listened since.
		} else if mine {
			h.log(ctx).Log(ctx, level, "reach: this app could not be somebody at roster, so its sync stream was not asked for; "+
				"it is asked for again", "in", pause.String(), "err", fmt.Sprint(self.err))
		} else if code == codes.PermissionDenied {
			h.log(ctx).Log(ctx, level, "reach: roster refuses this app its sync stream. "+
				"Either the role it is installed with does not hold /roster.SyncService/Watch, which `roster role patch` adds "+
				"and installing again does not, or the key's own list of methods leaves it out, which a key minted with it mends. "+
				"Until then a change to what somebody may call is heard when they are next asked about",
				slog.Any("err", err))
		} else if code == codes.Unimplemented {
			h.log(ctx).Log(ctx, level, "reach: this roster publishes no changes (its watch.broker), so there is no stream to hold. "+
				"A change to what somebody may call is heard when they are next asked about",
				slog.Any("err", err))
		} else {
			h.log(ctx).Log(ctx, level, "reach: roster's sync stream ended; opening it again", "in", pause.String(), "err", fmt.Sprint(err))
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
// It comes marked to run in every process (`spin.InEveryProcess`), and an app
// that marks it again changes nothing, for [standing.Accounts.Follower]'s
// reason: what the stream drops is what **this** process kept, and a stream
// held by the one process that holds the lease would leave every other replica
// answering from what it was told, for up to [standing.HeldTtl], after the
// role was taken away.
func (h *Holders) Follower(name string) spin.Spinner {
	// Named outside the mark, so that the loop handed back still says its
	// name; payday finds the mark beneath it.
	return spin.Called(name, spin.InEveryProcess(spin.Func(func(ctx context.Context) error {
		return h.Follow(ctx)
	})))
}

// follow is one stream, from opening to the error that ended it.
func (h *Holders) follow(ctx context.Context, listened func()) error {
	as, err := h.as(ctx)
	if err != nil {
		return &notSomebody{err}
	}
	stream, err := rstr.NewSyncServiceClient(h.conn).Watch(as, rstr.SyncWatchRequest_builder{}.Build())
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	// Whatever was kept may have changed unheard, and is forgotten when roster
	// says it has subscribed the stream -- its header, sent with nothing else
	// -- and not when Watch returned, which is before: an answer asked for in
	// between was kept over a write made in between, which nothing heard. A
	// stream whose header does not come is one that failed, and Recv says how.
	if md, err := stream.Header(); err != nil || md == nil {
		if err == nil {
			_, err = stream.Recv()
		}
		return fmt.Errorf("roster: %w", err)
	}
	h.mu.Lock()
	clear(h.known)
	h.dropped++
	h.mu.Unlock()
	listened()

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
		k, err := pdid.From(ev.GetHolder())
		if err != nil {
			h.log(ctx).WarnContext(ctx, "reach: a sync event about nobody this reads", slog.Any("err", err))
			continue
		}
		h.Recheck(k)
	}
}

// notSomebody is [Config.As] failing, so that the stream was not asked for:
// said as that, and not as roster refusing the stream.
type notSomebody struct{ err error }

func (e *notSomebody) Error() string { return e.err.Error() }
func (e *notSomebody) Unwrap() error { return e.err }
