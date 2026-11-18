package account

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSomebodyWhoLeavesIsToldFromWhenAndWhatIsKept: the page that answers a
// deleted account said *Until 2 January 2027, when it is forgotten ... after
// that, everything held about you is destroyed*. Four things about that are
// not so. The date is when the grace runs out, and the forget is made by the
// first pass after it, up to `holder.every` later; a restore works until that
// pass. A legal hold on the tenant's records stops the clock. What they
// agreed to is kept past them as proof, for `consent.keep`, which is for good
// unless the deployment says. And the record of what was done and of who read
// their data stays, by identifier.
//
// It is a promise made to somebody at the moment they leave, so it says what
// is done: *from* that date, and what is kept.
func TestSomebodyWhoLeavesIsToldFromWhenAndWhatIsKept(t *testing.T) {
	at := time.Date(2027, time.January, 2, 15, 4, 5, 0, time.UTC)

	for name, said := range map[string]string{
		"where the deployment names a window": leftSaying(at),
		"and where it names none":             leftSaying(time.Time{}),
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			x.NotContains(said, "everything held about you is destroyed", "it is told that everything is destroyed, and their consents and the record are kept")
			x.Contains(said, "bring it back", "that it can be undone, and by whom, is not said")
			x.Contains(said, "agreed to", "that what they agreed to is kept is not said")
			x.Contains(said, "record of what was done", "that the record of what was done and of who read their data is kept is not said")
			x.Contains(said, "legal claim", "that a hold puts it off is not said")
		})
	}

	t.Run("the date is from when, and not when", func(t *testing.T) {
		x := require.New(t)
		said := leftSaying(at)

		x.Contains(said, "From 2 January 2027", "the date is not said as the moment it is forgotten from")
		x.NotContains(said, "Until 2 January 2027, when it is forgotten", "the date is said as the moment it is forgotten")
		x.NotContains(leftSaying(time.Time{}), "From ", "a date is said where the deployment names none")
	})
}
