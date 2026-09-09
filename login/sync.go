package login

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
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
// of those into `DELETE /admin/oauth2/auth/sessions/login` is a fact about
// Hydra, so it lives in the package that already knows about Hydra.
//
// Without it there is a real hole and it is quiet: an operator signs somebody
// out everywhere, roster's own credentials stop working, and Hydra goes on
// remembering them -- so the next product they open gets a fresh token with no
// form in between.
//
// # One stream per operator, and no filter
//
// `SyncWatchRequest` is empty on purpose: what an app hears is narrowed by the
// **wall**, exactly as a read is. This app holds one `rt_` per operator, so it
// opens one stream per operator and each hears that tenant and no other. There
// is nothing to filter and nothing that could be filtered wrong.
//
// # What a reconnect means
//
// The stream replays nothing -- "a stream that ended is a stream that missed
// things" -- so what this holds is per connection: the last moment it acted on,
// per person, dropped when the stream is. A reconnect therefore revokes once
// more for anybody it hears about again, which is the safe direction: the call
// is idempotent, and the alternative is a session roster has voided and Hydra
// still honours.
//
// A deployment with no broker cannot serve this at all and says so rather than
// opening a stream that carries nothing. That is **logged and not fatal**: an
// app that refused to sign anybody in because it could not sign anybody out is
// worse than one that says so loudly and goes on working.
func (a *App) Watch(ctx context.Context) error {
	if len(a.operators) == 0 {
		return nil
	}

	g, ctx := errgroup.WithContext(ctx)
	for _, o := range a.operators {
		g.Go(func() error { return a.watch(ctx, o) })
	}

	return g.Wait()
}

// watchBackoff is how long a dropped stream waits, and how long it waits at
// most. Doubling between them.
const (
	watchBackoff = 1 * time.Second
	watchAtMost  = 30 * time.Second
)

func (a *App) watch(ctx context.Context, o *operator) error {
	log := slog.With("operator", o.alias)
	wait := watchBackoff

	for ctx.Err() == nil {
		err := a.stream(ctx, o)
		switch {
		case err == nil, ctx.Err() != nil:
			// A stream that ended because this app is stopping is not a stream
			// that dropped, and saying so at every shutdown is how a log stops
			// being read.
			return nil

		case status.Code(err) == codes.Unimplemented,
			status.Code(err) == codes.PermissionDenied:
			// Not something waiting fixes: a deployment with no broker, or a
			// key whose role does not name this. Loudly, once, and this app
			// goes on signing people in -- what it cannot do is sign them out
			// when roster says to, which an operator has to know.
			log.ErrorContext(ctx, "login: roster will not say when somebody is signed out; hydra will keep remembering them", "err", err)

			return nil
		}

		log.WarnContext(ctx, "login: the sync stream dropped", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, watchAtMost)
	}

	return nil
}

// stream is one connection, and what it remembers lives exactly as long.
func (a *App) stream(ctx context.Context, o *operator) error {
	s, err := a.sync.Watch(withKey(ctx, o.key), rstr.SyncWatchRequest_builder{}.Build())
	if err != nil {
		return err
	}

	// The last moment acted on, per person. What the stream sends is **state
	// and not a delta**, and `date_invalidated` is monotonic and never cleared
	// -- so without this, every later event about somebody would revoke again,
	// including the one that says they are back in good standing, which would
	// sign them out the moment after they signed in.
	seen := map[pdid.Id]time.Time{}

	for {
		v, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		who, err := pdid.From(v.GetHolder())
		if err != nil {
			continue
		}

		at := latest(v.GetDateInvalidated().AsTime(), v.GetDateDisabled().AsTime(), v.GetDateErased().AsTime())
		if at.IsZero() {
			// Nothing is set: somebody in good standing, including somebody
			// who **was** suspended and is not any more. Nothing to revoke, and
			// nothing to remember either -- if they are suspended again that
			// is a later moment and it will be newer than what is here.
			continue
		}
		if !at.After(seen[who]) {
			continue
		}
		seen[who] = at

		if err := a.admin.revokeSessions(ctx, who.String()); err != nil {
			// Left in `seen` on purpose: retrying this one on the next event
			// about them would be a retry at an arbitrary later time, and the
			// reconnect above is the retry this design has. Logged so it is not
			// silent.
			slog.ErrorContext(ctx, "login: could not tell hydra to forget somebody",
				"operator", o.alias, "subject", who, "err", err)
		}
	}
}

// latest is the newest of what a [rstr.SyncEvent] carries, or zero when none of
// it is set.
//
// Newest rather than any one of them, because they are three ways of having
// stopped being good and an app holding a session cares only that one of them
// happened -- and, for the comparison above, **when**.
func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	if out.Year() <= 1 {
		// An unset `Timestamp` reads back as the zero instant rather than as
		// Go's zero `Time`, and `IsZero` is false for it.
		return time.Time{}
	}

	return out
}
