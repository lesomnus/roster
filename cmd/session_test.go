package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/roster/rstr"

	entsession "github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/server/session"
)

// TestAConsoleSessionSurvivesTheProcess is the trap payday's own store names
// and cannot avoid.
//
// `MemStore` is *right for one replica and silently wrong for two*: a cookie
// minted on one is unknown to the other, so a browser is signed in or out
// depending on which one the load balancer picked — intermittently, per
// request, with nothing in any log saying why. It is lost on restart besides,
// so a deploy signs everybody out.
//
// A table is what replaces it, and this is the property that says so: a second
// store over the same rows answers what the first one wrote.
func TestAConsoleSessionSurvivesTheProcess(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")

	one := session.New(b.Control.Ent)

	v := authsession.Session{
		Key:     "a-cookie-value",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(time.Hour),
		Idle:    time.Now().Add(30 * time.Minute),
	}
	x.NoError(one.Put(ctx, v))

	// A second store, which is what a second replica is.
	two := session.New(b.Control.Ent)

	got, err := two.Get(ctx, "a-cookie-value")
	x.NoError(err)
	x.Equal(who.String(), got.Id)
	x.True(got.Grant.IsWhole(), "the grant did not survive the round trip")
	x.WithinDuration(v.Expires, got.Expires, time.Second)

	t.Run("and the cookie value is not in the table", func(t *testing.T) {
		x := require.New(t)

		row, err := b.Control.Ent.Session.Query().Only(ctx)
		x.NoError(err)
		x.NotContains(string(row.Secret), "a-cookie-value",
			"a copy of the rows is a set of live cookies")
		x.Len(row.Secret, 32)
	})

	t.Run("and signing out is immediate everywhere", func(t *testing.T) {
		x := require.New(t)

		x.NoError(one.Del(ctx, "a-cookie-value"))

		_, err := two.Get(ctx, "a-cookie-value")
		x.ErrorIs(err, authsession.ErrNoSession)

		// Ending one that is not there succeeds, which is what a retried
		// sign-out is.
		x.NoError(two.Del(ctx, "a-cookie-value"))
	})

	t.Run("and a key nobody minted is not a session", func(t *testing.T) {
		x := require.New(t)

		_, err := two.Get(ctx, "not-a-cookie")
		x.ErrorIs(err, authsession.ErrNoSession)
	})
}

// TestAnErasedOperatorHasNoSessions is the edge doing its job.
//
// The holder is an edge rather than the string `authsession.Session` carries,
// so a session for somebody who has been erased is a session nothing answers —
// without anybody having to remember it, because `<Entity>Pick` narrows to the
// live rows.
func TestAnErasedOperatorHasNoSessions(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")

	s := session.New(b.Control.Ent)
	x.NoError(s.Put(ctx, authsession.Session{
		Key:     "a-cookie-value",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(time.Hour),
	}))

	_, err := s.Get(ctx, "a-cookie-value")
	x.NoError(err)

	_, err = b.Control.Ungated.Holder().Erase(ctx,
		app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)

	_, err = s.Get(ctx, "a-cookie-value")
	x.ErrorIs(err, authsession.ErrNoSession,
		"an erased operator's cookie went on working")
}

// TestTheIdleClockIsTheOneColumnThatMoves.
//
// `authsession` writes at most once per half-window, so a busy browser is not a
// write per request. What that asks of a store is that a second `Put` with the
// same key is an update rather than a conflict.
func TestTheIdleClockIsTheOneColumnThatMoves(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")

	s := session.New(b.Control.Ent)
	v := authsession.Session{
		Key:     "a-cookie-value",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(time.Hour),
		Idle:    time.Now().Add(10 * time.Minute),
	}
	x.NoError(s.Put(ctx, v))

	v.Idle = time.Now().Add(30 * time.Minute)
	x.NoError(s.Put(ctx, v), "moving the idle clock was refused as a conflict")

	got, err := s.Get(ctx, "a-cookie-value")
	x.NoError(err)
	x.WithinDuration(v.Idle, got.Idle, time.Second)

	n, err := b.Control.Ent.Session.Query().Where(entsession.DateErasedIsNil()).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "a second write made a second row")
}
