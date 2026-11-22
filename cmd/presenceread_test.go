package cmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdpb"
)

// TestAFieldWrittenByHandHasPresenceWhereItIsRead: a field of a request or an
// answer written by hand says whether it was set only where something asks.
//
// The overlays in `proto/ext` are merged into generated files that declare no
// presence for the whole file, so each of their fields had explicit presence
// unless it said otherwise. In seventeen that is how *unchanged* is told from
// *cleared*, and each is read by a `Has…()`. In three it is what the access
// record keeps of a search: which of a fragment, a department and an employee
// number it was by, with the words taken out (`unword`). In eight nothing
// read it, and it still showed: `HolderService/Reaches` about somebody who
// holds nothing answered `"everySite": false` where `MeService/Get` about the
// same person leaves the field out, and a client's types said those eight
// were optional as much as the twenty were. Right is presence where it is
// read and nowhere else; nothing changes in an encoded message.
//
// So the by-hand fields with explicit presence are this list, each beside the
// line that reads it. A field added with presence that nothing reads fails
// here, and so does one of these whose `Has…()` is gone from its file.
func TestAFieldWrittenByHandHasPresenceWhereItIsRead(t *testing.T) {
	x := require.New(t)

	// The access record reads presence of every field alike, so it is said
	// here and held by `TestARowKeepsHowItWasAskedAndNotTheWordsItWasAskedBy`.
	const record = "cmd/record.go:378"
	read := map[string]string{
		"ConnectionUpdateRequest.issuer":        "server/core/connection.go:698",
		"ConnectionUpdateRequest.client_id":     "server/core/connection.go:701",
		"ConnectionUpdateRequest.secret_ref":    "server/core/connection.go:708",
		"ConnectionUpdateRequest.desc":          "server/core/connection.go:711",
		"ConnectionUpdateRequest.subject_claim": "server/core/connection.go:714",
		"ConnectionUpdateRequest.provisions":    "server/core/connection.go:717",
		"GroupUpdateRequest.name":               "server/core/group.go:332",
		"GroupUpdateRequest.desc":               "server/core/group.go:335",
		"HolderTellRequest.subject":             "server/core/directory.go:460",
		"HolderTellRequest.address":             "server/core/directory.go:476",
		"HolderTellRequest.name":                "server/core/directory.go:488",
		"HostUpdateRequest.desc":                "server/core/host.go:316",
		"MailDomainUpdateRequest.provider":      "server/core/host.go:411",
		"MailDomainUpdateRequest.desc":          "server/core/host.go:414",
		"TenantConfig.password":                 "rstr/tenant.go:34",
		"TenantUpdateRequest.name":              "server/core/tenant.go:230",
		"TenantUpdateRequest.desc":              "server/core/tenant.go:233",

		"HolderSearchRequest.q":           record,
		"HolderSearchRequest.department":  record,
		"HolderSearchRequest.employee_no": record,
	}

	// What the generator writes for an entity, by the name it gives it. Its
	// requests have presence where an `Add` may leave a field out, which is
	// payday's to say and not this test's.
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
	generated := func(m protoreflect.MessageDescriptor) bool {
		name := string(m.Name())
		for _, e := range entities {
			rest, ok := strings.CutPrefix(name, e)
			if !ok {
				continue
			}
			if rest == "" || strings.HasPrefix(rest, "RefBy") || slices.Contains([]string{
				"AddRequest", "GetRequest", "Ref", "Select", "PatchRequest", "ApplyRequest", "EraseResponse",
				"ListRequest", "ListResponse", "Filter", "WatchRequest", "WatchResponse", "WatchItem",
			}, rest) {
				return true
			}
		}
		return false
	}

	// A scalar alone: a message field has presence whatever it says, and so
	// does an arm of a `oneof`.
	has := []string{}
	asked := 0
	var walk func(protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := range ms.Len() {
			m := ms.Get(i)
			walk(m.Messages())
			if m.IsMapEntry() || generated(m) {
				continue
			}
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				if f.Cardinality() == protoreflect.Repeated || f.Kind() == protoreflect.MessageKind || f.ContainingOneof() != nil {
					continue
				}
				asked++
				if f.HasPresence() {
					has = append(has, string(m.Name())+"."+string(f.Name()))
				}
			}
		}
	}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		walk(file.Messages())
		return true
	})
	x.Greater(asked, 100, "few fields were asked, so this proves little")

	want := []string{}
	for f := range read {
		want = append(want, f)
	}
	slices.Sort(want)
	slices.Sort(has)
	x.Equal(want, has, "a field written by hand has presence that nothing reads, or has lost what was read")

	// And each `Has…()` is where it is said to be.
	for f, at := range read {
		if at == record {
			continue
		}
		path, _, _ := strings.Cut(at, ":")
		src, err := os.ReadFile(filepath.Join("..", path))
		x.NoError(err)

		_, field, _ := strings.Cut(f, ".")
		camel := ""
		for _, w := range strings.Split(field, "_") {
			camel += strings.ToUpper(w[:1]) + w[1:]
		}
		x.Contains(string(src), ".Has"+camel+"()", "%s is said to be read in %s, which asks nothing of it", f, path)
	}
}
