package cmd

import (
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
func TestTheListenersStopTogether(t *testing.T) {
	x := require.New(t)

	const each = 200 * time.Millisecond

	var ran atomic.Int32

	var v shutdown
	for range 5 {
		v.add(func() {
			time.Sleep(each)
			ran.Add(1)
		})
	}

	at := time.Now()
	v.run()
	took := time.Since(at)

	x.Equal(int32(5), ran.Load(), "a stop that was added was not run")

	// One of them and not five. Generously, because what is asserted is that
	// they do not queue rather than how precisely a sleep returns: five in
	// series is five times this, which is nowhere near.
	x.Less(took, 3*each,
		"the stops ran one after another, so the grace is per listener")
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
