package standing

import (
	"testing"
	"time"
)

// ShortenHeldWait makes [HeldWait] `d` for the accounts a test makes after it,
// so that no test sits out five seconds of a roster that says nothing.
func ShortenHeldWait(t *testing.T, d time.Duration) {
	was := heldWait
	heldWait = d
	t.Cleanup(func() { heldWait = was })
}
