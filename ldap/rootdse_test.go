package ldap_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap"
	"github.com/lesomnus/roster/ldap/wire"
)

// certified is a certificate for this machine, as `--tls` hands one over.
func certified(t *testing.T) *tls.Config {
	t.Helper()
	x := require.New(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "directory.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	x.NoError(err)

	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// extensions is what the root DSE says this server can be asked to do.
func extensions(t *testing.T, c *goldap.Conn) []string {
	t.Helper()
	root := search(t, c, "", goldap.ScopeBaseObject, "(objectClass=*)", "+")
	require.Len(t, root, 1)

	return root[0].GetAttributeValues("supportedExtension")
}

// TestTheRootDseListsStartTlsOnlyWhereItIsOffered: what the root DSE says can
// be asked for is what would be done if it were.
//
// `supportedExtension` always carried StartTLS. With no certificate the
// request is then refused -- *StartTLS is not offered* -- so a client that
// reads the root DSE to decide whether to protect a bind was told yes and
// answered no. It is listed where a StartTLS on that connection would be
// taken: the directory holds a certificate, and the connection is not under
// TLS already.
func TestTheRootDseListsStartTlsOnlyWhereItIsOffered(t *testing.T) {
	d := stand(t)
	insecure := &tls.Config{InsecureSkipVerify: true}

	t.Run("with no certificate it is not listed", func(t *testing.T) {
		x := require.New(t)
		c := clientOf(t, d.directoryOn(t, ldap.BindKey, nil, nil))

		got := extensions(t, c)
		x.NotContains(got, wire.OidStartTLS, "listed, and then refused")
		x.Contains(got, wire.OidWhoAmI)
		x.Error(c.StartTLS(insecure), "the control: there is no certificate to offer it with")
	})

	t.Run("with one it is, until the connection is under TLS", func(t *testing.T) {
		x := require.New(t)
		cert := certified(t)
		c := clientOf(t, d.directoryOn(t, ldap.BindKey,
			func(c *ldap.Config) { c.StartTLS = true },
			func(s *wire.Server) { s.TLS = cert }))

		x.Contains(extensions(t, c), wire.OidStartTLS)
		x.NoError(c.StartTLS(insecure))

		got := extensions(t, c)
		x.NotContains(got, wire.OidStartTLS, "a second StartTLS is refused, and the root DSE still offers one")
		x.Contains(got, wire.OidWhoAmI)
	})

	t.Run("and not on LDAPS", func(t *testing.T) {
		x := require.New(t)
		cert := certified(t)

		dir, err := ldap.New(t.Context(), ldap.Config{Roster: d.roster, Insecure: true, Key: d.deployed, StartTLS: true})
		x.NoError(err)
		t.Cleanup(func() { dir.Close() })
		l, err := tls.Listen("tcp", "127.0.0.1:0", cert)
		x.NoError(err)
		s := &wire.Server{Handler: dir.Handler(), TLS: cert}
		go func() { _ = s.Serve(l) }()
		t.Cleanup(func() { l.Close(); s.Close() })

		c, err := goldap.DialURL("ldaps://"+l.Addr().String(), goldap.DialWithTLSConfig(insecure))
		x.NoError(err)
		t.Cleanup(func() { c.Close() })

		x.NotContains(extensions(t, c), wire.OidStartTLS)
	})
}

// TestBeforeABindIsWhatTheManualSays holds the manual's paragraph on a
// connection that has not bound to what such a connection is answered.
//
// The paragraph said every operation but a read of the root DSE is
// `insufficientAccessRights` (50) before a bind. That is so of a search and of
// nothing else: WhoAmI succeeds and names nobody, the writes and compare are
// `unwillingToPerform` (53) whoever asks, as password modify is, and a bind
// with no name and no password succeeds and binds nobody. Nothing here is
// changed by this test; the sentences were, and this is what they are held to.
func TestBeforeABindIsWhatTheManualSays(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := clientOf(t, d.directoryOn(t, ldap.BindEither, nil, nil))

	x.NotEmpty(extensions(t, c), "the root DSE is read by anybody")

	unbound := func(what string) {
		for _, base := range []string{contosoDN, "o=nobody", ""} {
			_, err := c.Search(goldap.NewSearchRequest(base, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
			x.Equal(uint16(goldap.LDAPResultInsufficientAccessRights), codeOf(t, err), "%s, a search under %q: %v", what, base, err)
		}
		who, err := c.WhoAmI(nil)
		x.NoError(err, what)
		x.Empty(who.AuthzID, what)
	}
	unbound("before a bind")

	_, err := c.Compare(kimDN, "uid", "kim")
	x.Equal(uint16(goldap.LDAPResultUnwillingToPerform), codeOf(t, err), "compare: %v", err)
	add := goldap.NewAddRequest("uid=x,ou=people,o=contoso", nil)
	add.Attribute("uid", []string{"x"})
	x.Equal(uint16(goldap.LDAPResultUnwillingToPerform), codeOf(t, c.Add(add)), "add")
	x.Equal(uint16(goldap.LDAPResultUnwillingToPerform), codeOf(t, c.Del(goldap.NewDelRequest(kimDN, nil))), "delete")
	_, err = c.PasswordModify(goldap.NewPasswordModifyRequest(kimDN, "a", "b"))
	x.Equal(uint16(goldap.LDAPResultUnwillingToPerform), codeOf(t, err), "password modify: %v", err)

	x.NoError(c.UnauthenticatedBind(""), "a bind with no name and no password")
	unbound("after an anonymous bind")

	x.Equal(uint16(goldap.LDAPResultInvalidCredentials), codeOf(t, c.UnauthenticatedBind(kimDN)), "a name and no password")
	unbound("after a bind that was refused")
}
