package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
)

// TestTheGatesOwnReadHasAnIndex: what the gate reads for every call by
// somebody who is bound, and what narrows a list behind it, each has an index
// to read by. On PostgreSQL with a tenant of five thousand, `bindingsReaching`
// was a sequential scan of `binding` twice in every such call -- a table with
// no tenant column, so it grows with every tenant's bindings -- a list of a
// team's members walked an index and threw away 1,985 rows to find 21, and a
// caller bound in a site had its people narrowed by a sequential scan of
// `sitemembership`.
//
// By name and column, from the tables the migration is made from, so that
// taking one out fails here and not on a deployment's clock. None is unique:
// each column repeats.
func TestTheGatesOwnReadHasAnIndex(t *testing.T) {
	for table, want := range map[string]map[string]string{
		"binding":         {"binding_by_holder": "holder_id", "binding_by_group": "group_id"},
		"groupmembership": {"groupmembership_by_group": "group_id"},
		"teammembership":  {"teammembership_by_team": "team_id"},
		"sitemembership":  {"sitemembership_by_site": "site_id"},
	} {
		t.Run(table, func(t *testing.T) {
			x := require.New(t)

			var has map[string][]string
			unique := map[string]bool{}
			for _, tb := range entmigrate.Tables {
				if tb.Name != table {
					continue
				}
				has = map[string][]string{}
				for _, v := range tb.Indexes {
					for _, c := range v.Columns {
						has[v.Name] = append(has[v.Name], c.Name)
					}
					unique[v.Name] = v.Unique
				}
			}
			x.NotNil(has, "no table %s", table)

			for name, column := range want {
				x.Equal([]string{column}, has[name], "%s has no index %s over %s alone", table, name, column)
				x.False(unique[name], "%s is unique, and %s repeats", name, column)
			}
		})
	}
}
