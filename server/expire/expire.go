// Package expire removes rows whose own clock has run out -- a session past its
// expiry, an attempt nobody finished, a claim nobody proved -- and puts each
// removal on the trail as the act it is.
//
// # Why on the trail, when the sweeps go around the stack to stay off it
//
// The sweeps take the ent client so that a collected row costs no layer, no
// `Watch` event and no write through the servers: there is nobody to narrow a
// sweep to and nothing about the row to keep. What a check of the rows asks of
// a row the trail says is there, and that is not, is whether anything says it
// went (payday#115): one nothing does is what a row somebody removed in the
// database past the servers looks like -- a delegation revoked by deleting it, a
// session nobody can see ended. So each removal is a line of the trail that
// says the row went, `pd.RecordErased`, written in the transaction that removed
// it, a page at a time: which rows and when, and nothing of what they held.
package expire

import (
	"context"

	"uuid"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/server/pd"
)

// Rows removes what `due` finds -- at most [audit.RowsPage] at a time -- with
// `remove`, and records each as gone under `act` in the same transaction, until
// `due` finds nothing more. It answers how many went.
//
// A page is one transaction, so a pass that fails part way has removed and
// recorded the pages before it, and nothing of the one it failed in.
func Rows(
	ctx context.Context, db *ent.Client, cs audit.Contents, act string,
	due func(context.Context, *ent.Client) ([]uuid.UUID, error),
	remove func(context.Context, *ent.Client, []uuid.UUID) (int, error),
) (int, error) {
	n := 0
	for {
		m, more, err := page(ctx, db, cs, act, due, remove)
		n += m
		if err != nil || !more {
			return n, err
		}
	}
}

func page(
	ctx context.Context, db *ent.Client, cs audit.Contents, act string,
	due func(context.Context, *ent.Client) ([]uuid.UUID, error),
	remove func(context.Context, *ent.Client, []uuid.UUID) (int, error),
) (int, bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	ids, err := due(ctx, tx.Client())
	if err != nil || len(ids) == 0 {
		return 0, false, err
	}

	n, err := remove(ctx, tx.Client(), ids)
	if err != nil {
		return 0, false, err
	}

	gone := make([]pdid.Id, len(ids))
	for i, v := range ids {
		gone[i] = pdid.Id(v)
	}
	if _, err := pd.RecordErased(ctx, tx.Client(), cs, act, gone); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}

	return n, len(ids) >= audit.RowsPage, nil
}
