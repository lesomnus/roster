// Package moment is a time as roster's databases keep it, for the rows roster
// writes itself.
//
// The generated store cuts every time it writes to the microsecond, in UTC, on
// every database (lesomnus/payday#324): PostgreSQL's `timestamptz` keeps no
// more, and SQLite under payday keeps nanoseconds, so a time written with them
// was one thing on a checkout and another on a deployment. What roster writes
// past the store, straight through ent -- a session, a forgetting, a name
// released -- is not the store's, and goes through [Of] for the same reason: a
// time roster stores is the time a read gives back, wherever it runs, and what
// roster answers after such a write is that time.
//
// Only what is written. A comparison does not use it: a `Where(...LT(now))`, an
// expiry tested against the clock, a version sent as a patch's test are made
// with the time as it is, so that a row stored before this, which on SQLite
// can hold nanoseconds, is read, found and written over as it is.
//
// payday's `past.moment` is the model, and the generated store's `moment` is
// the same rule, unexported. A package of its own, and of nothing but the
// clock, so that every writer can import it without a dependency between
// packages that had none.
package moment

import "time"

// Of is `at` as a database keeps it: in UTC, to the microsecond. For a time
// roster writes to a row itself, a stamp or a time handed in or computed; never
// for one it compares.
func Of(at time.Time) time.Time { return at.UTC().Truncate(time.Microsecond) }

// Now is the clock's time as [Of] keeps it, for a stamp roster writes itself.
func Now() time.Time { return Of(time.Now()) }
