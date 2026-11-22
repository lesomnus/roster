package cli

import (
	"errors"

	"github.com/lesomnus/xli"
)

// Status is the exit status a process ends with for `err`, what [Cmd]'s Run
// answered: the one place it is decided, and `main` asks it.
//
// Every failure was 1, and a script told them apart by gRPC's word on
// standard error, if there was one (#284). A cron job running `roster trail
// verify` could not tell *the trail has a finding* from *the database was not
// there*. So three:
//
//   - 2, the invocation is wrong: what is refused by reading the command line
//     alone, before a file is read, a database opened or a call made. Run
//     again, it is refused again, whatever the deployment is. Everything the
//     parser refuses is ([xli.UsageError]), and what a command refuses of
//     its own flags and arguments says so ([invocation]).
//   - 3, a check ran to its end and found what it looks for ([finding]).
//   - 1, everything else: a refusal, nothing found, roster not there, a file
//     or a configuration that will not do, and a check that could not run.
//
// The kind travels with the error, and the error's words do not change: what
// a command prints is what it printed. `docs/usage/cli.md` § *Exit statuses*
// says the three, and which commands answer 3.
func Status(err error) int {
	if err == nil {
		return 0
	}

	var k *kinded
	if errors.As(err, &k) {
		return k.status
	}
	var u *xli.UsageError
	if errors.As(err, &u) {
		return statusInvocation
	}

	return statusFailed
}

const (
	statusFailed     = 1
	statusInvocation = 2
	statusFound      = 3
)

// kinded is an error and the status it ends a process with.
type kinded struct {
	status int
	err    error
}

func (e *kinded) Error() string { return e.err.Error() }
func (e *kinded) Unwrap() error { return e.err }

// invocation is `err`, a refusal of the command line itself: a flag or an
// argument missing, one that does not parse or is not one of its words, or
// two that do not go together. Status 2.
//
// Only what the command line alone decides. A flag that a setting of the file
// stands for (`--hydra`, `login.hydra.admin`) is the configuration's when it
// is missing, and a value compared with the file's is the file's too: those
// are 1, since the same line typed where the file says otherwise runs.
func invocation(err error) error {
	if err == nil {
		return nil
	}

	return &kinded{status: statusInvocation, err: err}
}

// finding is `err`, a check that ran to its end and found what it looks for.
// Status 3, and only for the checks `docs/usage/cli.md` lists: `trail verify`,
// `trail rows`, `login doctor`, `vouch verify` answered anything but yes, and
// `control vouch prove` answered no.
func finding(err error) error {
	return &kinded{status: statusFound, err: err}
}
