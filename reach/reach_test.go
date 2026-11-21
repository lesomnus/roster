package reach_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/reach"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/standing"
)

// TestAnAppSaysWhichRosterAndWhoItIsThere: neither is a default.
func TestAnAppSaysWhichRosterAndWhoItIsThere(t *testing.T) {
	x := require.New(t)
	conn := newEmbedded(t).dial(t)
	as := func(ctx context.Context) (context.Context, error) { return ctx, nil }

	_, err := reach.New(reach.Config{As: as})
	x.ErrorContains(err, "Conn")
	_, err = reach.New(reach.Config{Conn: conn})
	x.ErrorContains(err, "As")
	_, err = reach.New(reach.Config{Conn: conn, As: as, Refused: -time.Second})
	x.ErrorContains(err, "Refused")
	_, err = reach.New(reach.Config{Conn: conn, As: as})
	x.NoError(err)
}

// TestAHolderMayCallWhatTheirRolesCoverAcrossTheTenant: what an app asks is
// the same whichever roster its people are in -- a method a role bound across
// the tenant covers, by its name or by a pattern, and a permission method like
// any other; nothing for a method no role names, for somebody with no
// binding, for nobody, or for somebody suspended.
func TestAHolderMayCallWhatTheirRolesCoverAcrossTheTenant(t *testing.T) {
	for _, r := range bothRosters(t) {
		t.Run(r.name, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			h := holders(t, reach.Config{Conn: r.dial(t), As: r.as})
			d := r.door(t)

			x.ErrorIs(h.May(ctx, r.holder, get), reach.ErrDenied, "somebody bound to nothing")

			d.bind(t, d.role(t, "staff", "/loft.AssetService/*", forOthers), r.holder, nil)
			h.Recheck(r.holder)
			for _, m := range []string{get, list, forOthers} {
				x.NoError(h.May(ctx, r.holder, m), m)
			}
			for _, m := range []string{runWork, "/loft.ReservationService/Add", "/loft.AssetService", ""} {
				x.ErrorIs(h.May(ctx, r.holder, m), reach.ErrDenied, "%q", m)
			}

			v, err := h.Reach(ctx, r.holder)
			x.NoError(err)
			x.Equal(r.holder, v.Holder)
			x.ElementsMatch([]string{"/loft.AssetService/*", forOthers}, v.Everywhere)
			x.True(v.EverySite)

			nobody := pdid.New(pdid.Domain(2))
			err = h.May(ctx, nobody, get)
			x.ErrorIs(err, reach.ErrDenied, "nobody")
			x.ErrorIs(err, reach.ErrNoPerson)
			_, err = h.Reach(ctx, nobody)
			x.ErrorIs(err, reach.ErrNoPerson)

			_, err = d.disable(d.ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: r.holder.Bytes()}.Build()}.Build())
			x.NoError(err)
			h.Recheck(r.holder)
			x.ErrorIs(h.May(ctx, r.holder, get), reach.ErrDenied, "somebody suspended reaches nothing")
		})
	}
}

// TestAGrantAtOneSiteIsNotEverywhere: a role bound at one site is in what
// roster's gate asks and not in what an app with no sites of its own grants on
// -- read from `methods`, it was handed everywhere.
func TestAGrantAtOneSiteIsNotEverywhere(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	em := newEmbedded(t)
	d := em.door(t)
	site, err := em.s.Ungated.Site().Add(ctx, rstr.SiteAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: em.dir.Bytes()}.Build(), Alias: "seoul",
	}.Build())
	x.NoError(err)
	d.bind(t, d.role(t, "local", get), em.kim, rstr.SiteRef_builder{Id: site.GetId()}.Build())

	h := holders(t, reach.Config{Conn: em.dial(t), As: em.as})
	v, err := h.Reach(ctx, em.kim)
	x.NoError(err)
	x.Contains(v.Methods, get, "what roster's gate asks")
	x.False(v.EverySite)
	x.Len(v.Sites, 1)
	x.Empty(v.Everywhere)
	x.ErrorIs(h.May(ctx, em.kim, get), reach.ErrDenied, "a grant at one site, handed everywhere")
}

// TestWhatRosterSaidIsKeptForHeldTtl: what roster answered is believed for
// standing's HeldTtl -- a session and what it may call land late by the same
// time -- and Recheck drops it at once, for a change the app made itself on
// the roster in its process, which has no stream. What a caller is handed is
// theirs: changing it changes nothing kept.
func TestWhatRosterSaidIsKeptForHeldTtl(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	em := newEmbedded(t)
	d := em.door(t)
	bound := d.bind(t, d.role(t, "staff", get), em.kim, nil)

	c := &clock{now: time.Now()}
	a := &asking{}
	h := holders(t, reach.Config{Conn: em.dial(t, a.option()), As: em.as, Now: c.Now})
	x.NoError(h.May(ctx, em.kim, get))
	v, err := h.Reach(ctx, em.kim)
	x.NoError(err)
	v.Everywhere[0] = list
	x.Equal(int32(1), a.asked.Load(), "asked once")

	_, err = d.eraseBinding(d.ctx, bound)
	x.NoError(err)
	c.Add(standing.HeldTtl - time.Second)
	x.NoError(h.May(ctx, em.kim, get), "what roster said last, within HeldTtl")
	x.ErrorIs(h.May(ctx, em.kim, list), reach.ErrDenied, "what a caller did to its copy")
	c.Add(2 * time.Second)
	x.ErrorIs(h.May(ctx, em.kim, get), reach.ErrDenied, "taken away, and no later than HeldTtl")
	x.Equal(int32(2), a.asked.Load())

	d.bind(t, d.role(t, "again", get), em.kim, nil)
	h.Recheck(em.kim)
	x.NoError(h.May(ctx, em.kim, get), "dropped, and asked at once")
}

// TestARosterThatCannotBeAskedIsARefusal: a roster that does not answer is no
// permission -- it is ErrUnreachable, which tells the caller it is not their
// doing -- and is not asked again for HeldRetry. A call that ends while roster
// is asked has asked nothing, and the next call is served.
func TestARosterThatCannotBeAskedIsARefusal(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	em := newEmbedded(t)
	d := em.door(t)
	d.bind(t, d.role(t, "staff", get), em.kim, nil)

	c := &clock{now: time.Now()}
	a := &asking{}
	h := holders(t, reach.Config{Conn: em.dial(t, a.option()), As: em.as, Now: c.Now})

	a.down.Store(true)
	err := h.May(ctx, em.kim, get)
	x.ErrorIs(err, reach.ErrUnreachable)
	x.NotErrorIs(err, reach.ErrDenied, "roster down is not roster saying no")
	x.ErrorIs(err, standing.ErrUnreachable, "one answer from both packages")
	asked := a.asked.Load()

	a.down.Store(false)
	x.ErrorIs(h.May(ctx, em.kim, get), reach.ErrUnreachable, "asked again within HeldRetry")
	x.Equal(asked, a.asked.Load())
	c.Add(standing.HeldRetry)
	x.NoError(h.May(ctx, em.kim, get), "and asked again once it is over")

	// Left half loaded, while roster was asked.
	h.Recheck(em.kim)
	cctx, leave := context.WithCancel(ctx)
	a.cut.Store(&leave)
	x.ErrorIs(h.May(cctx, em.kim, get), context.Canceled)
	x.NoError(h.May(ctx, em.kim, get), "the next call, which was refused for HeldRetry when the cut was kept")
}

// TestAReachIsNoReadOfSomebodysData: what somebody may call is patterns and
// sites, and asking for it reads nothing declared personal -- so an app asking
// at every call is not, at every call, a row of roster's access record saying
// it read that person.
func TestAReachIsNoReadOfSomebodysData(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	h := holders(t, reach.Config{Conn: e.dial(t), As: e.as})

	_, err := h.ReachOf(t.Context(), e.kim)
	x.NoError(err)
	x.Zero(e.s.Access.Pending(), "a reach read as a read of the person")
}

// silentRoster is a listener that takes every connection and says nothing on
// it: a roster frozen, or a path that drops what it carries without closing.
func silentRoster(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	return l.Addr().String()
}

// TestARosterThatDoesNotAnswerCouldNotBeAsked: a roster that takes the call
// and never answers is one that could not be asked, within standing's
// HeldWait of each asking, and is kept as one for HeldRetry. Asked with
// nothing of the package's own to end it, every call waited out its caller's
// whole deadline and answered that the call ended before roster answered,
// and kept nothing, so the next call waited again; right is ErrUnreachable,
// and no asking.
func TestARosterThatDoesNotAnswerCouldNotBeAsked(t *testing.T) {
	x := require.New(t)
	reach.ShortenHeldWait(t, 200*time.Millisecond)
	a := &asking{}
	conn, err := grpc.NewClient(silentRoster(t), grpc.WithTransportCredentials(insecure.NewCredentials()), a.option())
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })
	c := &clock{now: time.Now()}
	h := holders(t, reach.Config{Conn: conn, As: func(ctx context.Context) (context.Context, error) { return ctx, nil }, Now: c.Now})
	kim := pdid.New(pdid.Domain(2))

	// The caller's own deadline is long, and is a bound on a test that fails.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	x.ErrorIs(h.May(ctx, kim, get), reach.ErrUnreachable)
	x.NoError(ctx.Err(), "the caller's own time was not what ended it")
	asked := a.asked.Load()
	x.ErrorIs(h.May(ctx, kim, get), reach.ErrUnreachable)
	x.Equal(asked, a.asked.Load(), "asked again within HeldRetry")

	_, err = h.ReachOf(ctx, kim)
	x.ErrorIs(err, reach.ErrUnreachable)
}

// TestEveryRoadSaysTheSameOfRosterBeingAway: a roster that could not be asked
// is ErrUnreachable on every road that asks it, and the app's own error from
// As is found beside it. ReachOf answered roster's error and no value an app
// compares.
func TestEveryRoadSaysTheSameOfRosterBeingAway(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	em := newEmbedded(t)
	a := &asking{}
	h := holders(t, reach.Config{Conn: em.dial(t, a.option()), As: em.as})

	a.down.Store(true)
	_, err := h.ReachOf(ctx, em.kim)
	x.ErrorIs(err, reach.ErrUnreachable, "ReachOf")
	_, err = h.Reach(ctx, em.kim)
	x.ErrorIs(err, reach.ErrUnreachable, "Reach")
	h.Recheck(em.kim)
	x.ErrorIs(h.May(ctx, em.kim, get), reach.ErrUnreachable, "May")

	mine := errors.New("no tenant yet")
	failing := holders(t, reach.Config{Conn: em.dial(t), As: func(context.Context) (context.Context, error) { return nil, mine }})
	_, err = failing.ReachOf(ctx, em.kim)
	x.ErrorIs(err, reach.ErrUnreachable, "As failing")
	x.ErrorIs(err, mine, "and the app's own error beside it")
	err = failing.May(ctx, em.kim, get)
	x.ErrorIs(err, reach.ErrUnreachable)
	x.ErrorIs(err, mine)
}

// handedOn says that what was answered is the status a caller handed it would
// be answered -- `code`, with the sentence of `says` and nothing else -- and
// that `hidden`, which is roster's or the transport's, is in what the app logs
// and not in what its caller reads.
func handedOn(t *testing.T, err error, code codes.Code, says error, hidden ...string) {
	t.Helper()
	x := require.New(t)
	x.Error(err)
	x.Equal(code, status.Code(err), "%v", err)
	x.Equal(says.Error(), status.Convert(err).Message())
	for _, v := range hidden {
		x.NotContains(status.Convert(err).Message(), v)
		x.Contains(err.Error(), v, "what the app logs says everything")
	}
}

// TestWhatIsAnsweredCanBeHandedToACaller: every error the package returns is,
// as it is returned, a gRPC status of the package's choosing in the package's
// own words, so that an app may put May in payday's gate with nothing between,
// as the manual does, where an error is the caller's answer. The errors
// wrapped roster's with `%w`, and gRPC reads a status through wrapping: a
// method denied was answered Unknown, and a roster that could not be asked
// roster's own code, or Unavailable with the transport's words, which name
// where roster is dialed. What the app logs still says everything, and the
// package's values, roster's error and the app's own are still in the chain.
func TestWhatIsAnsweredCanBeHandedToACaller(t *testing.T) {
	ctx := t.Context()
	em := newEmbedded(t)
	h := holders(t, reach.Config{Conn: em.dial(t), As: em.as})

	t.Run("denied", func(t *testing.T) {
		x := require.New(t)
		err := h.May(ctx, em.kim, runWork)
		handedOn(t, err, codes.PermissionDenied, reach.ErrDenied, runWork)
		x.ErrorIs(err, reach.ErrDenied)
	})

	t.Run("nobody", func(t *testing.T) {
		x := require.New(t)
		nobody := pdid.New(pdid.Domain(2))
		err := h.May(ctx, nobody, get)
		handedOn(t, err, codes.PermissionDenied, reach.ErrDenied)
		x.ErrorIs(err, reach.ErrNoPerson)
		_, err = h.Reach(ctx, nobody)
		handedOn(t, err, codes.PermissionDenied, reach.ErrDenied)
		x.ErrorIs(err, reach.ErrNoPerson)
		_, err = h.ReachOf(ctx, nobody)
		handedOn(t, err, codes.NotFound, reach.ErrNoPerson)
		x.ErrorIs(err, reach.ErrNoPerson)
	})

	t.Run("the caller left", func(t *testing.T) {
		x := require.New(t)
		h.Recheck(em.kim)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		err := h.May(cctx, em.kim, get)
		handedOn(t, err, codes.Canceled, context.Canceled)
		x.ErrorIs(err, context.Canceled)
	})

	t.Run("roster away", func(t *testing.T) {
		x := require.New(t)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		x.NoError(err)
		addr := l.Addr().String()
		x.NoError(l.Close())
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		x.NoError(err)
		t.Cleanup(func() { conn.Close() })
		away := holders(t, reach.Config{Conn: conn, As: em.as})

		err = away.May(ctx, em.kim, get)
		handedOn(t, err, codes.Unavailable, reach.ErrUnreachable, addr)
		x.ErrorIs(err, reach.ErrUnreachable)
		var st interface{ GRPCStatus() *status.Status }
		x.ErrorAs(errors.Unwrap(err), &st, "roster's own error, in the chain")
		_, err = away.ReachOf(ctx, em.kim)
		handedOn(t, err, codes.Unavailable, reach.ErrUnreachable, addr)
	})

	t.Run("roster refusing the app", func(t *testing.T) {
		e := newExternal(t)
		stranger := holders(t, reach.Config{Conn: e.dial(t), As: func(ctx context.Context) (context.Context, error) {
			return auth.BearerProvider("rk_nobody").Provide(ctx), nil
		}})
		_, raw := rstr.NewHolderServiceClient(e.dial(t)).Reaches(auth.BearerProvider("rk_nobody").Provide(ctx),
			rstr.HolderReachesRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		require.Error(t, raw)
		handedOn(t, stranger.May(ctx, e.kim, get), codes.Unavailable, reach.ErrUnreachable, status.Convert(raw).Message())
	})

	t.Run("the app's own error", func(t *testing.T) {
		x := require.New(t)
		mine := errors.New("no tenant yet")
		failing := holders(t, reach.Config{Conn: em.dial(t), As: func(context.Context) (context.Context, error) { return nil, mine }})
		err := failing.May(ctx, em.kim, get)
		handedOn(t, err, codes.Unavailable, reach.ErrUnreachable, mine.Error())
		x.ErrorIs(err, mine)
	})
}
