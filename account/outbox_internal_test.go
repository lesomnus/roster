package account

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stopped is an outbox on a clock that moves only when the test says.
func stopped() (*outbox, func(time.Duration)) {
	o := newOutbox()
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	o.now = func() time.Time { return now }

	return o, func(d time.Duration) { now = now.Add(d) }
}

// TestAnAllowanceIsABurstAndThenARate: three of a kind to one address at once,
// then one every twenty minutes -- and asking is not spending, and a long
// silence does not save up more than three.
//
// Red with the burst gone (a cooldown of one), with asking spending, and with
// the bucket not held to whole (an idle address saving up without end).
func TestAnAllowanceIsABurstAndThenARate(t *testing.T) {
	x := require.New(t)
	o, pass := stopped()

	for range 10 {
		_, ok := o.may(forRecovery, "bob@fabrikam.com", "")
		x.True(ok)
	}
	for i := range addressBurst {
		_, ok := o.take(forRecovery, "bob@fabrikam.com", "")
		x.True(ok, "message %d of the burst was refused", i+1)
	}
	wait, ok := o.take(forRecovery, "Bob@Fabrikam.com ", "")
	x.False(ok, "a fourth went at once, written differently")
	x.Equal(addressEvery, wait)
	wait, ok = o.may(forRecovery, "bob@fabrikam.com", "")
	x.False(ok)
	x.Equal(addressEvery, wait)

	pass(addressEvery - time.Second)
	_, ok = o.take(forRecovery, "bob@fabrikam.com", "")
	x.False(ok, "one went before its time")
	pass(time.Second)
	_, ok = o.take(forRecovery, "bob@fabrikam.com", "")
	x.True(ok, "none went once its time came")
	_, ok = o.take(forRecovery, "bob@fabrikam.com", "")
	x.False(ok)

	pass(5 * time.Hour)
	for range addressBurst {
		_, ok := o.take(forRecovery, "bob@fabrikam.com", "")
		x.True(ok)
	}
	_, ok = o.take(forRecovery, "bob@fabrikam.com", "")
	x.False(ok, "five quiet hours saved up more than a burst")
}

// TestBothAllowancesOrNeither: a message counts against its address and the
// person who asked for it, and one that either refuses spends neither -- so
// an address that has had its share costs the next asker nothing. Kinds are
// counted apart, and the recovery form, which nobody is signed in at, counts
// only the address.
//
// Red with the account spent when the address refuses, and with one allowance
// for every kind.
func TestBothAllowancesOrNeither(t *testing.T) {
	x := require.New(t)
	o, _ := stopped()

	for i := range addressBurst {
		_, ok := o.take(forConfirm, "shared@fabrikam.com", fmt.Sprint("someone ", i))
		x.True(ok)
	}
	_, ok := o.take(forConfirm, "shared@fabrikam.com", "erin")
	x.False(ok, "the address took a fourth")

	for i := range accountBurst {
		_, ok := o.take(forConfirm, fmt.Sprintf("erin%d@contoso.com", i), "erin")
		x.True(ok, "a refusal about another address spent erin's message %d", i+1)
	}
	wait, ok := o.take(forConfirm, "erin9@contoso.com", "erin")
	x.False(ok, "erin had a sixth sent at once")
	x.Equal(accountEvery, wait)
	_, ok = o.take(forConfirm, "erin9@contoso.com", "")
	x.True(ok, "erin's allowance held a message nobody signed in asked for")

	_, ok = o.take(forRecovery, "shared@fabrikam.com", "")
	x.True(ok, "a recovery message was held to the confirmations")
	_, ok = o.take(forLeaving, "shared@fabrikam.com", "frank")
	x.True(ok, "a deletion link was held to the confirmations")
}

// TestWhatIsWholeAgainIsForgotten: an address or a person whose allowance has
// filled again is not held any more, so the outbox holds what went out in the
// last hour and nothing older.
//
// Red with nothing ever forgotten.
func TestWhatIsWholeAgainIsForgotten(t *testing.T) {
	x := require.New(t)
	o, pass := stopped()

	for i := range 3 {
		_, ok := o.take(forConfirm, fmt.Sprintf("a%d@contoso.com", i), fmt.Sprint("holder ", i))
		x.True(ok)
	}
	x.Len(o.to, 3)
	x.Len(o.by, 3)

	pass(max(addressEvery, accountEvery))
	_, ok := o.take(forRecovery, "bob@fabrikam.com", "")
	x.True(ok)
	x.Len(o.to, 1, "an address whose allowance is whole is still held")
	x.Empty(o.by, "a person whose allowance is whole is still held")
}

// TestAsManyAsMayBeOnTheirWay: [inFlight] slots, the next refused rather than
// waited for, and one given back is one more.
//
// Red with no cap.
func TestAsManyAsMayBeOnTheirWay(t *testing.T) {
	x := require.New(t)
	o := newOutbox()

	var held []func()
	for range inFlight {
		done, ok := o.slot()
		x.True(ok)
		held = append(held, done)
	}
	_, ok := o.slot()
	x.False(ok, "one more than may be went on its way")

	held[0]()
	done, ok := o.slot()
	x.True(ok, "a slot given back was not one more")
	done()
	for _, done := range held[1:] {
		done()
	}
}
