package core

import (
	"context"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// atomically runs `fn` as one transaction, for a rule whose writes must land
// together or not at all -- an address that leaves one row and arrives on
// another, which is no rows for a moment if the two are seen apart.
//
// Its own transaction where this stack has a driver to begin one on
// ([Core.drv]), with `fn` handed the server rebound onto it. And where the
// stack is itself running on somebody else's -- a batch ([Core.rebound]) --
// `fn` runs in that one, on the server this layer is over: a transaction begun
// inside it would be a second one, and refusing, which this did, made the rule
// one that no batch could carry.
//
// A stack with neither, built without a driver and called outside any
// transaction, cannot make the writes one, and is refused: `what` says what it
// will not do.
func (s Core) atomically(ctx context.Context, what string, fn func(next app.Server) error) error {
	if s.drv == nil {
		if !s.rebound {
			return status.Errorf(codes.FailedPrecondition, "this server cannot open a transaction, so it will not %s", what)
		}

		return fn(s.Next())
	}

	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return err
	}
	if err := fn(next); err != nil {
		return err
	}

	return tx.Commit()
}
