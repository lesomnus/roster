package reach_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
