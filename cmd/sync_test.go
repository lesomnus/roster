package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"

	app "github.com/lesomnus/roster/rstr"
	rostersync "github.com/lesomnus/roster/server/sync"
)

// The sync channel, which item 4 has carried from the beginning and D26 named
// the first subject of.
//
// `cmd/watch_test.go` runs the increment underneath this one -- that the two
// facts travel on the entity stream at all -- and closes by saying the event
// stream is still deferred. It is not any more, and the reason it stopped being
// a deferral is the sentence that justified it: *a `Holder` changes for reasons
// nobody needs to hear about.* An app given the entity watch has to be told
// about every rename in the deployment to hear about one suspension, and has to
// name in advance whose renames -- the generated `Watch` refuses a subscription
// with no filters -- which an app cannot do about people who have not signed in
// yet.
//
// Every write below goes **over the wire**, and that is not incidental: what
// publishes to the broker is the interceptor on the served stack, so a direct
// call through `Ungated` writes the row and tells nobody.

// syncing opens the stream and waits until it is really subscribed.
//
// There is no snapshot -- `sync.proto` argues that at length -- so nothing
// arrives to say the server has got as far as subscribing, and a write made
// before it does is a write nobody hears. So this pokes somebody nobody else in
// the test cares about until something about them comes back.
//
// `Invalidate` as the poke, because it moves a stamp **forward every time**: a
// poke that set a flag would be a no-op on the second attempt and this would
// hang waiting for an event its own idempotence had suppressed.
func syncing(t *testing.T, ctx context.Context, conn *grpc.ClientConn, canary pdid.Id) <-chan *app.SyncEvent {
	t.Helper()
	x := require.New(t)

	out, err := app.NewSyncServiceClient(conn).Watch(ctx, app.SyncWatchRequest_builder{}.Build())
	x.NoError(err)

	c := make(chan *app.SyncEvent, 32)
	go func() {
		defer close(c)
		for {
			v, err := out.Recv()
			if err != nil {
				return
			}

			c <- v
		}
	}()

	h := app.NewHolderServiceClient(conn)
	ref := app.HolderRef_builder{Id: canary.Bytes()}.Build()

	for range 100 {
		_, err := h.Invalidate(ctx, app.HolderInvalidateRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		select {
		case v, ok := <-c:
			if !ok {
				t.Fatal("the stream ended before it began")
			}

			x.Equal(canary.Bytes(), v.GetHolder(), "somebody else's event arrived first")

			return c

		case <-time.After(50 * time.Millisecond):
		}
	}

	t.Fatal("the stream never subscribed")

	return nil
}

// about reads until something about this person arrives, or fails rather than
// hanging.
//
// Filtered by holder because the stream is not: there is no field to say whose
// events to send -- `SyncWatchRequest` is empty on purpose -- so the canary
// that woke it shares the channel with whoever else the test touches.
func about(t *testing.T, c <-chan *app.SyncEvent, who pdid.Id) *app.SyncEvent {
	t.Helper()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case v, ok := <-c:
			if !ok {
				t.Fatal("the stream ended")
			}
			if string(v.GetHolder()) != string(who.Bytes()) {
				continue
			}

			return v

		case <-deadline:
			t.Fatal("nothing arrived")

			return nil
		}
	}
}

// TestAnAppHearsThatADecisionStoppedBeingGood.
//
// An app that has signed somebody in holds a copy of a decision. It is true
// when it is made, and roster is where it stops being true. Until this there
// was no way for the app to hear about it -- which meant a suspension took
// effect at roster immediately and at every app that had already asked whenever
// their session happened to expire.
func TestAnAppHearsThatADecisionStoppedBeingGood(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Cancelled rather than left to the test's own context, so the goroutine
	// reading the stream ends with the test rather than after it.
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)

	// Somebody other than the caller, for the reason `watch_test.go` writes
	// down: disabling the caller answers `Unauthenticated` to the call after
	// it, which is D26 working and is not what this is about.
	who := b.holder(t, ctx, b.Contoso, "watched")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	c := syncing(t, as, conn, b.holder(t, ctx, b.Contoso, "canary"))
	h := app.NewHolderServiceClient(conn)

	_, err := h.Disable(as, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)

	v := about(t, c, who)
	x.Equal(b.Contoso.Bytes(), v.GetTenant(), "an app serving several tenants was not told which")
	x.NotNil(v.GetDateDisabled(), "the fact itself was left off the event")

	t.Run("and that it may stop refusing them", func(t *testing.T) {
		x := require.New(t)

		_, err := h.Enable(as, app.HolderEnableRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		v := about(t, c, who)
		x.Equal(app.SyncReason_SYNC_REASON_REINSTATED, v.GetReason(),
			"an app that dropped their sessions has no other way to learn this")
		x.Nil(v.GetDateDisabled())
	})

	t.Run("and when everything issued before now went void", func(t *testing.T) {
		x := require.New(t)

		res, err := h.Invalidate(as, app.HolderInvalidateRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		v := about(t, c, who)
		x.Equal(app.SyncReason_SYNC_REASON_INVALIDATED, v.GetReason())

		// The instant itself and not merely a notification: everything issued
		// before it is void and a credential minted after it is not, so an app
		// compares rather than drops.
		x.Equal(res.GetDateInvalidated().AsTime(), v.GetDateInvalidated().AsTime())
	})

	t.Run("and when they are gone", func(t *testing.T) {
		x := require.New(t)

		_, err := h.Erase(as, ref)
		x.NoError(err)

		v := about(t, c, who)
		x.Equal(app.SyncReason_SYNC_REASON_ERASED, v.GetReason())
		x.NotNil(v.GetDateErased(), "an app was told they are gone without being told since when")
		x.Equal(b.Contoso.Bytes(), v.GetTenant(), "and without being told whose they were")
	})
}

// TestNobodyIsWokenForARename, which is the whole reason this is not the entity
// watch.
//
// `Holder` declares `watch: {}` and every fact this stream carries is a column
// on that row, so an app could watch holders and read the three columns itself.
// Item 4 says why not in one sentence: *a `Holder` changes for reasons nobody
// needs to hear about.*
func TestNobodyIsWokenForARename(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)

	who := b.holder(t, ctx, b.Contoso, "renamed")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	c := syncing(t, as, conn, b.holder(t, ctx, b.Contoso, "canary"))
	h := app.NewHolderServiceClient(conn)

	// Two writes that mean nothing to a session, then one that means
	// everything. A stream that woke for the first two would deliver them
	// before the third, and this asserts the third arrives **first** -- which
	// it can only do if the first two sent nothing at all.
	for _, name := range []string{"one", "two"} {
		was, err := h.Get(as, app.HolderGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		_, err = h.Update(as, app.HolderUpdateRequest_builder{
			Ref:         ref,
			Profile:     app.Profile_builder{DisplayName: name}.Build(),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.NoError(err)
	}

	_, err := h.Disable(as, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)

	v := about(t, c, who)
	x.Equal(app.SyncReason_SYNC_REASON_SUSPENDED, v.GetReason(),
		"a rename woke an app that had nothing to do about it")
}

// TestOneCustomerIsNotToldAboutAnother.
//
// The stream takes no argument saying whose events to send, so the **only**
// thing deciding what an app hears is the wall -- `Service` is handed `Walled`
// and reads each person through it, exactly as a `Get` would. A stream is the
// easiest place in a codebase to leak a whole table, because the narrowing that
// is obvious on a read is a loop somewhere else here.
func TestOneCustomerIsNotToldAboutAnother(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)

	// Somebody who may write in fabrikam, so that the writes below are real writes
	// and not refusals this test would pass for the wrong reason.
	other := b.holder(t, ctx, b.Fabrikam, "theirs")
	b.mayAnything(other, b.Fabrikam)
	theirs := asOverTheWire(ctx, other)

	c := syncing(t, as, conn, b.holder(t, ctx, b.Contoso, "canary"))
	h := app.NewHolderServiceClient(conn)

	_, err := h.Disable(theirs, app.HolderDisableRequest_builder{
		Ref: app.HolderRef_builder{Id: other.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// And then one in the caller's own tenant, after it. The contoso event
	// arriving is what makes the fabrikam one's absence a fact rather than a race:
	// the write that would have produced it landed first, so if it were coming
	// it would already be here.
	mine := b.holder(t, ctx, b.Contoso, "mine")
	_, err = h.Disable(as, app.HolderDisableRequest_builder{
		Ref: app.HolderRef_builder{Id: mine.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	for {
		v := about2(t, c)
		if string(v.GetHolder()) == string(mine.Bytes()) {
			break
		}

		x.NotEqual(other.Bytes(), v.GetHolder(),
			"an app was told about somebody in a tenant it cannot read")
	}
}

// about2 reads whatever arrives next, whoever it is about.
func about2(t *testing.T, c <-chan *app.SyncEvent) *app.SyncEvent {
	t.Helper()

	select {
	case v, ok := <-c:
		if !ok {
			t.Fatal("the stream ended")
		}

		return v

	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")

		return nil
	}
}

// TestADeploymentWithNoBrokerSaysSoRatherThanGoingQuiet.
//
// The one failure a client cannot tell from a healthy system: a stream that
// opens, sends nothing, and stays open forever. `watch.Stream` decides before
// it reads anything, which is what makes the refusal reach the caller.
func TestADeploymentWithNoBrokerSaysSoRatherThanGoingQuiet(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Handed no broker at all, which every other harness here does give -- so
	// this is the one place the absence is arranged on purpose.
	s := rostersync.Service(b.Walled, nil)

	err := s.Watch(app.SyncWatchRequest_builder{}.Build(), &collector{ctx: ctx})
	x.ErrorIs(err, watch.ErrNoBroker, "a stream opened on a deployment that can never feed it")
}

// collector is a server stream that keeps what it was sent.
//
// Hand-written for the same reason `collected` in `watch_test.go` is: this one
// is called directly rather than over a listener, and there is no
// `ServerStream` to get from a connection that was never made.
type collector struct {
	grpc.ServerStream

	ctx context.Context
	vs  []*app.SyncEvent
}

func (c *collector) Context() context.Context { return c.ctx }

func (c *collector) Send(v *app.SyncEvent) error {
	c.vs = append(c.vs, v)

	return nil
}

// TestTheSyncStreamAnswersToRealKeys is the stream under the credentials a
// deployment actually serves, which every other sync test stands in for with
// `auth.Plain`.
//
// The two claims are the two kinds. A deployment key belongs to every tenant,
// so the one stream is the whole deployment's changes -- which is the shape an
// SSO app in front of many customers needs, and which nothing asserted. A
// tenant key is somebody inside one, so the same argument-less stream narrows
// to their tenant by the wall alone -- the claim
// `TestOneCustomerIsNotToldAboutAnother` makes under Plain, re-asked with the
// resolver and the key store in the path.
func TestTheSyncStreamAnswersToRealKeys(t *testing.T) {
	const (
		watchSync  = "/roster.SyncService/Watch"
		invalidate = "/roster.HolderService/Invalidate"
		suspend    = "/roster.HolderService/Disable"
	)

	x := require.New(t)
	b := keyFor(t, watchSync, invalidate, suspend)
	ctx := t.Context()

	fabrikam := add(t, ctx, b.Server, "fabrikam")

	// Over the wire, as every sync test writes: an `Ungated` Go call slips
	// under the watch interceptor and publishes nothing, which is not a
	// shortcut but a different (and eventless) write.
	disable := func(who pdid.Id) {
		t.Helper()

		_, err := app.NewHolderServiceClient(b.Conn).Disable(bearing(ctx, b.Token),
			app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
		x.NoError(err)
	}

	t.Run("a deployment key hears every tenant", func(t *testing.T) {
		x := require.New(t)

		ctx, stop := context.WithCancel(ctx)
		defer stop()

		c := syncing(t, bearing(ctx, b.Token), b.Conn, addHolder(t, ctx, b.Server, b.Contoso, "canary"))

		victim := addHolder(t, ctx, b.Server, fabrikam, "victim")
		disable(victim)

		for {
			v := about2(t, c)
			if string(v.GetHolder()) == string(victim.Bytes()) {
				x.NotNil(v.GetDateDisabled(), "the event carries the state, not a signal")

				return
			}
		}
	})

	t.Run("a tenant key hears its own and no other", func(t *testing.T) {
		x := require.New(t)

		ctx, stop := context.WithCancel(ctx)
		defer stop()

		permits(t, ctx, b, b.Contoso, b.Who, "sync-reader", watchSync, invalidate)
		key := mintFor(t, ctx, b, b.Who, "their-app", []string{watchSync, invalidate}, time.Time{})

		c := syncing(t, bearing(ctx, key), b.Conn, addHolder(t, ctx, b.Server, b.Contoso, "canary-2"))

		// Theirs first, then ours: the ordering is what makes the absence
		// below a fact rather than a race, as in the Plain variant.
		theirs := addHolder(t, ctx, b.Server, fabrikam, "victim-2")
		disable(theirs)

		mine := addHolder(t, ctx, b.Server, b.Contoso, "mine")
		disable(mine)

		for {
			v := about2(t, c)
			x.NotEqual(string(theirs.Bytes()), string(v.GetHolder()),
				"a tenant key heard another customer's suspension")
			if string(v.GetHolder()) == string(mine.Bytes()) {
				return
			}
		}
	})
}
