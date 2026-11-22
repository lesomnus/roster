package cmd

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTheListenersStopTogether.
//
// [ShutdownGrace] is chosen against a number somebody else owns: `docker stop`
// waits ten seconds and then sends SIGKILL, so a longer grace is one nothing
// lives to use. That argument holds for **one** listener, and a deployment with
// a control plane and an admin port opens five -- each of which waits its whole
// grace when a stream is in flight on it, which for these ports means a console
// with a live screen open or a product app watching.
//
// Stopped by a `defer` each, they ran one after another: five graces end to
// end, twenty-five seconds against a ten-second budget, with four of the five
// never having been asked before the process was killed. They have nothing to
// say to each other -- one listener draining is not a reason for the next to
// still be accepting -- so the budget is the grace whatever a deployment
// opened.
//
// Written against the type rather than against a served deployment, and the
// reason is worth saying: a test that opens all five and holds a stream can
// only hold one without a session cookie, so serial and together both answer in
// one grace and it discriminates nothing. What has to be true is a property of
// this loop, so this is where it is asked.
//
// Asked as what it means: the five are in flight at one moment. Each stop
// waits until all five have begun, and only then returns; run together they
// all begin at once and none waits for long. It timed five sleeps and held
// them to under three, which a slow machine fails with the loop right. The
// bound below is only how long a stop waits for the others before the test
// says they did not come, which is what one after another looks like.
func TestTheListenersStopTogether(t *testing.T) {
	x := require.New(t)

	const stops = 5

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var began, ran atomic.Int32
	all := make(chan struct{})

	var v shutdown
	for range stops {
		v.add(func() {
			// All five begun, and none of them over yet.
			if began.Add(1) == stops && ran.Load() == 0 {
				close(all)
			}
			select {
			case <-all:
			case <-ctx.Done():
			}
			ran.Add(1)
		})
	}

	v.run()

	x.Equal(int32(stops), ran.Load(), "a stop that was added was not run")
	select {
	case <-all:
	default:
		x.Fail("the stops ran one after another, so the grace is per listener")
	}
}

// TestEveryStopAddedIsRun, which is the whole reason this is a type rather
// than a slice and a loop written at the end of `Serve`.
//
// The loop was what was there. What got written instead was a `defer` beside
// each listener, which is the same thing said five times and the version that
// is one line away from being said four.
func TestEveryStopAddedIsRun(t *testing.T) {
	x := require.New(t)

	var v shutdown
	x.NotPanics(v.run, "a deployment that opened no listener still stops")

	seen := make(chan int, 3)
	for i := range 3 {
		v.add(func() { seen <- i })
	}
	v.run()

	x.Len(seen, 3)
}
