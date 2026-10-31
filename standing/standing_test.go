package standing_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/standing"
)

// TestAStandingIsTheFourFactsAndWhenTheyBegan: a session is good when it began
// after the last time everything issued was voided, and while nothing shuts the
// account out -- suspended, erased, or a directory closed.
func TestAStandingIsTheFourFactsAndWhenTheyBegan(t *testing.T) {
	x := require.New(t)
	now := time.Now()

	x.True(standing.Standing{}.Good(now), "nothing ever happened to them")
	x.True(standing.Standing{}.Since().IsZero())

	signedOut := standing.Standing{Invalidated: now}
	x.False(signedOut.Good(now.Add(-time.Second)), "a session from before signing out everywhere")
	x.True(signedOut.Good(now), "and one from the moment of it, or after")

	for name, v := range map[string]standing.Standing{
		"suspended": {Disabled: now.Add(-time.Hour)},
		"erased":    {Erased: now.Add(-time.Hour)},
		"closed":    {Closed: now.Add(-time.Hour)},
	} {
		x.False(v.Good(now), "%s, and a session that began after it", name)
	}

	x.Equal(now, standing.Standing{Invalidated: now.Add(-time.Hour), Disabled: now, Closed: now.Add(-time.Minute)}.Since(), "the latest of them")
}

// TestAnAppSaysWhichRosterAndWhoItIsThere: neither is a default.
func TestAnAppSaysWhichRosterAndWhoItIsThere(t *testing.T) {
	x := require.New(t)
	conn := newEmbedded(t).dial(t)
	as := func(ctx context.Context) (context.Context, error) { return ctx, nil }

	_, err := standing.New(standing.Config{As: as})
	x.ErrorContains(err, "Conn")
	_, err = standing.New(standing.Config{Conn: conn})
	x.ErrorContains(err, "As")
	_, err = standing.New(standing.Config{Conn: conn, As: as, Refused: -time.Second})
	x.ErrorContains(err, "Refused")
	_, err = standing.New(standing.Config{Conn: conn, As: as})
	x.NoError(err)
}

// TestAnAccountIsHeldToWhatRosterSaysOfItOnEitherRoster: what an app asks of
// roster, and holds a session to, is the same whichever roster its accounts are
// in -- signed out everywhere and suspended reaching a session -- and a
// suspension nobody told it of reaches a session already open no later than
// HeldTtl after roster was last asked.
func TestAnAccountIsHeldToWhatRosterSaysOfItOnEitherRoster(t *testing.T) {
	for _, r := range bothRosters(t) {
		t.Run(r.name, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			c := &clock{now: time.Now()}
			a := accounts(t, standing.Config{Conn: r.dial(t), As: r.as, Now: c.Now})

			st, err := a.StandingOf(ctx, r.account)
			x.NoError(err)
			x.Equal(standing.Standing{Holder: r.account, Tenant: r.tenant}, st, "nothing has happened to them")
			nobody := pdid.New(pdid.Domain(2))
			_, err = a.StandingOf(ctx, nobody)
			x.ErrorIs(err, standing.ErrNoPerson)
			x.ErrorIs(a.Held(ctx, nobody, time.Now()), standing.ErrRefused, "a session of nobody")

			// A session, from now on.
			began := time.Now()
			x.NoError(a.Held(ctx, r.account, began))

			// Signed out everywhere, and asked again: the sessions from
			// before end, and one after is good.
			x.NoError(r.invalidate())
			a.Recheck(r.account)
			x.ErrorIs(a.Held(ctx, r.account, began), standing.ErrRefused, "a session from before")
			after := time.Now().Add(time.Second)
			x.NoError(a.Held(ctx, r.account, after), "and one that began after it")

			// Suspended, and nobody told: believed as roster last said for
			// HeldTtl, and refused once that is over.
			x.NoError(r.suspend())
			c.Add(standing.HeldTtl - time.Second)
			x.NoError(a.Held(ctx, r.account, after), "what roster said last, within HeldTtl")
			c.Add(2 * time.Second)
			x.ErrorIs(a.Held(ctx, r.account, after), standing.ErrRefused, "suspended, and no later than HeldTtl")

			st, err = a.StandingOf(ctx, r.account)
			x.NoError(err)
			x.False(st.Invalidated.IsZero())
			x.False(st.Disabled.IsZero())
			x.True(st.Erased.IsZero())
		})
	}
}

// TestAStandingIsNoReadOfSomebodysData: what an account is held to is four
// stamps, and asking for them reads nothing declared personal -- so a session
// held to roster every HeldTtl is not, every HeldTtl, a row of roster's access
// record saying the app read that person. A read of the whole row is.
func TestAStandingIsNoReadOfSomebodysData(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})

	_, err := a.StandingOf(ctx, e.kim)
	x.NoError(err)
	x.Zero(e.s.Access.Pending(), "a standing read as a read of the person")

	// What was asked before this: the whole row.
	as, err := at(e.key, "acme")(ctx)
	x.NoError(err)
	_, err = rstr.NewHolderServiceClient(e.dial(t)).Get(as, rstr.HolderGetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build(),
		Select: rstr.HolderSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(1, e.s.Access.Pending(), "the whole row, which is a read of the person")
}

// cutting is roster as the app asks it, over a connection of its own: one that
// counts every asking, answers none while roster is `down`, and ends the call
// that asks -- as a page left half loaded ends it, or as it runs out of its own
// time -- at the asking `cut` names.
type cutting struct {
	asked atomic.Int32
	down  atomic.Bool
	cut   atomic.Pointer[cut]
}

// cut is where the next call that asks roster ends, and how: at an asking of
// `method`, or at its first for "".
type cut struct {
	method string
	end    func()
}

func (c *cutting) option() grpc.DialOption {
	return grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, res any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		c.asked.Add(1)
		if v := c.cut.Load(); v != nil && (v.method == "" || v.method == method) && c.cut.CompareAndSwap(v, nil) {
			v.end()
		}
		if c.down.Load() {
			return status.Error(codes.Unavailable, "connection refused")
		}
		return invoke(ctx, method, req, res, cc, opts...)
	})
}

// TestACallThatEndsWhileRosterIsAskedSaysNothingOfRoster: a call that ends
// while roster is being asked about its account -- its page was left half
// loaded, or it ran out of its own time -- is not roster failing to answer, and
// the account's next call is served at once. Kept as roster's failing, it
// refused every call the account made for HeldRetry: two of Loft's browser
// tests that had nothing to do with each other failed on it. So whatever the
// call was cut short asking -- what the app asks to know who it is there, or
// the account -- and on either roster.
//
// A roster that cannot be asked is still a refusal, and is still not asked
// again for HeldRetry.
func TestACallThatEndsWhileRosterIsAskedSaysNothingOfRoster(t *testing.T) {
	for _, r := range bothRosters(t) {
		t.Run(r.name, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			c := &cutting{}
			conn := r.dial(t, c.option())
			clk := &clock{now: time.Now()}
			a := accounts(t, standing.Config{Conn: conn, As: asking(conn, r.as, r.tenant), Now: clk.Now})
			began := time.Now().Add(time.Second)

			// Left half loaded: at the first thing it asks roster, which is
			// the app's own, and at asking about the account.
			for _, at := range []string{"", rstr.HolderService_Get_FullMethodName} {
				a.Recheck(r.account)
				cctx, leave := context.WithCancel(ctx)
				c.cut.Store(&cut{method: at, end: leave})
				x.ErrorIs(a.Held(cctx, r.account, began), context.Canceled, "cut short at %q", at)
				x.Nil(c.cut.Load(), "it ended asking roster")
				x.NoError(a.Held(ctx, r.account, began), "the next call, cut short at %q", at)
			}

			// Out of its own time.
			a.Recheck(r.account)
			cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			c.cut.Store(&cut{method: rstr.HolderService_Get_FullMethodName, end: func() { <-cctx.Done() }})
			x.ErrorIs(a.Held(cctx, r.account, began), context.DeadlineExceeded)
			x.NoError(a.Held(ctx, r.account, began), "the next call, after one that ran out of time")

			// Roster's own failing is a refusal, and kept as one.
			a.Recheck(r.account)
			c.down.Store(true)
			x.ErrorIs(a.Held(ctx, r.account, began), standing.ErrUnreachable)
			c.down.Store(false)
			asked := c.asked.Load()
			x.ErrorIs(a.Held(ctx, r.account, began), standing.ErrUnreachable, "within HeldRetry, as it was")
			x.Equal(asked, c.asked.Load(), "roster asked again within HeldRetry")
			clk.Add(standing.HeldRetry)
			x.NoError(a.Held(ctx, r.account, began), "and asked again once it is over")
		})
	}
}

// TestWhatIsDroppedWhileRosterIsAskedIsNotKept: an account dropped while a
// call is asking roster about it -- the stream said something new of them --
// is asked about again at the next call. What roster answered the call that
// was asking may be from before what the stream said, and kept, it was
// believed over the stream's word for HeldTtl.
func TestWhatIsDroppedWhileRosterIsAskedIsNotKept(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	em := newEmbedded(t)
	c := &cutting{}
	clk := &clock{now: time.Now()}
	a := accounts(t, standing.Config{Conn: em.dial(t, c.option()), As: em.as, Now: clk.Now})

	c.cut.Store(&cut{method: rstr.HolderService_Get_FullMethodName, end: func() { a.Recheck(em.kim) }})
	x.NoError(a.Held(ctx, em.kim, time.Now()), "the call that was asking has its answer")
	x.Nil(c.cut.Load())

	asked := c.asked.Load()
	x.NoError(a.Held(ctx, em.kim, time.Now()))
	x.Equal(asked+1, c.asked.Load(), "the next call asked")
	x.NoError(a.Held(ctx, em.kim, time.Now()))
	x.Equal(asked+1, c.asked.Load(), "and what it was told is kept, as ever")
}

// TestOnlyAnAddressSomebodyCheckedIsAnAccounts: what an invitation is found by.
// An address on an account that nobody checked -- one a provider passed on from
// a directory whose administrator typed it -- is not the account's.
func TestOnlyAnAddressSomebodyCheckedIsAnAccounts(t *testing.T) {
	// Checked by somebody, the way roster stamps one, or not.
	address := func(t *testing.T, s interface {
		Email() rstr.EmailServiceServer
	}, who pdid.Id, address string, checked bool) {
		t.Helper()
		x := require.New(t)
		v, err := s.Email().Add(t.Context(), rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(), Address: address,
		}.Build())
		x.NoError(err)
		if !checked {
			return
		}
		_, err = s.Email().Patch(t.Context(), rstr.EmailPatchRequest_builder{
			Ref:          rstr.EmailRef_builder{Id: v.GetId()}.Build(),
			DateVerified: timestamppb.Now(),
			DateUpdated:  v.GetDateUpdated(),
		}.Build())
		x.NoError(err)
	}

	t.Run("on the roster in this process", func(t *testing.T) {
		x := require.New(t)
		em := newEmbedded(t)
		address(t, em.s.Ungated, em.kim, "kim@home.example", true)
		address(t, em.s.Ungated, em.kim, "boss@home.example", false)

		a := accounts(t, standing.Config{Conn: em.dial(t), As: em.as})
		got, err := a.Addresses(t.Context(), em.kim)
		x.NoError(err)
		x.Equal([]string{"kim@home.example"}, got)

		got, err = a.Addresses(t.Context(), pdid.New(pdid.Domain(2)))
		x.NoError(err)
		x.Empty(got, "nobody's")
	})

	t.Run("on a roster of its own", func(t *testing.T) {
		x := require.New(t)
		e := newExternal(t)
		address(t, e.s.Ungated, e.kim, "kim@acme.example", true)
		address(t, e.s.Ungated, e.kim, "ceo@acme.example", false)

		for name, as := range map[string]func(context.Context) (context.Context, error){
			"with a deployment key": at(e.key, "acme"),
			"with a tenant key":     bearer(e.tenantKey),
		} {
			a := accounts(t, standing.Config{Conn: e.dial(t), As: as})
			got, err := a.Addresses(t.Context(), e.kim)
			x.NoError(err, name)
			x.Equal([]string{"kim@acme.example"}, got, name)
		}
	})

	// One more than a page of them, and the one somebody checked last: a list
	// read as far as its first page reads none of it.
	t.Run("on every page of them", func(t *testing.T) {
		x := require.New(t)
		em := newEmbedded(t)
		for i := range 20 {
			address(t, em.s.Ungated, em.kim, "kim+"+string(rune('a'+i))+"@home.example", false)
		}
		address(t, em.s.Ungated, em.kim, "kim@home.example", true)

		a := accounts(t, standing.Config{Conn: em.dial(t), As: em.as})
		got, err := a.Addresses(t.Context(), em.kim)
		x.NoError(err)
		x.Equal([]string{"kim@home.example"}, got)
	})
}
