package cmd_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/lesomnus/roster/rstr"
)

// TestNoRpcIsDeclaredThatNothingServesAndNothingCalls: what the descriptors
// declare is what every generated client is handed, and a method in them is a
// method a caller may build against.
//
// `SessionService` had all six of the generated verbs, was registered on no
// port, and was called by nothing in process: the store writes a session
// through ent. `LinkService` and `ContinuationService` had `Patch`, `Apply`
// and `List` that nothing called, and every field of both is immutable, so a
// `Patch` could write nothing. Answered: declared, and in every client; right
// is declared only where something here serves or calls it.
//
// So a session is read, which the box still lists, and nothing writes one by
// a call; a link and a continuation are added, read and erased, as
// `server/vouch` and `server/core` do. `List` comes with `Get` from the
// generator and is kept with it. Putting `crud: true` back on any of the
// three fails here.
func TestNoRpcIsDeclaredThatNothingServesAndNothingCalls(t *testing.T) {
	x := require.New(t)

	declared := map[string][]string{
		"roster.SessionService":      {"Get", "List"},
		"roster.LinkService":         {"Add", "Erase", "Get", "List"},
		"roster.ContinuationService": {"Add", "Erase", "Get", "List"},
	}
	for name, want := range declared {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		x.NoError(err, name)
		s, ok := d.(protoreflect.ServiceDescriptor)
		x.True(ok, "%s is not a service", name)

		has := []string{}
		for i := range s.Methods().Len() {
			has = append(has, string(s.Methods().Get(i).Name()))
		}
		slices.Sort(has)
		x.Equal(want, has, "%s declares RPCs nothing here serves or calls", name)
	}
}
