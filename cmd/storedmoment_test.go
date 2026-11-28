package cmd_test

import (
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	entsession "github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/session"
)

// storedToTheMicrosecond fails `t` where `at`, a time roster wrote itself and
// read back, keeps digits below the microsecond: on SQLite, which keeps them,
// a time roster wrote uncut.
func storedToTheMicrosecond(t *testing.T, what string, at time.Time) {
	t.Helper()
	require.True(t, at.Equal(at.Truncate(time.Microsecond)),
		"%s was stored with digits below the microsecond, which PostgreSQL does not keep: %s", what, at.Format(time.RFC3339Nano))
}

// nanosecondsOf is a moment of the clock's with digits below the microsecond,
// whatever the clock read: what a time handed to roster may carry.
func nanosecondsOf(at time.Time) time.Time {
	return at.Truncate(time.Microsecond).Add(789 * time.Nanosecond)
}

// TestASessionsTimesAreWhatTheDatabaseKeeps.
//
// The generated store cuts every time it writes to the microsecond, on every
// database (lesomnus/payday#324), and a session is written past it, through
// ent, as it was: with the sign-in's nanoseconds. SQLite keeps them and
// PostgreSQL does not, so a session read back on PostgreSQL was not the
// session written, and a suite on SQLite never saw it. Each time of a session
// is now written as the database keeps it, on every database: what `Get`
// answers is what was handed in, cut, and is what the row holds.
//
// The store reads no clock a caller can set, so the seam is the times handed
// in, with nanoseconds.
func TestASessionsTimesAreWhatTheDatabaseKeeps(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	s := session.New(b.Ent, audit.Contents{})

	at := nanosecondsOf(time.Now())
	v := authsession.Session{
		Key:               "a-session-to-the-nanosecond",
		Id:                b.ContosoUser.String(),
		Grant:             frame.Whole(),
		DateAuthenticated: at.Add(-time.Minute),
		Expires:           at.Add(time.Hour),
		Idle:              at.Add(10 * time.Minute),
	}
	x.NoError(s.Put(ctx, v))

	row := func() *ent.Session {
		t.Helper()
		got, err := b.Ent.Session.Query().Where(entsession.SecretEQ(session.Sum(v.Key))).Only(ctx)
		require.NoError(t, err)

		return got
	}

	got, err := s.Get(ctx, v.Key)
	x.NoError(err)
	x.True(v.DateAuthenticated.Truncate(time.Microsecond).Equal(got.DateAuthenticated), "when its person signed in, read back: %s", got.DateAuthenticated.Format(time.RFC3339Nano))
	x.True(v.Expires.Truncate(time.Microsecond).Equal(got.Expires), "its expiry, read back: %s", got.Expires.Format(time.RFC3339Nano))
	x.True(v.Idle.Truncate(time.Microsecond).Equal(got.Idle), "its idle clock, read back: %s", got.Idle.Format(time.RFC3339Nano))

	made := row()
	storedToTheMicrosecond(t, "when the session was made", made.DateCreated)
	storedToTheMicrosecond(t, "when the session was written", made.DateUpdated)

	t.Run("and its idle clock moved is too", func(t *testing.T) {
		x := require.New(t)

		v.Idle = at.Add(20 * time.Minute)
		x.NoError(s.Put(ctx, v))

		got, err := s.Get(ctx, v.Key)
		x.NoError(err)
		x.True(v.Idle.Truncate(time.Microsecond).Equal(got.Idle), "its idle clock moved, read back: %s", got.Idle.Format(time.RFC3339Nano))
		storedToTheMicrosecond(t, "when the idle clock was moved", row().DateUpdated)
	})

	t.Run("and its ending is too", func(t *testing.T) {
		x := require.New(t)

		x.NoError(s.Del(ctx, v.Key))

		ended := row()
		x.NotNil(ended.DateErased)
		storedToTheMicrosecond(t, "when the session ended", *ended.DateErased)
		storedToTheMicrosecond(t, "when the ending was written", ended.DateUpdated)
	})
}

// TestASessionStoredBeforeIsReadAsItWas.
//
// A session written before roster cut what it writes holds, on SQLite, the
// nanoseconds it was written with. Nothing is migrated: it is read with them,
// moved with them, and found by every comparison that found it before -- the
// sweep's expiry, the stamp that voids a sign-in -- because a comparison is
// made with the time as it is and not cut (`internal/moment`).
func TestASessionStoredBeforeIsReadAsItWas(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	s := session.New(b.Ent, audit.Contents{})
	trusted := bare.TrustAdmission(ctx)

	at := nanosecondsOf(time.Now())
	old := func(key string, expires time.Time) *ent.Session {
		t.Helper()
		v, err := b.Ent.Session.Create().
			SetId(pdid.New(pd.SessionDomain).Uuid()).
			SetHolderId(b.ContosoUser.Uuid()).
			SetSecret(session.Sum(key)).
			SetGrant([]byte{}).
			SetDateCreated(at.Add(-time.Hour)).
			SetDateUpdated(at.Add(-time.Hour)).
			SetDateAuthenticated(at.Add(-time.Hour)).
			SetDateExpired(expires).
			SetDateIdle(expires).
			Save(trusted)
		require.NoError(t, err)

		// What the database kept of it, which is the old row as a read gives
		// it: with its nanoseconds on SQLite, rounded on PostgreSQL.
		kept, err := b.Ent.Session.Get(ctx, v.Id)
		require.NoError(t, err)

		return kept
	}

	kept := old("a-session-from-before", at.Add(time.Hour))
	if b.Drv.Dialect() == dialect.SQLite {
		x.NotZero(kept.DateExpired.Nanosecond()%1000, "SQLite kept no nanoseconds, so this holds nothing")
	}

	got, err := s.Get(ctx, "a-session-from-before")
	x.NoError(err)
	x.True(kept.DateAuthenticated.Equal(got.DateAuthenticated), "when its person signed in was not read as it was: %s", got.DateAuthenticated.Format(time.RFC3339Nano))
	x.True(kept.DateExpired.Equal(got.Expires), "its expiry was not read as it was: %s", got.Expires.Format(time.RFC3339Nano))
	x.True(kept.DateIdle.Equal(got.Idle), "its idle clock was not read as it was: %s", got.Idle.Format(time.RFC3339Nano))

	t.Run("and moving its idle clock leaves the rest as it was", func(t *testing.T) {
		x := require.New(t)

		got.Idle = time.Now().Add(time.Hour)
		x.NoError(s.Put(ctx, got))

		moved, err := b.Ent.Session.Get(ctx, kept.Id)
		x.NoError(err)
		x.True(kept.DateCreated.Equal(moved.DateCreated))
		x.True(kept.DateAuthenticated.Equal(*moved.DateAuthenticated))
		x.True(kept.DateExpired.Equal(*moved.DateExpired))
	})

	t.Run("and one past its expiry is collected", func(t *testing.T) {
		x := require.New(t)

		gone := old("a-session-from-before-that-ran-out", nanosecondsOf(time.Now().Add(-time.Minute)))
		n, err := session.Collect(ctx, b.Ent, audit.Contents{})
		x.NoError(err)
		x.Equal(1, n)

		left, err := b.Ent.Session.Query().Where(entsession.IdEQ(gone.Id)).Exist(ctx)
		x.NoError(err)
		x.False(left, "a session stored before and past its expiry was left by the sweep")
	})

	t.Run("and a stamp in the instant it was made voids it", func(t *testing.T) {
		x := require.New(t)

		x.NoError(b.Ent.Holder.UpdateOneId(b.ContosoUser.Uuid()).SetDateInvalidated(kept.DateCreated).Exec(trusted))

		_, err := s.Get(ctx, "a-session-from-before")
		x.ErrorIs(err, authsession.ErrNoSession, "a sign-in stamped void in the instant it was made answered")
	})
}

// TestSomebodyErasedBeforeIsDueAsTheyWere.
//
// A person erased before roster cut what it writes, with nanoseconds on
// SQLite, is due to be forgotten by the comparison that found them before.
func TestSomebodyErasedBeforeIsDueAsTheyWere(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erased-long-ago")
	erased := nanosecondsOf(time.Now().Add(-2 * time.Hour))
	x.NoError(b.Ent.Holder.UpdateOneId(who.Uuid()).SetDateErased(erased).Exec(bare.TrustAdmission(ctx)))

	kept, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)

	vs, err := forget.Due(ctx, b.Ent, kept.DateErased.Add(time.Microsecond))
	x.NoError(err)
	x.Contains(vs, who, "somebody erased before was not due a microsecond after it")

	vs, err = forget.Due(ctx, b.Ent, *kept.DateErased)
	x.NoError(err)
	x.NotContains(vs, who, "somebody was due in the instant they were erased")
}
