package cli

// The engines this app runs on, blank-imported here rather than in `cl`.
//
// Here rather than by payday so that an app does not carry one it never opens,
// and here rather than in `cl` so that the **sandbox** does not: `cl` is what
// `wasm/main.go` imports, and the linker follows imports. The wazero SQLite
// engine alone was megabytes of a module a browser downloads to run a database
// that is already in the page, on a driver of its own (`sqlite3-wasm`).
//
// Both engines, because both are used: `compose.yaml` runs roster on PostgreSQL
// and a test runs it on SQLite. Linking only the second made `docker compose
// up` -- the quickstart `docs/operating.md` gives -- fail at
// `unknown driver "pgx"`, which is a sentence about a name rather than about a
// missing import and reads as a typo in the configuration.
import (
	_ "github.com/lesomnus/payday/config/dbpgx"
	_ "github.com/lesomnus/payday/config/dbsqlite3"

	// And the broker that rides the first of them, so that
	// `watch.broker: postgres` is a name this binary has.
	//
	// The one thing that stopped roster running more than one replica was that
	// a client watching against one never heard about a write that landed on
	// another -- and the answer for a deployment already on PostgreSQL needs no
	// second piece of infrastructure. See `docs/operating.md`, "Running more
	// than one".
	//
	// Linked whatever this deployment runs on, like the drivers above: what it
	// costs is a `LISTEN` client in the binary, and what the other arrangement
	// costs is `watch.broker: postgres` reading as a typo.
	_ "github.com/lesomnus/payday/config/brokerpg"
)
