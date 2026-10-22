// Package mail delivers what roster mints and leaves to its front door to
// deliver: a link that recovers an account, one that confirms an address, and
// one that deletes an account (`account.Config.Mail`).
//
// # Where the line is
//
// roster mints and does not deliver (D19): it answers a token, and what
// reaches a mailbox is the front door's to send. This is that sending, as the
// front door's and nobody else's -- `roster account serve` and the account app
// `roster serve` runs both build it from `account.mail` -- and roster's own
// servers never import it.
//
// # SMTP, and how it is protected
//
// SMTP and nothing more particular, because every transactional provider
// speaks it, so which one a deployment sends through is configuration. The
// connection is TLS unless a deployment says otherwise for one host it names:
//
//	starttls   the default: the server must offer STARTTLS, and a server that
//	           does not is refused rather than spoken to in the clear
//	implicit   TLS from the first byte, which is port 465
//	none       in the clear, and only to this machine or to a host named in
//	           PlaintextHosts: a sink in development (Mailpit), never a
//	           provider
//
// A password goes only over TLS, or to this machine.
//
// # What it says about what it sent
//
// Nothing a log may not carry. An error names the step that failed and the
// server's code, with any address in what the server said cut to its domain
// and any link cut out; [Mask] is an address as a log may show it.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TLS is how the connection to the server is protected.
type TLS string

const (
	// StartTLS is a connection that becomes TLS before anything is said: the
	// server must offer STARTTLS. The default.
	StartTLS TLS = "starttls"

	// Implicit is TLS from the first byte, which is port 465.
	Implicit TLS = "implicit"

	// None is the clear, to this machine or a host named in
	// [Config.PlaintextHosts].
	None TLS = "none"
)

// Config is the server a front door sends through, its secrets read.
type Config struct {
	// Addr is the server, `host:port`. A port left out is 587, 465 for
	// [Implicit], and 25 for [None].
	Addr string

	// Username and Password sign in with AUTH PLAIN; an empty Username signs
	// in as nobody. Password is the password itself: reading it from wherever
	// it is kept is the caller's.
	Username string
	Password string

	// From is who a message is from, as RFC 5322 has it:
	// `Contoso <no-reply@contoso.example>`, or the address alone.
	From string

	// TLS is how the connection is protected. Empty is [StartTLS].
	TLS TLS

	// PlaintextHosts is the hosts besides this machine that [None] may reach:
	// a sink on the network in development -- `mailpit` in a compose file --
	// named, so that pointing Addr at a provider does not leave it in the
	// clear.
	PlaintextHosts []string

	// Timeout bounds one message, from the dial to the server's answer to the
	// last line. Zero is 30 seconds.
	Timeout time.Duration

	// RootCAs is who a server's certificate is checked against. Nil is the
	// system's.
	RootCAs *x509.CertPool
}

// Sender sends through one server.
type Sender struct {
	host     string
	addr     string
	username string
	password string
	from     *netmail.Address
	tls      TLS
	timeout  time.Duration
	roots    *x509.CertPool
}

// New is a sender as the configuration says, or why it cannot be one: refused
// here rather than at the first message, so that a deployment learns it when
// it starts.
func New(c Config) (*Sender, error) {
	mode := c.TLS
	switch mode {
	case "":
		mode = StartTLS
	case StartTLS, Implicit, None:
	default:
		return nil, fmt.Errorf("tls: %q is none of starttls, implicit, none", c.TLS)
	}

	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		// A host alone, which is the provider's name and the port its mode's.
		host, port = c.Addr, ""
	}
	if host == "" {
		return nil, errors.New("addr: which server, as host:port")
	}
	if port == "" {
		port = map[TLS]string{StartTLS: "587", Implicit: "465", None: "25"}[mode]
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return nil, fmt.Errorf("addr: %q is not a port", port)
	}

	if mode == None && !loopback(host) && !slices.ContainsFunc(c.PlaintextHosts, func(v string) bool { return strings.EqualFold(v, host) }) {
		return nil, fmt.Errorf("tls: none sends in the clear, which is for a sink on this machine or a host named in plaintext_hosts, and %s is neither", host)
	}
	if mode == None && c.Username != "" && !loopback(host) {
		return nil, fmt.Errorf("tls: none to %s with a username would send the password in the clear, which this never does", host)
	}

	if c.From == "" {
		return nil, errors.New("from: who a message is from, as `Name <address>`")
	}
	from, err := netmail.ParseAddress(c.From)
	if err != nil {
		return nil, fmt.Errorf("from: %q is not an address to send from: %w", c.From, err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &Sender{
		host:     host,
		addr:     net.JoinHostPort(host, port),
		username: c.Username,
		password: c.Password,
		from:     from,
		tls:      mode,
		timeout:  timeout,
		roots:    c.RootCAs,
	}, nil
}

// loopback is whether a host is this machine: `localhost`, or an address on
// the loopback network.
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// Send sends one message in plain text.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	rcpt, err := netmail.ParseAddress(to)
	if err != nil {
		return errors.New("mail: the recipient is not an address")
	}
	msg, err := compose(s.from, rcpt, subject, body)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cfg := &tls.Config{ServerName: s.host, RootCAs: s.roots, MinVersion: tls.VersionTLS12}
	d := &net.Dialer{}
	var conn net.Conn
	if s.tls == Implicit {
		conn, err = (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", s.addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", s.addr)
	}
	if err != nil {
		return failed("dial", s.addr, err)
	}
	// The whole message under one deadline, and the connection closed when
	// the context is done before it: a server that answers nothing holds a
	// request for as long as this says and no longer.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return failed("greeting", s.addr, err)
	}
	defer c.Close()

	if s.tls == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("mail: %s offers no STARTTLS, and this does not send in the clear; `tls: implicit` is a server that speaks TLS from the start", s.addr)
		}
		if err := c.StartTLS(cfg); err != nil {
			return failed("starttls", s.addr, err)
		}
	}
	if s.username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("mail: %s offers no AUTH, and a username is configured", s.addr)
		}
		// net/smtp also refuses a password over a connection that is neither
		// TLS nor to this machine, which New has already refused.
		if err := c.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return failed("auth", s.addr, err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return failed("mail from", s.addr, err)
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return failed("rcpt", s.addr, err)
	}
	w, err := c.Data()
	if err != nil {
		return failed("data", s.addr, err)
	}
	if _, err := w.Write(msg); err != nil {
		return failed("data", s.addr, err)
	}
	if err := w.Close(); err != nil {
		return failed("data", s.addr, err)
	}
	if err := c.Quit(); err != nil {
		return failed("quit", s.addr, err)
	}

	return nil
}

// failed is an error a log may carry: the step, the server, and what went
// wrong with any address in it cut to its domain and any link cut out -- a
// server that refuses a recipient says who, and it is not the log's to keep.
func failed(step, addr string, err error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		return fmt.Errorf("mail: %s: %s: %d %s", addr, step, te.Code, Redact(te.Msg))
	}

	return fmt.Errorf("mail: %s: %s: %s", addr, step, Redact(err.Error()))
}

var (
	addresses = regexp.MustCompile(`[^\s<>"'(),;:@]+@([A-Za-z0-9.-]+)`)
	links     = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)
)

// Redact is `s` with every link cut out and every address cut to its domain:
// what may go to a log of something a server or a mailbox said.
func Redact(s string) string {
	s = links.ReplaceAllString(s, "<link>")

	return addresses.ReplaceAllString(s, "…@$1")
}

// Mask is an address as a log may show it: its domain, and nothing of whose.
func Mask(address string) string {
	if a, err := netmail.ParseAddress(address); err == nil {
		address = a.Address
	}
	if _, domain, ok := strings.Cut(address, "@"); ok && domain != "" {
		return "…@" + domain
	}

	return "…"
}

// compose is a message as it is sent: headers in ASCII, the subject encoded,
// and the body in UTF-8 and base64, which nothing in between rewrites.
func compose(from, to *netmail.Address, subject, body string) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("mail: a subject is one line")
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	_, domain, _ := strings.Cut(from.Address, "@")

	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("UTF-8", subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=UTF-8")
	header("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")

	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")

	return b.Bytes(), nil
}
