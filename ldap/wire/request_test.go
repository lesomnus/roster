package wire_test

import (
	"context"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap/wire"
)

// identified is a message with whatever is written where its identifier
// goes, around an operation.
func identified(id []byte, op *ber.Packet) []byte {
	return tlv(0x30, append(id, op.Bytes()...)...)
}

// TestAMessageIdentifierIsOneARequestMayHave holds an identifier to what
// RFC 4511 § 4.1.1.1 says of one: an INTEGER from one to `maxInt`, and not
// that of a request still in progress on the connection. None of it was
// asked. Zero, which is the identifier of what a server says unasked, was
// taken and answered under; so were minus one, 2^62 and an OCTET STRING. And
// two searches under one identifier were one entry where a connection keeps
// what it has in flight: an abandon reached one of them, a second abandon
// neither, and the count of what was running said one.
//
// There is no answer to such a request that its client could tell from an
// answer to another -- a result under identifier zero reads as a notice, one
// under an identifier in use ends the wrong search -- so it is the notice of
// disconnection, `protocolError`, and the connection is ended.
func TestAMessageIdentifierIsOneARequestMayHave(t *testing.T) {
	for _, c := range []struct {
		name string
		id   []byte
	}{
		{"zero", []byte{0x02, 0x01, 0x00}},
		{"less than zero", []byte{0x02, 0x01, 0xff}},
		{"one past maxInt", []byte{0x02, 0x05, 0x00, 0x80, 0x00, 0x00, 0x00}},
		{"two to the sixty-two", []byte{0x02, 0x08, 0x40, 0, 0, 0, 0, 0, 0, 0}},
		{"an integer of no octets", []byte{0x02, 0x00}},
		{"a string", []byte{0x04, 0x01, 0x07}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &directory{}
			by := open(t, serve(t, d, nil))
			by.write(identified(c.id, root()))

			a := by.noticed(wire.ProtocolError)
			require.Contains(t, a.text, "message id")

			d.mu.Lock()
			defer d.mu.Unlock()
			require.Empty(t, d.filters, "the request reached the directory")
		})
	}

	t.Run("maxInt itself", func(t *testing.T) {
		c := open(t, serve(t, &directory{}, nil))
		c.write(identified([]byte{0x02, 0x04, 0x7f, 0xff, 0xff, 0xff}, root()))
		require.EqualValues(t, wire.Success, c.done(math.MaxInt32).code)
	})

	t.Run("that of a search still running", func(t *testing.T) {
		x := require.New(t)
		h := hold()
		c := open(t, serving(t, h, nil))
		c.bind()

		c.send(5, things())
		h.awaited(t)
		c.send(5, things())

		a := c.noticed(wire.ProtocolError)
		x.Contains(a.text, "message id 5")
		x.EqualValues(1, h.started.Load(), "a second search under an identifier in use reached the directory")
		// And the first ends with the connection, as anything in flight does.
		x.Eventually(func() bool { return h.stopped.Load() == 1 }, patience, 5*time.Millisecond)
	})

}

// lingering is a connection whose writes are a moment in returning after
// the client has what was written: a client quicker than the server, made
// certain.
type lingering struct {
	net.Conn
	on atomic.Bool
}

func (c *lingering) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if c.on.Load() {
		time.Sleep(aMoment)
	}

	return n, err
}

// TestASearchIsCountedOutByItsAnswer is when a search stops being in flight:
// when its answer is written, and not some time after. It was counted out
// once the write had returned, so a client that had its answer and spoke at
// once was told of a search it had already been answered for: StartTLS was
// refused as having an operation beside it, and now that an identifier in
// use is refused, the same identifier used for the next request would have
// ended the connection.
func TestASearchIsCountedOutByItsAnswer(t *testing.T) {
	x := require.New(t)
	ours, theirs := net.Pipe()
	served := &lingering{Conn: theirs}
	s := &wire.Server{Handler: &directory{n: 1}, TLS: selfSigned(t)}
	go s.ServeConn(served)
	t.Cleanup(func() { ours.Close(); s.Close() })

	c := &byHand{t: t, nc: ours}
	c.bind()
	served.on.Store(true)

	// Each is said the moment the answer before it is read, while the server
	// is still in the write that carried it.
	c.send(5, things())
	x.EqualValues(wire.Success, c.done(5).code)
	c.send(5, things())
	x.EqualValues(wire.Success, c.done(5).code, "an identifier was not free once its search was answered")

	c.send(6, startTLS())
	a := c.read()
	x.Equal(int64(6), a.id)
	x.EqualValues(wire.Success, a.code, "StartTLS was refused for a search that had been answered: %s", a.text)
}

// TestAFieldOfASearchIsWhatItsGrammarSays reads a search's numbers as the
// numbers they are declared to be. Scope, size limit and time limit were
// each taken from whatever bytes stood in their place, so an OCTET STRING
// holding the byte two was the scope *subtree*. And a time limit was any
// INTEGER at all: 2^62 seconds is past what a duration holds, came out as
// no limit, and the search ran with none.
//
// A scope is an ENUMERATED and the two limits are INTEGERs from nothing to
// `maxInt` (RFC 4511 § 4.5.1). Anything else is `protocolError`, answered as
// the search it is, and the connection goes on: its identifier was good.
func TestAFieldOfASearchIsWhatItsGrammarSays(t *testing.T) {
	reached := make(chan wire.SearchRequest, 1)
	h := &scripted{search: func(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
		reached <- req

		return wire.Ok
	}}
	c := open(t, serving(t, h, nil))
	c.bind()

	id := int64(1)
	for _, wrong := range []struct {
		name   string
		change func(*asked)
	}{
		{"a scope that is a string holding a two", func(s *asked) { s.scope = octets(ber.ClassUniversal, ber.TagOctetString, "\x02") }},
		{"a scope that is an INTEGER", func(s *asked) { s.scope = number(ber.TagInteger, 2) }},
		{"a scope of no octets", func(s *asked) { s.scope = octets(ber.ClassUniversal, ber.TagEnumerated, "") }},
		{"a size limit that is a string", func(s *asked) { s.size = octets(ber.ClassUniversal, ber.TagOctetString, "\x05") }},
		{"a size limit one past maxInt", func(s *asked) { s.size = number(ber.TagInteger, math.MaxInt32+1) }},
		{"a time limit that is an ENUMERATED", func(s *asked) { s.limit = number(ber.TagEnumerated, 5) }},
		{"a time limit of 2^62 seconds", func(s *asked) { s.limit = number(ber.TagInteger, 1<<62) }},
		{"a time limit one past maxInt", func(s *asked) { s.limit = number(ber.TagInteger, math.MaxInt32+1) }},
	} {
		t.Run(wrong.name, func(t *testing.T) {
			id++
			s := searchOf("ou=things", wire.ScopeSubtree, present("objectClass"))
			wrong.change(&s)
			c.send(id, s.op())

			a := c.done(id)
			select {
			case req := <-reached:
				require.Fail(t, "it reached the directory", "as a search of scope %d, size limit %d, time limit %s", req.Scope, req.SizeLimit, req.TimeLimit)
			default:
			}
			require.EqualValues(t, wire.ProtocolError, a.code)
		})
	}

	t.Run("limits of maxInt are limits", func(t *testing.T) {
		x := require.New(t)
		id++
		s := searchOf("ou=things", wire.ScopeOne, present("objectClass"))
		s.size, s.limit = number(ber.TagInteger, math.MaxInt32), number(ber.TagInteger, math.MaxInt32)
		c.send(id, s.op())
		x.EqualValues(wire.Success, c.done(id).code)

		req := <-reached
		x.Equal(wire.ScopeOne, req.Scope)
		x.Equal(math.MaxInt32, req.SizeLimit)
		x.Equal(math.MaxInt32*time.Second, req.TimeLimit)
	})
}
