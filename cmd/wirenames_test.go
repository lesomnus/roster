package cmd_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdpb"

	_ "github.com/lesomnus/roster/rstr"
)

// wireNamesEntities is every entity of the package, by the name the generator
// gives it.
func wireNamesEntities(x *require.Assertions) []string {
	entities := []string{}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		for i := range file.Messages().Len() {
			m := file.Messages().Get(i)
			if proto.HasExtension(m.Options(), pdpb.E_Entity) {
				entities = append(entities, string(m.Name()))
			}
		}
		return true
	})
	x.Greater(len(entities), 20, "few entities were read, so this proves little")
	return entities
}

// wireNamesGenerated tells a message the generator writes for an entity from
// one written by hand, by the name it gives it.
func wireNamesGenerated(entities []string, m protoreflect.MessageDescriptor) bool {
	name := string(m.Name())
	for _, e := range entities {
		rest, ok := strings.CutPrefix(name, e)
		if !ok {
			continue
		}
		if rest == "" || strings.HasPrefix(rest, "RefBy") || slices.Contains([]string{
			"AddRequest", "GetRequest", "Ref", "Select", "PatchRequest", "ApplyRequest", "EraseRequest", "EraseResponse",
			"ListRequest", "ListResponse", "Filter", "WatchRequest", "WatchResponse", "WatchItem",
		}, rest) {
			return true
		}
	}
	return false
}

// TestARequestsRefIsItsServicesOwnRow: in every request written by hand, a
// field named `ref` is a reference to the entity its service is of, and a
// person who is not that row is `holder`.
//
// `CredentialService/Set`, `Enrol`, `Unlock` and `Issue` took the person as
// `ref`, a `HolderRef`, on the service whose `Erase` takes a credential as
// `ref`; the account page sent both on one screen. In twenty-three other
// requests `ref` is the service's own row, and where a request names a person
// who is not, as `ApiKey.Issue` does, the field is `holder`. Answered: `ref`
// is a person in four; right is a person is `holder` there as everywhere.
func TestARequestsRefIsItsServicesOwnRow(t *testing.T) {
	x := require.New(t)

	entities := wireNamesEntities(x)
	asked := 0
	other := []string{}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		for i := range file.Services().Len() {
			s := file.Services().Get(i)
			entity, ok := strings.CutSuffix(string(s.Name()), "Service")
			if !ok || !slices.Contains(entities, entity) {
				// A service of no entity (`MeService`, `VouchService`, ...)
				// has no row of its own for a `ref` to be.
				continue
			}
			for j := range s.Methods().Len() {
				in := s.Methods().Get(j).Input()
				if wireNamesGenerated(entities, in) {
					continue
				}
				f := in.Fields().ByName("ref")
				if f == nil {
					continue
				}
				asked++
				if f.Kind() != protoreflect.MessageKind || string(f.Message().Name()) != entity+"Ref" {
					other = append(other, string(in.Name())+".ref")
				}
			}
		}
		return true
	})
	x.Greater(asked, 15, "few requests were read, so this proves little")
	x.Empty(other, "a request's `ref` that is not its service's own row: a person is `holder`")
}
