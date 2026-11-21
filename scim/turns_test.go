package scim_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// guard is how long a test waits for what it expects before it says it did
// not come. It times nothing: an answer that comes is right however soon, and
// one held for good is what the guard is for.
const guard = 20 * time.Second

// gate holds the calls of roster made under one key at a method, once it is
// shut, until it is opened: what a request is doing while it holds whatever
// it holds.
type gate struct {
	mu     sync.Mutex
	method string
	key    string
	shut   bool

	// arrived is told of each call held.
	arrived chan struct{}
	open    chan struct{}
}

func newGate() *gate {
	return &gate{arrived: make(chan struct{}, 64), open: make(chan struct{})}
}

// close holds every call of method made under key from now on.
func (g *gate) close(method, key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.method, g.key, g.shut = method, key, true
}

// release lets go of what is held, and holds nothing after.
func (g *gate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shut {
		g.shut = false
		close(g.open)
	}
}

func (g *gate) holds(ctx context.Context, method string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.shut || method != g.method {
		return false
	}
	md, _ := metadata.FromIncomingContext(ctx)

	return slices.Contains(md.Get("authorization"), "Bearer "+g.key)
}

func (g *gate) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	if g.holds(ctx, info.FullMethod) {
		g.arrived <- struct{}{}
		<-g.open
	}

	return next(ctx, req)
}

// wait is n calls held.
func (g *gate) wait(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-g.arrived:
		case <-time.After(guard):
			t.Fatalf("%d of %d calls reached roster", i, n)
		}
	}
}

// gated is a second endpoint in front of a second listener of the same
// roster, whose calls pass through g.
func (d *deployment) gated(t *testing.T, g *gate) string {
	t.Helper()
	x := require.New(t)

	s, err := d.s.Grpc(t.Context(), cmd.Config{}, grpc.ChainUnaryInterceptor(g.intercept))
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = s.Serve(l) }()
	t.Cleanup(s.Stop)
	t.Cleanup(g.release)

	e, err := scim.New(scim.Config{Roster: l.Addr().String(), Insecure: true, Log: slog.New(slog.NewTextHandler(d.log, nil))})
	x.NoError(err)
	t.Cleanup(func() { e.Close() })
	front := httptest.NewServer(e)
	t.Cleanup(front.Close)

	return front.URL
}

// another is a second tenant whose directory provisions through a connection
// of its own, and the key that directory presents.
func (d *deployment) another(t *testing.T, alias string) string {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	tn, err := d.s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: alias, Name: alias}.Build())
	x.NoError(err)
	at := rstr.TenantRef_builder{Id: tn.GetId()}.Build()
	_, err = d.s.Ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/" + alias + "/v2.0", ClientId: "the-app",
		SubjectClaim: "oid", Provisions: true,
	}.Build())
	x.NoError(err)
	h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "scim"}.Build())
	x.NoError(err)
	role, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: at, Alias: "scim", Methods: scim.Bound()}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	return d.mint(t, h.GetId(), "scim", scim.Methods)
}

// ask is one request of the endpoint at base, answered within the guard or
// not at all: the status, or nought where none came.
func ask(base, key, method, path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	return res.StatusCode
}

// halfway opens a connection to base and says a request whose body is a
// thousand bytes, of which it sends one.
func halfway(t *testing.T, base, authorization, method, path string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	head := ""
	if authorization != "" {
		head = "Authorization: " + authorization + "\r\n"
	}
	_, err = fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: scim\r\n%sContent-Type: application/scim+json\r\nContent-Length: 1000\r\n\r\n{", method, path, head)
	require.NoError(t, err)

	return c
}

// TestABodyIsWaitedForOutsideATurn: a request whose body is slow to come
// holds nothing anybody else waits for.
//
// A body was read after one of the endpoint's turns was taken, and the turns
// were four for the process: four requests under any one tenant's key that
// each sent one byte of a body they declared stopped every tenant's directory
// until they hung up -- a read under the same key and under another tenant's
// had no answer. The body is read once the key is accepted and before a turn
// is taken, so a turn is held only for work.
func TestABodyIsWaitedForOutsideATurn(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)
	other := d.another(t, "fabrikam")

	// Each of the four is held as it asks roster who the key is, so that all
	// four are known to be past that before anything else is asked; then let
	// go to wait for their bodies.
	g := newGate()
	base := d.gated(t, g)
	g.close("/"+rstr.MeService_ServiceDesc.ServiceName+"/Get", d.key)
	for range scim.DefaultConcurrency {
		halfway(t, base, "Bearer "+d.key, http.MethodPatch, scim.Base+"/Users/"+erinId)
	}
	g.wait(t, scim.DefaultConcurrency)
	g.release()

	x.Equal(http.StatusOK, ask(base, d.key, http.MethodGet, scim.Base+"/Users/"+erinId),
		"a read under the key of four bodies that are slow to come")
	x.Equal(http.StatusOK, ask(base, other, http.MethodGet, scim.Base+"/Users"),
		"a read under another tenant's key")
}

// TestOneTenantsRequestsDoNotHoldAnothersTurns: `scim.concurrency` is how many
// requests of one tenant's directory are worked on at once.
//
// The turns were four for the process, so one tenant's directory in the
// middle of a burst -- or a key that leaked, asking slowly -- had every other
// tenant's wait behind it. The reason there are few is that the rate a
// tenant's callers are held to is shared with that tenant's people signing
// in, which is a reason about one tenant; so they are counted by the tenant
// the key is of.
func TestOneTenantsRequestsDoNotHoldAnothersTurns(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)
	other := d.another(t, "fabrikam")

	g := newGate()
	base := d.gated(t, g)

	// Both keys known, so what is held below is work and nothing else.
	x.Equal(http.StatusOK, ask(base, d.key, http.MethodGet, scim.Base+"/Users/"+erinId))
	x.Equal(http.StatusOK, ask(base, other, http.MethodGet, scim.Base+"/Users"))

	g.close("/"+rstr.HolderService_ServiceDesc.ServiceName+"/Get", d.key)
	held := make(chan int, scim.DefaultConcurrency)
	for range scim.DefaultConcurrency {
		go func() { held <- ask(base, d.key, http.MethodGet, scim.Base+"/Users/"+erinId) }()
	}
	g.wait(t, scim.DefaultConcurrency)

	fifth := make(chan int, 1)
	go func() { fifth <- ask(base, d.key, http.MethodGet, scim.Base+"/Users/"+erinId) }()

	x.Equal(http.StatusOK, ask(base, other, http.MethodGet, scim.Base+"/Users"),
		"another tenant's read, while this one's four turns are held")
	select {
	case code := <-fifth:
		x.Failf("a fifth request of the same tenant was worked on beside its four", "answered %d", code)
	default:
	}

	g.release()
	x.Equal(http.StatusOK, <-fifth, "the fifth, once a turn is let go")
	for range scim.DefaultConcurrency {
		x.Equal(http.StatusOK, <-held)
	}
}

// TestNothingOfABodyIsReadUnderNoKey: a request that is refused before its
// key is accepted is answered at once, and its body is not waited for.
//
// Nothing of a body is read under no key or under a key nobody has: they are
// `401`. The handler read none, and `net/http` read what was left of a
// declared body before it wrote the answer, so a request that declared one
// and sent a byte of it was neither answered nor let go. The answer says the
// connection ends, and it does.
func TestNothingOfABodyIsReadUnderNoKey(t *testing.T) {
	d := stand(t)
	nobody := "Bearer rt_" + strings.Repeat("A", 43)

	for _, tc := range []struct{ desc, authorization string }{
		{"no key", ""},
		{"another scheme", "Basic c2NpbTpzY2lt"},
		{"a key nobody has", nobody},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)
			c := halfway(t, d.scim.URL, tc.authorization, http.MethodPatch, scim.Base+"/Users/"+strings.Repeat("0", 26))
			x.NoError(c.SetReadDeadline(time.Now().Add(guard)))

			res, err := http.ReadResponse(bufio.NewReader(c), nil)
			x.NoError(err, "no answer while the body was still to come")
			x.Equal(http.StatusUnauthorized, res.StatusCode)
			x.True(res.Close, "the connection is said to end")
			_, err = io.Copy(io.Discard, res.Body)
			x.NoError(err)
			res.Body.Close()

			_, err = c.Read(make([]byte, 1))
			x.ErrorIs(err, io.EOF, "the connection was kept for the rest of the body")
		})
	}
}

// TestABodyPastTheBoundIs413: a body longer than a megabyte is refused as one
// that is too large, the bound said (RFC 7644 § 3.12). It was `400
// invalidSyntax`, which is what a body that is not JSON is told; one cut
// short still is.
func TestABodyPastTheBoundIs413(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	code, got, h := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", strings.Repeat(" ", 1<<20)+"{}")
	x.Equal(http.StatusRequestEntityTooLarge, code, "%v", got)
	x.Equal("413", got["status"])
	x.Contains(got["detail"], "megabyte")
	x.Equal("application/scim+json", h.Get("Content-Type"))
	x.Nil(got["scimType"])

	// Cut short: the connection says a hundred bytes, sends ten and is done.
	c, err := net.Dial("tcp", d.scim.Listener.Addr().String())
	x.NoError(err)
	defer c.Close()
	_, err = fmt.Fprintf(c, "POST %s/Users HTTP/1.1\r\nHost: scim\r\nAuthorization: Bearer %s\r\nContent-Type: application/scim+json\r\nContent-Length: 100\r\n\r\n{\"userName\"", scim.Base, d.key)
	x.NoError(err)
	x.NoError(c.(*net.TCPConn).CloseWrite())
	x.NoError(c.SetReadDeadline(time.Now().Add(guard)))
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadRequest, res.StatusCode)
}
