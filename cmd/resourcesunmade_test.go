package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
)

// TestResourcesApplyAtADatabaseNeverMadeSaysSo: `roster resources apply` at a
// deployment nothing had started -- no `init`, no `serve` -- answered the
// driver's words, `sqlite3: SQL logic error: no such table: tenant`, exit 1,
// having made the data plane's tables where `db.migrate` was on; and its dry
// run printed the whole migration it was not going to run, `CREATE TABLE` by
// `CREATE TABLE`, with no sentence of its own.
//
// Both refuse before a row is read or written, saying that the database has
// not been made and what makes it. They do not make it: the command writes
// rows, and making the schema of a new deployment is `init`'s and `serve`'s.
func TestResourcesApplyAtADatabaseNeverMadeSaysSo(t *testing.T) {
	const file = "resources:\n  - kind: Tenant\n    alias: contoso\n    name: Contoso\n"

	for _, args := range [][]string{
		{"resources", "apply"},
		{"resources", "apply", "--dry-run"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			x := require.New(t)
			c := seedbed(t)
			c.Db.Migrate, c.Control.Db.Migrate = true, true

			out, err := entities(t, &c, append(args, "--file", declare(t, file)[0])...)
			x.Error(err)
			x.Contains(err.Error(), "this deployment's database has not been made yet")
			x.Contains(err.Error(), "roster init")
			x.NotContains(err.Error(), "no such table")
			x.NotContains(err.Error(), "CREATE TABLE")
			x.NotContains(out, "CREATE TABLE")
			x.Equal(1, cli.Status(err))

			s := dryBuilt(t, c)
			_, err = s.Ent.Tenant.Query().Count(t.Context())
			x.Error(err, "resources apply made the data plane's tables")
			_, err = s.Control.Ent.Tenant.Query().Count(t.Context())
			x.Error(err, "resources apply made the control plane's tables")
		})
	}
}
