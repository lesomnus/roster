package ldap_test

import (
	"net"
	"sync"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/roster/ldap"
	"github.com/lesomnus/roster/ldap/wire"
	"github.com/lesomnus/roster/server/front"
)

// rosterRelay is roster as a directory under test dials it: every call is
// passed on to the real one and counted by its method, and answered in
// roster's place where a test says roster is to fail. What a test reads off
// it is how often the directory asked, and what the directory makes of a
// roster that does not answer.
type rosterRelay struct {
	addr string
	g    *grpc.Server
	to   *grpc.ClientConn

	mu    sync.Mutex
	calls map[string]int
	fails map[string]error
}

// relayed stands a relay in front of the deployment's roster.
func (d *deployment) relayed(t *testing.T) *rosterRelay {
	t.Helper()
	x := require.New(t)

	to, err := grpc.NewClient(d.roster,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(asSent{})))
	x.NoError(err)
	t.Cleanup(func() { to.Close() })

	r := &rosterRelay{to: to, calls: map[string]int{}, fails: map[string]error{}}
	r.g = grpc.NewServer(grpc.ForceServerCodec(asSent{}), grpc.UnknownServiceHandler(r.pass))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = r.g.Serve(l) }()
	t.Cleanup(r.g.Stop)
	r.addr = l.Addr().String()

	return r
}

func (r *rosterRelay) pass(_ any, ss grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(ss)

	r.mu.Lock()
	r.calls[method]++
	err := r.fails[method]
	if err == nil {
		err = r.fails[""]
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}

	// The two things a directory says about a call, and nothing of the
	// transport's own.
	ctx := ss.Context()
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, k := range []string{"authorization", front.HeaderAt} {
			for _, v := range md.Get(k) {
				ctx = metadata.AppendToOutgoingContext(ctx, k, v)
			}
		}
	}

	var in, out sent
	if err := ss.RecvMsg(&in); err != nil {
		return err
	}
	if err := r.to.Invoke(ctx, method, &in, &out); err != nil {
		return err
	}

	return ss.SendMsg(&out)
}

// asked is how many calls of a method reached the relay, and of every method
// when none is named.
func (r *rosterRelay) asked(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if method != "" {
		return r.calls[method]
	}
	n := 0
	for _, v := range r.calls {
		n += v
	}

	return n
}

// answers makes the relay answer a method with an error in roster's place,
// every method when none is named, and pass it on again when the error is nil.
func (r *rosterRelay) answers(method string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err == nil {
		delete(r.fails, method)

		return
	}
	r.fails[method] = err
}

// away is roster gone: nothing listens where the directory dials.
func (r *rosterRelay) away() { r.g.Stop() }

// sent is a message as it crossed the wire, and asSent the codec that leaves
// it so: the relay passes on what it does not read.
type sent struct{ b []byte }

type asSent struct{}

func (asSent) Marshal(v any) ([]byte, error) { return v.(*sent).b, nil }

func (asSent) Unmarshal(b []byte, v any) error {
	v.(*sent).b = append([]byte(nil), b...)

	return nil
}

func (asSent) Name() string { return "proto" }

// handClock is a clock a test moves by hand: the time it is, and as much later
// as the test has said. It starts at now and never goes back, so a moment read
// off it is comparable with one roster stamped.
type handClock struct {
	mu    sync.Mutex
	ahead time.Duration
}

func (c *handClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return time.Now().Add(c.ahead)
}

func (c *handClock) pass(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ahead += d
}

// directoryOn stands the directory up in the mode given and answers where it
// listens, for a test that dials it more than once or says what the listener
// offers.
func (d *deployment) directoryOn(t *testing.T, mode ldap.Mode, with func(*ldap.Config), offers func(*wire.Server)) string {
	t.Helper()
	x := require.New(t)

	cfg := ldap.Config{Roster: d.roster, Insecure: true, Key: d.deployed, Bind: mode}
	if with != nil {
		with(&cfg)
	}
	dir, err := ldap.New(t.Context(), cfg)
	x.NoError(err)
	t.Cleanup(func() { dir.Close() })

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	s := &wire.Server{Handler: dir.Handler()}
	if offers != nil {
		offers(s)
	}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { l.Close(); s.Close() })

	return l.Addr().String()
}

// clientOf is one more connection to a directory.
func clientOf(t *testing.T, addr string) *goldap.Conn {
	t.Helper()
	c, err := goldap.DialURL("ldap://" + addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	return c
}

// codeOf is the result code an operation was answered with, and success for
// no error.
func codeOf(t *testing.T, err error) uint16 {
	t.Helper()
	if err == nil {
		return goldap.LDAPResultSuccess
	}
	var le *goldap.Error
	require.ErrorAs(t, err, &le, "not an answer of the directory's")

	return le.ResultCode
}
