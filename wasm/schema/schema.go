// Package schema is the sandbox's database, already made.
//
// One SQL script: every table this app's entities need, as ent creates them.
// The page executes it and has a database to seed.
//
// # Why a script and not `Schema.Create`
//
// Because `Schema.Create` is ent's migration engine, and ent's migration engine
// is Atlas -- its diff planner, all three of its SQL dialects, and the HCL
// parser those import for a schema language nothing here writes. Measured on
// this app that is 10.5 MB of the module a browser downloads, spent deciding
// what to do to a database that does not exist yet.
//
// There is nothing here for a migration to decide. A reload is a new database,
// so what the page wants is not "bring this in line" but "be this".
//
// Both planes take the same script, because they are the same entities in two
// databases; that is the whole of what roster's control plane is.
//
// # What keeps it true
//
// `TestTheScriptIsThisSchema`, which creates the schema the way a process
// creates it -- ent, Atlas and all, which is fine in a test -- and compares. A
// column added to an entity and not to this file would otherwise be a sandbox
// that says `no such column` on the first read, in a browser, with the command
// that would have fixed it three away:
//
//	PDTEST_UPDATE=1 go test ./wasm/schema/
package schema

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

// Script is the schema, as SQL.
//
// Exported because what a sandbox is made of is worth being able to print.
//
//go:embed schema.sql
var Script string

// Load puts the schema into db.
//
// One `ExecContext` and not a statement at a time: the driver's EXEC loops the
// multi-statement tail itself, so this is a single round trip to the Worker the
// engine runs in. Statement by statement it would be one per table.
func Load(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, Script); err != nil {
		return fmt.Errorf("execute the schema script: %w", err)
	}

	return nil
}
