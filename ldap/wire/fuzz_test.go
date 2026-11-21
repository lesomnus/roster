package wire

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// The fuzz targets of the one part of this repository that reads a stranger's
// bytes before anybody has proved anything.
//
// `go test` runs each over its seeds like any test, which is what CI does.
// The seeds are every input a reader of this package found to cost more than
// it should or to be read as what it was not (#281): they are what a change
// here is held to, and a `-fuzz` run starts from them.
//
//	go test ./ldap/wire -run '^$' -fuzz '^FuzzServe$' -fuzztime 2m

func fuzzOctets(class ber.Class, tag ber.Tag, v string) *ber.Packet {
	return ber.NewString(class, ber.TypePrimitive, tag, v, "")
}

func fuzzNumber(tag ber.Tag, v int64) *ber.Packet {
	return ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, tag, v, "")
}

func fuzzMessage(id int64, op *ber.Packet, controls ...*ber.Packet) []byte {
	return envelope(id, op, controls).Bytes()
}

func fuzzBind(dn, password string) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tagBindRequest, nil, "")
	p.AppendChild(fuzzNumber(ber.TagInteger, ProtocolVersion))
	p.AppendChild(fuzzOctets(ber.ClassUniversal, ber.TagOctetString, dn))
	p.AppendChild(fuzzOctets(ber.ClassContext, 0, password))

	return p
}

// fuzzSearch is a search with whatever a seed wants in each of its fields.
func fuzzSearch(base string, scope, limit, filter *ber.Packet, attrs ...string) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tagSearchRequest, nil, "")
	p.AppendChild(fuzzOctets(ber.ClassUniversal, ber.TagOctetString, base))
	p.AppendChild(scope)
	p.AppendChild(fuzzNumber(ber.TagEnumerated, 0))
	p.AppendChild(fuzzNumber(ber.TagInteger, 0))
	p.AppendChild(limit)
	p.AppendChild(ber.NewBoolean(ber.ClassUniversal, ber.TypePrimitive, ber.TagBoolean, false, ""))
	p.AppendChild(filter)
	names := ber.NewSequence("")
	for _, a := range attrs {
		names.AppendChild(fuzzOctets(ber.ClassUniversal, ber.TagOctetString, a))
	}
	p.AppendChild(names)

	return p
}

func fuzzSubtree(base string, filter *ber.Packet, attrs ...string) *ber.Packet {
	return fuzzSearch(base, fuzzNumber(ber.TagEnumerated, int64(ScopeSubtree)), fuzzNumber(ber.TagInteger, 0), filter, attrs...)
}

func fuzzOf(tag ber.Tag, children ...*ber.Packet) *ber.Packet {
	p := ber.Encode(ber.ClassContext, ber.TypeConstructed, tag, nil, "")
	for _, c := range children {
		p.AppendChild(c)
	}

	return p
}

func fuzzEqual(attr, v string) *ber.Packet {
	return fuzzOf(3, fuzzOctets(ber.ClassUniversal, ber.TagOctetString, attr), fuzzOctets(ber.ClassUniversal, ber.TagOctetString, v))
}

func fuzzSubstrings(attr string, parts ...*ber.Packet) *ber.Packet {
	ps := ber.NewSequence("")
	for _, p := range parts {
		ps.AppendChild(p)
	}

	return fuzzOf(4, fuzzOctets(ber.ClassUniversal, ber.TagOctetString, attr), ps)
}

func fuzzControl(oid string, critical bool, value []byte) *ber.Packet {
	c := ber.NewSequence("")
	c.AppendChild(fuzzOctets(ber.ClassUniversal, ber.TagOctetString, oid))
	if critical {
		c.AppendChild(ber.NewBoolean(ber.ClassUniversal, ber.TypePrimitive, ber.TagBoolean, true, ""))
	}
	if value != nil {
		c.AppendChild(fuzzOctets(ber.ClassUniversal, ber.TagOctetString, string(value)))
	}

	return c
}

func fuzzExtended(oid string) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tagExtendedRequest, nil, "")
	p.AppendChild(fuzzOctets(ber.ClassContext, 0, oid))

	return p
}

func fuzzAbandon(id int64) *ber.Packet {
	return ber.NewInteger(ber.ClassApplication, ber.TypePrimitive, tagAbandonRequest, id, "")
}

// fuzzElement is an element written out by hand, with a length of four
// octets whatever it is.
func fuzzElement(tag byte, content ...byte) []byte {
	n := len(content)

	return append([]byte{tag, 0x84, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, content...)
}

func fuzzNested(depth int, around []byte) []byte {
	for range depth {
		around = fuzzElement(0x30, around...)
	}

	return around
}

func fuzzJoin(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

// fuzzSeeds is what one connection said, case by case.
func fuzzSeeds() [][]byte {
	present := fuzzOctets(ber.ClassContext, 7, "objectClass")
	whole := fuzzOf(0, fuzzEqual("objectClass", "person"),
		fuzzOf(1, fuzzSubstrings("cn", fuzzOctets(ber.ClassContext, 0, "kim"), fuzzOctets(ber.ClassContext, 1, "a"), fuzzOctets(ber.ClassContext, 2, "b")), fuzzEqual("uid", "kim")),
		fuzzOf(2, fuzzOctets(ber.ClassContext, 7, "mail")),
		fuzzOf(8, fuzzOctets(ber.ClassUniversal, ber.TagOctetString, "cn"), fuzzOctets(ber.ClassUniversal, ber.TagOctetString, "kim")),
		fuzzOf(9, fuzzOctets(ber.ClassContext, 1, "2.5.13.2"), fuzzOctets(ber.ClassContext, 2, "cn"), fuzzOctets(ber.ClassContext, 3, "kim")))
	deep := func(tag ber.Tag, n int) *ber.Packet {
		f := present
		for range n {
			f = fuzzOf(tag, f)
		}

		return f
	}
	bind := fuzzMessage(1, fuzzBind("cn=admin", "secret"))
	things := fuzzSubtree("ou=things", present)
	rootDSE := fuzzSearch("", fuzzNumber(ber.TagEnumerated, int64(ScopeBase)), fuzzNumber(ber.TagInteger, 0), present, "supportedLDAPVersion")
	identified := func(id ...byte) []byte {
		return fuzzElement(0x30, fuzzJoin(append([]byte{0x02, byte(len(id))}, id...), things.Bytes())...)
	}
	many := func(first []byte, n int) []byte {
		out := first
		for i := range n {
			out = append(out, fuzzMessage(int64(10+i), things)...)
		}

		return out
	}

	return [][]byte{
		// What a client says.
		bind,
		fuzzMessage(2, rootDSE),
		fuzzMessage(3, fuzzSubtree("ou=things", whole, "cn", "mail", "*", "+")),
		fuzzMessage(4, things, encodePaging(Paging{Size: 10, Cookie: []byte(`{"s":"g"}`)}), fuzzControl("1.2.3.4", true, nil)),
		fuzzMessage(5, fuzzExtended(OidWhoAmI)),
		fuzzMessage(6, fuzzExtended(OidStartTLS)),
		fuzzMessage(7, fuzzAbandon(3)),
		fuzzMessage(8, ber.Encode(ber.ClassApplication, ber.TypePrimitive, tagUnbindRequest, nil, "")),
		fuzzMessage(9, ber.Encode(ber.ClassApplication, ber.TypeConstructed, tagModifyRequest, nil, "")),
		fuzzJoin(bind, fuzzMessage(2, fuzzSubtree("ou=things", whole, "cn"))),

		// A length that is a lie, and one that is not a length: two gigabytes
		// less one with nothing after it, which was waited on; four gigabytes;
		// the same on a string; nine octets of length; eight with leading
		// zeros, which is a length; `ff`; the indefinite form and its end;
		// the indefinite form on a string.
		{0x30, 0x84, 0x7f, 0xff, 0xff, 0xff},
		{0x30, 0x84, 0x40, 0x00, 0x00, 0x00},
		{0x30, 0x84, 0xff, 0xff, 0xff, 0xff},
		{0x30, 0x84, 0x80, 0x00, 0x00, 0x00},
		{0x04, 0x84, 0x7f, 0xff, 0xff, 0xff},
		append([]byte{0x30, 0x89, 0, 0, 0, 0, 0, 0, 0, 0, byte(len(fuzzMessage(2, rootDSE)) - 2)}, fuzzMessage(2, rootDSE)[2:]...),
		append([]byte{0x30, 0x88, 0, 0, 0, 0, 0, 0, 0, byte(len(fuzzMessage(2, rootDSE)) - 2)}, fuzzMessage(2, rootDSE)[2:]...),
		{0x30, 0xff, 0x00},
		fuzzJoin([]byte{0x30, 0x80}, fuzzMessage(2, rootDSE)[2:], []byte{0x00, 0x00}),
		{0x04, 0x80, 0x00, 0x00},
		bytes.Repeat([]byte{0x30, 0x80}, 1000),

		// A bind whose name says it is 256 MiB long, of which the process
		// held every byte that arrived.
		fuzzJoin([]byte{0x30, 0x84, 0x10, 0x00, 0x00, 0x10, 0x02, 0x01, 0x01}, []byte{0x60, 0x84, 0x10, 0x00, 0x00, 0x09, 0x02, 0x01, 0x03}, []byte{0x04, 0x84, 0x10, 0x00, 0x00, 0x00}, []byte("uid=")),

		// A value under 999 sequences, which was held 999 times; a sequence
		// of sixty-five thousand elements of nothing, which was ninety times
		// its bytes; and both again from somebody who has bound.
		fuzzNested(999, fuzzElement(0x04, bytes.Repeat([]byte{'a'}, 64<<10)...)),
		fuzzElement(0x30, bytes.Repeat([]byte{0x05, 0x00}, 65536)...),
		fuzzJoin(bind, fuzzNested(999, fuzzElement(0x04, bytes.Repeat([]byte{'a'}, 64<<10)...))),
		fuzzJoin(bind, fuzzElement(0x30, bytes.Repeat([]byte{0x05, 0x00}, 65536)...)),

		// A filter 998 deep, which was decoded and walked, of each kind that
		// nests; and one as deep as a filter may be.
		fuzzMessage(2, fuzzSubtree("", deep(2, 997))),
		fuzzMessage(2, fuzzSubtree("", deep(0, 997))),
		fuzzMessage(2, fuzzSubtree("", deep(1, 997))),
		fuzzJoin(bind, fuzzMessage(2, fuzzSubtree("ou=things", deep(2, MaxFilterDepth-1)))),
		fuzzJoin(bind, fuzzMessage(2, fuzzSubtree("ou=things", deep(2, MaxFilterDepth)))),

		// An identifier of nothing, of less than nothing, past what one may
		// be, of no octets and of nine; and one used twice, abandoned twice.
		identified(0x00),
		identified(0xff),
		identified(0x40, 0, 0, 0, 0, 0, 0, 0),
		identified(),
		identified(1, 0, 0, 0, 0, 0, 0, 0, 0),
		fuzzJoin(bind, fuzzMessage(5, things), fuzzMessage(5, things), fuzzMessage(6, fuzzAbandon(5)), fuzzMessage(8, fuzzAbandon(5))),

		// A scope that is a string holding a two, and a time limit of 2^62
		// seconds, which is no limit once it is a duration.
		fuzzJoin(bind, fuzzMessage(2, fuzzSearch("ou=things", fuzzOctets(ber.ClassUniversal, ber.TagOctetString, "\x02"), fuzzNumber(ber.TagInteger, 0), present))),
		fuzzJoin(bind, fuzzMessage(2, fuzzSearch("ou=things", fuzzNumber(ber.TagEnumerated, 2), fuzzNumber(ber.TagInteger, 1<<62), present))),

		// A search and its abandon in one write; StartTLS behind a search;
		// and two hundred searches at once, bound and not.
		fuzzJoin(bind, fuzzMessage(2, things), fuzzMessage(3, fuzzAbandon(2))),
		fuzzJoin(bind, fuzzMessage(2, things), fuzzMessage(3, fuzzExtended(OidStartTLS))),
		many(bind, 200),
		many(nil, 200),

		// What else is pointed at this port.
		[]byte("GET / HTTP/1.1\r\nHost: ldap.example\r\n\r\n"),
		append([]byte{0x16, 0x03, 0x01, 0x02, 0x00, 0x01, 0x00, 0x01, 0xfc, 0x03, 0x03}, bytes.Repeat([]byte{0x5a}, 200)...),
	}
}

// FuzzDecode reads bytes as one message and hands what was read to every
// decoder of this package, whatever the operation says it is: more than the
// loop does with one message, so a failure here is then asked of the loop.
//
// And it holds this package's reader to the library's, for what both read:
// the same elements, of the same kinds, holding the same bytes. The library's
// is the one every LDAP client written in Go reads with, and a tree this
// package read differently is a request it understood as another.
func FuzzDecode(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := readMessage(bytes.NewReader(data), MaxMessage)
		if err != nil {
			return
		}
		// Of a message short enough that the library's copies are few.
		if len(data) <= 4<<10 {
			if theirs, err := ber.ReadPacket(bytes.NewReader(data)); err == nil {
				fuzzSame(t, p, theirs, 1)
			}
		}

		m, err := decodeMessage(p)
		if err != nil {
			return
		}
		_ = m.criticalUnknown()
		_, _ = m.paging()
		_, _, _ = decodeBind(m)
		if req, err := decodeSearch(m); err == nil {
			_ = req.Filter.String()
			_ = req.Filter.Mentions("mail")
		}
		_, _, _ = decodeExtended(m)
		if f, err := decodeFilter(m.op, 1); err == nil {
			_ = f.String()
		}
		_, _ = ber.ParseInt64(m.op.Data.Bytes())
	})
}

func fuzzSame(t *testing.T, ours, theirs *ber.Packet, depth int) {
	if ours.Identifier != theirs.Identifier {
		t.Fatalf("%d deep: read as %+v, and the library reads %+v", depth, ours.Identifier, theirs.Identifier)
	}
	switch {
	case ours.TagType != ber.TypeConstructed:
		if !bytes.Equal(ours.Data.Bytes(), theirs.Data.Bytes()) {
			t.Fatalf("%d deep: read as %x, and the library reads %x", depth, ours.Data.Bytes(), theirs.Data.Bytes())
		}
	case depth < maxDepth:
		if len(ours.Children) != len(theirs.Children) {
			t.Fatalf("%d deep: read as holding %d elements, and the library reads %d", depth, len(ours.Children), len(theirs.Children))
		}
		for i := range ours.Children {
			fuzzSame(t, ours.Children[i], theirs.Children[i], depth+1)
		}
	}
}

// fuzzDirectory binds one name and answers every search with two entries
// made of what was asked for.
type fuzzDirectory struct{}

func (fuzzDirectory) Bind(ctx context.Context, c *Conn, req BindRequest) Result {
	if req.DN == "cn=admin" {
		return Ok
	}

	return Refuse(InvalidCredentials, "")
}

func (fuzzDirectory) Search(ctx context.Context, c *Conn, req SearchRequest, w *Search) Result {
	_ = req.Filter.String()
	for range 2 {
		e := Entry{DN: req.BaseDN}
		for _, a := range req.Attributes {
			e.Attributes = append(e.Attributes, Attribute{Name: a, Values: []string{a}})
		}
		if err := w.Entry(e); err != nil {
			return Refuse(OperationsError, err.Error())
		}
	}
	if req.Paging != nil {
		w.Cookie(req.Paging.Cookie)
	}

	return Ok
}

// fuzzConn is a connection that says the bytes it was made with and then
// hangs up, and reads none of what it is told.
type fuzzConn struct{ r *bytes.Reader }

func (c *fuzzConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *fuzzConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *fuzzConn) Close() error                     { return nil }
func (c *fuzzConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fuzzConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *fuzzConn) SetDeadline(time.Time) error      { return nil }
func (c *fuzzConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fuzzConn) SetWriteDeadline(time.Time) error { return nil }

func fuzzCertificate(f *testing.F) *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ldap.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		f.Fatal(err)
	}

	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// FuzzServe is the loop itself. The bytes are everything one connection says
// before it hangs up, to a server that offers StartTLS and to one that does
// not, and the loop has to end: without a panic, and without something a
// search left behind holding it.
func FuzzServe(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cert := fuzzCertificate(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, offered := range []*tls.Config{nil, cert} {
			s := &Server{Handler: fuzzDirectory{}, Log: quiet, TLS: offered}
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				s.ServeConn(&fuzzConn{r: bytes.NewReader(data)})
			}()
			select {
			case <-ended:
			case <-time.After(time.Minute):
				t.Fatalf("the loop did not end")
			}
		}
	})
}

// FuzzEncodeEntry is the writer: what an entry is made of is what a second
// reading of its bytes says, a name or a value of no bytes or of any
// included.
func FuzzEncodeEntry(f *testing.F) {
	f.Add("uid=kim,ou=people,o=contoso", "cn", "Kim Minji", "")
	f.Add("", "", "", "\x00")
	f.Add("cn=\xff\xfe", "description;lang-ko", "\xed\x95\x9c", "a\x00b")
	f.Fuzz(func(t *testing.T, dn, name, v1, v2 string) {
		b := envelope(7, encodeEntry(Entry{DN: dn, Attributes: []Attribute{{Name: name, Values: []string{v1, v2}}, {Name: name}}}), nil).Bytes()
		p, err := ber.DecodePacketErr(b)
		if err != nil {
			t.Fatalf("what was written is not read: %v", err)
		}
		e := p.Children[1]
		if got := string(e.Children[0].Data.Bytes()); got != dn {
			t.Fatalf("the name %q is read as %q", dn, got)
		}
		a := e.Children[1].Children[0]
		if got := string(a.Children[0].Data.Bytes()); got != name {
			t.Fatalf("the attribute %q is read as %q", name, got)
		}
		vs := a.Children[1].Children
		if len(vs) != 2 || string(vs[0].Data.Bytes()) != v1 || string(vs[1].Data.Bytes()) != v2 {
			t.Fatalf("the values %q and %q are read as others", v1, v2)
		}
		if len(e.Children[1].Children[1].Children[1].Children) != 0 {
			t.Fatalf("an attribute of no values is read as having some")
		}
	})
}
