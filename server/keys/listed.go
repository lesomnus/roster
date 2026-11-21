package keys

import (
	"fmt"

	"github.com/lesomnus/payday/pderr"
)

// A list of methods has a length: how many it names, how long each is, and
// how much it is in all.
//
// # Why
//
// A role's list, a key's and a delegation's were whatever their caller sent.
// `ApiKeyService/Issue` is everybody's own to call, for a key on their own
// account, so somebody bound one role wrote five keys of 900 KiB of methods
// each and their administrator's list of keys answered `ResourceExhausted`,
// as the list of people did before a name had a length
// (`server/core/form.go`).
//
// # The numbers
//
// roster serves 213 methods, the longest of them 36 bytes, and a list that
// named every one of them is six thousand bytes. So nothing real is near a
// thousand entries, an entry of 255 bytes, or sixteen kibibytes in all -- and
// a list that means *all of it* is one pattern.
//
// The third is what makes the other two a bound: 1,024 entries of 255 bytes
// are a quarter of a megabyte in one row, and a page of twenty such rows is
// not answered. Sixteen kibibytes is what leaves a page of a hundred roles,
// each with its name, its note, its labels and its list at their bounds,
// inside one answer.
//
// # Why it is here
//
// It is asked where every list is written -- `server/core`'s form, in front
// of the store -- and that would be its home. But a delegation's list is
// first read where a front door says it, before a password is checked or a
// link spent (`server/vouch`, `mayDelegate`), and that package is one
// `server/core` imports. So the rule is in the package both of them do.
const (
	// MaxMethods is the most entries a list of methods has.
	MaxMethods = 1024

	// MaxMethod is the most bytes one entry is.
	MaxMethod = 255

	// MaxMethodsSize is the most bytes the entries are together.
	MaxMethodsSize = 16 << 10
)

// Listed refuses a list of methods that is past one of the three, naming
// which.
func Listed(field string, methods []string) error {
	if len(methods) > MaxMethods {
		return pderr.Invalidf(field, "at most %d entries, and these are %d", MaxMethods, len(methods))
	}

	size := 0
	for i, m := range methods {
		if len(m) > MaxMethod {
			return pderr.Invalidf(fmt.Sprintf("%s[%d]", field, i), "at most %d bytes, and this is %d", MaxMethod, len(m))
		}
		size += len(m)
	}
	if size > MaxMethodsSize {
		return pderr.Invalidf(field, "at most %d bytes in all, and these are %d", MaxMethodsSize, size)
	}

	return nil
}
