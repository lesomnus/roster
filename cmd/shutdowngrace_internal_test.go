package cmd

import "time"

// SeeStops has `f` told as each listener of `s` begins its stop, with the
// deadline it was given, and as it has stopped: what `shutdowngrace_test.go` reads
// instead of timing the stop.
func SeeStops(s *Server, f func(listener string, until time.Time, stopped bool)) {
	s.stopsSeen = f
}
