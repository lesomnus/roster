package cmd_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/internal/ent"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	entsession "github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/server/expire"
	"github.com/lesomnus/roster/server/session"
)

// sweptSessions is `n` sessions of somebody on the control plane, each past
// its expiry, and the database they are in.
func sweptSessions(t *testing.T, n int) (*ent.Client, context.Context) {
	t.Helper()
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")
	db := b.Control.Ent
	store := session.New(db, audit.Contents{})
	for i := range n {
		x.NoError(store.Put(ctx, authsession.Session{
			Key:     fmt.Sprintf("swept-%d", i),
			Id:      who.String(),
			Grant:   frame.Whole(),
			Expires: time.Now().Add(-time.Minute),
		}))
	}

	return db, ctx
}

// sweptLines is how many lines the trail has saying each row went as `act`.
func sweptLines(t *testing.T, ctx context.Context, db *ent.Client, act string) map[uuid.UUID]int {
	t.Helper()

	vs, err := db.Audit.Query().Where(entaudit.ActionEQ(act)).All(ctx)
	require.NoError(t, err)

	out := map[uuid.UUID]int{}
	for _, v := range vs {
		out[v.ObjectId]++
	}

	return out
}

// TestTwoSweepsAtOnceSayEachRowWentOnce: a loop may run twice at once around
// a failover, and the manual says roster's are fine with that. A sweep was
// not: each pass read the rows that were due, deleted them, and said on the
// trail that every row it had **read** went -- so the second of two passes,
// whose delete removed nothing, said it all again. 9,500 lines for 6,000
// sessions on a run.
//
// A line is for a row the pass's own delete removed, and for no other.
//
// On PostgreSQL. SQLite takes one writer at a time, and a sweep's page is a
// write from its first read: the second pass reads once the first has
// committed, so the two never overlap there and this would pass whatever a
// pass said. `TestAPassSaysOnlyTheRowsItsOwnDeleteRemoved` asks the same of
// the function on either.
func TestTwoSweepsAtOnceSayEachRowWentOnce(t *testing.T) {
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skipf("%s is not set; SQLite takes one writer at a time, so two passes do not overlap on it", pdtest.Postgres)
	}

	x := require.New(t)

	const n = 3 * audit.RowsPage
	db, ctx := sweptSessions(t, n)

	start := make(chan struct{})
	var wg sync.WaitGroup
	gone := make([]int, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			<-start
			gone[i], errs[i] = session.Collect(ctx, db, audit.Contents{})
		})
	}
	close(start)
	wg.Wait()

	x.NoError(errs[0])
	x.NoError(errs[1])
	x.Equal(n, gone[0]+gone[1], "the two passes say they removed %d and %d", gone[0], gone[1])

	left, err := db.Session.Query().Count(ctx)
	x.NoError(err)
	x.Zero(left)

	lines := sweptLines(t, ctx, db, session.Expired)
	x.Len(lines, n, "a row went with no line, or a line is of no row")
	for id, k := range lines {
		x.Equal(1, k, "session %s is said to have gone %d times", id, k)
	}
}

// TestAPassSaysOnlyTheRowsItsOwnDeleteRemoved is the same rule asked of
// `expire.Rows` on either engine: a pass that read a row another pass then
// removed -- as the second of two at once does on PostgreSQL -- says nothing of
// that row. It said that every row it read went.
func TestAPassSaysOnlyTheRowsItsOwnDeleteRemoved(t *testing.T) {
	x := require.New(t)

	db, ctx := sweptSessions(t, 3)

	// What a pass read, and then one of those rows removed by somebody else
	// before its delete: the second of two passes at once.
	vs, err := db.Session.Query().All(ctx)
	x.NoError(err)
	read := make([]expire.Row, len(vs))
	for i, v := range vs {
		read[i] = expire.Row{Id: v.Id}
	}
	x.NoError(db.Session.DeleteOneId(vs[0].Id).Exec(ctx))

	const act = "roster.test.expire"
	first := true
	n, err := expire.Rows(ctx, db, audit.Contents{}, act,
		func(ctx context.Context, db *ent.Client) ([]expire.Row, error) {
			if first {
				first = false
				return read, nil
			}

			vs, err := db.Session.Query().Where(entsession.DateExpiresLT(time.Now())).All(ctx)
			if err != nil {
				return nil, err
			}
			rows := make([]expire.Row, len(vs))
			for i, v := range vs {
				rows[i] = expire.Row{Id: v.Id}
			}

			return rows, nil
		},
		func(ctx context.Context, db *ent.Client, ids []uuid.UUID) (int, error) {
			return db.Session.Delete().Where(entsession.IdIn(ids...)).Exec(ctx)
		},
	)
	x.NoError(err)
	x.Equal(2, n)

	lines := sweptLines(t, ctx, db, act)
	x.NotContains(lines, vs[0].Id, "the row somebody else removed is said to have gone by this pass")
	x.Equal(map[uuid.UUID]int{vs[1].Id: 1, vs[2].Id: 1}, lines)
}
