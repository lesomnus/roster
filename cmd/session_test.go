package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
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

	one := session.New(b.Control.Ent, audit.Contents{})

	v := authsession.Session{
		Key:     "a-cookie-value",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(time.Hour),
		Idle:    time.Now().Add(30 * time.Minute),
	}
	x.NoError(one.Put(ctx, v))

	// A second store, which is what a second replica is.
	two := session.New(b.Control.Ent, audit.Contents{})

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

	s := session.New(b.Control.Ent, audit.Contents{})
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

	s := session.New(b.Control.Ent, audit.Contents{})
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

// TestASessionThatEndedIsNotWrittenAgain: once a session has been ended, the
// idle clock of a request that was already in flight does not bring it back.
//
// # What it was
//
// `Put` is asked for two things by one name: a cookie being minted, and --
// rarely -- the idle clock of a live one moving. It told them apart by looking
// for a live row, and made one when it found none. A request reads its
// session, the person signs out in another tab, and the request's clock is
// written: no live row, so a new one, with the same secret, the expiry it had,
// and *created now*. The cookie that was signed out answered again until that
// expiry -- and past `Holder.Invalidate` too, which voids what was issued
// before its stamp and reads when a row was made (#275).
//
// The sweep's own comment said *`Put` resurrects on a miss*, of rows it
// collects, and arranged itself around it.
//
// A cookie's value is thirty-two random bytes and is minted once, so a secret
// that has ever been a row here is not one being minted.
func TestASessionThatEndedIsNotWrittenAgain(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")
	store := session.New(b.Control.Ent, audit.Contents{})

	v := authsession.Session{
		Key:     "a-cookie-value",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(time.Hour),
		Idle:    time.Now().Add(30 * time.Minute),
	}
	x.NoError(store.Put(ctx, v))

	// Signed out, between a request reading its session and writing its clock.
	x.NoError(store.Del(ctx, v.Key))

	later := v
	later.Idle = time.Now().Add(45 * time.Minute)
	x.ErrorIs(store.Put(ctx, later), authsession.ErrNoSession,
		"the clock of a session that had ended was written as a session")

	_, err := store.Get(ctx, v.Key)
	x.ErrorIs(err, authsession.ErrNoSession, "a session that was signed out answers again")

	n, err := b.Control.Ent.Session.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "and a second row was made for it")

	// The clock of a live one still moves, and a new cookie is still a row.
	other := v
	other.Key = "another-cookie-value"
	x.NoError(store.Put(ctx, other))
	other.Idle = time.Now().Add(50 * time.Minute)
	x.NoError(store.Put(ctx, other))

	got, err := store.Get(ctx, other.Key)
	x.NoError(err)
	x.WithinDuration(other.Idle, got.Idle, time.Second)
}
