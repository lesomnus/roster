package cmd

import (
	"sort"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestEveryKindOnTheRecordSaysWhoseTenantItIsIn holds the list a read's tenant
// is looked up by ([whoseTenant]) to the schema: every kind that declares a
// field personal is a kind whose reads are rows of the access record, and a
// row is filed under the tenant of whoever was read only where this knows how
// that kind says whose it is.
//
// The list was four kinds, and the part that wrote it declared five more in
// another change (#280): each of the two was right alone, and together an
// operator's read of a customer's key was on the record under the operators'
// own tenant. A kind declared tomorrow fails here until it is asked.
func TestEveryKindOnTheRecordSaysWhoseTenantItIsIn(t *testing.T) {
	x := require.New(t)

	personal := map[pdid.Domain]string{}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		for i := range file.Messages().Len() {
			m := file.Messages().Get(i)
			if !proto.HasExtension(m.Options(), pdpb.E_Entity) {
				continue
			}
			for j := range m.Fields().Len() {
				o, _ := proto.GetExtension(m.Fields().Get(j).Options(), pdpb.E_Field).(*pdpb.Field)
				if o.GetPersonal() {
					d, ok := pdid.Lookup(string(m.FullName()))
					x.True(ok, "%s declares a field personal and is a kind of no identifier", m.FullName())
					personal[d] = string(m.Name())

					break
				}
			}
		}

		return true
	})
	x.GreaterOrEqual(len(personal), 9, "few kinds were read, so this proves little")

	var unasked []string
	for d, name := range personal {
		if _, ok := whoseTenant[d]; !ok {
			unasked = append(unasked, name)
		}
	}
	sort.Strings(unasked)
	x.Empty(unasked, "a read of each of these is a row of the access record, and nothing here says which tenant a row of it is in, "+
		"so an operator's read is filed under the operators' own: add it to whoseTenant in cmd/record.go")

	for d := range whoseTenant {
		x.Contains(personal, d, "%s is looked up for the record and declares nothing personal, so no read of it is a row", d)
	}
}
