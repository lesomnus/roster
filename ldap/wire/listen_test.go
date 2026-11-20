package wire_test

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap/wire"
)

// failing is a listener whose next accepts fail as it is told, and which then
// goes on as the listener it wraps.
type failing struct {
	net.Listener
	mu   sync.Mutex
	with []error
}

func (l *failing) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.with) > 0 {
		err := l.with[0]
		l.with = l.with[1:]
		l.mu.Unlock()

		return nil, err
	}
	l.mu.Unlock()

	return l.Listener.Accept()
}

// TestTheListenerGoesOnAfterAnAcceptThatFails is the directory outliving a
// moment in which it could not take a connection. `Serve` returned on any
// error from `Accept` but the listener's own closing, so a process that was
// out of descriptors for one accept -- which a burst of connections does, and
// which passes -- answered LDAP to nobody from then on; and where the
// directory is a block of `roster serve` it took roster's own ports with it,
// since the two stop together.
//
// Right is what `net/http`'s loop does with the same error: wait, a little
// longer each time, and accept again. An error that is not of a moment still
// ends it, as it ends any listener.
func TestTheListenerGoesOnAfterAnAcceptThatFails(t *testing.T) {
	noDescriptors := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}

	t.Run("out of descriptors, three times over", func(t *testing.T) {
		x := require.New(t)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		x.NoError(err)
		s := &wire.Server{Handler: &directory{}, Log: quiet}
		ended := make(chan error, 1)
		go func() {
			ended <- s.Serve(&failing{Listener: l, with: []error{noDescriptors, noDescriptors, noDescriptors}})
		}()
		t.Cleanup(func() { l.Close(); s.Close() })

		answered := make(chan error, 1)
		go func() {
			c, err := ldap.DialURL("ldap://" + l.Addr().String())
			if err != nil {
				answered <- err

				return
			}
			defer c.Close()
			_, err = c.WhoAmI(nil)
			answered <- err
		}()

		select {
		case err := <-ended:
			x.Failf("the listener ended", "on an accept that failed for a moment: %v", err)
		case err := <-answered:
			x.NoError(err)
		}
	})

	t.Run("a listener that is broken", func(t *testing.T) {
		x := require.New(t)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		x.NoError(err)
		defer l.Close()
		broken := errors.New("not a listener any more")
		s := &wire.Server{Handler: &directory{}, Log: quiet}
		ended := make(chan error, 1)
		go func() { ended <- s.Serve(&failing{Listener: l, with: []error{broken}}) }()

		select {
		case err := <-ended:
			x.ErrorIs(err, broken)
		case <-time.After(patience):
			x.Fail("the loop went on accepting on a listener that is broken")
		}
	})
}

// arrivals is a listener a test hands connections to, each from the address
// it says: what a thousand clients at five addresses are, with no sockets
// between. A connection is handed over when the loop next accepts, so by the
// time one has been handed over, what the loop made of the one before it is
// decided.
type arrivals struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	port   int
}

func arriving(t *testing.T, s *wire.Server, wrap func(net.Listener) net.Listener) *arrivals {
	t.Helper()
	l := &arrivals{conns: make(chan net.Conn), closed: make(chan struct{})}
	var served net.Listener = l
	if wrap != nil {
		served = wrap(l)
	}
	go func() { _ = s.Serve(served) }()
	t.Cleanup(func() { l.Close(); s.Close() })

	return l
}

func (l *arrivals) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *arrivals) Close() error {
	l.once.Do(func() { close(l.closed) })

	return nil
}

func (l *arrivals) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 389} }

// from is the client's end of a connection that arrives from ip.
func (l *arrivals) from(t *testing.T, ip string) net.Conn {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close() })
	l.port++
	select {
	case l.conns <- &arrived{Conn: theirs, remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 20000 + l.port}}:
	case <-time.After(patience):
		require.Fail(t, "the listener stopped accepting")
	}

	return ours
}

type arrived struct {
	net.Conn
	remote net.Addr
}

func (c *arrived) RemoteAddr() net.Addr { return c.remote }

// turnedAway is that the server hung up on a connection having said nothing
// and asked nothing.
func turnedAway(t *testing.T, c net.Conn, why string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(patience))
	n, err := c.Read(make([]byte, 1))
	require.Zero(t, n, why)
	require.ErrorIs(t, err, io.EOF, why)
}

// letIn is that a connection is one the server answers.
func letIn(t *testing.T, c net.Conn, why string) {
	t.Helper()
	require.NoError(t, answers(c), why)
}

func answers(c net.Conn) error {
	_ = c.SetDeadline(time.Now().Add(patience))
	if _, err := c.Write(message(1, whoAmI())); err != nil {
		return err
	}
	_, err := ber.ReadPacket(c)

	return err
}

// TestAConnectionOverTheBoundIsClosedAtOnce counts connections, which
// nothing did: three thousand from one address that said nothing were all
// held, each for as long as the idle clock ran, and the last was answered as
// the first was.
//
// There are two bounds. One is on all of them, under what a process has
// descriptors for, so that the directory is never what runs roster out of
// them. The other is on one address, a quarter of the first, so that one
// client that leaks its connections is refused its next and nobody else is.
// A deployment whose clients all arrive from one address -- a proxy in front,
// one NAT -- has the second bound between them, which is why it is 256 and not
// what one client needs. An IPv6 address is counted with the /64 it is in,
// since a host there has all of it to arrive from.
//
// One over either bound is closed with nothing said, before a TLS handshake
// where there is one: saying why would be work done for exactly the
// connections there are too many of.
func TestAConnectionOverTheBoundIsClosedAtOnce(t *testing.T) {
	t.Run("from one address, and in all", func(t *testing.T) {
		x := require.New(t)
		l := arriving(t, &wire.Server{Handler: &directory{}, Log: quiet}, nil)

		held := make([]net.Conn, 0, wire.MaxConnsPerAddr)
		for range wire.MaxConnsPerAddr {
			held = append(held, l.from(t, "192.0.2.1"))
		}
		letIn(t, held[len(held)-1], "the last one under the bound was not answered")
		turnedAway(t, l.from(t, "192.0.2.1"), "one more than an address may have")
		letIn(t, l.from(t, "192.0.2.2"), "another address was refused for what the first holds")

		// A place is free again when the connection that had it is gone.
		x.NoError(held[0].Close())
		x.Eventually(func() bool { return answers(l.from(t, "192.0.2.1")) == nil }, patience, 10*time.Millisecond,
			"an address that hung up one connection was not let another")

		// And in all: the first address has its share, the second has one.
		open := wire.MaxConnsPerAddr + 1
		for i := 2; open < wire.MaxConns; i++ {
			room := wire.MaxConnsPerAddr
			if i == 2 {
				room--
			}
			for ; room > 0 && open < wire.MaxConns; room-- {
				l.from(t, fmt.Sprintf("192.0.2.%d", i))
				open++
			}
		}
		turnedAway(t, l.from(t, "198.51.100.7"), "one more than there may be in all, from an address that has none")
	})

	t.Run("an IPv6 address is the /64 it is in", func(t *testing.T) {
		l := arriving(t, &wire.Server{Handler: &directory{}, Log: quiet}, nil)

		for i := range wire.MaxConnsPerAddr {
			l.from(t, fmt.Sprintf("2001:db8:0:1::%x", i+1))
		}
		turnedAway(t, l.from(t, "2001:db8:0:1:ffff::1"), "one more from the same /64, at an address of its own")
		letIn(t, l.from(t, "2001:db8:0:2::1"), "the /64 beside it was refused")
	})

	t.Run("before a TLS handshake", func(t *testing.T) {
		cfg := selfSigned(t)
		l := arriving(t, &wire.Server{Handler: &directory{}, Log: quiet}, func(l net.Listener) net.Listener { return tls.NewListener(l, cfg) })

		for range wire.MaxConnsPerAddr {
			l.from(t, "192.0.2.1")
		}
		// It has sent no hello and is asked for none.
		turnedAway(t, l.from(t, "192.0.2.1"), "one more than an address may have, over LDAPS")
	})
}

// clocked is a connection that keeps how long it was given, each time it was
// given a time to be read by.
type clocked struct {
	net.Conn
	mu    sync.Mutex
	given []time.Duration
}

func (c *clocked) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.given = append(c.given, time.Until(t))
	c.mu.Unlock()

	return c.Conn.SetReadDeadline(t)
}

// TestAConnectionThatHasNotBoundHasLessTimeToSpeak is the idle clock, which
// was one clock: a connection that had said nothing and proved nothing was
// held five minutes, as long as one that had bound and gone quiet between two
// searches.
//
// A client that is going to bind does so in the round trip after it connects,
// or after a look at the root DSE and StartTLS, so one that has not bound has
// half a minute for each thing it says, the first included. The five minutes
// are for a connection somebody answers for.
func TestAConnectionThatHasNotBoundHasLessTimeToSpeak(t *testing.T) {
	x := require.New(t)
	ours, theirs := net.Pipe()
	served := &clocked{Conn: theirs}
	s := &wire.Server{Handler: &directory{}, Log: quiet}
	go s.ServeConn(served)
	t.Cleanup(func() { ours.Close(); s.Close() })

	// Each answer is read before the next thing is said, so by then the
	// server has been given its time to read the request it answers.
	c := &byHand{t: t, nc: ours}
	c.send(1, whoAmI())
	c.read()
	c.send(2, bindOf("cn=admin", "secret"))
	c.read()
	c.send(3, whoAmI())
	c.read()

	served.mu.Lock()
	defer served.mu.Unlock()
	x.GreaterOrEqual(len(served.given), 3)
	for i, what := range []string{"its first message", "its second, having spoken and not bound"} {
		x.LessOrEqual(served.given[i], wire.UnboundReadTimeout, "a connection that has not bound was given %s for %s", served.given[i], what)
		x.Positive(served.given[i])
	}
	x.Greater(served.given[2], wire.UnboundReadTimeout, "a connection that has bound was given no longer than one that has not")
	x.LessOrEqual(served.given[2], wire.DefaultReadTimeout)
	x.LessOrEqual(10*wire.UnboundReadTimeout, wire.DefaultReadTimeout, "the time before a bind is not much shorter than the time after")
}
