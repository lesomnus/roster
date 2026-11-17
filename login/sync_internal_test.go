package login

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// of is the moment the watcher reads off an event.
func of(v *rstr.SyncEvent) time.Time {
	return latest(v.GetDateInvalidated().AsTime(), v.GetDateDisabled().AsTime(), v.GetDateErased().AsTime(), v.GetDateClosed().AsTime())
}

// TestAnEventWithNothingSetIsNoMoment: an unset `Timestamp` reads back as the
// first instant of 1970, and `latest` meant to answer Go's zero `Time` for an
// event with nothing set on it by asking whether the year was one. So somebody
// back in good standing with no stamp left was a moment in 1970: never newer
// than what was held for them, so Hydra was told nothing, but written down
// where the loop says there is nothing to remember. Answered 1970; right is no
// moment at all.
func TestAnEventWithNothingSetIsNoMoment(t *testing.T) {
	x := require.New(t)

	x.True(of(rstr.SyncEvent_builder{}.Build()).IsZero(), "an event with no stamp set named a moment")

	when := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	x.Equal(when, of(rstr.SyncEvent_builder{
		DateInvalidated: timestamppb.New(when.Add(-time.Hour)),
		DateDisabled:    timestamppb.New(when),
	}.Build()), "the newest of what is set")
}

// TestASignOutHeardWhileHydraIsToldOfTheLastIsToldToo: one person is owed one
// forgetting however many moments arrive, and Hydra forgets by subject -- so
// whether a `DELETE` already on its way was read before or after a second
// sign-out is nothing this app can know. Hydra is told once more, and a
// sign-in of theirs waits for that one too rather than going ahead between
// the two.
func TestASignOutHeardWhileHydraIsToldOfTheLastIsToldToo(t *testing.T) {
	x := require.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	who := pdid.MustParse("01a1e3ea-730d-8bd1-9e02-13e12bd9722b")
	o := newSignOuts(time.Now())

	asked, answer := make(chan string, 8), make(chan error)
	go o.carry(ctx, func(_ context.Context, subject string) error {
		asked <- subject

		return <-answer
	})
	next := func(why string) {
		t.Helper()
		select {
		case got := <-asked:
			x.Equal(who.String(), got)
		case <-time.After(10 * time.Second):
			t.Fatal(why)
		}
	}

	at := time.Now()
	o.hear(who, at)
	next("hydra was never told to forget them")

	// A second sign-out while Hydra is still answering the first, and a
	// sign-in after both.
	o.hear(who, at.Add(time.Second))
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		o.settle(ctx, who.String())
	}()

	answer <- nil
	next("hydra took the first and was not told of the second")
	select {
	case <-settled:
		t.Fatal("a sign-in went ahead between a forgetting hydra took and one it is still owed")
	case <-time.After(50 * time.Millisecond):
	}

	answer <- nil
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("hydra took everything it was owed and the sign-in went on waiting")
	}

	// And the same moment said again is not news.
	o.hear(who, at.Add(time.Second))
	select {
	case <-asked:
		t.Fatal("hydra was told again of a moment it had taken")
	case <-time.After(50 * time.Millisecond):
	}
}

// ended is a roster whose stream is over as soon as it is opened, with no
// error to say why, and how often it was asked for one.
type ended struct{ asked atomic.Int32 }

func (e *ended) Watch(context.Context, *rstr.SyncWatchRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[rstr.SyncEvent], error) {
	e.asked.Add(1)

	return over{}, nil
}

type over struct{ grpc.ClientStream }

func (over) Recv() (*rstr.SyncEvent, error) { return nil, io.EOF }

// TestAStreamRosterEndedIsAskedForAgain: a stream roster closes without an
// error was read as *nothing more to hear*, and the watcher returned -- for the
// life of the process, while the listener went on answering. A stream that
// ended is a stream that ended, however politely. Asked once; right is again.
func TestAStreamRosterEndedIsAskedForAgain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	r := &ended{}
	a := &App{sync: r, outs: newSignOuts(time.Now())}
	done := make(chan error, 1)
	go func() { done <- a.watch(ctx) }()

	require.Eventually(t, func() bool { return r.asked.Load() > 1 },
		10*time.Second, 20*time.Millisecond, "a stream that ended was the last one asked for")

	cancel()
	require.NoError(t, <-done)
}
