package core

import (
	"context"
	"errors"
)

// A dry run is held to every check a write is held to, and stops short of the
// write alone.
//
// `roster resources apply --dry-run` says what the run would do before a
// restart does it. It read each row and answered `+` or `~` without asking any
// of the rules on the way to the write, so it promised rows the run then
// refused: a connection's name that is no alias, an issuer not written as it
// is compared, a claim a provider does not issue. A dry run that answers from
// a copy of the rules is a copy that falls behind them; so it goes the run's
// own way through this layer, and the one thing it does not do is the write.
//
// The stop is in [held], which is where the form is asked and the last thing
// in front of the store each write of a row with a name passes: every rule of
// this layer has been asked by then, the form too, and nothing has been
// written. A rule that makes several writes one makes them in a transaction,
// and [ErrDry] is an error, so whatever that transaction had begun is rolled
// back with it.
//
// What only the store says is not asked: that a tenant a request names is
// there, and a unique index. The caller that asks for a dry run says those
// itself, or says that it cannot (`cmd/resources.go`).

type dryKey struct{}

// Dry is `ctx` for a dry run: each write of a row this layer holds to its form
// answers [ErrDry] where it would have been made.
func Dry(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryKey{}, true)
}

// ErrDry is what a write answers in a dry run once it has passed everything it
// is asked: it would have been made.
var ErrDry = errors.New("a dry run: this would have been written")

// dry is whether `ctx` is a dry run's.
func dry(ctx context.Context) bool {
	v, _ := ctx.Value(dryKey{}).(bool)

	return v
}
