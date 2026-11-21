package standing_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/standing"
)

// heard makes a write once and returns when the stream has said what was
// written of `who`. It is made once the follower listens
// ([external.listening]): a write made before roster has subscribed the stream
// is heard by nobody.
func heard(t *testing.T, got <-chan standing.Standing, who pdid.Id, write func() error, ok func(standing.Standing) bool) standing.Standing {
	t.Helper()
	require.NoError(t, write())
	deadline := time.After(10 * time.Second)
	for {
		select {
		case v := <-got:
			if v.Holder == who && ok(v) {
				return v
			}
		case <-deadline:
			t.Fatal("nothing about them came down the stream")
		}
	}
}

// follow runs [standing.Accounts.Follow] until the test ends, and is what it
// handed on.
func follow(t *testing.T, a *standing.Accounts) <-chan standing.Standing {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	got := make(chan standing.Standing, 64)
	go func() {
		defer close(done)
		_ = a.Follow(ctx, func(_ context.Context, v standing.Standing) error {
			select {
			case got <- v:
			default:
			}
			return nil
		})
	}()

	return got
}

// TestWhatRosterSaysArrivesAsItIsSaid: what roster says of an account comes
// down its stream as it is said -- signed out everywhere, suspended, erased --
// to an app with a roster of its own, and what this process knew of the
// account is dropped with it: a session held to roster is refused at once,
// not once HeldTtl is over.
func TestWhatRosterSaysArrivesAsItIsSaid(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	// A clock that does not move, so that nothing known runs out on its own.
	c := &clock{now: time.Now()}
	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme"), Now: c.Now})
	got := follow(t, a)
	e.listening(t)
	op, as := e.asOperator(t)
	ref := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	v := heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: ref}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	x.Equal(e.acme, v.Tenant)
	x.True(v.Disabled.IsZero())
	x.True(v.Erased.IsZero())

	// Believed, and then suspended: heard, and the next call asks.
	after := time.Now().Add(time.Second)
	x.NoError(a.Held(ctx, e.kim, after))
	v = heard(t, got, e.kim, func() error {
		_, err := op.Disable(as, rstr.HolderDisableRequest_builder{Ref: ref}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Disabled.IsZero() })
	x.False(v.Good(time.Now()))
	x.ErrorIs(a.Held(ctx, e.kim, after), standing.ErrRefused, "what the stream said, before HeldTtl was over")

	v = heard(t, got, e.kim, func() error {
		_, err := op.Erase(as, ref)
		return err
	}, func(v standing.Standing) bool { return !v.Erased.IsZero() })
	x.Equal(e.acme, v.Tenant)
}

// TestAnErasureIsHeardOfSomebodyNothingElseHappenedTo: most people's erasure
// is the first thing roster's stream hears of them since the app connected --
// nothing was written about them before -- and it is heard all the same, with
// when roster erased them (roster#104). Somebody erased in a tenant the app
// does not serve is not.
func TestAnErasureIsHeardOfSomebodyNothingElseHappenedTo(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)

	// Made before the stream opens, through roster's server in this process,
	// which publishes nothing: the stream has never read them.
	quiet := addPerson(t, e.s, e.acme, "quiet")
	elsewhere := addPerson(t, e.s, e.beta, "elsewhere")

	got := follow(t, accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}))
	e.listening(t)
	op, as := e.asOperator(t)

	// The one the app may not hear of first, so that the other arriving makes
	// its absence a fact rather than a race.
	_, err := op.Erase(as, rstr.HolderRef_builder{Id: elsewhere.Bytes()}.Build())
	x.NoError(err)
	_, err = op.Erase(as, rstr.HolderRef_builder{Id: quiet.Bytes()}.Build())
	x.NoError(err)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case v := <-got:
			x.NotEqual(elsewhere, v.Holder, "an erasure in a tenant the app does not serve")
			if v.Holder != quiet {
				continue
			}
			x.False(v.Erased.IsZero(), "heard, and as an erasure")
			x.Equal(e.acme, v.Tenant)
			return
		case <-deadline:
			t.Fatal("the erasure of somebody nothing else happened to was not heard")
		}
	}
}

// TestADirectoryClosedShutsOutEveryoneInIt: roster closing the directory an
// app's accounts are in is heard as an event about each of them, with when --
// and is no session for any of them -- until roster restores it, which is
// heard too (roster#105).
func TestADirectoryClosedShutsOutEveryoneInIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})
	got := follow(t, a)
	op, as := e.asOperator(t)
	tenants := rstr.NewTenantServiceClient(e.dial(t))
	ref := rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build()
	kim := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()
	invalidate := func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: kim}.Build())
		return err
	}

	e.listening(t)
	began := time.Now()
	x.NoError(a.Held(ctx, e.kim, began))

	// The stream reads a tenant when it moves, and a tenant it had seen
	// before closing is a closing it names for everybody in it.
	was, err := tenants.Get(as, rstr.TenantGetRequest_builder{Ref: ref, Select: rstr.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build()}.Build())
	x.NoError(err)
	_, err = tenants.Update(as, rstr.TenantUpdateRequest_builder{Ref: ref, DateUpdated: was.GetDateUpdated(), Desc: proto.String("seen")}.Build())
	x.NoError(err)
	v := heard(t, got, e.kim, func() error {
		_, err := tenants.Close(as, rstr.TenantCloseRequest_builder{Ref: ref}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Closed.IsZero() })
	x.Equal(e.acme, v.Tenant)
	x.True(v.Disabled.IsZero(), "a directory closing is not its people suspended")
	x.False(v.Good(time.Now()))
	x.Error(a.Held(ctx, e.kim, began), "a session in a closed directory")

	_, err = tenants.Restore(as, rstr.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	v = heard(t, got, e.kim, invalidate, func(v standing.Standing) bool { return v.Closed.IsZero() })
	x.True(v.Disabled.IsZero())
	x.NoError(a.Held(ctx, e.kim, time.Now()), "a session again, once it is restored")
}

// TestAClosedDirectoryIsTheFourthFactToAKeyOutsideIt: asked as a holder in a
// closed directory, roster answers nothing at all, which is a roster that will
// not answer; asked by a deployment key as itself, from outside every tenant,
// the closing is read with the other three, as the stream reads it.
func TestAClosedDirectoryIsTheFourthFactToAKeyOutsideIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	_, as := e.asOperator(t)
	_, err := rstr.NewTenantServiceClient(e.dial(t)).Close(as, rstr.TenantCloseRequest_builder{
		Ref: rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	inside := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})
	_, err = inside.StandingOf(ctx, e.kim)
	x.Error(err)
	x.NotErrorIs(err, standing.ErrNoPerson, "a closed directory is not its people gone")
	x.ErrorIs(inside.Held(ctx, e.kim, time.Now()), standing.ErrUnreachable)

	reader := stdoutOf(t, cli.NewCmdControl(&e.c), "key", "add", "--allow", rstr.HolderService_Get_FullMethodName, "reader")
	outside := accounts(t, standing.Config{Conn: e.dial(t), As: bearer(reader)})
	st, err := outside.StandingOf(ctx, e.kim)
	x.NoError(err)
	x.False(st.Closed.IsZero(), "the directory's closing")
	x.Equal(e.acme, st.Tenant)
	x.ErrorIs(outside.Held(ctx, e.kim, time.Now()), standing.ErrRefused)
}

// TestAStreamRosterRefusesIsSaidAndThenHeld: an app whose role does not hold
// `SyncService/Watch` is refused roster's stream. That is said once, as an
// error, with what to do -- and doing it is noticed without a restart.
func TestAStreamRosterRefusesIsSaidAndThenHeld(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	e.role(t, slices.DeleteFunc(slices.Clone(methods), func(m string) bool { return m == rstr.SyncService_Watch_FullMethodName }))

	l := &logs{}
	a := accounts(t, standing.Config{
		Conn: e.dial(t), As: at(e.key, "acme"),
		Log:     slog.New(slog.NewTextHandler(l, nil)),
		Refused: 300 * time.Millisecond,
	})
	got := follow(t, a)

	x.Eventually(func() bool { return strings.Contains(l.String(), "roster role patch") }, 10*time.Second, 20*time.Millisecond)
	x.Contains(l.String(), "level=ERROR")
	x.NotContains(l.String(), "level=WARN", "a refusal is not a stream that dropped")

	e.role(t, methods)
	e.listening(t)
	op, as := e.asOperator(t)
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
}

// TestARosterThatPublishesNothingIsSaidOnce: the roster an app runs in its own
// process has no broker, so it has no stream -- and says so, which is said
// once as an error and asked about again only after Refused, rather than as a
// stream that keeps dropping.
func TestARosterThatPublishesNothingIsSaidOnce(t *testing.T) {
	x := require.New(t)
	em := newEmbedded(t)

	l := &logs{}
	follow(t, accounts(t, standing.Config{Conn: em.dial(t), As: em.as, Log: slog.New(slog.NewTextHandler(l, nil))}))

	x.Eventually(func() bool { return strings.Contains(l.String(), "watch.broker") }, 10*time.Second, 20*time.Millisecond)
	x.Contains(l.String(), "level=ERROR")
	time.Sleep(200 * time.Millisecond)
	x.Equal(1, strings.Count(l.String(), "level=ERROR"), "said once, and not asked again within Refused")
	x.NotContains(l.String(), "level=WARN")
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

// TestAStreamOpenedAgainForgetsWhatWasKnown: a stream that ended missed
// things, so when it is open again nothing known before is believed -- here, a
// suspension written where no stream hears it, while the stream was down.
func TestAStreamOpenedAgainForgetsWhatWasKnown(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	d := &dropping{}
	c := &clock{now: time.Now()}
	a := accounts(t, standing.Config{Conn: e.dial(t, d.option()), As: at(e.key, "acme"), Now: c.Now})
	got := follow(t, a)
	e.listening(t)
	op, as := e.asOperator(t)

	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	after := time.Now().Add(time.Second)
	x.NoError(a.Held(ctx, e.kim, after))

	// Through roster's own server, which publishes nothing: no stream says it.
	_, err := e.s.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
	x.NoError(err)
	x.NoError(a.Held(ctx, e.kim, after), "believed, as nothing said otherwise and the clock has not moved")

	d.cut()
	x.Eventually(func() bool { return d.opens() > 1 }, 10*time.Second, 20*time.Millisecond, "opened again")
	x.Eventually(func() bool { return errors.Is(a.Held(ctx, e.kim, after), standing.ErrRefused) }, 10*time.Second, 20*time.Millisecond,
		"what was known before the stream was open again")
}

// TestTheFollowerIsALoopEveryProcessRuns: the follower is a named loop, and
// marked to run in every process it runs in one that holds no lease -- which
// is each process but one, under `spin.lease`.
func TestTheFollowerIsALoopEveryProcessRuns(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})

	got := make(chan standing.Standing, 64)
	loop := a.Follower("test.roster-stream", func(_ context.Context, v standing.Standing) error {
		select {
		case got <- v:
		default:
		}
		return nil
	})
	n, ok := loop.(spin.Named)
	x.True(ok)
	x.Equal("test.roster-stream", n.SpinName())

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- spin.Run(ctx, slices.Values([]spin.Spinner{spin.InEveryProcess(loop)}), spin.WithLease(spin.Nowhere()))
	}()
	t.Cleanup(func() {
		stop()
		x.NoError(<-done, "the loop stopped as asked")
	})

	e.listening(t)
	op, as := e.asOperator(t)
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
}

// TestWhatSomebodyMayCallIsNoNewsOfTheirStanding: roster's stream says when
// what somebody may call may have changed (`SYNC_REASON_REACH`), which is
// `reach`'s to act on. None of the four facts on such an event is news, so the
// follower drops nothing it knew for it and hands `on` nothing -- or a role
// edit would be a standing asked again, and `on` run, for everybody the role
// reaches.
func TestWhatSomebodyMayCallIsNoNewsOfTheirStanding(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	as := at(e.key, "acme")
	c := &cutting{}
	a := accounts(t, standing.Config{Conn: e.dial(t, c.option()), As: as, Now: (&clock{now: time.Now()}).Now})
	got := follow(t, a)
	op, opAs := e.asOperator(t)
	canary := addPerson(t, e.s, e.acme, "canary")
	poke := func(who pdid.Id) func() error {
		return func() error {
			_, err := op.Invalidate(opAs, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: who.Bytes()}.Build()}.Build())
			return err
		}
	}

	// The stream is open, and kim's standing is known.
	e.listening(t)
	heard(t, got, e.kim, poke(e.kim), func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	x.NoError(a.Held(ctx, e.kim, time.Now().Add(time.Second)))
	asked := c.asked.Load()

	// And another stream, to see what roster says: a role bound to kim, by
	// acme's administrator over the wire.
	raw, err := rstr.NewSyncServiceClient(e.dial(t)).Watch(func() context.Context { v, _ := as(ctx); return v }(), rstr.SyncWatchRequest_builder{}.Build())
	x.NoError(err)
	md, err := raw.Header()
	x.NoError(err)
	x.NotNil(md, "the second stream never listened")
	events := make(chan *rstr.SyncEvent, 64)
	go func() {
		for {
			v, err := raw.Recv()
			if err != nil {
				return
			}
			events <- v
		}
	}()
	admin := auth.BearerProvider(e.admin).Provide(ctx)
	role, err := rstr.NewRoleServiceClient(e.dial(t)).Add(admin, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build(), Alias: "reader", Methods: []string{rstr.HolderService_Get_FullMethodName},
	}.Build())
	x.NoError(err)
	_, err = rstr.NewBindingServiceClient(e.dial(t)).Add(admin, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	for v := range events {
		if string(v.GetHolder()) == string(e.kim.Bytes()) {
			x.Equal(rstr.SyncReason_SYNC_REASON_REACH, v.GetReason(), "what roster said of kim")
			break
		}
	}

	// The follower is past it once three pokes made after it have arrived,
	// each known by the moment it wrote: the first may have been taken with
	// it, and the stream reads what it took in no order.
	for range 3 {
		res, err := op.Invalidate(opAs, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: canary.Bytes()}.Build()}.Build())
		x.NoError(err)
		for done := false; !done; {
			select {
			case v := <-got:
				x.NotEqual(e.kim, v.Holder, "a role bound to kim was handed on as news of their standing")
				done = v.Holder == canary && !v.Invalidated.Before(res.GetDateInvalidated().AsTime())
			case <-time.After(10 * time.Second):
				t.Fatal("the follower stopped hearing anything")
			}
		}
	}
	x.NoError(a.Held(ctx, e.kim, time.Now().Add(time.Second)))
	x.Equal(asked, c.asked.Load(), "kim's standing was dropped, and asked again, for a role")
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

// TestTheFollowerForgetsWhenTheStreamListens: what was known is forgotten
// when roster says the stream listens, and not when `Watch` returns, which is
// before roster has subscribed anything. Forgotten then, an answer asked for
// and kept in between was believed for HeldTtl over a suspension written in
// between, which no stream heard: Held answered nil with roster saying
// suspended; right is ErrRefused.
func TestTheFollowerForgetsWhenTheStreamListens(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	road := newLateRoad(t, e.addr)
	waits := make(chan struct{})
	slow, err := grpc.NewClient(road.addr, grpc.WithTransportCredentials(insecure.NewCredentials()), waiting(waits))
	x.NoError(err)
	t.Cleanup(func() { slow.Close() })

	clk := &clock{now: time.Now()}
	a := accounts(t, standing.Config{Conn: streamsApart{calls: e.dial(t), streams: slow}, As: at(e.key, "acme"), Now: clk.Now})
	follow(t, a)
	select {
	case <-waits:
	case <-time.After(10 * time.Second):
		t.Fatal("the follower never opened its stream")
	}

	// Asked, and kept, while the stream's opening is on its way.
	began := time.Now()
	x.NoError(a.Held(ctx, e.kim, began))
	op, as := e.asOperator(t)
	_, err = op.Disable(as, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
	x.NoError(err)

	close(road.open)
	x.Eventually(func() bool { return errors.Is(a.Held(ctx, e.kim, began), standing.ErrRefused) }, 10*time.Second, 20*time.Millisecond,
		"what was kept before the stream listened")
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

// newScripted serves a [scripted] roster on a listener in memory, and dials it.
func newScripted(t *testing.T, more ...func(grpc.ServiceRegistrar)) (*scripted, *grpc.ClientConn) {
	t.Helper()
	s := &scripted{events: make(chan *rstr.SyncEvent)}
	g := grpc.NewServer()
	rstr.RegisterSyncServiceServer(g, s)
	for _, f := range more {
		f(g)
	}
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
	s, conn := newScripted(t)
	l := &logs{}
	a := accounts(t, standing.Config{Conn: conn, As: func(ctx context.Context) (context.Context, error) { return ctx, nil },
		Log: slog.New(slog.NewTextHandler(l, nil))})
	got := follow(t, a)

	someone := pdid.New(pdid.Domain(2))
	s.send(t, rstr.SyncEvent_builder{}.Build())
	s.send(t, rstr.SyncEvent_builder{Holder: []byte("not an identifier")}.Build())
	s.send(t, rstr.SyncEvent_builder{Holder: someone.Bytes()}.Build())
	select {
	case v := <-got:
		x.Equal(someone, v.Holder, "the first event handed on")
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was handed on")
	}
	x.Equal(1, strings.Count(l.String(), "level=WARN"), "a line for the one that names somebody unreadable, and none for nobody:\n%s", l.String())
}
