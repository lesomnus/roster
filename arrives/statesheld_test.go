package arrives_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/arrives"
)

// TestRoundTripsBegunAreABoundedMany: what a front door holds for round trips
// nobody has finished has a ceiling.
//
// A round trip is begun by `GET /login`, which asks for no session, and what
// it began was held for ten minutes -- as many of them as anybody cared to
// begin, each insert sweeping the whole of what was held under the one lock a
// callback takes too (#277). Past [arrives.StatesHeld] a new one is not held,
// and whoever asked is told to come back; what has run out makes room.
func TestRoundTripsBegunAreABoundedMany(t *testing.T) {
	x := require.New(t)
	s := arrives.Held[int]()

	for i := range arrives.StatesHeld {
		x.True(s.Put(fmt.Sprintf("state-%d", i), i, time.Minute), "the %dth was not held", i)
	}
	x.False(s.Put("one-too-many", 0, time.Minute), "a round trip was held past the ceiling")

	_, ok := s.Take("one-too-many")
	x.False(ok)

	// One finished is one more that may begin.
	v, ok := s.Take("state-7")
	x.True(ok)
	x.Equal(7, v)
	x.True(s.Put("after", 0, time.Minute))

	// And what has run out is not counted.
	short := arrives.Held[int]()
	for i := range arrives.StatesHeld {
		x.True(short.Put(fmt.Sprintf("state-%d", i), i, time.Nanosecond))
	}
	time.Sleep(time.Millisecond)
	x.True(short.Put("later", 0, time.Minute), "round trips that had run out still took the room")
}
