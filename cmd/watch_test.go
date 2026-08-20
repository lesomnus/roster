package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// The sync channel, at the increment that came free.
//
// PLAN.md D26 says it: `Holder` already declared `watch: {}`, justified in
// `holder.proto` because *the one fact about somebody that has to travel is
// that they are gone* -- and two more facts that have to travel were put on the
// same row, so they arrive on a stream that already exists.
//
// That was **asserted and never run**. What follows runs it, because "it comes
// free" is exactly the kind of claim that is true about the schema and false
// about the wire: a column can be added, be correct in the database, be
// answered by `Get`, and still not be on the stream -- and nothing would say
// so until an app that trusted the stream kept trusting a signed-out session.
//
// The event stream item 4 argues for is still a second increment, and is still
// deferred: it is *taken when the noise is measured rather than predicted*, and
// nothing has run long enough to measure any.

// watching opens a Watch and answers with what it sends, one message at a time.
//
// Served over a real connection, since a stream is the one thing a direct call
// cannot travel: there is no `ServerStream` to hand a handler.
func watching(t *testing.T, ctx context.Context, conn *grpc.ClientConn, req *app.HolderWatchRequest) <-chan *app.HolderWatchResponse {
	t.Helper()

	out, err := app.NewHolderServiceClient(conn).Watch(ctx, req)
	require.NoError(t, err)

	c := make(chan *app.HolderWatchResponse, 8)
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

	return c
}

// arrives reads one message, or fails rather than hanging.
func arrives(t *testing.T, c <-chan *app.HolderWatchResponse) *app.Holder {
	t.Helper()

	select {
	case v, ok := <-c:
		if !ok {
			t.Fatal("the stream ended")
		}
		require.Len(t, v.GetItems(), 1)

		return v.GetItems()[0].GetValue()

	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")

		return nil
	}
}

// TestBeingGoneTravels is item 4's first increment, run rather than asserted.
//
// Both facts, because they are two columns and a stream that carried one and
// not the other would pass half of this -- and the half it dropped would be the
// quiet one. Being **disabled** is a person who may not sign in; an **epoch**
// moving is every credential issued before it going void, which is the one an
// app cannot work out for itself and the one it is holding a stale answer to.
func TestBeingGoneTravels(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Cancelled rather than left to the test's own context, so the goroutine
	// reading the stream ends with the test rather than after it.
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	conn := served(t, b.Server)
	as := asOverTheWire(ctx, b.AcmeUser)
	b.mayAnything(b.AcmeUser, b.Acme)

	// Somebody other than the caller, and the reason is worth writing down: the
	// first draft watched and disabled the **caller**, and the call after the
	// disable came back `Unauthenticated`. That is D26 working -- a disabled
	// holder is not signed in -- and it is not what this test is about.
	who := b.holder(t, ctx, b.Acme, "watched")
	me := app.HolderRef_builder{Id: who.Bytes()}.Build()
	c := watching(t, as, conn, app.HolderWatchRequest_builder{
		Filters: []*app.HolderFilter{app.HolderFilter_builder{Ref: me}.Build()},
	}.Build())

	// The snapshot the stream opens with, which is why a client does not have
	// to Get and then subscribe and race the two.
	was := arrives(t, c)
	x.Nil(was.GetDateDisabled(), "nobody has been disabled yet")
	x.Nil(was.GetDateInvalidated(), "and nothing has been voided")

	// Over the wire and not in process: what publishes to the broker is the
	// interceptor on the served stack, so a direct call writes the row and
	// tells nobody. That is not a defect -- it is why `Ungated` exists -- but it
	// does mean a test that reached for the short path would prove nothing.
	h := app.NewHolderServiceClient(conn)

	_, err := h.Disable(as, app.HolderDisableRequest_builder{Ref: me}.Build())
	x.NoError(err)

	v := arrives(t, c)
	x.NotNil(v.GetDateDisabled(), "being disabled did not travel")

	_, err = h.Invalidate(as, app.HolderInvalidateRequest_builder{Ref: me}.Build())
	x.NoError(err)

	// Read until the epoch is there rather than taking the next message: a
	// `Disable` and an `Invalidate` are two writes and the stream is free to
	// say so in more messages than there were calls.
	for range 4 {
		if v = arrives(t, c); v.GetDateInvalidated() != nil {
			break
		}
	}
	x.NotNil(v.GetDateInvalidated(), "an epoch moving did not travel")

	// And still disabled, because a watch sends **state and not a delta** --
	// which is the property an app relies on to be correct after missing a
	// message.
	x.NotNil(v.GetDateDisabled())
}

// TestACredentialHashIsNotStreamed is F10, closed and kept closed here.
//
// `CredentialService` is not registered, so there is no route to this on the
// wire and this reaches the layer directly. That is the point: the reason it is
// unregistered is `Get` answering with whatever columns it was asked for, and
// **a stream had no such column to ask for** -- it carried the whole message,
// with no `select` to narrow it and no wrapper to blank it. So the one control
// covering it was a registration nobody had a reason to keep off.
//
// Fixed in payday, where the generator writes the wrapper, and pinned here
// because this app is the one with a password hash in the column. If the pin
// ever moves back, this says so rather than the next person finding out from a
// stream.
func TestACredentialHashIsNotStreamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	who := b.holder(t, ctx, b.Acme, "hashed")

	// Written through `Ungated`, which answers with the column on purpose:
	// `pd.Secret` is on the walled stack and on no other, because `vouch` and
	// `keys` read the same column in process and comparing a verifier is the
	// whole of their job. So the write is not what this is about.
	v, err := b.Ungated.Credential().Add(ctx, app.CredentialAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Kind:   "password",
		Secret: []byte("a-hash-shaped-thing"),
	}.Build())
	x.NoError(err)

	// And watched through `Walled`, which is the stack a caller reaches and the
	// one the layer is on.
	out := &collected{
		ctx: b.as(ctx, b.AcmeUser, b.Acme),
		c:   make(chan *app.CredentialWatchResponse, 8),
		bad: make(chan error, 1),
	}
	go func() {
		out.bad <- b.Walled.Credential().Watch(app.CredentialWatchRequest_builder{
			Filters: []*app.CredentialFilter{
				app.CredentialFilter_builder{
					Ref: app.CredentialRef_builder{Id: v.GetId()}.Build(),
				}.Build(),
			},
		}.Build(), out)
	}()

	got := out.first(t)
	x.NotEmpty(got.GetKind(), "the rest of the row travels")
	x.Empty(got.GetSecret(), "the stream carried the verifier")

	// And it is still in the database, which is the half that was never the
	// problem: `VouchService` reads it in process on every sign-in.
	row, err := b.Ent.Credential.Get(ctx, mustId(t, v.GetId()).Uuid())
	x.NoError(err)
	x.Equal([]byte("a-hash-shaped-thing"), row.Secret)
}

// collected is a `grpc.ServerStreamingServer` that is a channel.
//
// Hand-written because there is no connection to get one from: the service this
// streams is deliberately not on the wire, and a test that served it to reach
// it would be testing a server this app does not run.
type collected struct {
	grpc.ServerStream

	ctx context.Context
	c   chan *app.CredentialWatchResponse
	bad chan error
}

func (s *collected) Context() context.Context { return s.ctx }

func (s *collected) Send(v *app.CredentialWatchResponse) error {
	s.c <- v

	return nil
}

func (s *collected) first(t *testing.T) *app.Credential {
	t.Helper()

	select {
	case v := <-s.c:
		require.Len(t, v.GetItems(), 1)

		return v.GetItems()[0].GetValue()

	case err := <-s.bad:
		// A watch that refused says why, and saying so beats a timeout that
		// says only that nothing came.
		t.Fatalf("the watch never started: %v", err)

		return nil

	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")

		return nil
	}
}

// The control plane publishes somewhere, and now it is somewhere a deployment
// chose.
//
// It said `memory` in the code -- `cmd/serve.go`, inside the nested `Build`
// that makes the second plane -- which made the console the one screen a second
// replica broke without saying so. An operator watching on process A would
// never hear about a key issued on process B, on a stream that stayed open and
// looked healthy.
//
// payday goes to some trouble to stop exactly this: `watch.broker` has no
// default and a configuration that leaves it out is refused, precisely so that
// scaling to two replicas means reading a line and deciding about it. A literal
// in the code is that line deleted.

// TestTheControlPlaneTakesTheBrokerItWasGiven, and takes the data plane's kind
// when it was given none.
func TestTheControlPlaneTakesTheBrokerItWasGiven(t *testing.T) {
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	// Said nowhere for the control plane, which is what every deployment
	// written before this looks like.
	s, err := cmd.Build(t.Context(), cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerNone},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NotNil(s.Control)

	// Inherited rather than hardcoded. `none` is the one that shows it: before
	// this, a deployment that had said "publish nothing" still got an
	// in-process broker on its control plane, and nothing anywhere said so.
	x.Nil(s.Control.Watch.Broker(),
		"the control plane built a broker the deployment did not ask for")

	// And the data plane's, for the same reason and by the same route.
	x.Nil(s.Watch.Broker())
}

// TestTheTwoPlanesDoNotShareABroker is the reason it is a second setting rather
// than the same one read twice.
//
// A control plane publishing into the data plane's would have a key changing
// look like a person changing, to every client watching -- and a client cannot
// tell them apart, because what a watch carries is the row and the RPC that
// touched it.
func TestTheTwoPlanesDoNotShareABroker(t *testing.T) {
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	s, err := cmd.Build(t.Context(), cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NotNil(s.Watch.Broker())
	x.NotNil(s.Control.Watch.Broker())
	x.NotSame(s.Watch.Broker(), s.Control.Watch.Broker(),
		"one broker for both planes: a key change would look like a person changing")
}
