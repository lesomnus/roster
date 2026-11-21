package reach

import (
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
)

// ShortenHeldWait makes `standing.HeldWait` `d` for the holders a test makes
// after it, so that no test sits out five seconds of a roster that says
// nothing.
func ShortenHeldWait(t *testing.T, d time.Duration) {
	was := heldWait
	heldWait = d
	t.Cleanup(func() { heldWait = was })
}

// Kept is whether an answer about somebody is kept just now: for the tests,
// which have no other way to tell an answer that was kept from one that was
// asked for again, and so none to know that a later answer came of a drop.
func (h *Holders) Kept(who pdid.Id) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.known[who]

	return ok
}
