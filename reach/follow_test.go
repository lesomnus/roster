package reach_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
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

// opened makes a marker and flips it until the follower is known to hear:
// there is nothing to say a stream has subscribed, and a write before it has
// is heard by nobody.
func opened(t *testing.T, e *external, h *reach.Holders, d door) *marker {
	t.Helper()
	m := &marker{h: h, d: d, who: addPerson(t, e.s, e.acme, "canary"), role: d.role(t, "canary", get)}
	require.ErrorIs(t, h.May(t.Context(), m.who, get), reach.ErrDenied)
	require.Eventually(t, func() bool {
		if m.bound != nil {
			_, err := d.eraseBinding(d.ctx, m.bound)
			require.NoError(t, err)
		}
		m.bound = d.bind(t, m.role, m.who, nil)
		time.Sleep(50 * time.Millisecond)

		return h.May(t.Context(), m.who, get) == nil
	}, 10*time.Second, 10*time.Millisecond, "the follower never heard anything")

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

	opened(t, e, h, d)
}
