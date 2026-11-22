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
//
// # And under the tenant the row was of
//
// A line about a row is filed under the row's tenant, which the recorder reads
// off the row. A line that says a row went has no row to read, and is filed
// under the tenant its context is framed as -- and a sweep's context is framed
// as nothing, so what it collected of a tenant's was said under no tenant at
// all. The tenant's own reader of its trail was answered the line that made a
// row and never the one that says it went; and a hold, which keeps what the
// held tenant may read, kept the first and not the second -- the half of the
// record that makes the other half a finding (#280).
//
// So whoever says which rows are due says whose each is, while there is still
// a row to ask ([Row]), and the line is recorded in a context framed as that
// tenant ([Under], [Gone]). Whoever removes rows past the servers for another
// reason -- a person forgotten, a tenant destroyed, a consent past its keep --
// records the same way.
package expire

import (
	"context"
	"maps"
	"slices"

	"uuid"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/server/pd"
)

// Row is a row whose clock has run out, as a sweep finds it: which row, and
// the tenant it is of, read while it is still there. The nil tenant is a row
// of none.
type Row struct {
	Id     uuid.UUID
	Tenant uuid.UUID
}

// Under is `ctx` as a line that says a row of `tenant` went is recorded in:
// framed as that tenant, which is where `pd.RecordErased` files a line that
// has no row to read one from.
//
// Whoever `ctx` is framed as is still who the line is by. A sweep and a
// command at the box are framed as nobody, and the line says nobody: who acted
// is a holder's identifier on the trail, and neither is a holder. What did it
// is the act the line is recorded as. The actor's tenant is said to be the
// row's with it, since the frame has one tenant for both.
func Under(ctx context.Context, tenant pdid.Id) context.Context {
	f := frame.Frame{}
	if was, ok := frame.From(ctx); ok {
		f = *was
	}
	f.Tenant = tenant

	return frame.Into(ctx, &f)
}

// Gone records `rows`, which the transaction `db` runs in removed, as gone
// under `act`: a line of the trail for each, filed under the tenant it was of
// ([Under]).
//
// A tenant at a time, since a context says one, and in the order of their
// identifiers so that what one transaction says is said in one order.
func Gone(ctx context.Context, db *ent.Client, cs audit.Contents, act string, rows []Row) error {
	gone := map[uuid.UUID][]pdid.Id{}
	for _, v := range rows {
		gone[v.Tenant] = append(gone[v.Tenant], pdid.Id(v.Id))
	}

	for _, tenant := range slices.SortedFunc(maps.Keys(gone), uuid.UUID.Compare) {
		if _, err := pd.RecordErased(Under(ctx, pdid.Id(tenant)), db, cs, act, gone[tenant]); err != nil {
			return err
		}
	}

	return nil
}

// Rows removes what `due` finds -- at most [audit.RowsPage] at a time -- with
// `remove`, and records each as gone under `act` in the same transaction, until
// `due` finds nothing more. It answers how many went.
//
// `due` says whose each row is as well as which, and each line is filed under
// that tenant ([Gone]).
//
// A page is one transaction, so a pass that fails part way has removed and
// recorded the pages before it, and nothing of the one it failed in. And a
// line is recorded for a row this pass's delete removed and for no other: two
// passes at once read the same rows, and the second's delete removes none of
// what the first's did, so a page whose delete removed fewer rows than it read
// is read again rather than said (#285).
func Rows(
	ctx context.Context, db *ent.Client, cs audit.Contents, act string,
	due func(context.Context, *ent.Client) ([]Row, error),
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
	due func(context.Context, *ent.Client) ([]Row, error),
	remove func(context.Context, *ent.Client, []uuid.UUID) (int, error),
) (int, bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	rows, err := due(ctx, tx.Client())
	if err != nil || len(rows) == 0 {
		return 0, false, err
	}

	ids := make([]uuid.UUID, len(rows))
	for i, v := range rows {
		ids[i] = v.Id
	}

	n, err := remove(ctx, tx.Client(), ids)
	if err != nil {
		return 0, false, err
	}
	if n != len(rows) {
		// Somebody else removed some of what this page read before its delete
		// reached them -- another pass of the same sweep, as there is around a
		// failover -- and a line is for a row this pass's own delete removed.
		// Which ones those are the count does not say, so nothing of the page
		// is kept and it is read again: what the other removed is no longer
		// due, since its delete was waited for and had committed before this
		// one counted. No statement is added to a page that is not raced.
		return 0, true, tx.Rollback()
	}
	if err := Gone(ctx, tx.Client(), cs, act, rows); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}

	return n, len(rows) >= audit.RowsPage, nil
}
