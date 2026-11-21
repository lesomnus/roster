package wire_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap/wire"
)

// TestAMessageLongerThanARequestIsRefusedOnItsHeader holds a message to a
// length, which nothing did. The length a header claimed was held only to the
// BER library's own bound, two gigabytes, so a connection that had proved
// nothing sent six bytes that claimed a gigabyte and was waited on for it for
// as long as the idle clock ran; one that sent 256 MiB of a bind it never
// finished made the process hold 317 MiB more.
//
// Right is a refusal on the header, before a byte of what it claims is read:
// the one message the protocol has for a server that is hanging up, and the
// connection ended. Before a bind the bound is what a bind is; after one it is
// what a search is.
func TestAMessageLongerThanARequestIsRefusedOnItsHeader(t *testing.T) {
	addr := serve(t, &directory{}, nil)

	t.Run("a header that claims a gigabyte, and nothing after it", func(t *testing.T) {
		c := open(t, addr)
		c.write([]byte{0x30, 0x84, 0x40, 0x00, 0x00, 0x00})

		a := c.noticed(wire.ProtocolError)
		require.Contains(t, a.text, "1073741824 bytes")
	})

	t.Run("one byte more than a connection that has not bound may send", func(t *testing.T) {
		c := open(t, addr)
		c.write(append([]byte{0x30}, lengthOf(wire.MaxMessageUnbound+1)...))

		a := c.noticed(wire.ProtocolError)
		require.Contains(t, a.text, "bound", "the refusal does not say a bind would have let it be read: %q", a.text)
	})

	t.Run("a connection that has bound sends more, and not more than a search is", func(t *testing.T) {
		x := require.New(t)
		c := open(t, addr)
		c.bind()

		// Twice what is read before a bind, and it is a search like another.
		long := message(2, searchOf("", wire.ScopeBase, equal("cn", strings.Repeat("a", 2*wire.MaxMessageUnbound))).op())
		x.Greater(len(long), wire.MaxMessageUnbound)
		x.Less(len(long), wire.MaxMessage)
		c.write(long)
		x.EqualValues(wire.Success, c.done(2).code)

		c.write(append([]byte{0x30}, lengthOf(wire.MaxMessage+1)...))
		c.noticed(wire.ProtocolError)
	})
}

// TestAMessageSaysItsLengthBeforeItsBytes is the other way a header claimed
// nothing: a length of the indefinite form, which ends where its content says
// it does. LDAP has no such length (RFC 4511 § 5.1), no client sends one, and
// a message that does not say how long it is cannot be refused for being too
// long before it is read. It was read and answered.
func TestAMessageSaysItsLengthBeforeItsBytes(t *testing.T) {
	addr := serve(t, &directory{}, nil)
	c := open(t, addr)

	body := message(1, root())[2:]
	c.write(append(append([]byte{0x30, 0x80}, body...), 0x00, 0x00))

	c.noticed(wire.ProtocolError)
}

// TestAMessageNestedAThousandDeepCostsWhatItIsLong is the reader's case. The
// library that read a message kept, in every constructed element, a copy of
// everything inside it, so a value under 999 sequences was held 999 times:
// 256 KiB on the wire allocated 516 MiB, and two megabytes would have been two
// gigabytes, on a connection that had not bound.
//
// Right is a cost in proportion to what was sent: the bytes, once, and what a
// message as deep as LDAP's grammar goes takes to describe. What is under that
// depth is left as the bytes it is, since nothing reads it.
func TestAMessageNestedAThousandDeepCostsWhatItIsLong(t *testing.T) {
	x := require.New(t)
	c := open(t, serve(t, &directory{}, nil))
	c.bind()

	deep := nested(999, tlv(0x04, bytes.Repeat([]byte{'a'}, 256<<10)...))
	x.Less(len(deep), wire.MaxMessage)

	before := allocated()
	c.write(deep)
	// It is a sequence of one thing and so not a message: nothing to answer.
	c.ended()
	cost := allocated() - before

	// Under three times its bytes as measured, and five hundred times before.
	x.Less(cost, uint64(16*len(deep)), "a message of %d bytes cost %d to read", len(deep), cost)
}

// TestAMessageOfMoreElementsThanARequestHasIsRefused is the same cost come at
// from the side: not deep, wide. Every element is a node of the tree a message
// is decoded into, about ninety times the two bytes the smallest one takes on
// the wire, so the megabyte somebody bound may send was 92 MiB held while it
// was read. A search of a filter of a few thousand terms is a few thousand
// elements; one of two hundred thousand names of no letters is not a search.
func TestAMessageOfMoreElementsThanARequestHasIsRefused(t *testing.T) {
	x := require.New(t)
	c := open(t, serve(t, &directory{}, nil))
	c.bind()

	s := searchOf("", wire.ScopeBase, present("objectClass"))
	s.attrs = ber.NewSequence("")
	s.attrs.Data.Write(bytes.Repeat([]byte{0x04, 0x00}, 200_000))
	wide := message(2, s.op())
	x.Less(len(wide), wire.MaxMessage)

	before := allocated()
	c.write(wide)
	a := c.noticed(wire.ProtocolError)
	cost := allocated() - before

	x.Contains(a.text, "elements")
	// Its bytes, and a node for each element it was let have before the
	// refusal, at about two hundred bytes a node: under 5 MiB as measured.
	// It was 75 MiB, and the search was answered.
	x.Less(cost, uint64(4*len(wide)+512*wire.MaxElements), "a message of %d bytes cost %d to read", len(wide), cost)
}

// TestAFilterNestsAsDeepAsOneIsWrittenAndNoDeeper holds the code that walks a
// filter to a depth of its own. It was held to the BER library's, a thousand
// levels and a variable any package in the process may set: a `not` of a `not`
// 998 deep was decoded, handed to the directory and walked for every entry.
//
// The bound is past any filter there is a reason to write. One a person writes
// nests six or seven deep; one a program builds by wrapping what it has in
// another term nests as deep as it has terms. A filter over it is answered as
// the search it is, and the connection is ended as for any message over a
// bound.
func TestAFilterNestsAsDeepAsOneIsWrittenAndNoDeeper(t *testing.T) {
	nots := func(n int) *ber.Packet {
		f := present("mail")
		for range n - 1 {
			f = of(2, f)
		}

		return f
	}

	t.Run("a filter somebody wrote", func(t *testing.T) {
		x := require.New(t)
		d := &directory{}
		c := open(t, serve(t, d, nil))

		// Everybody in payroll who is staff, or a contractor with a manager and
		// a number: seven deep, and about as deep as one is written by hand.
		written := of(0,
			equal("objectClass", "inetOrgPerson"),
			equal("memberOf", "cn=payroll,ou=groups,o=contoso"),
			of(1,
				equal("employeeType", "staff"),
				of(0,
					equal("employeeType", "contractor"),
					of(2, of(1,
						of(2, present("manager")),
						of(0, of(2, present("employeeNumber")), of(2, present("departmentNumber"))),
					)),
				),
			),
		)
		c.send(1, searchOf("", wire.ScopeBase, written).op())
		x.EqualValues(wire.Success, c.done(1).code)

		d.mu.Lock()
		defer d.mu.Unlock()
		x.Equal([]string{"(&(objectClass=inetOrgPerson)(memberOf=cn=payroll,ou=groups,o=contoso)(|(employeeType=staff)(&(employeeType=contractor)(!(|(!(manager=*))(&(!(employeeNumber=*))(!(departmentNumber=*))))))))"}, d.filters)
	})

	t.Run("one as deep as the bound", func(t *testing.T) {
		x := require.New(t)
		d := &directory{}
		c := open(t, serve(t, d, nil))

		c.send(1, searchOf("", wire.ScopeBase, nots(wire.MaxFilterDepth)).op())
		x.EqualValues(wire.Success, c.done(1).code)

		d.mu.Lock()
		defer d.mu.Unlock()
		x.Len(d.filters, 1)
		x.Equal(wire.MaxFilterDepth-1, strings.Count(d.filters[0], "!"))
	})

	for _, depth := range []int{wire.MaxFilterDepth + 1, 998} {
		t.Run(fmt.Sprintf("one %d deep", depth), func(t *testing.T) {
			x := require.New(t)
			d := &directory{}
			c := open(t, serve(t, d, nil))

			c.send(1, searchOf("", wire.ScopeBase, nots(depth)).op())
			a := c.done(1)
			x.EqualValues(wire.ProtocolError, a.code, "a filter %d deep", depth)
			x.Contains(a.text, "filter")
			c.ended()

			d.mu.Lock()
			defer d.mu.Unlock()
			x.Empty(d.filters, "a filter %d deep reached the directory", depth)
		})
	}
}
