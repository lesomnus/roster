package ldap

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"

	"github.com/lesomnus/roster/ldap/wire"
)

// The fuzz targets of what stands between a decoded request and roster: the
// names, and a filter over an entry. `ldap/wire/fuzz_test.go` has the bytes
// before that, and says how these are run.

// FuzzParseDN holds a name to one reading: any text is a name or a refusal,
// and a name that was read is written and read back as the same.
func FuzzParseDN(f *testing.F) {
	for _, s := range []string{
		"", "uid=kim,ou=people,o=contoso", `cn=a\,b,o=x`, `cn=\#a\ `, `cn=a\00b`, "cn=a+sn=b", `cn=a\`, "=x", "cn", `cn="a,b"`, "CN = Kim , O = x",
		// Found by this target (#281): a value that ends in a tab, which was
		// read back without it.
		`0=0\09`, "cn=a ,o=b\t", `cn=a\ `, `cn=\ a\20\ `,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := parseDN(s)
		if err != nil {
			return
		}
		again, err := parseDN(d.String())
		if err != nil {
			t.Fatalf("%q was read, written as %q, and that is refused: %v", s, d.String(), err)
		}
		if len(again) != len(d) {
			t.Fatalf("%q was read as %d parts, written as %q, and read back as %d", s, len(d), d.String(), len(again))
		}
		for i := range d {
			if d[i] != again[i] {
				t.Fatalf("%q part %d: %q=%q was written as %q and read back as %q=%q", s, i, d[i].attr, d[i].value, d.String(), again[i].attr, again[i].value)
			}
		}
	})
}

// FuzzEscape holds a value to the same from the other end: one written into a
// name is the one read out of it.
func FuzzEscape(f *testing.F) {
	for _, s := range []string{
		"kim", "a,b", "#a", " a", "a ", "a\x00b", `a\b`, "a+b", `"`, "<>;", "=", " ",
		// Found by this target (#281): a value that ends in a form feed, and
		// was read back without it. So was one that ends in a space.
		"000\f", "a  ", "\ta\t", "a\u00a0", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		name := dn{}.child("cn", v).String()
		d, err := parseDN(name)
		if err != nil {
			t.Fatalf("%q was written as %q, which is refused: %v", v, name, err)
		}
		if len(d) != 1 || d[0].value != v {
			t.Fatalf("%q was written as %q and read back as %+v", v, name, d)
		}
	})
}

// evaluating is a directory of one person, who is asked whether a filter
// keeps them and is then projected by what the search asked for.
type evaluating struct {
	attr  string
	asked *bool
}

func (evaluating) Bind(context.Context, *wire.Conn, wire.BindRequest) wire.Result { return wire.Ok }

func (h evaluating) Search(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
	*h.asked = true

	e := newEntry(dn{{attr: "uid", value: "kim"}, {attr: "o", value: "contoso"}})
	e.add("objectClass", "top", "person")
	e.add("uid", "kim")
	e.add("cn", "Kim Minji")
	e.add("mail", "kim@contoso.example", "k@contoso.example")
	e.add("entryUUID", "0191-aaaa")
	_ = e.matches(req.Filter)
	_ = req.Filter.Mentions(h.attr)
	_ = w.Entry(e.project(req.Attributes, req.TypesOnly))

	return wire.Ok
}

// said is a connection that says what it was made with and hangs up.
type said struct{ r *bytes.Reader }

func (c *said) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *said) Write(p []byte) (int, error)      { return len(p), nil }
func (c *said) Close() error                     { return nil }
func (c *said) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *said) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *said) SetDeadline(time.Time) error      { return nil }
func (c *said) SetReadDeadline(time.Time) error  { return nil }
func (c *said) SetWriteDeadline(time.Time) error { return nil }

// FuzzEval is a filter of any bytes over an entry: an answer, whatever the
// filter, and the entry projected by any name.
//
// The bytes go where a filter goes in a search and the search goes through
// the wire's own loop, since that is the only door a filter comes in by:
// what the wire refuses of one is never evaluated, and this holds the two
// together.
func FuzzEval(f *testing.F) {
	f.Add([]byte{0x87, 0x02, 'c', 'n'}, "cn", false)
	f.Add([]byte{0xa0, 0x00}, "*", true)
	f.Add([]byte{0xa2, 0x04, 0x87, 0x02, 'c', 'n'}, "+", false)
	f.Add([]byte{0xa4, 0x0b, 0x04, 0x02, 'c', 'n', 0x30, 0x05, 0x81, 0x03, 'm', 'i', 'n'}, "1.1", false)
	// A `not` of a `not` nine hundred times over, which was walked.
	deep := []byte{0x87, 0x02, 'c', 'n'}
	for range 900 {
		deep = append([]byte{0xa2, 0x82, byte(len(deep) >> 8), byte(len(deep))}, deep...)
	}
	f.Add(deep, "cn", false)

	f.Fuzz(func(t *testing.T, filter []byte, attr string, typesOnly bool) {
		evaluate(filter, attr, typesOnly)
	})
}

// evaluate asks the directory of one person a search whose filter is these
// bytes, as they are: whether they are a filter is the wire's to say. It
// reports whether the search reached the directory.
func evaluate(filter []byte, attr string, typesOnly bool) (asked bool) {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 3, nil, "")
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "o=contoso", ""))
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(wire.ScopeSubtree), ""))
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, 0, ""))
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, 0, ""))
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, 0, ""))
	op.AppendChild(ber.NewBoolean(ber.ClassUniversal, ber.TypePrimitive, ber.TagBoolean, typesOnly, ""))
	op.Data.Write(filter)
	names := ber.NewSequence("")
	names.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, attr, ""))
	names.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "cn", ""))
	op.AppendChild(names)

	msg := ber.NewSequence("")
	msg.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, 1, ""))
	msg.AppendChild(op)

	s := &wire.Server{Handler: evaluating{attr: attr, asked: &asked}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.ServeConn(&said{r: bytes.NewReader(msg.Bytes())})

	return asked
}

// TestTheFuzzOfAFilterReachesTheEntry is that [FuzzEval] does not pass
// because nothing arrived: a filter that is one is evaluated. And one nine
// hundred deep is not, which it was while the only bound was the BER
// library's thousand.
func TestTheFuzzOfAFilterReachesTheEntry(t *testing.T) {
	if !evaluate([]byte{0x87, 0x02, 'c', 'n'}, "cn", false) {
		t.Fatal("a search of a filter that is one did not reach the directory")
	}

	deep := []byte{0x87, 0x02, 'c', 'n'}
	for range 900 {
		deep = append([]byte{0xa2, 0x82, byte(len(deep) >> 8), byte(len(deep))}, deep...)
	}
	if evaluate(deep, "cn", false) {
		t.Fatal("a filter nine hundred deep reached the directory")
	}
}
