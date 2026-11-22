package cmd_test

import (
	"strings"
	"sync"
)

// storeBusyTries is how many times a call the store answered busy is asked
// in all. A busy store is the machine's word, and a loaded runner says it
// more often; five asks is room for a slow machine and still ends a test
// whose store never answers.
const storeBusyTries = 5

// answeredByTheRule makes calls at once, each behind one gate so that all of
// them are in flight together, and asks again those the store answered busy
// until each is answered by the rule, or the tries are spent. It answers each
// call's last error.
//
// A test of two callers at once holds that both do not win, and that the one
// that lost was refused by the rule. SQLite has one writer and gives up
// waiting for the lock after the DSN's `busy_timeout`: on a loaded machine
// both callers of a pair came back `database is locked`, and the test said
// neither went through, which is not the rule's answer but the store's. Such a
// call wrote nothing, since its transaction never committed, so asking it
// again asks the rule what it would have asked; and a pair of which one went
// through and the other was busy asks the second once the first is written,
// which is the order the lock would have put them in anyway.
//
// Only the store's busy word is asked again. PostgreSQL waits for a row's
// lock and does not say busy; a deadlock there is a defect of the order locks
// are taken in, and is left for the test to see.
func answeredByTheRule(calls ...func() error) []error {
	errs := make([]error, len(calls))
	pending := make([]int, len(calls))
	for n := range calls {
		pending[n] = n
	}

	for range storeBusyTries {
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, n := range pending {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[n] = calls[n]()
			}()
		}
		close(start)
		wg.Wait()

		pending = pending[:0]
		for n, err := range errs {
			if storeBusy(err) {
				pending = append(pending, n)
			}
		}
		if len(pending) == 0 {
			break
		}
	}

	return errs
}

// storeBusy is whether err is SQLite saying its one writer is somebody
// else, by the words it says it in: over the wire it arrives as a status
// with those words, and in process as the driver's error wrapped by ent's.
func storeBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is locked")
}
