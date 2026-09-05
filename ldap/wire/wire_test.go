package wire_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap/wire"
)

// directory is a handler with nothing behind it: one root DSE, one bind that
// works, and as many numbered entries as a search asks for. What it records is
// what the tests read -- the filter as decoded, whether the connection was
// under TLS, whether a search's context ended.
type directory struct {
	mu        sync.Mutex
	filters   []string
	secure    []bool
	cancelled int
	// hold makes a search wait until it is released, or its context ends.
	hold chan struct{}
	n    int
}

func (d *directory) Bind(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result {
	d.mu.Lock()
	d.secure = append(d.secure, c.Secure())
	d.mu.Unlock()
	if req.DN == "cn=admin" && string(req.Password) == "secret" {
		return wire.Ok
	}

	return wire.Refuse(wire.InvalidCredentials, "")
}

func (d *directory) Search(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
	d.mu.Lock()
	d.filters = append(d.filters, req.Filter.String())
	hold := d.hold
	d.mu.Unlock()

	if req.BaseDN == "" && req.Scope == wire.ScopeBase {
		_ = w.Entry(wire.Entry{DN: "", Attributes: []wire.Attribute{{Name: "supportedLDAPVersion", Values: []string{"3"}}}})

		return wire.Ok
	}
	if _, ok := c.Bound(); !ok {
		return wire.Refuse(wire.InsufficientAccessRights, "bind first")
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			d.mu.Lock()
			d.cancelled++
			d.mu.Unlock()

			return wire.Ok
		}
	}

	from, size := 0, d.n
	if req.Paging != nil {
		if len(req.Paging.Cookie) > 0 {
			from, _ = strconv.Atoi(string(req.Paging.Cookie))
		}
		size = min(req.Paging.Size, d.n-from)
	}
	for i := from; i < from+size; i++ {
		if req.SizeLimit > 0 && w.Sent() >= req.SizeLimit {
			return wire.Refuse(wire.SizeLimitExceeded, "")
		}
		if err := w.Entry(wire.Entry{DN: fmt.Sprintf("cn=%d,ou=things", i), Attributes: []wire.Attribute{{Name: "cn", Values: []string{strconv.Itoa(i)}}}}); err != nil {
			return wire.Refuse(wire.OperationsError, err.Error())
		}
	}
	if req.Paging != nil && from+size < d.n {
		w.Cookie([]byte(strconv.Itoa(from + size)))
	}

	return wire.Ok
}

func serve(t *testing.T, d *directory, with func(*wire.Server)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &wire.Server{Handler: d}
	if with != nil {
		with(s)
	}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { l.Close(); s.Close() })

	return l.Addr().String()
}

func dial(t *testing.T, addr string) *ldap.Conn {
	t.Helper()
	c, err := ldap.DialURL("ldap://" + addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	return c
}

func TestABindIsAnsweredAndWhoAmISaysSo(t *testing.T) {
	x := require.New(t)
	d := &directory{n: 3}
	c := dial(t, serve(t, d, nil))

	err := c.Bind("cn=admin", "wrong")
	x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials), "%v", err)

	who, err := c.WhoAmI(nil)
	x.NoError(err)
	x.Equal("", who.AuthzID, "a failed bind left the connection bound")

	x.NoError(c.Bind("cn=admin", "secret"))
	who, err = c.WhoAmI(nil)
	x.NoError(err)
	x.Equal("dn:cn=admin", who.AuthzID)

	t.Run("and a search after it is answered, with its filter as a tree", func(t *testing.T) {
		x := require.New(t)

		res, err := c.Search(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
			"(&(objectClass=person)(|(cn=kim*)(uid=kim)(sn=*a*b))(!(mail=*))(cn~=kim))", []string{"cn"}, nil))
		x.NoError(err)
		x.Len(res.Entries, 3)
		x.Equal("cn=1,ou=things", res.Entries[1].DN)
		x.Equal([]string{"1"}, res.Entries[1].GetAttributeValues("cn"))

		d.mu.Lock()
		defer d.mu.Unlock()
		x.Equal("(&(objectClass=person)(|(cn=kim*)(uid=kim)(sn=*a*b))(!(mail=*))(cn?))", d.filters[len(d.filters)-1])
	})

	t.Run("and the client's size limit is a bound", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Search(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 0, false,
			"(objectClass=*)", nil, nil))
		x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded), "%v", err)
	})
}

func TestTheRootDseNeedsNoBind(t *testing.T) {
	x := require.New(t)
	c := dial(t, serve(t, &directory{}, nil))

	res, err := c.Search(ldap.NewSearchRequest("", ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=*)", []string{"supportedLDAPVersion"}, nil))
	x.NoError(err)
	x.Len(res.Entries, 1)
	x.Equal([]string{"3"}, res.Entries[0].GetAttributeValues("supportedLDAPVersion"))

	_, err = c.Search(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=*)", nil, nil))
	x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights), "an anonymous connection read past the root: %v", err)
}

func TestPagingCarriesTheCookieBothWays(t *testing.T) {
	x := require.New(t)
	c := dial(t, serve(t, &directory{n: 25}, nil))
	x.NoError(c.Bind("cn=admin", "secret"))

	res, err := c.SearchWithPaging(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=*)", []string{"cn"}, nil), 10)
	x.NoError(err)
	x.Len(res.Entries, 25)
	seen := map[string]bool{}
	for _, e := range res.Entries {
		x.False(seen[e.DN], "%s twice", e.DN)
		seen[e.DN] = true
	}
}

func TestTwoSearchesShareAConnection(t *testing.T) {
	x := require.New(t)
	d := &directory{n: 4, hold: make(chan struct{})}
	c := dial(t, serve(t, d, nil))
	x.NoError(c.Bind("cn=admin", "secret"))

	// Both are in flight before either is released; both answers come back,
	// each to its own request.
	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i := range counts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.Search(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
				"(objectClass=*)", nil, nil))
			if err == nil {
				counts[i] = len(res.Entries)
			}
		}()
	}
	x.Eventually(func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()

		return len(d.filters) == 2
	}, 5*time.Second, 10*time.Millisecond)
	close(d.hold)
	wg.Wait()
	x.Equal([]int{4, 4}, counts)
}

// TestAnAbandonedSearchAnswersNothing speaks the wire by hand: the client
// library has no abandon. A search that is held is abandoned; the next
// request is answered and the abandoned one never is.
func TestAnAbandonedSearchAnswersNothing(t *testing.T) {
	x := require.New(t)
	d := &directory{n: 4, hold: make(chan struct{})}
	addr := serve(t, d, nil)

	nc, err := net.Dial("tcp", addr)
	x.NoError(err)
	defer nc.Close()

	send := func(id int64, op *ber.Packet) {
		p := ber.NewSequence("")
		p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, ""))
		p.AppendChild(op)
		_, err := nc.Write(p.Bytes())
		x.NoError(err)
	}
	read := func() (int64, *ber.Packet) {
		p, err := ber.ReadPacket(nc)
		x.NoError(err)
		id, err := ber.ParseInt64(p.Children[0].Data.Bytes())
		x.NoError(err)

		return id, p.Children[1]
	}

	bind := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 0, nil, "")
	bind.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, 3, ""))
	bind.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "cn=admin", ""))
	bind.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, 0, "secret", ""))
	send(1, bind)
	id, op := read()
	x.Equal(int64(1), id)
	x.Equal(ber.Tag(1), op.Tag)

	search := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 3, nil, "")
	search.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "ou=things", ""))
	search.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, 2, ""))
	search.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, 0, ""))
	search.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, 0, ""))
	search.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, 0, ""))
	search.AppendChild(ber.NewBoolean(ber.ClassUniversal, ber.TypePrimitive, ber.TagBoolean, false, ""))
	search.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, 7, "objectClass", ""))
	search.AppendChild(ber.NewSequence(""))
	send(2, search)

	x.Eventually(func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()

		return len(d.filters) == 1
	}, 5*time.Second, 10*time.Millisecond)

	send(3, ber.NewInteger(ber.ClassApplication, ber.TypePrimitive, 16, 2, "abandon"))
	x.Eventually(func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()

		return d.cancelled == 1
	}, 5*time.Second, 10*time.Millisecond)

	// Something else, and it is the first thing answered: the abandoned
	// search wrote nothing.
	who := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 23, nil, "")
	who.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, 0, wire.OidWhoAmI, ""))
	send(4, who)
	id, op = read()
	x.Equal(int64(4), id)
	x.Equal(ber.Tag(24), op.Tag)
}

func TestASaslBindIsNotSupported(t *testing.T) {
	x := require.New(t)
	c := dial(t, serve(t, &directory{}, nil))

	err := c.MD5Bind("example", "admin", "secret")
	x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultAuthMethodNotSupported), "%v", err)

	// And the connection is still usable.
	x.NoError(c.Bind("cn=admin", "secret"))
}

func TestStartTlsTurnsTheConnection(t *testing.T) {
	x := require.New(t)
	d := &directory{}
	cfg := selfSigned(t)
	c := dial(t, serve(t, d, func(s *wire.Server) { s.TLS = cfg; s.RequireTLS = true }))

	err := c.Bind("cn=admin", "secret")
	x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultConfidentialityRequired), "a password went in the clear: %v", err)

	x.NoError(c.StartTLS(&tls.Config{InsecureSkipVerify: true}))
	x.NoError(c.Bind("cn=admin", "secret"))

	d.mu.Lock()
	defer d.mu.Unlock()
	x.Equal([]bool{true}, d.secure, "the handler saw the bind before or without TLS")

	t.Run("and a second StartTLS is an error, not a second handshake", func(t *testing.T) {
		x := require.New(t)
		err := c.StartTLS(&tls.Config{InsecureSkipVerify: true})
		x.Error(err)
	})
}

func TestLdapsIsTheListenersBusiness(t *testing.T) {
	x := require.New(t)
	d := &directory{}
	l, err := tls.Listen("tcp", "127.0.0.1:0", selfSigned(t))
	x.NoError(err)
	s := &wire.Server{Handler: d, RequireTLS: true}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { l.Close(); s.Close() })

	c, err := ldap.DialURL("ldaps://"+l.Addr().String(), ldap.DialWithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	x.NoError(err)
	defer c.Close()
	x.NoError(c.Bind("cn=admin", "secret"))
}

func TestTheDirectoryIsReadOnlyOnTheWire(t *testing.T) {
	x := require.New(t)
	c := dial(t, serve(t, &directory{n: 1}, nil))
	x.NoError(c.Bind("cn=admin", "secret"))

	add := ldap.NewAddRequest("cn=new,ou=things", nil)
	add.Attribute("cn", []string{"new"})
	x.True(ldap.IsErrorWithCode(c.Add(add), ldap.LDAPResultUnwillingToPerform))

	mod := ldap.NewModifyRequest("cn=0,ou=things", nil)
	mod.Replace("cn", []string{"zero"})
	x.True(ldap.IsErrorWithCode(c.Modify(mod), ldap.LDAPResultUnwillingToPerform))

	x.True(ldap.IsErrorWithCode(c.Del(ldap.NewDelRequest("cn=0,ou=things", nil)), ldap.LDAPResultUnwillingToPerform))

	_, err := c.PasswordModify(ldap.NewPasswordModifyRequest("cn=admin", "secret", "other"))
	x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultUnwillingToPerform), "%v", err)

	// Each refusal named its own operation: the connection is still in step.
	res, err := c.Search(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
	x.NoError(err)
	x.Len(res.Entries, 1)
}

func TestAnUnknownCriticalControlRefusesTheOperation(t *testing.T) {
	x := require.New(t)
	c := dial(t, serve(t, &directory{n: 1}, nil))
	x.NoError(c.Bind("cn=admin", "secret"))

	critical := ldap.NewControlString("1.2.3.4.5", true, "")
	_, err := c.Search(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, []ldap.Control{critical}))
	x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultUnavailableCriticalExtension), "%v", err)

	harmless := ldap.NewControlString("1.2.3.4.5", false, "")
	res, err := c.Search(ldap.NewSearchRequest("ou=things", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, []ldap.Control{harmless}))
	x.NoError(err)
	x.Len(res.Entries, 1)
}

func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	x := require.New(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ldap.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	x.NoError(err)

	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}
