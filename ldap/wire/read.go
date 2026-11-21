package wire

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// What a message may be, said here and held by this package where it reads
// one. None of it is the BER library's to say: its own bounds are two
// gigabytes and a thousand levels, in two variables every package of the
// process shares, and its reader keeps in each constructed element a copy of
// everything inside it, so a value under a thousand sequences was held a
// thousand times. A stranger's bytes do not reach it. [readMessage] reads the
// header, refuses on it, takes as many bytes as it says and no more, and
// builds the tree over those bytes without copying one.
//
// They are constants and not settings. Each is far past what a client of a
// directory that only binds and searches sends, and a deployment that meets
// one has a client worth looking at before it has a number worth raising.
const (
	// MaxMessage is the longest message read from a connection that has
	// bound. The largest thing such a connection honestly sends is a search
	// whose filter names people one by one: `(|(uid=…)(uid=…)…)` is about
	// twenty-five bytes a term, so a megabyte is a filter of forty thousand
	// of them, a hundred times what a client that batches its lookups sends.
	// Nothing is written through this directory, so no message carries a
	// picture or a certificate.
	MaxMessage = 1 << 20

	// MaxMessageUnbound is the longest message read from a connection that
	// has not bound, which is anybody. What such a connection honestly sends
	// is a bind, a read of the root DSE and StartTLS. A simple bind is a name
	// and a password of at most a kilobyte of characters; the long one is a
	// SASL bind carrying a Kerberos ticket, which is answered
	// `authMethodNotSupported` so that the client goes on to a simple bind
	// on the same connection, and has to be read to be answered. Windows
	// issues tickets of up to 48 000 bytes.
	MaxMessageUnbound = 64 << 10

	// MaxElements is how many elements one message may be made of. Every
	// element is a node of the tree a message is decoded into, and a node is
	// some ninety times the two bytes the smallest element takes on the wire,
	// so without this the megabyte above was ninety megabytes while it was
	// read. A term of a filter is three elements and a name asked for is
	// one: sixteen thousand is a filter of five thousand terms.
	MaxElements = 1 << 14

	// MaxFilterDepth is how deep a filter nests: a `not` of an `and` of an
	// `or` is three. One a person writes is six or seven deep. One a program
	// builds by wrapping what it has in another term is as deep as it has
	// terms, which is the case this leaves room for. Everything that walks a
	// filter -- [decodeFilter], and after it [Filter.String],
	// [Filter.Mentions] and the directory's evaluation of one over every
	// entry -- calls itself once a level, and this is what bounds them.
	MaxFilterDepth = 100
)

// maxDepth is how deep [decode] follows a message: the envelope and the
// operation around a filter of [MaxFilterDepth], and under the filter's last
// node a substrings' sequence and its parts. Nothing in this package reads
// what is under that, so nothing is made of it.
const maxDepth = MaxFilterDepth + 4

// ending is why a connection is ended, in the words its client is told
// first: a message past what this package reads of one, or a request no
// answer to which could be told from an answer to another. A client that sent
// either is not going to send better next.
type ending string

func (e ending) Error() string { return string(e) }

// readMessage reads one LDAPMessage off a connection.
//
// The header first, in as many bytes as it has, and it is refused there: for
// a first byte that is not a sequence's, which is whatever else was pointed
// at this port, and for a length over `most` or of the indefinite form, which
// LDAP does not have (RFC 4511 § 5.1) and which would be a message that does
// not say how long it is. Then exactly the bytes it claimed, kept as they
// arrive and so costing what was sent and not what was claimed. Nothing past
// the message is read, which is what lets the byte after a StartTLS response
// be the handshake's.
func readMessage(r io.Reader, most int) (*ber.Packet, error) {
	// A tag, the first octet of a length, and the eight more a length may
	// take.
	head := make([]byte, 2, 10)
	if _, err := io.ReadFull(r, head[:1]); err != nil {
		return nil, err
	}
	if head[0] != byte(ber.ClassUniversal)|byte(ber.TypeConstructed)|byte(ber.TagSequence) {
		return nil, errors.New("not an LDAPMessage")
	}
	if _, err := io.ReadFull(r, head[1:]); err != nil {
		return nil, unexpected(err)
	}
	if more := int(head[1] & ber.LengthValueBitmask); head[1] > ber.LengthLongFormBitmask && head[1] != 0xff && more <= 8 {
		head = head[:2+more]
		if _, err := io.ReadFull(r, head[2:]); err != nil {
			return nil, unexpected(err)
		}
	}

	_, n, _, err := header(head)
	switch {
	case err != nil:
		return nil, ending("a message whose length is not one: " + err.Error())
	case n > uint64(most):
		return nil, ending(fmt.Sprintf("a message of %d bytes, and the longest read here is %d", n, most))
	}

	// Room for an ordinary request on the header's word, and for a long one
	// as its bytes arrive.
	buf := bytes.NewBuffer(make([]byte, 0, len(head)+int(min(n, 4<<10))))
	buf.Write(head)
	if _, err := io.CopyN(buf, r, int64(n)); err != nil {
		return nil, unexpected(err)
	}

	return decode(buf.Bytes())
}

// decode builds the tree of one element over the bytes it was read from.
//
// The library's own reader is not used: see the top of this file. What this
// makes is the library's `Packet` all the same, since that is what the
// decoders beside this read and what the answers are written with: a node an
// element, its `Data` the element's content as it arrived, its `Children`
// the elements a constructed one holds. Bytes after the element are left.
func decode(b []byte) (*ber.Packet, error) {
	d := decoder{left: MaxElements}
	p, _, err := d.element(b, 1)

	return p, err
}

type decoder struct {
	// left is how many more elements this message may have.
	left int
}

func (d *decoder) element(b []byte, depth int) (p *ber.Packet, rest []byte, err error) {
	id, n, used, err := header(b)
	if err != nil {
		return nil, nil, err
	}
	b = b[used:]
	if n > uint64(len(b)) {
		return nil, nil, errors.New("an element longer than what holds it")
	}
	if d.left == 0 {
		return nil, nil, ending(fmt.Sprintf("a message of more than %d elements", MaxElements))
	}
	d.left--

	// Capped, so that a write to the node's buffer would move it and not run
	// on into the element after.
	content := b[:n:n]
	p = &ber.Packet{Identifier: id, Data: bytes.NewBuffer(content)}
	if id.TagType == ber.TypeConstructed && depth < maxDepth {
		for len(content) > 0 {
			var child *ber.Packet
			child, content, err = d.element(content, depth+1)
			if err != nil {
				return nil, nil, err
			}
			p.Children = append(p.Children, child)
		}
	}

	return p, b[n:], nil
}

// header reads an element's identifier and its length off the front of b, and
// says how many bytes the two took. X.690 § 8.1.2 and § 8.1.3, as the library
// reads them, but for the indefinite length.
func header(b []byte) (id ber.Identifier, length uint64, used int, err error) {
	cut := errors.New("an element cut short")
	if len(b) < 2 {
		return id, 0, 0, cut
	}
	id.ClassType = ber.Class(b[0]) & ber.ClassBitmask
	id.TagType = ber.Type(b[0]) & ber.TypeBitmask
	id.Tag = ber.Tag(b[0]) & ber.TagBitmask
	used = 1

	// A tag of thirty-one or more is written after the first octet, seven
	// bits at a time. Nothing LDAP defines has one.
	if id.Tag == ber.HighTag {
		id.Tag = 0
		for octets := 1; ; octets++ {
			if used == len(b) {
				return id, 0, 0, cut
			}
			c := b[used]
			used++
			id.Tag = id.Tag<<7 | ber.Tag(c)&ber.HighTagValueBitmask
			if (octets == 1 && id.Tag == 0) || octets > 9 {
				return id, 0, 0, errors.New("a tag that is not one")
			}
			if ber.Tag(c)&ber.HighTagContinueBitmask == 0 {
				break
			}
		}
	}

	if used == len(b) {
		return id, 0, 0, cut
	}
	c := b[used]
	used++
	switch more := int(c & ber.LengthValueBitmask); {
	case c < ber.LengthLongFormBitmask:
		length = uint64(c)
	case c == ber.LengthLongFormBitmask:
		return id, 0, 0, errors.New("a length of the indefinite form, which LDAP has not (RFC 4511 § 5.1)")
	case c == 0xff || more > 8:
		return id, 0, 0, errors.New("a length that is not one")
	case len(b)-used < more:
		return id, 0, 0, cut
	default:
		for _, c := range b[used : used+more] {
			length = length<<8 | uint64(c)
		}
		used += more
	}

	// The end-of-contents mark of a length that is not definite, where every
	// length is.
	if id == (ber.Identifier{}) && length == 0 {
		return id, 0, 0, errors.New("an end of contents with nothing to end")
	}

	return id, length, used, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}

	return err
}
