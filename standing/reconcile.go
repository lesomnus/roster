package standing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lesomnus/payday/pdid"
)

// A mark is what an app writes down of an account roster said was erased --
// Loft's `date_leaving` on each membership, levyan's payer whose links end
// with the period -- and acts on later: once roster's own grace is over, or
// the period is. It is a copy of roster's word, and the two writes that undo
// that word do not reach the stream. roster's `restore` brings somebody erased
// back and `forget` destroys what was left of them, and both write past
// roster's servers -- where the stream is published from -- so neither is
// ever heard.
//
// Which gives the rule, and the two calls below keep it: **a mark is acted on
// only after asking roster again**, which is also how a restore is heard; and
// what the stream did not carry at all -- an erasure while the app was not
// listening -- is found by asking about everybody, now and then.

// Reconciled is what roster says of the accounts an app holds, against the
// marks the app keeps on them.
type Reconciled struct {
	// Asked is every account asked about, in the order the app gave them, and
	// roster has Found of them.
	Asked []pdid.Id
	Found int

	// Gone is the accounts roster has nobody by that the app has not marked:
	// marks the stream should have made, and did not. Back is what roster says
	// of the ones the app marked that it has again: marks to take back.
	Gone []pdid.Id
	Back []Standing
}

// NobodyError is a reconcile in which roster had none of the accounts asked
// about and some of them are unmarked: nobody at all is far more often the
// wrong directory, or a key that sees none of it, than everybody having left.
type NobodyError struct{ Accounts int }

func (e *NobodyError) Error() string {
	return fmt.Sprintf("roster has none of the %d account(s) asked about, which is more likely the directory or the key than all of them leaving; mark nothing on it", e.Accounts)
}

// Reconcile asks roster about every account an app holds -- `accounts`, with
// `marked` saying which of them the app marked as gone -- and answers which
// marks are missing and which are wrong ([Reconciled]). It decides nothing and
// writes nothing: putting the marks right is the app's, as making them is.
//
// It is for what the stream missed. The stream replays nothing
// ([Accounts.Follow]), so an account erased while the app was not listening is
// never marked and never acted on. An app runs it now and then by itself, and
// once when it is asked to: Loft's `identity reconcile` and its
// `app.accounts.reconcile`.
//
// Everything is asked before anything is answered, so that a roster which stops
// answering half way decides nothing: then it answers nil and the error. And a
// roster that has none of them, when there are unmarked ones to mark, answers
// what it found **and** a [NobodyError] -- refused unless the app says
// otherwise, because marking everybody is the one mistake a wrong
// configuration makes look like news. A report of what roster said may print
// it all the same; an app that marks does not.
//
// The ones whose marks are to change are dropped from what this process knows
// ([Accounts.Recheck]), since what they were held to is out of date.
func (a *Accounts) Reconcile(ctx context.Context, accounts []pdid.Id, marked func(pdid.Id) bool) (*Reconciled, error) {
	if marked == nil {
		marked = func(pdid.Id) bool { return false }
	}

	out := &Reconciled{}
	seen := make(map[pdid.Id]bool, len(accounts))
	for _, v := range accounts {
		if seen[v] {
			continue
		}
		seen[v] = true
		out.Asked = append(out.Asked, v)

		st, err := a.StandingOf(ctx, v)
		switch {
		case errors.Is(err, ErrNoPerson):
			if !marked(v) {
				out.Gone = append(out.Gone, v)
			}
		case err != nil:
			return nil, fmt.Errorf("roster could not be asked about %s, so nothing was decided: %w", v, err)
		default:
			out.Found++
			if st.Erased.IsZero() && marked(v) {
				out.Back = append(out.Back, st)
			}
		}
	}

	for _, v := range out.Gone {
		a.Recheck(v)
	}
	for _, st := range out.Back {
		a.Recheck(st.Holder)
	}
	if out.Found == 0 && len(out.Gone) > 0 {
		return out, &NobodyError{Accounts: len(out.Asked)}
	}

	return out, nil
}

// Settle is one look at the accounts an app marked as gone and is about to act
// on, asking roster about each again first: `gone` is called for each that
// roster still has nobody by, which is the mark to act on, and `back` with
// what roster says of each it has again -- restored, which is what roster's
// grace was for -- which is the mark to take back. Either may be nil, for
// nothing to do.
//
// About an account roster could not answer for, nothing is decided: it is said
// and left, and asked about again at the next look. What `gone` or `back`
// answer with is gathered and answered once every account was looked at, so
// that one that failed does not keep the rest waiting a whole pass.
//
// Each account roster answered for is dropped from what this process knows
// ([Accounts.Recheck]), since acting on a mark or taking it back is a change.
func (a *Accounts) Settle(
	ctx context.Context,
	marked []pdid.Id,
	gone func(ctx context.Context, account pdid.Id) error,
	back func(ctx context.Context, v Standing) error,
) error {
	seen := make(map[pdid.Id]bool, len(marked))
	var errs []error
	for _, v := range marked {
		if seen[v] {
			continue
		}
		seen[v] = true

		st, err := a.StandingOf(ctx, v)
		switch {
		case errors.Is(err, ErrNoPerson):
			a.Recheck(v)
			if gone != nil {
				if err := gone(ctx, v); err != nil {
					errs = append(errs, fmt.Errorf("account %s: %w", v, err))
				}
			}
		case err != nil:
			if ctx.Err() != nil {
				return errors.Join(append(errs, ctx.Err())...)
			}
			a.log(ctx).WarnContext(ctx, "standing: roster could not say whether an account is still gone; it is asked again next time",
				"account", v.String(), slog.Any("err", err))
		case st.Erased.IsZero():
			a.Recheck(v)
			if back != nil {
				if err := back(ctx, st); err != nil {
					errs = append(errs, fmt.Errorf("account %s: %w", v, err))
				}
			}
		default:
			// Erased, and still readable: nothing roster serves answers that
			// today, and if one ever does it is a mark kept rather than one
			// acted on before roster said nobody.
			a.Recheck(v)
		}
	}

	return errors.Join(errs...)
}
