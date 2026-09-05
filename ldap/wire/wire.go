// Package wire is the LDAPv3 this repository speaks, and nothing above it.
//
// It reads and writes the handful of messages `roster ldap serve` needs --
// simple bind, search, unbind, abandon, StartTLS -- on the BER codec, and
// hands each decoded request to a [Handler] that knows what a person is. It
// does not know: there is no directory, no DN grammar and no schema here.
// Everything that decides anything is the handler's; what this package decides
// is only what the protocol decides for it.
//
// # Why this is written here rather than imported
//
// The subset is small -- five requests, their answers, one control and a
// table of refusals -- and it is the whole of what the process says on the
// wire, which is the kind of thing this repository keeps rather than borrows.
// The library that would have done it (`jimlambrt/gldap`) is one person's and
// stopped in 2024-08, so the repository's rule about fixing upstream when it
// is in the way would have meant a fork; and it hands the filter over as a
// string to be compiled back into the tree it was decoded from, where reading
// the tree directly is both less code and the natural shape. `docs/ldap.md`
// § The wire is ours.
//
// # What the loop gets right, and tests
//
// Message identifiers: every answer carries its request's, and a connection
// may have several searches in flight. Abandon stops a search that is still
// sending. The StartTLS turn writes its response in the clear and the very
// next byte is the handshake, which is why nothing here buffers the reader.
// A critical control this package does not know refuses the operation rather
// than ignoring it. The client's size and time limits are handed to the
// handler as a bound and a deadline.
//
// # What it refuses on the handler's behalf
//
// Every write -- add, modify, delete, modify DN -- and compare, with
// `unwillingToPerform`, because a directory front is a read and there is no
// handler method for them to reach. A SASL bind, with `authMethodNotSupported`.
// A bind in the clear when the server requires TLS, with
// `confidentialityRequired`, before the handler sees the password.
package wire

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// Result codes, RFC 4511 § 4.1.9. The ones this process answers with.
const (
	Success                      = 0
	OperationsError              = 1
	ProtocolError                = 2
	TimeLimitExceeded            = 3
	SizeLimitExceeded            = 4
	AuthMethodNotSupported       = 7
	UnavailableCriticalExtension = 12
	ConfidentialityRequired      = 13
	NoSuchObject                 = 32
	InvalidCredentials           = 49
	InsufficientAccessRights     = 50
	UnwillingToPerform           = 53
)

// Extended operations this package knows by name.
const (
	OidStartTLS        = "1.3.6.1.4.1.1466.20037"
	OidWhoAmI          = "1.3.6.1.4.1.4203.1.11.3"
	OidPasswordModify  = "1.3.6.1.4.1.4203.1.11.1"
	OidPagedResults    = "1.2.840.113556.1.4.319"
	ProtocolVersion    = 3
	DefaultReadTimeout = 5 * time.Minute
)

// Result is an LDAPResult: what every operation answers with.
type Result struct {
	Code      int
	MatchedDN string
	Message   string
}

// Ok is success.
var Ok = Result{Code: Success}

// Refuse is a result with a code and a diagnostic.
func Refuse(code int, msg string) Result { return Result{Code: code, Message: msg} }

// Scope of a search, RFC 4511 § 4.5.1.2.
type Scope int

const (
	ScopeBase Scope = iota
	ScopeOne
	ScopeSubtree
)

// BindRequest is a simple bind: a name and a password. A SASL bind never
// reaches the handler.
type BindRequest struct {
	DN       string
	Password []byte
}

// SearchRequest is what a client asked to read. The filter is the tree the
// request arrived as; the paging control, when the client sent one, is its
// size and cookie.
type SearchRequest struct {
	BaseDN     string
	Scope      Scope
	SizeLimit  int
	TimeLimit  time.Duration
	TypesOnly  bool
	Filter     *Filter
	Attributes []string
	Paging     *Paging
}

// Paging is the simple paged results control, RFC 2696. On a request it is
// the page size and the cookie of the page before (empty for the first). On
// a response it is the cookie of the next page, or empty for the last.
type Paging struct {
	Size   int
	Cookie []byte
}

// Entry is one search result.
type Entry struct {
	DN         string
	Attributes []Attribute
}

// Attribute is one attribute and its values.
type Attribute struct {
	Name   string
	Values []string
}

// Search is where a handler writes a search's results as it finds them.
type Search struct {
	c      *Conn
	id     int64
	cookie []byte
	sent   int
	mu     sync.Mutex
}

// Entry sends one result to the client.
func (s *Search) Entry(e Entry) error {
	s.mu.Lock()
	s.sent++
	s.mu.Unlock()

	return s.c.write(envelope(s.id, encodeEntry(e), nil))
}

// Sent is how many entries have gone out.
func (s *Search) Sent() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sent
}

// Cookie names the next page for a paged search. Empty is the last page.
func (s *Search) Cookie(cookie []byte) { s.cookie = cookie }

// Handler is what a directory is to this package.
type Handler interface {
	// Bind answers a simple bind. On [Success] the connection is bound as
	// the request's DN until the next bind or the end of the connection; on
	// anything else it is anonymous.
	Bind(ctx context.Context, c *Conn, req BindRequest) Result

	// Search writes its results to `w` and returns the result of the whole
	// operation. The context ends when the client abandons the search or
	// its time limit passes.
	Search(ctx context.Context, c *Conn, req SearchRequest, w *Search) Result
}

// Server speaks the protocol on every connection it accepts and hands the
// decoded requests to its handler.
type Server struct {
	Handler Handler

	// TLS offers StartTLS, and is what a StartTLS handshake uses. Nil offers
	// none. LDAPS is a `tls.Listener` handed to [Server.Serve], and needs no
	// setting here.
	TLS *tls.Config

	// RequireTLS refuses a bind in the clear with `confidentialityRequired`,
	// before the handler sees the password. Everything else is allowed in
	// the clear -- a client reads the root DSE to learn StartTLS is there,
	// and an unbound connection cannot read anything a bind guards.
	RequireTLS bool

	// ReadTimeout is how long a connection may be idle. Zero is
	// [DefaultReadTimeout].
	ReadTimeout time.Duration

	Log *slog.Logger

	mu    sync.Mutex
	conns map[*Conn]struct{}
}

// Serve accepts on `l` until it is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		nc, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}

			return err
		}

		go s.ServeConn(nc)
	}
}

// Close hangs up every connection. The listener is the caller's to close.
func (s *Server) Close() {
	s.mu.Lock()
	conns := make([]*Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		c.close()
	}
}

func (s *Server) track(c *Conn, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = map[*Conn]struct{}{}
	}
	if on {
		s.conns[c] = struct{}{}
	} else {
		delete(s.conns, c)
	}
}

func (s *Server) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}

	return s.Log
}

// Conn is one client connection: what it has bound as, and whether it is
// under TLS.
type Conn struct {
	s  *Server
	nc net.Conn

	writeMu sync.Mutex

	mu       sync.Mutex
	bound    string
	tls      bool
	inflight map[int64]context.CancelFunc
	closed   bool
}

// Bound is the DN this connection is bound as, and false for anonymous.
func (c *Conn) Bound() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.bound, c.bound != ""
}

// Secure is whether the connection is under TLS -- LDAPS or after StartTLS.
func (c *Conn) Secure() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.tls
}

// RemoteAddr is the client's.
func (c *Conn) RemoteAddr() net.Addr { return c.nc.RemoteAddr() }

func (c *Conn) write(p *ber.Packet) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_, err := c.nc.Write(p.Bytes())

	return err
}

func (c *Conn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		return
	}
	c.closed = true
	for _, cancel := range c.inflight {
		cancel()
	}
	c.mu.Unlock()

	_ = c.nc.Close()
}

func (c *Conn) begin(id int64, ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.inflight[id] = cancel
	c.mu.Unlock()

	return ctx, func() {
		cancel()
		c.mu.Lock()
		delete(c.inflight, id)
		c.mu.Unlock()
	}
}

func (c *Conn) abandon(id int64) {
	c.mu.Lock()
	cancel, ok := c.inflight[id]
	c.mu.Unlock()
	if ok {
		cancel()
	}
}

func (c *Conn) busy() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.inflight)
}

// ServeConn speaks the protocol on one connection until it is unbound, closed
// or fails. Exported for a caller that accepts its own connections.
func (s *Server) ServeConn(nc net.Conn) {
	c := &Conn{s: s, nc: nc, inflight: map[int64]context.CancelFunc{}}
	if _, ok := nc.(*tls.Conn); ok {
		c.tls = true
	}
	s.track(c, true)
	defer s.track(c, false)
	defer c.close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	timeout := s.ReadTimeout
	if timeout == 0 {
		timeout = DefaultReadTimeout
	}

	// Searches run beside the loop so a second request can arrive while one
	// is answering, and so an abandon can reach one. Binds, unbinds and
	// StartTLS run in the loop: each changes what the connection is, and
	// nothing may be read while that is happening.
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		// Through the connection as it is now: after StartTLS it is another.
		c.mu.Lock()
		rc := c.nc
		c.mu.Unlock()
		_ = rc.SetReadDeadline(time.Now().Add(timeout))
		p, err := ber.ReadPacket(rc)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.log().Debug("ldap: connection ended", slog.String("from", nc.RemoteAddr().String()), slog.String("err", err.Error()))
			}

			return
		}

		msg, err := decodeMessage(p)
		if err != nil {
			// Nothing to answer with: without an identifier the client could
			// not match a response, and RFC 4511 § 4.1.1 says to disconnect.
			s.log().Debug("ldap: not a message", slog.String("err", err.Error()))

			return
		}

		switch msg.op.Tag {
		case tagBindRequest:
			s.bind(ctx, c, msg)
		case tagUnbindRequest:
			return
		case tagSearchRequest:
			if code := msg.criticalUnknown(); code != 0 {
				_ = c.write(envelope(msg.id, encodeResult(tagSearchResultDone, Refuse(code, "a critical control this server does not know")), nil))

				continue
			}
			req, err := decodeSearch(msg)
			if err != nil {
				_ = c.write(envelope(msg.id, encodeResult(tagSearchResultDone, Refuse(ProtocolError, err.Error())), nil))

				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.search(ctx, c, msg.id, req)
			}()
		case tagAbandonRequest:
			if id, err := ber.ParseInt64(msg.op.Data.Bytes()); err == nil {
				c.abandon(id)
			}
			// An abandon has no response, RFC 4511 § 4.11.
		case tagExtendedRequest:
			if s.extended(ctx, c, msg) {
				return
			}
		case tagModifyRequest, tagAddRequest, tagDelRequest, tagModifyDNRequest, tagCompareRequest:
			_ = c.write(envelope(msg.id, encodeResult(msg.op.Tag+1, Refuse(UnwillingToPerform, "this directory is read-only")), nil))
		default:
			// RFC 4511 § 4.1.1: an operation the server does not recognise is
			// answered with a notice of disconnection, and the connection ends.
			_ = c.write(envelope(0, encodeExtended(Refuse(ProtocolError, "unknown operation"), OidNoticeOfDisconnection, nil), nil))

			return
		}
	}
}

// OidNoticeOfDisconnection is the unsolicited notification a server sends
// before it hangs up on a client it cannot follow.
const OidNoticeOfDisconnection = "1.3.6.1.4.1.1466.20036"

func (s *Server) bind(ctx context.Context, c *Conn, msg *message) {
	// A bind resets the authentication state whatever it answers, and waits
	// for what is in flight, RFC 4511 § 4.2.1.
	c.mu.Lock()
	c.bound = ""
	c.mu.Unlock()

	req, sasl, err := decodeBind(msg)
	switch {
	case err != nil:
		_ = c.write(envelope(msg.id, encodeResult(tagBindResponse, Refuse(ProtocolError, err.Error())), nil))

		return
	case sasl:
		_ = c.write(envelope(msg.id, encodeResult(tagBindResponse, Refuse(AuthMethodNotSupported, "simple bind only; see docs/ldap.md")), nil))

		return
	case s.RequireTLS && !c.Secure():
		_ = c.write(envelope(msg.id, encodeResult(tagBindResponse, Refuse(ConfidentialityRequired, "a password in the clear; StartTLS first")), nil))

		return
	case req.DN == "" && len(req.Password) == 0:
		// An anonymous bind: allowed by the protocol, and it makes the
		// connection exactly what it was.
		_ = c.write(envelope(msg.id, encodeResult(tagBindResponse, Ok), nil))

		return
	}

	res := s.Handler.Bind(ctx, c, req)
	if res.Code == Success {
		c.mu.Lock()
		c.bound = req.DN
		c.mu.Unlock()
	}
	_ = c.write(envelope(msg.id, encodeResult(tagBindResponse, res), nil))
}

func (s *Server) search(ctx context.Context, c *Conn, id int64, req SearchRequest) {
	ctx, done := c.begin(id, ctx)
	defer done()
	if req.TimeLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.TimeLimit)
		defer cancel()
	}

	w := &Search{c: c, id: id}
	res := s.Handler.Search(ctx, c, req, w)
	if ctx.Err() != nil {
		// Abandoned: no response at all, RFC 4511 § 4.11. Or timed out,
		// which has a code.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && req.TimeLimit > 0 {
			res = Refuse(TimeLimitExceeded, "")
		} else {
			return
		}
	}

	var controls []*ber.Packet
	if req.Paging != nil {
		controls = append(controls, encodePaging(Paging{Cookie: w.cookie}))
	}
	_ = c.write(envelope(id, encodeResult(tagSearchResultDone, res), controls))
}

// extended answers an extended request, and reports whether the connection
// is to end.
func (s *Server) extended(ctx context.Context, c *Conn, msg *message) (hangup bool) {
	name, _, err := decodeExtended(msg)
	if err != nil {
		_ = c.write(envelope(msg.id, encodeExtended(Refuse(ProtocolError, err.Error()), "", nil), nil))

		return false
	}

	switch name {
	case OidStartTLS:
		switch {
		case s.TLS == nil:
			_ = c.write(envelope(msg.id, encodeExtended(Refuse(ProtocolError, "StartTLS is not offered"), OidStartTLS, nil), nil))
		case c.Secure():
			_ = c.write(envelope(msg.id, encodeExtended(Refuse(OperationsError, "already under TLS"), OidStartTLS, nil), nil))
		case c.busy() > 0:
			_ = c.write(envelope(msg.id, encodeExtended(Refuse(OperationsError, "operations in flight"), OidStartTLS, nil), nil))
		default:
			// The answer goes out in the clear and the next byte read is the
			// client's hello, RFC 4511 § 4.14.1.
			if err := c.write(envelope(msg.id, encodeExtended(Ok, OidStartTLS, nil), nil)); err != nil {
				return true
			}
			tc := tls.Server(c.nc, s.TLS)
			if err := tc.HandshakeContext(ctx); err != nil {
				s.log().Debug("ldap: StartTLS handshake failed", slog.String("err", err.Error()))

				return true
			}
			c.mu.Lock()
			c.nc = tc
			c.tls = true
			c.mu.Unlock()
		}
	case OidWhoAmI:
		// RFC 4532: the authorization identity, `dn:` and the DN, or empty for
		// anonymous. Free, and what a client uses to check a bind took.
		dn, ok := c.Bound()
		var v []byte
		if ok {
			v = []byte("dn:" + dn)
		}
		_ = c.write(envelope(msg.id, encodeExtended(Ok, "", v), nil))
	case OidPasswordModify:
		_ = c.write(envelope(msg.id, encodeExtended(Refuse(UnwillingToPerform, "passwords are changed at the account app"), "", nil), nil))
	default:
		_ = c.write(envelope(msg.id, encodeExtended(Refuse(ProtocolError, fmt.Sprintf("unknown extended operation %s", name)), "", nil), nil))
	}

	return false
}
