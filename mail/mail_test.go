package mail_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	netmail "net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/mail"
)

// server is an SMTP server as small as these tests need: it offers STARTTLS
// or speaks TLS from the first byte, takes AUTH PLAIN, and keeps what each
// conversation was.
type server struct {
	t        *testing.T
	l        net.Listener
	cert     tls.Certificate
	implicit bool
	starttls bool
	auth     bool
	user     string
	pass     string
	rcpt     string // a reply to RCPT in place of 250
	silent   bool   // say nothing at all

	mu   sync.Mutex
	seen []seen
}

// seen is one conversation, as far as it went.
type seen struct {
	tls     bool // whether it was TLS when the sender said who it was from
	authed  string
	authTLS bool
	from    string
	rcpt    string
	data    string
}

func serve(t *testing.T, opts func(*server)) (*server, *x509.CertPool) {
	t.Helper()
	cert, pool := certificate(t)
	s := &server{t: t, cert: cert}
	opts(s)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	if s.implicit {
		l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}})
	}
	s.l = l
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.talk(c)
		}
	}()

	return s, pool
}

func (s *server) addr() string { return s.l.Addr().String() }

func (s *server) conversations() []seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seen{}, s.seen...)
}

func (s *server) talk(c net.Conn) {
	defer c.Close()
	if s.silent {
		_, _ = io.Copy(io.Discard, c)
		return
	}

	// Kept when the sender says goodbye, before the server answers it -- so
	// that a send that returned is a conversation already kept -- or when the
	// connection ends some other way.
	v := seen{tls: s.implicit}
	kept := false
	keep := func() {
		if kept {
			return
		}
		kept = true
		s.mu.Lock()
		s.seen = append(s.seen, v)
		s.mu.Unlock()
	}
	defer keep()

	tp := textproto.NewConn(c)
	say := func(line string) { _ = tp.PrintfLine("%s", line) }
	say("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, rest, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			lines := []string{"fake"}
			if s.starttls && !v.tls {
				lines = append(lines, "STARTTLS")
			}
			if s.auth {
				lines = append(lines, "AUTH PLAIN")
			}
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				say("250" + sep + l)
			}
		case "STARTTLS":
			say("220 go ahead")
			t := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := t.Handshake(); err != nil {
				return
			}
			c = t
			tp = textproto.NewConn(c)
			say = func(line string) { _ = tp.PrintfLine("%s", line) }
			v.tls = true
		case "AUTH":
			mech, b64, _ := strings.Cut(rest, " ")
			raw, err := base64.StdEncoding.DecodeString(b64)
			parts := strings.Split(string(raw), "\x00")
			if !strings.EqualFold(mech, "PLAIN") || err != nil || len(parts) != 3 || parts[1] != s.user || parts[2] != s.pass {
				say("535 5.7.8 authentication failed")
				continue
			}
			v.authed, v.authTLS = parts[1], v.tls
			say("235 2.7.0 ok")
		case "MAIL":
			v.from = rest
			say("250 ok")
		case "RCPT":
			v.rcpt = rest
			if s.rcpt != "" {
				say(s.rcpt)
				continue
			}
			say("250 ok")
		case "DATA":
			say("354 go ahead")
			b, err := io.ReadAll(tp.DotReader())
			if err != nil {
				return
			}
			v.data = string(b)
			say("250 queued")
		case "QUIT":
			keep()
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// certificate is one a server shows for 127.0.0.1, and the pool that trusts
// it.
func certificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// message is what a conversation's DATA said, read the way a mail client
// reads it.
func message(t *testing.T, data string) (*netmail.Message, string) {
	t.Helper()
	m, err := netmail.ReadMessage(bufio.NewReader(strings.NewReader(data)))
	require.NoError(t, err)
	body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	require.NoError(t, err)

	return m, string(body)
}

const link = "https://login.contoso.example/redeem?token=s3cr3t-t0ken&at=abc"

// TestAMessageGoesOverStartTLSAndSaysWhoItIsFrom: the default, against a
// server that offers STARTTLS -- the connection is TLS before the password or
// anything of the message is said, and the message is from the name and the
// address configured, to the recipient, with the body as given.
//
// Red with AUTH before STARTTLS (authTLS), and with the From's name dropped.
func TestAMessageGoesOverStartTLSAndSaysWhoItIsFrom(t *testing.T) {
	x := require.New(t)
	srv, pool := serve(t, func(s *server) { s.starttls, s.auth, s.user, s.pass = true, true, "roster", "hunter2" })

	s, err := mail.New(mail.Config{
		Addr: srv.addr(), Username: "roster", Password: "hunter2",
		From: "Contoso <no-reply@contoso.example>", RootCAs: pool,
	})
	x.NoError(err)
	x.NoError(s.Send(t.Context(), "erin@contoso.example", "Recover your account", "the body\n"+link+"\n"))

	vs := srv.conversations()
	x.Len(vs, 1)
	v := vs[0]
	x.True(v.tls, "the message went in the clear")
	x.Equal("roster", v.authed)
	x.True(v.authTLS, "the password went in the clear")
	x.Equal("FROM:<no-reply@contoso.example>", v.from)
	x.Equal("TO:<erin@contoso.example>", v.rcpt)

	m, body := message(t, v.data)
	x.Equal(`"Contoso" <no-reply@contoso.example>`, m.Header.Get("From"))
	x.Equal("<erin@contoso.example>", m.Header.Get("To"))
	x.Equal("Recover your account", m.Header.Get("Subject"))
	x.Equal("text/plain; charset=UTF-8", m.Header.Get("Content-Type"))
	x.Equal("the body\n"+link+"\n", body)
}

// TestAServerWithoutStartTLSIsRefused: a server that does not offer STARTTLS
// is not spoken to in the clear instead -- nothing of the message reaches it.
//
// Red with the check gone, which sends the message in the clear.
func TestAServerWithoutStartTLSIsRefused(t *testing.T) {
	x := require.New(t)
	srv, pool := serve(t, func(s *server) {})

	s, err := mail.New(mail.Config{Addr: srv.addr(), From: "no-reply@contoso.example", RootCAs: pool})
	x.NoError(err)
	err = s.Send(t.Context(), "erin@contoso.example", "Recover your account", link)
	x.ErrorContains(err, "offers no STARTTLS")
	for _, v := range srv.conversations() {
		x.Empty(v.from, "the message was sent in the clear")
		x.Empty(v.data)
	}
}

// TestImplicitTLSIsTLSFromTheFirstByte: port 465's way.
//
// Red with the dial in the clear, which never hears the server's greeting.
func TestImplicitTLSIsTLSFromTheFirstByte(t *testing.T) {
	x := require.New(t)
	srv, pool := serve(t, func(s *server) { s.implicit = true })

	s, err := mail.New(mail.Config{Addr: srv.addr(), TLS: mail.Implicit, From: "no-reply@contoso.example", RootCAs: pool, Timeout: 5 * time.Second})
	x.NoError(err)
	x.NoError(s.Send(t.Context(), "erin@contoso.example", "Recover your account", link))
	vs := srv.conversations()
	x.Len(vs, 1)
	x.True(vs[0].tls)
	x.NotEmpty(vs[0].data)
}

// TestTheClearIsForThisMachineOrAHostNamed: `tls: none` reaches loopback, or a
// host plaintext_hosts names -- a sink on the network in development -- and
// nothing else; and never with a password to a host that is not this machine.
//
// Red with the host check gone, and with the password check gone.
func TestTheClearIsForThisMachineOrAHostNamed(t *testing.T) {
	x := require.New(t)

	_, err := mail.New(mail.Config{Addr: "smtp.example.com:25", TLS: mail.None, From: "no-reply@contoso.example"})
	x.ErrorContains(err, "smtp.example.com is neither", "a provider would be spoken to in the clear")

	_, err = mail.New(mail.Config{Addr: "mailpit:1025", TLS: mail.None, PlaintextHosts: []string{"mailpit"}, From: "no-reply@contoso.example"})
	x.NoError(err, "a sink named is refused")

	_, err = mail.New(mail.Config{Addr: "mailpit:1025", TLS: mail.None, PlaintextHosts: []string{"mailpit"}, Username: "u", Password: "p", From: "no-reply@contoso.example"})
	x.ErrorContains(err, "password in the clear")

	// And to this machine it sends, in the clear.
	srv, _ := serve(t, func(s *server) {})
	s, err := mail.New(mail.Config{Addr: srv.addr(), TLS: mail.None, From: "no-reply@contoso.example"})
	x.NoError(err)
	x.NoError(s.Send(t.Context(), "erin@contoso.example", "Recover your account", link))
	vs := srv.conversations()
	x.Len(vs, 1)
	x.False(vs[0].tls)
	x.NotEmpty(vs[0].data)
}

// TestARefusedSignInSaysSoAndNotThePassword: the server's refusal is the
// error, and the password is in no part of it.
func TestARefusedSignInSaysSoAndNotThePassword(t *testing.T) {
	x := require.New(t)
	srv, pool := serve(t, func(s *server) { s.starttls, s.auth, s.user, s.pass = true, true, "roster", "right" })

	s, err := mail.New(mail.Config{Addr: srv.addr(), Username: "roster", Password: "wr0ng-passw0rd", From: "no-reply@contoso.example", RootCAs: pool})
	x.NoError(err)
	err = s.Send(t.Context(), "erin@contoso.example", "Recover your account", link)
	x.ErrorContains(err, "auth: 535")
	x.NotContains(err.Error(), "wr0ng-passw0rd")
	for _, v := range srv.conversations() {
		x.Empty(v.data, "a message was sent by somebody the server refused")
	}
}

// TestATimeoutBoundsAMessage: a server that takes the connection and says
// nothing holds a send for as long as the timeout and no longer.
//
// Red with neither the deadline nor the close, which waits for ever.
func TestATimeoutBoundsAMessage(t *testing.T) {
	x := require.New(t)
	srv, _ := serve(t, func(s *server) { s.silent = true })

	s, err := mail.New(mail.Config{Addr: srv.addr(), TLS: mail.None, From: "no-reply@contoso.example", Timeout: 200 * time.Millisecond})
	x.NoError(err)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- s.Send(t.Context(), "erin@contoso.example", "Recover your account", link) }()
	select {
	case err := <-done:
		x.Error(err)
		x.Less(time.Since(start), 5*time.Second)
	case <-time.After(10 * time.Second):
		x.FailNow("a send to a server that says nothing never returned")
	}
}

// TestAnErrorCarriesNoAddressAndNoLink: a server that refuses a recipient says
// who, and may say more; what the error keeps is the step, the code and the
// domain -- and that is all a log of it can carry.
//
// Red with the server's words kept as they were.
func TestAnErrorCarriesNoAddressAndNoLink(t *testing.T) {
	x := require.New(t)
	srv, pool := serve(t, func(s *server) {
		s.starttls = true
		s.rcpt = "550 5.1.1 <erin@contoso.example>: no such user; see " + link
	})

	s, err := mail.New(mail.Config{Addr: srv.addr(), From: "no-reply@contoso.example", RootCAs: pool})
	x.NoError(err)
	err = s.Send(t.Context(), "erin@contoso.example", "Recover your account", link)
	x.Error(err)
	x.Contains(err.Error(), "rcpt: 550")
	x.Contains(err.Error(), "…@contoso.example")
	for _, leak := range []string{"erin@", "erin@contoso.example", "s3cr3t-t0ken", "https://"} {
		x.NotContains(err.Error(), leak)
	}

	_, err = mail.New(mail.Config{Addr: srv.addr(), From: "no-reply@contoso.example", RootCAs: pool})
	x.NoError(err)
	err = s.Send(t.Context(), "not an address", "Recover your account", link)
	x.EqualError(err, "mail: the recipient is not an address")
}

// TestMaskAndRedact: an address as a log may show it is its domain, and what
// somebody said is kept without anybody's address or any link.
func TestMaskAndRedact(t *testing.T) {
	x := require.New(t)
	x.Equal("…@contoso.example", mail.Mask("erin@contoso.example"))
	x.Equal("…@contoso.example", mail.Mask("Erin <erin@contoso.example>"))
	x.Equal("…", mail.Mask("nobody"))
	x.Equal("550 <…@contoso.example> rejected, see <link>", mail.Redact("550 <erin.k+x@contoso.example> rejected, see "+link))
}

// TestAConfigurationIsRefusedAtTheStart: what cannot work is said when the
// sender is made, which is when the process starts.
func TestAConfigurationIsRefusedAtTheStart(t *testing.T) {
	x := require.New(t)

	_, err := mail.New(mail.Config{Addr: "smtp.example.com:587"})
	x.ErrorContains(err, "from:")
	_, err = mail.New(mail.Config{Addr: "smtp.example.com:587", From: "not an address"})
	x.ErrorContains(err, "from:")
	_, err = mail.New(mail.Config{Addr: "smtp.example.com:587", From: "no-reply@contoso.example", TLS: "ssl"})
	x.ErrorContains(err, "tls:")
	_, err = mail.New(mail.Config{From: "no-reply@contoso.example"})
	x.ErrorContains(err, "addr:")
	_, err = mail.New(mail.Config{Addr: "smtp.example.com:ninety", From: "no-reply@contoso.example"})
	x.ErrorContains(err, "addr:")

	// A host alone is the port its mode is on.
	_, err = mail.New(mail.Config{Addr: "smtp.example.com", From: "no-reply@contoso.example"})
	x.NoError(err)
}
