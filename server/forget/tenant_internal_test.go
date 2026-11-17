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

// TestEveryTableKeyedToATenantIsWhereItsLeftoversAreLookedFor is the other
// thing [tenanted] says, held to the schema: which of a tenant's kinds say
// whose they are in a column of their own.
//
// [Unfinished] finds a destruction that stopped by what is left of the tenant,
// and looks where those columns are. A table with a key to the tenant that is
// not written with `own` is one whose rows a stopped destruction could leave
// with nothing to find them by -- the defect this list was added for, where
// the only place looked was the people.
func TestEveryTableKeyedToATenantIsWhereItsLeftoversAreLookedFor(t *testing.T) {
	x := require.New(t)

	looked := map[string]string{}
	for _, v := range tenanted() {
		if v.table != "" {
			looked[v.table] = v.tenant
		}
	}
	x.NotEmpty(looked, "nothing is looked for, so this proves nothing")

	keyed := 0
	for _, v := range migrate.Tables {
		if v.Name == "holder" || v.Name == "tenant" {
			continue
		}
		for _, fk := range v.ForeignKeys {
			if fk.RefTable == nil || fk.RefTable.Name != "tenant" {
				continue
			}

			keyed++
			x.Len(fk.Columns, 1)
			x.Equal(fk.Columns[0].Name, looked[v.Name],
				"%s is keyed to a tenant by %s, and a destruction that stopped is not found by it", v.Name, fk.Columns[0].Name)
		}
	}
	x.NotZero(keyed, "no table is keyed to the tenant, so this proves nothing")

	// And each place looked is a column there is: a name mistyped is a query
	// that fails at every pass of the sweep.
	for table, column := range looked {
		has := false
		for _, v := range migrate.Tables {
			if v.Name != table {
				continue
			}
			for _, c := range v.Columns {
				has = has || c.Name == column
			}
		}
		x.True(has, "%s.%s is looked in and is not a column", table, column)
	}
}
