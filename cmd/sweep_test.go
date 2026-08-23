package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/session"
)

// Somebody has to collect the sessions, and nobody did.
//
// payday's `MemStore` drops every expired entry on each write, and a table
// cannot: what roster inherited from `authsession.Store` is `Put`, `Get` and
// `Del`, and none of those is a pass over everything -- correctly, since a
// store over a hundred million rows should not be walked by whoever happens to
// be signing in.
//
// And `Del` is not a delete. It is the soft erase every entity here has, so
// even signing out leaves the row. So the table was one row per sign-in since
// the deployment started, forever, and `docs/operating.md` said so out loud
// while the table was being written.

func TestExpiredSessionsAreCollected(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")

	db := b.Control.Ent
	store := session.New(db)

	live := authsession.Session{
		Key:     "live-one",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(time.Hour),
	}
	dead := authsession.Session{
		Key:     "dead-one",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(-time.Minute),
	}
	x.NoError(store.Put(ctx, live))
	x.NoError(store.Put(ctx, dead))

	// Refused before it is collected, which is the half that was never in
	// question: the read decides and the sweep is housekeeping.
	//
	// The refusal is `Session.Dead`, asked by `authsession.Handler`, and not
	// the store -- which answers with the row and lets the caller decide. That
	// division is why a sweep was needed at all: nothing on the read path has a
	// reason to delete anything.
	stale, err := store.Get(ctx, dead.Key)
	x.NoError(err)
	x.True(stale.Dead(time.Now()), "an expired session did not read as dead")

	n, err := session.Collect(ctx, db)
	x.NoError(err)
	x.Equal(1, n, "the expired one was not collected, or the live one was")

	got, err := store.Get(ctx, live.Key)
	x.NoError(err, "the live session went with it")
	x.Equal(who.String(), got.Id)

	// Idempotent, which is what lets every replica run it.
	n, err = session.Collect(ctx, db)
	x.NoError(err)
	x.Zero(n)
}

// TestSigningOutLeavesARowUntilItsOwnClockRuns is the shape that makes the
// predicate `date_expires` alone.
//
// `Del` erases softly, so a signed-out session is out of reach and still
// present. It blocks nothing while it waits -- the unique index on the secret
// is partial, `date_erased IS NULL` -- and it is collected when its own expiry
// passes, exactly as `keys.Sweep` leaves a revoked delegation.
func TestSigningOutLeavesARowUntilItsOwnClockRuns(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")

	db := b.Control.Ent
	store := session.New(db)

	v := authsession.Session{
		Key:     "signed-out",
		Id:      who.String(),
		Grant:   frame.Whole(),
		Expires: time.Now().Add(time.Hour),
	}
	x.NoError(store.Put(ctx, v))
	x.NoError(store.Del(ctx, v.Key))

	_, err := store.Get(ctx, v.Key)
	x.Error(err, "a signed-out session resolved")

	n, err := session.Collect(ctx, db)
	x.NoError(err)
	x.Zero(n, "a revoked session was collected before its own clock ran out")

	// And the secret can be used again, because the index that would have
	// stopped it does not cover erased rows.
	x.NoError(store.Put(ctx, v))
	got, err := store.Get(ctx, v.Key)
	x.NoError(err)
	x.Equal(who.String(), got.Id)
}

// TestTheControlPlaneRunsItsOwnBackgroundWork.
//
// `Build` is recursive, so the control plane assembled its own sweeps and its
// own drain into `control.Spin` -- and nothing ran them: `spin.Run` is handed
// the outer `s.Spin` alone. A deployment with a control plane had a second set
// of background work that was configured, constructed and silently never
// started.
func TestTheControlPlaneRunsItsOwnBackgroundWork(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)

	x.NotNil(b.Control)
	x.NotEmpty(b.Control.Spin, "the control plane arranged no background work at all")

	// Its own, plus this plane's, plus the session sweep that exists only when
	// there is a control plane holding the rows.
	x.Greater(len(b.Spin), len(b.Control.Spin),
		"the control plane's background work was not carried up")
}

// TestEveryPlaneWritesToTheSameQueue.
//
// `Build` assembles the recorder list once, conditionally appending the outbox
// one -- and the admin port re-typed that list, without the condition. So a
// deployment with `watch.outbox` on had it on for every write except an
// operator's, which are the ones the sync channel's first subject is about.
//
// Nothing anywhere said so, and nothing could have: both lists are correct Go
// and produce a working server. What makes it visible is turning the queue on
// and asking the two ports the same question.
func TestEveryPlaneWritesToTheSameQueue(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	s, err := cmd.Build(ctx, cmd.Config{
		Db: config.DbConfig{Driver: drv, Dsn: dsn},

		// The setting this is about. Nothing in the suite had ever turned it
		// on, which is why a whole plane could be missing from it.
		Watch:   config.WatchConfig{Broker: config.BrokerMemory, Outbox: true},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)

	// Whatever adding a tenant queued is not what this is about.
	_, err = s.Ent.Outbox.Delete().Exec(ctx)
	x.NoError(err)

	admin, err := cmd.Admin(s)
	x.NoError(err)
	x.NotNil(admin, "no admin stack was built")

	// Through the admin stack, as an operator: no wall, no gate.
	_, err = admin.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "written-by-an-operator",
	}.Build())
	x.NoError(err)

	// Nothing drains here, which is exactly the crash this is about: the write
	// happened and the publishing never did.
	n, err := s.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(n, "an operator's write left nothing behind to replay")
}
