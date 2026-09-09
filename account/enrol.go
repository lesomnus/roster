package account

import (
	"github.com/lesomnus/roster/arrives"
)

// The enrolment policy is `arrives`', because both of roster's front doors ask
// it the same question and a second copy of the answer would drift. These names
// stay for the deployments and tests that already say `account.Enrolling()`.

// Caller is who a provider said signed in. See [arrives.Caller].
type Caller = arrives.Caller

// Enrol decides what happens when somebody signs in at a provider and roster
// has never seen them. See [arrives.Enrol].
type Enrol = arrives.Enrol

// ErrUninvited is what an [Enrol] answers when this person gets no account.
var ErrUninvited = arrives.ErrUninvited

// Invited refuses everybody roster has not been told about.
func Invited() Enrol { return arrives.Invited() }

// Expected admits somebody an operator entered, by the address on their row,
// and nobody else.
func Expected() Enrol { return arrives.Expected() }

// Enrolling makes an account for anybody the provider vouches for, after
// looking for an invitation the way [Expected] does.
func Enrolling() Enrol { return arrives.Enrolling() }
