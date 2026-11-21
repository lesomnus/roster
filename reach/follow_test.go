package reach_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/reach"
	"github.com/lesomnus/roster/rstr"
)

// follow runs [reach.Holders.Follow] until the test ends.
func follow(t *testing.T, h *reach.Holders) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		_ = h.Follow(ctx)
	}()
}

// until asks `may` until it answers `want`: what the stream drops is asked
// again on the next call, and nothing says when the stream has.
func until(t *testing.T, may func() error, want error, why string) {
	t.Helper()
	require.Eventually(t, func() bool {
		err := may()
		if want == nil {
			return err == nil
		}
		return errors.Is(err, want)
	}, 10*time.Second, 20*time.Millisecond, why)
}

// marker is somebody nobody else asks about, whose role the test binds and
// takes away to know what the follower has heard: with a clock that does not
// move, what is kept of them changes only when the stream drops it.
type marker struct {
	h     *reach.Holders
	d     door
	who   pdid.Id
	role  *rstr.RoleRef
	bound *rstr.BindingRef
}

// opened makes a marker once the follower listens ([external.listening]):
// a write made before roster has subscribed the stream is heard by nobody.
func opened(t *testing.T, e *external, h *reach.Holders, d door) *marker {
	t.Helper()
	m := &marker{h: h, d: d, who: addPerson(t, e.s, e.acme, "canary"), role: d.role(t, "canary", get)}
	e.listening(t)

	return m
}

// flip binds the marker's role, or takes it away, and waits until the
// follower has heard it.
func (m *marker) flip(t *testing.T) {
	t.Helper()
	var want error = reach.ErrDenied
	if m.bound == nil {
		m.bound, want = m.d.bind(t, m.role, m.who, nil), nil
	} else {
		_, err := m.d.eraseBinding(m.d.ctx, m.bound)
		require.NoError(t, err)
		m.bound = nil
	}
	until(t, func() error { return m.h.May(t.Context(), m.who, get) }, want, "the follower stopped hearing anything")
}

// settled returns once the follower has heard every write made before it was
// called. Twice: an event after a write is not the stream having read past
// it, since what it took at once it reads in no order -- the first flip may
// have been taken with them, and the second was written after the first was
// heard.
func (m *marker) settled(t *testing.T) {
	t.Helper()
	m.flip(t)
	m.flip(t)
}

// TestAChangeAtRosterArrivesAsItIsMade: what roster says of what somebody may
// call reaches a call here as it is said -- a role of theirs widened and
// narrowed, their binding taken away, a group they joined, their suspension --
// and not once HeldTtl is over: the clock here does not move.
func TestAChangeAtRosterArrivesAsItIsMade(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	d := e.door(t)
	c := &clock{now: time.Now()}
	h := holders(t, reach.Config{Conn: e.dial(t), As: e.as, Now: c.Now})
	follow(t, h)
	opened(t, e, h, d)

	staff := d.role(t, "staff", get)
	bound := d.bind(t, staff, e.kim, nil)
	until(t, func() error { return h.May(ctx, e.kim, get) }, nil, "a role bound to them")
	x.ErrorIs(h.May(ctx, e.kim, list), reach.ErrDenied)

	_, err := d.widen(d.ctx, rstr.RoleWidenRequest_builder{Ref: staff, Methods: []string{list}}.Build())
	x.NoError(err)
	until(t, func() error { return h.May(ctx, e.kim, list) }, nil, "their role widened")

	_, err = d.narrow(d.ctx, rstr.RoleNarrowRequest_builder{Ref: staff, Methods: []string{list}}.Build())
	x.NoError(err)
	until(t, func() error { return h.May(ctx, e.kim, list) }, reach.ErrDenied, "their role narrowed")
	x.NoError(h.May(ctx, e.kim, get), "and what was left in it")

	_, err = d.eraseBinding(d.ctx, bound)
	x.NoError(err)
	until(t, func() error { return h.May(ctx, e.kim, get) }, reach.ErrDenied, "their binding taken away")

	g, err := d.addGroup(d.ctx, rstr.GroupAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build(), Alias: "bookers",
	}.Build())
	x.NoError(err)
	group := rstr.GroupRef_builder{Id: g.GetId()}.Build()
	_, err = d.addBinding(d.ctx, rstr.BindingAddRequest_builder{Role: d.role(t, "booker", forOthers), Group: group}.Build())
	x.NoError(err)
	x.ErrorIs(h.May(ctx, e.kim, forOthers), reach.ErrDenied, "a group they are not in yet")
	_, err = d.addMember(d.ctx, rstr.GroupMembershipAddRequest_builder{
		Group: group, Holder: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	until(t, func() error { return h.May(ctx, e.kim, forOthers) }, nil, "a group they joined")

	_, err = d.disable(d.ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
	x.NoError(err)
	until(t, func() error { return h.May(ctx, e.kim, forOthers) }, reach.ErrDenied, "suspended")
}

// dropping is a connection whose streams the test can end, as a network does.
type dropping struct {
	mu     sync.Mutex
	opened int
	drop   []context.CancelFunc
}

func (d *dropping) option() grpc.DialOption {
	return grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx, cancel := context.WithCancel(ctx)
		d.mu.Lock()
		d.opened++
		d.drop = append(d.drop, cancel)
		d.mu.Unlock()
		return streamer(ctx, desc, cc, method, opts...)
	})
}

func (d *dropping) opens() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opened
}

func (d *dropping) cut() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range d.drop {
		f()
	}
}

// TestAStreamOpenedAgainForgetsWhatWasKept: a stream that ended missed things,
// so once it is open again nothing kept before is believed -- here, a binding
// taken away where no stream hears it, while the stream was down.
func TestAStreamOpenedAgainForgetsWhatWasKept(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	d := e.door(t)
	cut := &dropping{}
	c := &clock{now: time.Now()}
	h := holders(t, reach.Config{Conn: e.dial(t, cut.option()), As: e.as, Now: c.Now})
	follow(t, h)
	m := opened(t, e, h, d)
	bound := d.bind(t, d.role(t, "staff", get), e.kim, nil)
	m.settled(t)
	x.NoError(h.May(ctx, e.kim, get), "a role bound to them")

	// Through roster's own server, which publishes nothing: no stream says it.
	_, err := e.s.Ungated.Binding().Erase(ctx, bound)
	x.NoError(err)
	x.NoError(h.May(ctx, e.kim, get), "kept, as nothing said otherwise and the clock has not moved")

	cut.cut()
	x.Eventually(func() bool { return cut.opens() > 1 }, 10*time.Second, 20*time.Millisecond, "opened again")
	until(t, func() error { return h.May(ctx, e.kim, get) }, reach.ErrDenied, "what was kept before the stream was open again")
}

// TestAStreamRosterRefusesIsSaid: an app whose role does not hold
// `SyncService/Watch` is refused roster's stream, and the roster an app runs
// in its own process publishes nothing. Each is said once, as an error, with
// what to do -- and is not a stream that keeps dropping.
func TestAStreamRosterRefusesIsSaid(t *testing.T) {
	t.Run("a role without the stream", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		e := newExternal(t)
		role, err := e.s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
			Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
				Alias: proto.String(app + "-app"), Tenant: rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build(),
			}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		_, err = e.s.Ungated.Role().Patch(ctx, rstr.RolePatchRequest_builder{
			Ref:         rstr.RoleRef_builder{Id: role.GetId()}.Build(),
			Methods:     []string{rstr.HolderService_Reaches_FullMethodName},
			DateUpdated: role.GetDateUpdated(),
		}.Build())
		x.NoError(err)

		l := &logs{}
		follow(t, holders(t, reach.Config{Conn: e.dial(t), As: e.as, Log: slog.New(slog.NewTextHandler(l, nil))}))
		x.Eventually(func() bool { return strings.Contains(l.String(), "roster role patch") }, 10*time.Second, 20*time.Millisecond)
		x.Contains(l.String(), "level=ERROR")
		x.NotContains(l.String(), "level=WARN", "a refusal is not a stream that dropped")
	})

	t.Run("a roster that publishes nothing", func(t *testing.T) {
		x := require.New(t)
		em := newEmbedded(t)
		l := &logs{}
		follow(t, holders(t, reach.Config{Conn: em.dial(t), As: em.as, Log: slog.New(slog.NewTextHandler(l, nil))}))
		x.Eventually(func() bool { return strings.Contains(l.String(), "watch.broker") }, 10*time.Second, 20*time.Millisecond)
		time.Sleep(200 * time.Millisecond)
		x.Equal(1, strings.Count(l.String(), "level=ERROR"), "said once, and not asked again within Refused")
		x.NotContains(l.String(), "level=WARN")
	})
}

// TestTheFollowerIsALoopEveryProcessRuns: the follower is a named loop, and
// marked to run in every process it runs in one that holds no lease.
func TestTheFollowerIsALoopEveryProcessRuns(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	d := e.door(t)
	c := &clock{now: time.Now()}
	h := holders(t, reach.Config{Conn: e.dial(t), As: e.as, Now: c.Now})

	loop := h.Follower("test.roster-reach")
	n, ok := loop.(spin.Named)
	x.True(ok)
	x.Equal("test.roster-reach", n.SpinName())

	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- spin.Run(run, slices.Values([]spin.Spinner{spin.InEveryProcess(loop)}), spin.WithLease(spin.Nowhere()))
	}()
	t.Cleanup(func() {
		stop()
		x.NoError(<-done, "the loop stopped as asked")
	})

	opened(t, e, h, d).flip(t)
}

// lateRoad is a TCP path in front of roster that holds everything the client
// sends until `open` is closed: a stream's opening that is slow to reach
// roster, as it is where the stream and the calls reach different replicas.
type lateRoad struct {
	addr string
	open chan struct{}
}

func newLateRoad(t *testing.T, to string) *lateRoad {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	r := &lateRoad{addr: l.Addr().String(), open: make(chan struct{})}

	var mu sync.Mutex
	var conns []net.Conn
	keep := func(c net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		conns = append(conns, c)
	}
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	pipe := func(dst, src net.Conn, held bool) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if err != nil {
				return
			}
			if held {
				<-r.open
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			keep(c)
			d, err := net.Dial("tcp", to)
			if err != nil {
				c.Close()
				continue
			}
			keep(d)
			go pipe(d, c, true)
			go pipe(c, d, false)
		}
	}()

	return r
}

// streamsApart sends the stream one way and the calls another, as a channel
// balanced over two replicas of roster does.
type streamsApart struct{ calls, streams grpc.ClientConnInterface }

func (s streamsApart) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	return s.calls.Invoke(ctx, method, args, reply, opts...)
}

func (s streamsApart) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return s.streams.NewStream(ctx, desc, method, opts...)
}

// waiting is a connection that says when the follower first waits on a stream
// it opened: for its header, or for its first event.
func waiting(began chan<- struct{}) grpc.DialOption {
	var once sync.Once
	return grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		s, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}
		return &waited{ClientStream: s, began: func() { once.Do(func() { close(began) }) }}, nil
	})
}

type waited struct {
	grpc.ClientStream
	began func()
}

func (s *waited) Header() (metadata.MD, error) {
	s.began()
	return s.ClientStream.Header()
}

func (s *waited) RecvMsg(m any) error {
	s.began()
	return s.ClientStream.RecvMsg(m)
}

// TestTheFollowerForgetsWhenTheStreamListens: what was kept is forgotten when
// roster says the stream listens, and not when `Watch` returns, which is
// before roster has subscribed anything. Forgotten then, an answer asked for
// and kept in between was believed for HeldTtl over a binding taken away in
// between, which no stream heard: May answered nil with roster saying
// nothing; right is ErrDenied.
func TestTheFollowerForgetsWhenTheStreamListens(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	d := e.door(t)
	bound := d.bind(t, d.role(t, "staff", get), e.kim, nil)
	road := newLateRoad(t, e.addr)
	waits := make(chan struct{})
	slow, err := grpc.NewClient(road.addr, grpc.WithTransportCredentials(insecure.NewCredentials()), waiting(waits))
	x.NoError(err)
	t.Cleanup(func() { slow.Close() })

	c := &clock{now: time.Now()}
	h := holders(t, reach.Config{Conn: streamsApart{calls: e.dial(t), streams: slow}, As: e.as, Now: c.Now})
	follow(t, h)
	select {
	case <-waits:
	case <-time.After(10 * time.Second):
		t.Fatal("the follower never opened its stream")
	}

	// Asked, and kept, while the stream's opening is on its way.
	x.NoError(h.May(ctx, e.kim, get))
	_, err = d.eraseBinding(d.ctx, bound)
	x.NoError(err)

	close(road.open)
	until(t, func() error { return h.May(ctx, e.kim, get) }, reach.ErrDenied, "what was kept before the stream listened")
}

// scripted is a roster whose stream sends what the test hands it, once it has
// said it listens.
type scripted struct {
	rstr.UnimplementedSyncServiceServer
	events chan *rstr.SyncEvent
}

func (s *scripted) Watch(_ *rstr.SyncWatchRequest, out grpc.ServerStreamingServer[rstr.SyncEvent]) error {
	if err := out.SendHeader(nil); err != nil {
		return err
	}
	for {
		select {
		case <-out.Context().Done():
			return nil
		case v := <-s.events:
			if err := out.Send(v); err != nil {
				return err
			}
		}
	}
}

// reaching is what a scripted roster answers of what somebody may call:
// whatever `may` holds, and a count of the askings.
type reaching struct {
	rstr.UnimplementedHolderServiceServer
	asked atomic.Int32
	may   []string
}

func (r *reaching) Reaches(_ context.Context, req *rstr.HolderReachesRequest) (*rstr.HolderReachesResponse, error) {
	r.asked.Add(1)
	return rstr.HolderReachesResponse_builder{Everywhere: r.may}.Build(), nil
}

// newScripted serves a [scripted] roster on a listener in memory, and dials it.
func newScripted(t *testing.T, r *reaching) (*scripted, *grpc.ClientConn) {
	t.Helper()
	s := &scripted{events: make(chan *rstr.SyncEvent)}
	g := grpc.NewServer()
	rstr.RegisterSyncServiceServer(g, s)
	rstr.RegisterHolderServiceServer(g, r)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///scripted",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return s, conn
}

// send hands the stream an event, and returns once it is sent.
func (s *scripted) send(t *testing.T, v *rstr.SyncEvent) {
	t.Helper()
	select {
	case s.events <- v:
	case <-time.After(10 * time.Second):
		t.Fatal("nobody is listening to the stream")
	}
}

// TestAnEventAboutNobodyIsPassedBy: an event with no holder is about nobody,
// and is passed by without a line, so that roster may one day send one as a
// sign of life on an idle stream and no follower built on this release
// complains of it; it was a warning. An event that names somebody this does
// not read as an identifier is still one.
func TestAnEventAboutNobodyIsPassedBy(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	r := &reaching{may: []string{get}}
	s, conn := newScripted(t, r)
	l := &logs{}
	c := &clock{now: time.Now()}
	h := holders(t, reach.Config{Conn: conn, As: func(ctx context.Context) (context.Context, error) { return ctx, nil },
		Log: slog.New(slog.NewTextHandler(l, nil)), Now: c.Now})
	follow(t, h)

	someone := pdid.New(pdid.Domain(2))
	x.NoError(h.May(ctx, someone, get))
	x.Equal(int32(1), r.asked.Load())
	s.send(t, rstr.SyncEvent_builder{}.Build())
	s.send(t, rstr.SyncEvent_builder{Holder: []byte("not an identifier")}.Build())
	s.send(t, rstr.SyncEvent_builder{Holder: someone.Bytes()}.Build())
	// Read in the order they were sent: once the unreadable one's line is
	// there, the one about nobody has been read and said nothing. Not told
	// by `asked`, which a follower forgetting what it held when the stream
	// began to listen moves too, whenever that lands beside the first `May`.
	x.Eventually(func() bool { return strings.Count(l.String(), "level=WARN") >= 1 }, 10*time.Second, 10*time.Millisecond,
		"the event that names somebody unreadable was not read")
	x.Eventually(func() bool { return h.May(ctx, someone, get) == nil && r.asked.Load() >= 2 }, 10*time.Second, 10*time.Millisecond,
		"the event about somebody was not heard")
	x.Equal(1, strings.Count(l.String(), "level=WARN"), "a line for the one that names somebody unreadable, and none for nobody:\n%s", l.String())
}
