package wire_test

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap/wire"
)

// What follows is the wire spoken by hand, for what a client library will not
// send: a length that is a lie, a message nested a thousand deep, an
// identifier used twice, a search and its abandon in one write.

// byHand is one connection a test writes bytes to and reads answers from.
type byHand struct {
	t  *testing.T
	nc net.Conn
}

// patience is how long a test waits for an answer that is not coming. Nothing
// that passes waits it out: it is what makes a missing answer a failure with
// a sentence and not a test that never ends.
const patience = 10 * time.Second

func open(t *testing.T, addr string) *byHand {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close() })

	return &byHand{t: t, nc: nc}
}

func (c *byHand) write(b []byte) {
	c.t.Helper()
	_, err := c.nc.Write(b)
	require.NoError(c.t, err)
}

func (c *byHand) send(id int64, op *ber.Packet, controls ...*ber.Packet) {
	c.t.Helper()
	c.write(message(id, op, controls...))
}

// answer is one message the server said.
type answer struct {
	id   int64
	tag  ber.Tag
	code int64
	text string
	name string
}

// next is the next message, or how the connection ended instead.
func (c *byHand) next() (answer, error) {
	_ = c.nc.SetReadDeadline(time.Now().Add(patience))
	p, err := ber.ReadPacket(c.nc)
	if err != nil {
		return answer{}, err
	}
	if len(p.Children) < 2 {
		return answer{}, errors.New("an answer that is not a message")
	}
	a := answer{tag: p.Children[1].Tag}
	a.id, _ = ber.ParseInt64(p.Children[0].Data.Bytes())
	if op := p.Children[1]; a.tag != 4 && len(op.Children) >= 3 {
		a.code, _ = ber.ParseInt64(op.Children[0].Data.Bytes())
		a.text = string(op.Children[2].Data.Bytes())
		for _, more := range op.Children[3:] {
			if more.ClassType == ber.ClassContext && more.Tag == 10 {
				a.name = string(more.Data.Bytes())
			}
		}
	}

	return a, nil
}

// read is the next message, which there has to be.
func (c *byHand) read() answer {
	c.t.Helper()
	a, err := c.next()
	require.NoError(c.t, err, "the server said nothing where an answer was due")

	return a
}

// done reads to the end of one search and says how it ended.
func (c *byHand) done(id int64) answer {
	c.t.Helper()
	for {
		a := c.read()
		require.Equal(c.t, id, a.id)
		if a.tag == 5 {
			return a
		}
	}
}

// bind binds as the one name the test directory knows.
func (c *byHand) bind() {
	c.t.Helper()
	c.send(1, bindOf("cn=admin", "secret"))
	a := c.read()
	require.Equal(c.t, ber.Tag(1), a.tag)
	require.EqualValues(c.t, wire.Success, a.code)
}

// noticed is that the server said it was hanging up, in the one message the
// protocol has for that, and then did.
func (c *byHand) noticed(code int) answer {
	c.t.Helper()
	a := c.read()
	require.Equal(c.t, int64(0), a.id, "a notice of disconnection answers no request: %+v", a)
	require.Equal(c.t, ber.Tag(24), a.tag)
	require.Equal(c.t, wire.OidNoticeOfDisconnection, a.name)
	require.EqualValues(c.t, code, a.code, "%s", a.text)
	c.ended()

	return a
}

// ended is that the server hung up, having said nothing more.
func (c *byHand) ended() {
	c.t.Helper()
	a, err := c.next()
	var ne net.Error
	require.False(c.t, errors.As(err, &ne) && ne.Timeout(), "the server neither answered nor hung up")
	require.Error(c.t, err, "the server went on, and said %+v", a)
	require.True(c.t, errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || isReset(err), "%v", err)
}

func isReset(err error) bool {
	var oe *net.OpError

	return errors.As(err, &oe)
}

func message(id int64, op *ber.Packet, controls ...*ber.Packet) []byte {
	p := ber.NewSequence("")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, ""))
	p.AppendChild(op)
	if len(controls) > 0 {
		cs := ber.Encode(ber.ClassContext, ber.TypeConstructed, 0, nil, "")
		for _, c := range controls {
			cs.AppendChild(c)
		}
		p.AppendChild(cs)
	}

	return p.Bytes()
}

func octets(class ber.Class, tag ber.Tag, v string) *ber.Packet {
	return ber.NewString(class, ber.TypePrimitive, tag, v, "")
}

func number(tag ber.Tag, v int64) *ber.Packet {
	return ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, tag, v, "")
}

func bindOf(dn, password string) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 0, nil, "")
	p.AppendChild(number(ber.TagInteger, 3))
	p.AppendChild(octets(ber.ClassUniversal, ber.TagOctetString, dn))
	p.AppendChild(octets(ber.ClassContext, 0, password))

	return p
}

// asked is a search request field by field, so that a test can put the wrong
// thing in one of them.
type asked struct {
	base, scope, deref, size, limit, typesOnly, filter, attrs *ber.Packet
}

func searchOf(base string, scope wire.Scope, filter *ber.Packet, attrs ...string) asked {
	names := ber.NewSequence("")
	for _, a := range attrs {
		names.AppendChild(octets(ber.ClassUniversal, ber.TagOctetString, a))
	}

	return asked{
		base:      octets(ber.ClassUniversal, ber.TagOctetString, base),
		scope:     number(ber.TagEnumerated, int64(scope)),
		deref:     number(ber.TagEnumerated, 0),
		size:      number(ber.TagInteger, 0),
		limit:     number(ber.TagInteger, 0),
		typesOnly: ber.NewBoolean(ber.ClassUniversal, ber.TypePrimitive, ber.TagBoolean, false, ""),
		filter:    filter,
		attrs:     names,
	}
}

func (s asked) op() *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 3, nil, "")
	for _, c := range []*ber.Packet{s.base, s.scope, s.deref, s.size, s.limit, s.typesOnly, s.filter, s.attrs} {
		p.AppendChild(c)
	}

	return p
}

// things is a search of everything under the test directory's one branch,
// which a bind guards; the root is a search of the one entry nothing guards.
func things() *ber.Packet {
	return searchOf("ou=things", wire.ScopeSubtree, present("objectClass")).op()
}

func root() *ber.Packet {
	return searchOf("", wire.ScopeBase, present("objectClass")).op()
}

func present(attr string) *ber.Packet { return octets(ber.ClassContext, 7, attr) }

func equal(attr, v string) *ber.Packet {
	return of(3, octets(ber.ClassUniversal, ber.TagOctetString, attr), octets(ber.ClassUniversal, ber.TagOctetString, v))
}

// of is a filter choice around others: 0 an and, 1 an or, 2 a not.
func of(tag ber.Tag, children ...*ber.Packet) *ber.Packet {
	p := ber.Encode(ber.ClassContext, ber.TypeConstructed, tag, nil, "")
	for _, c := range children {
		p.AppendChild(c)
	}

	return p
}

func whoAmI() *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 23, nil, "")
	p.AppendChild(octets(ber.ClassContext, 0, wire.OidWhoAmI))

	return p
}

func startTLS() *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 23, nil, "")
	p.AppendChild(octets(ber.ClassContext, 0, wire.OidStartTLS))

	return p
}

func abandonOf(id int64) *ber.Packet {
	return ber.NewInteger(ber.ClassApplication, ber.TypePrimitive, 16, id, "")
}

// tlv is an element written out by hand: its tag octet, a definite length,
// its content.
func tlv(tag byte, content ...byte) []byte {
	return append(append([]byte{tag}, lengthOf(len(content))...), content...)
}

func lengthOf(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n < 0x100:
		return []byte{0x81, byte(n)}
	case n < 0x10000:
		return []byte{0x82, byte(n >> 8), byte(n)}
	case n < 0x1000000:
		return []byte{0x83, byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		return []byte{0x84, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

// nested is `depth` sequences one inside the other around a leaf.
func nested(depth int, leaf []byte) []byte {
	out := leaf
	for range depth {
		out = tlv(0x30, out...)
	}

	return out
}

// allocated is every byte this process has asked for so far. A test reads it
// on either side of something the server does, in this process, and the
// difference is what that cost.
func allocated() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return m.TotalAlloc
}

// scripted is a handler that does what a test says: for a search that is to
// be held, a bind that is to look at its context, a result that never ends.
type scripted struct {
	bind   func(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result
	search func(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result
}

func (h *scripted) Bind(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result {
	if h.bind != nil {
		return h.bind(ctx, c, req)
	}

	return wire.Ok
}

func (h *scripted) Search(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
	if h.search != nil {
		return h.search(ctx, c, req, w)
	}

	return wire.Ok
}

// serving is [serve] for a handler that is not the numbered directory.
func serving(t *testing.T, h wire.Handler, with func(*wire.Server)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &wire.Server{Handler: h}
	if with != nil {
		with(s)
	}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { l.Close(); s.Close() })

	return l.Addr().String()
}
