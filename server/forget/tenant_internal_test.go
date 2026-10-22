package forget

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/ent/migrate"
)

// TestEveryTenantedTableGoesWithItsTenant is [tenanted] held to the schema.
//
// The list is written out on purpose -- which rows are a tenant's is not a
// thing a foreign key states -- so what keeps it whole is this: every table
// with a key to the tenant, and every table with a key to one of those, is
// removed when the tenant is destroyed, or is a person's and goes with them
// ([subjects]). A table added to the schema and to neither fails here rather
// than surviving its tenant.
//
// Except `holder`, which is not removed at all: the blank rows stay for the
// trail, and [Forget] is what empties them.
func TestEveryTenantedTableGoesWithItsTenant(t *testing.T) {
	x := require.New(t)

	named := func(of []subject) map[string]bool {
		out := map[string]bool{}
		for _, v := range of {
			out[strings.ReplaceAll(v.name, " ", "")] = true
		}

		return out
	}
	removed := named(tenanted())
	persons := named(subjects())

	reaches := func(from, to string) bool {
		for _, v := range migrate.Tables {
			if v.Name != from {
				continue
			}
			for _, fk := range v.ForeignKeys {
				if fk.RefTable != nil && fk.RefTable.Name == to {
					return true
				}
			}
		}

		return false
	}

	direct := []string{}
	for _, v := range migrate.Tables {
		if v.Name != "holder" && v.Name != "tenant" && reaches(v.Name, "tenant") {
			direct = append(direct, v.Name)
			x.True(removed[v.Name], "%s is a tenant's and nothing destroys it with the tenant", v.Name)
		}
	}
	x.NotEmpty(direct, "no table reaches the tenant, so this proves nothing")

	for _, v := range migrate.Tables {
		for _, of := range direct {
			if reaches(v.Name, of) {
				x.True(removed[v.Name] || persons[v.Name],
					"%s hangs off %s and nothing destroys it with the tenant", v.Name, of)
			}
		}
	}

	// And what hangs off a person, which goes with them or, when it is kept
	// past them -- a consent -- with their tenant. A table with a key to the
	// holder in neither list outlives everybody.
	for _, v := range migrate.Tables {
		if v.Name != "holder" && reaches(v.Name, "holder") {
			x.True(removed[v.Name] || persons[v.Name],
				"%s hangs off a holder and nothing destroys it, with them or with their tenant", v.Name)
		}
	}
}
