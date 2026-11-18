package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMeLeaveSaysFromWhenAndWhatIsKept: `roster me leave` printed *until
// <time> whoever runs this deployment can restore it; then it is forgotten*,
// which is the account page's sentence at a shell and wrong the same way
// (`account.TestSomebodyWhoLeavesIsToldFromWhenAndWhatIsKept`): the moment is
// when the grace runs out, the forget is the first pass after it, a hold puts
// it off, and what was consented to and the record are kept.
func TestMeLeaveSaysFromWhenAndWhatIsKept(t *testing.T) {
	erased := time.Date(2026, time.December, 3, 15, 4, 5, 0, time.UTC)
	at := erased.Add(720 * time.Hour)

	for name, lines := range map[string][]string{
		"where the deployment names a window": leftSaying(erased, at),
		"and where it names none":             leftSaying(erased, time.Time{}),
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			said := strings.Join(lines, "\n")

			x.Contains(said, "erased (2026-12-03T15:04:05Z)")
			x.NotContains(said, "then it is forgotten", "the moment the grace runs out is said as the moment of the forget")
			x.Contains(said, "restore", "that it can be undone, and by whom, is not said")
			x.Contains(said, "consent", "that what they consented to is kept is not said")
			x.Contains(said, "access record", "that the record of who read their data is kept is not said")
			x.Contains(said, "hold", "that a hold puts it off is not said")
		})
	}

	t.Run("the moment is from when, and not when", func(t *testing.T) {
		x := require.New(t)

		x.Contains(strings.Join(leftSaying(erased, at), "\n"), "from 2027-01-02T15:04:05Z", "the moment is not said as from when")
		x.NotContains(strings.Join(leftSaying(erased, time.Time{}), "\n"), "from 20", "a moment is said where the deployment names none")
	})
}
