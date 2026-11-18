package forget

import (
	"slices"
	"strings"
	"testing"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// TestEveryRowThatNamesAPersonIsAskedAboutThem is [naming] held to the schema.
//
// A forgotten person's row stays, so nothing in the database refuses a row
// written for one, and what does is a list ([Nobody]). A kind added to the
// schema with a key to a holder, and not to the list, is one whose rows can
// be left on somebody forgotten for good -- which is what the list was
// written for, where five kinds were.
//
// Held twice, since the list is spelled in both: to the messages, for which
// writes name a person -- an `Add` of every kind with an edge to a holder,
// and a `Patch` and an `Apply` of each whose edge is not fixed when its row
// is made -- and to the tables, for the columns such an edge is kept in.
func TestEveryRowThatNamesAPersonIsAskedAboutThem(t *testing.T) {
	x := require.New(t)

	person := (&app.Holder{}).ProtoReflect().Descriptor()

	// What the tables key to a holder.
	keyed := map[string][]string{}
	for _, v := range migrate.Tables {
		for _, fk := range v.ForeignKeys {
			if fk.RefTable == nil || fk.RefTable.Name != holder.Table {
				continue
			}

			x.Len(fk.Columns, 1)
			keyed[v.Name] = append(keyed[v.Name], fk.Columns[0].Name)
		}
	}
	x.NotEmpty(keyed, "no table is keyed to a holder, so this proves nothing")

	// What the messages say: every kind with an edge to a holder, and whether
	// a write can point one of its edges at somebody else.
	want := map[string][]string{}
	kinds := 0
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != person.ParentFile().Package() {
			return true
		}

		for i := range file.Messages().Len() {
			m := file.Messages().Get(i)

			columns, moves := []string{}, false
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				if f.Message() == nil || f.Message().FullName() != person.FullName() || !proto.HasExtension(f.Options(), ormpb.E_Edge) {
					continue
				}

				columns = append(columns, string(f.Name())+"_id")
				if o, _ := proto.GetExtension(f.Options(), ormpb.E_Edge).(*ormpb.EdgeOptions); !o.GetImmutable() {
					moves = true
				}
			}
			if len(columns) == 0 {
				continue
			}
			kinds++

			table := strings.ToLower(string(m.Name()))
			x.ElementsMatch(columns, keyed[table], "%s names a holder in %v, and its table is keyed to one by %v", m.Name(), columns, keyed[table])

			service := "/" + string(m.FullName()) + "Service/"
			want[service+"Add"] = columns
			if moves {
				want[service+"Patch"] = columns
				want[service+"Apply"] = columns
			}
		}

		return true
	})
	x.Equal(len(keyed), kinds, "the messages and the tables do not agree about which kinds name a holder")

	for by, columns := range want {
		of, asked := naming[by]
		x.True(asked, "%s writes a row that names a person, and nothing asks whether they are forgotten", by)
		x.ElementsMatch(columns, of.columns, "%s is asked about %v, and names a person in %v", by, of.columns, columns)
		x.Equal(strings.ToLower(strings.TrimSuffix(strings.Split(by, "/")[1], "Service")[len(person.ParentFile().Package())+1:]), of.table,
			"%s is asked about another kind's table", by)
	}
	for by := range naming {
		x.Contains(want, by, "%s is asked about and writes no row that names a person", by)
	}

	// And a nomination is the one kind whose edge moves, which is why two of
	// its writes besides `Add` are here. If that stops being so, or another
	// kind's edge is freed, the comment on [naming] is what to read again.
	moved := []string{}
	for by := range want {
		if !strings.HasSuffix(by, "/Add") {
			moved = append(moved, by)
		}
	}
	slices.Sort(moved)
	x.Equal([]string{app.NominationService_Apply_FullMethodName, app.NominationService_Patch_FullMethodName}, moved)
}
