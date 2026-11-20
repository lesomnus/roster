package wire_test

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap/wire"
)

// aMoment is how long a test looks for something that must not happen.
// Looking for longer cannot make a test that passes fail: it is the other
// half of [patience], for what is wrong when it does arrive.
const aMoment = 200 * time.Millisecond

// holding is a directory whose searches wait to be let go, or to be told to
// stop, and say which it was.
type holding struct {
	scripted
	started atomic.Int64
	stopped atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func hold() *holding {
	h := &holding{entered: make(chan struct{}, 4*wire.MaxInFlight), release: make(chan struct{})}
	h.search = func(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
		h.started.Add(1)
		h.entered <- struct{}{}
		select {
		case <-ctx.Done():
			h.stopped.Add(1)
		case <-h.release:
		}

		return wire.Ok
	}

	return h
}

// awaited is that a search reached the directory.
func (h *holding) awaited(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(patience):
		require.Fail(t, "no search reached the directory")
	}
}

// TestASearchOverWhatAConnectionHasInFlightIsAnsweredBusy counts the searches
// of one connection, which nothing did: a goroutine was started for every one
// read and the loop read on, so five thousand ran at once on one connection,
// each a walk of roster with the directory's own key.
//
// One over the bound is answered `busy` (51), and the connection goes on:
// what was running is answered when it is done, and the next search after
// that is a search like another.
func TestASearchOverWhatAConnectionHasInFlightIsAnsweredBusy(t *testing.T) {
	x := require.New(t)
	h := hold()
	c := open(t, serving(t, h, nil))
	c.bind()

	for i := range wire.MaxInFlight {
		c.send(int64(10+i), things())
	}
	for range wire.MaxInFlight {
		h.awaited(t)
	}

	c.send(1000, things())
	a := c.read()
	x.Equal(int64(1000), a.id, "something was answered while every search was held: %+v", a)
	x.Equal(ber.Tag(5), a.tag)
	x.EqualValues(wire.Busy, a.code)
	x.EqualValues(wire.MaxInFlight, h.started.Load(), "the one over the bound reached the directory")

	close(h.release)
	for range wire.MaxInFlight {
		a := c.read()
		x.Equal(ber.Tag(5), a.tag)
		x.EqualValues(wire.Success, a.code)
	}

	c.send(1001, things())
	x.EqualValues(wire.Success, c.done(1001).code)
}

// TestAConnectionThatHasNotBoundHasOneThingGoingAtOnce is what a stranger's
// searches cost. A search sent before any bind was given a goroutine like
// any other, to be refused in: twenty thousand of them on one connection
// that never read its answers were twenty thousand goroutines and 116 MiB,
// each standing in a write with the filter it had been sent.
//
// Before a bind a search is run where it is read. The directory answers it
// as it did -- the root DSE, or a refusal -- and the next message is not
// read until it has; there is nothing in such a search to abandon. So a
// connection that has proved nothing is one goroutine, whatever it sends.
func TestAConnectionThatHasNotBoundHasOneThingGoingAtOnce(t *testing.T) {
	x := require.New(t)
	h := hold()
	c := open(t, serving(t, h, nil))

	c.write(append(append(message(1, root()), message(2, things())...), message(3, whoAmI())...))
	h.awaited(t)
	select {
	case <-h.entered:
		x.Fail("two searches of a connection that has not bound were running at once")
	case <-time.After(aMoment):
	}

	// Each in its turn, and in the order asked.
	close(h.release)
	x.Equal(int64(1), c.read().id)
	x.Equal(int64(2), c.read().id)
	a := c.read()
	x.Equal(int64(3), a.id)
	x.Equal(ber.Tag(24), a.tag)
}

// TestAClientThatHangsUpEndsItsSearch is the order two things were done in
// when a connection ended. The loop waited for its searches and then told
// them to stop, so a client that hung up did not end what it had asked: a
// search whose filter keeps nobody, which writes nothing and so never finds
// the connection gone, read the whole tenant for nobody.
func TestAClientThatHangsUpEndsItsSearch(t *testing.T) {
	x := require.New(t)
	h := hold()
	c := open(t, serving(t, h, nil))
	c.bind()
	c.send(2, things())
	h.awaited(t)

	x.NoError(c.nc.Close())

	x.Eventually(func() bool { return h.stopped.Load() == 1 }, patience, 5*time.Millisecond,
		"the client hung up and its search was not told")
}

// TestAnAbandonSentWithItsSearchReachesIt is where a search was counted in:
// inside its own goroutine, some time after the loop had gone on to read the
// next message. An abandon that arrived in the same write as its search
// found nothing to stop 91 times of 100, and the search ran on.
//
// The loop counts a search in before it starts it, so what the next message
// says of it is said of something that is there.
func TestAnAbandonSentWithItsSearchReachesIt(t *testing.T) {
	x := require.New(t)
	h := hold()
	addr := serving(t, h, nil)

	const rounds = 50
	for i := range rounds {
		c := open(t, addr)
		c.write(append(append(append(
			message(1, bindOf("cn=admin", "secret")),
			message(2, things())...),
			message(3, abandonOf(2))...),
			message(4, whoAmI())...))

		x.Equal(int64(1), c.read().id)
		// The abandoned search answers nothing, so this is the next thing said.
		x.Equal(int64(4), c.read().id)
		x.Eventually(func() bool { return h.stopped.Load() == int64(i+1) }, patience, time.Millisecond,
			"an abandon sent with its search did not reach it, the %d. time", i+1)
	}
}

// TestStartTlsIsRefusedBesideASearch is the same late count read by another:
// StartTLS is refused while anything is in flight, since the connection
// cannot become another under a search that is writing to it, and it asked
// a count the search had not yet been added to.
func TestStartTlsIsRefusedBesideASearch(t *testing.T) {
	x := require.New(t)
	h := hold()
	cfg := selfSigned(t)
	addr := serving(t, h, func(s *wire.Server) { s.TLS = cfg })

	const rounds = 50
	for range rounds {
		c := open(t, addr)
		c.write(append(append(
			message(1, bindOf("cn=admin", "secret")),
			message(2, things())...),
			message(3, startTLS())...))

		x.Equal(int64(1), c.read().id)
		a := c.read()
		x.Equal(int64(3), a.id)
		x.EqualValues(wire.OperationsError, a.code, "StartTLS was begun with a search in flight")
	}
}

// hurried is a connection that keeps how long each write was given, and once
// a test says so gives it a moment instead: what a write deadline does, seen
// without waiting it out.
type hurried struct {
	net.Conn
	mu    sync.Mutex
	given []time.Duration
	hurry atomic.Bool
}

func (c *hurried) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.given = append(c.given, time.Until(t))
	c.mu.Unlock()
	if c.hurry.Load() {
		t = time.Now().Add(10 * time.Millisecond)
	}

	return c.Conn.SetWriteDeadline(t)
}

// TestAClientThatDoesNotReadIsLetGo gives a write a time to be done by. No
// write had one: a client that asked for a large result and did not read it
// stopped the search in its write, and the server had not let go of either
// after ten times the idle clock, which is the read's clock and runs out to
// no effect while the loop is waiting to read.
//
// Every write has [wire.WriteTimeout] now. One that is not taken in that
// time ends the connection, and what it had in flight with it.
func TestAClientThatDoesNotReadIsLetGo(t *testing.T) {
	x := require.New(t)
	row := wire.Entry{DN: "cn=x,ou=things", Attributes: []wire.Attribute{{Name: "description", Values: []string{strings.Repeat("x", 4096)}}}}
	wrote := make(chan error, 1)
	h := &scripted{search: func(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
		for {
			if err := w.Entry(row); err != nil {
				wrote <- err

				return wire.Refuse(wire.OperationsError, err.Error())
			}
		}
	}}

	ours, theirs := net.Pipe()
	served := &hurried{Conn: theirs}
	s := &wire.Server{Handler: h}
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		s.ServeConn(served)
	}()
	t.Cleanup(func() { ours.Close(); s.Close() })

	c := &byHand{t: t, nc: ours}
	c.bind()

	// A search, and from here nothing the server says is read.
	served.hurry.Store(true)
	c.send(2, things())

	select {
	case <-gone:
	case <-time.After(patience):
		x.Fail("a client that does not read was not let go")
	}
	select {
	case err := <-wrote:
		x.Error(err)
	default:
		x.Fail("the connection ended and its search did not")
	}

	served.mu.Lock()
	defer served.mu.Unlock()
	x.GreaterOrEqual(len(served.given), 2, "a write was given no time to be done by")
	for _, d := range served.given {
		x.Positive(d)
		x.LessOrEqual(d, wire.WriteTimeout)
	}
}

// TestABindHasADeadlineOfItsOwn bounds the one thing the loop waits for
// with nothing running beside it. A bind is answered by the directory asking
// roster, and nothing is read meanwhile, so the idle clock could not end
// one: a bind the directory did not answer held the connection for as long
// as it took.
func TestABindHasADeadlineOfItsOwn(t *testing.T) {
	x := require.New(t)
	given := make(chan time.Duration, 1)
	h := &scripted{bind: func(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result {
		by, ok := ctx.Deadline()
		if !ok {
			given <- 0

			return wire.Ok
		}
		given <- time.Until(by)

		return wire.Ok
	}}
	c := open(t, serving(t, h, nil))
	c.bind()

	d := <-given
	x.Positive(d, "a bind was given no time to be answered by")
	x.LessOrEqual(d, wire.BindTimeout)
}

// TestABindEndsWhatIsInFlight makes a comment true. It said a bind "waits
// for what is in flight", RFC 4511 § 4.2.1, and nothing did: a search asked
// as one person went on answering after the connection had become another's,
// or nobody's.
//
// The protocol says what is in flight is completed or abandoned before a
// bind is processed. Abandoned, here, and waited for: a bind that waited for
// a search to finish would wait as long as the search cared to take, with
// nothing reading the connection.
func TestABindEndsWhatIsInFlight(t *testing.T) {
	x := require.New(t)
	h := hold()
	var binds atomic.Int64
	sawItEnded := make(chan bool, 1)
	h.bind = func(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result {
		if binds.Add(1) == 2 {
			sawItEnded <- h.stopped.Load() == 1
		}

		return wire.Ok
	}
	c := open(t, serving(t, h, nil))
	c.bind()
	c.send(2, things())
	h.awaited(t)

	c.send(3, bindOf("cn=admin", "secret"))
	a := c.read()
	x.Equal(int64(3), a.id, "the search a bind ended was answered: %+v", a)
	x.Equal(ber.Tag(1), a.tag)
	x.True(<-sawItEnded, "the directory was asked to bind with a search still running")
}
