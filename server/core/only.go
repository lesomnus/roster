package core

import (
	"context"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// only runs `fn` where two callers asking at once are one caller asking twice.
//
// # Why a count is not a decision
//
// Every other rule in this package looks at the **request**: does this tenant
// match that one, does the caller hold this method, is this subject shaped like
// an address. A look is enough for those, because what it looked at cannot have
// changed by the time the write lands -- it is in the request.
//
// [coreIdentity.Erase] is the exception. What it asks is *how many other ways in
// does this person have*, and the answer is a count of other rows. Two callers
// removing a person's last two ways in each count before either writes, each see
// the other's still there, and both go through -- so the state the rule exists
// to refuse is reached by asking for it twice at once. Forty people, each
// unlinked twice at the same moment: thirty-nine lost on PostgreSql, four on
// SQLite, which serialises writers and so only leaves the gap between the count
// and the write.
//
// # What they contend for
//
// A transaction is necessary and is not enough. Under READ COMMITTED two
// transactions each take their own snapshot for a read, so both still count
// two, and two erases of **different** rows conflict over nothing.
//
// What makes them one caller is a row they both have to write. The person is
// the obvious one -- it is their set of ways in that is being changed -- so the
// write is on their version column, inside the transaction, before anything is
// counted. The second transaction blocks on that row, and by the time it is let
// through the first has committed and the count it then takes is the true one.
// Whichever way it goes after that, its erase is inside the transaction and
// goes with it.
//
// # And what it is not, which is where the first attempt went
//
// It is not the generated `Patch` with a version precondition and nothing else.
// That compiles to an existence check and no write at all -- D34 recorded it
// about a continuation, and it is the same sentence here a year later -- so two
// callers each validated a version, neither wrote anything, and they contended
// for nothing. It passed on SQLite, which serialises writers anyway, and lost
// thirty-nine of forty on PostgreSql.
//
// Which is the whole lesson: an optimistic lock is a **write**. A precondition
// with no write beside it is a read with an opinion.
//
// # What it costs
//
// The person's `date_updated` moves when an identity of theirs is removed. It
// is a concurrency token rather than a fact about them, so nothing untrue is
// recorded -- and the write goes around the recorder, so the trail carries the
// erase that explains it and no entry for the lock. What a console holding an
// older version of that row is told is to read again before its next write,
// which is what a version is for.
//
// # And when the caller has already arranged one
//
// A batch, or anything reached through a stack payday rebound onto its own
// transaction, arrives here with no driver -- see [Core.drv] -- and then this
// runs `fn` in what it was handed. That is not a weaker rule: the outer
// transaction is the serialisation point, and the swap below still runs inside
// it against the same row.
func (s Core) only(ctx context.Context, holder []byte, fn func(next app.Server) error) error {
	if len(holder) == 0 {
		// A row that reaches nobody. There is no set to be the last of.
		return fn(s.Next())
	}
	if s.drv == nil {
		return s.locked(ctx, nil, s.Next(), holder, fn)
	}

	drv, tx, err := enttx.Begin(ctx, s.drv)
	if err != nil {
		return err
	}

	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		tx.Rollback()

		return err
	}

	if err := s.locked(ctx, drv, next, holder, fn); err != nil {
		tx.Rollback()

		return err
	}

	return tx.Commit()
}

// locked is the swap and the work, in whatever transaction it was given.
//
// The swap goes **first** so that a caller who is going to lose finds out
// before it has read anything, rather than after: the second transaction blocks
// here, and what it does next is answer a conflict rather than count a set that
// is about to change under it.
func (s Core) locked(ctx context.Context, drv dialect.Driver, next app.Server, holder []byte, fn func(app.Server) error) error {
	if s.lock == nil {
		// A stack assembled without one, which is every test that builds a
		// layer by hand. The rule still refuses what it can see; what it cannot
		// do is make two callers see each other.
		return fn(next)
	}

	k, err := pdid.From(holder)
	if err != nil {
		return err
	}

	// The write both callers make, first, so that whoever is going to lose
	// blocks here rather than after counting a set that is about to change
	// underneath them. What it writes is the version column, which is a token
	// rather than a fact about the person -- so it moves and the trail says
	// nothing, and what a reader finds there is the erase that explains it.
	if err := s.lock(ctx, drv, k); err != nil {
		return err
	}

	return fn(next)
}
