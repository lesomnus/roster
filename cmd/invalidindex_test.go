package cmd_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/valid"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// TestAnIndexWhoseBuildDidNotFinishRefusesTheMigration (payday#176): the
// trail's index by time, built by an operator ahead of the migration that
// declares it, `CONCURRENTLY` so the trail takes writes meanwhile -- and a
// build that did not finish, which PostgreSQL leaves in the catalog, invalid.
// A migration finds an index by its name, so it took that one for made. A
// plane that migrates refuses it now, naming the plane, the index and what to
// run; one that only checks says it and serves; and what it says to run makes
// the index, after which the migration goes on.
func TestAnIndexWhoseBuildDidNotFinishRefusesTheMigration(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true}},
	}
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Ready(ctx, s, c))
	if s.Dialect != "postgres" {
		t.Skipf("only PostgreSQL keeps an index a build did not finish; set %s", pdtest.Postgres)
	}

	// The control plane's, which is the one an error has to name.
	db := s.Control.Db
	build := `CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_by_time ON audit (date_created, id)`
	execOn(t, db, `DROP INDEX audit_by_time`)
	cancelledBuild(t, db, "audit", build)
	execOn(t, db, build)
	x.False(isValidIndex(t, db, "audit_by_time"), "running it again made it")

	err = cli.Ready(ctx, s, c)
	x.ErrorIs(err, valid.ErrInvalid)
	x.ErrorContains(err, "control: ")
	x.ErrorContains(err, "audit_by_time, on audit")
	x.ErrorIs(cli.Migrate(ctx, s), valid.ErrInvalid)

	// A server that only checks says it, naming the plane, and serves.
	checks := c
	checks.Db.Migrate, checks.Control.Db.Migrate = false, false
	b := &bytes.Buffer{}
	x.NoError(cli.Ready(log.Into(ctx, slog.New(slog.NewJSONHandler(b, nil))), s, checks))
	said := []map[string]any{}
	for d := json.NewDecoder(b); d.More(); {
		v := map[string]any{}
		x.NoError(d.Decode(&v))
		said = append(said, v)
	}
	x.Len(said, 1)
	x.Equal("WARN", said[0]["level"])
	x.Equal("control", said[0]["plane"])
	x.Contains(said[0]["err"], "audit_by_time, on audit")

	// What it says to run, as it says to: a statement at a time, outside a
	// transaction.
	run := []string{}
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); strings.HasSuffix(line, ";") && !strings.HasPrefix(line, "--") {
			run = append(run, line)
		}
	}
	x.Len(run, 2)
	execOn(t, db, run...)
	x.True(isValidIndex(t, db, "audit_by_time"))
	x.NoError(cli.Ready(ctx, s, c))
	x.NoError(cli.Ready(ctx, s, checks))
}

// cancelledBuild runs `q`, a build `CONCURRENTLY` on `table`, as a timeout
// leaves one: a write is open on the table, the build waits for it to end
// before it reads a row, and the session's `statement_timeout` runs out first.
func cancelledBuild(t *testing.T, db *sql.DB, table string, q string) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	tx, err := db.BeginTx(ctx, nil)
	x.NoError(err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `LOCK TABLE `+table+` IN ROW EXCLUSIVE MODE`)
	x.NoError(err)

	conn, err := db.Conn(ctx)
	x.NoError(err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET statement_timeout = '200ms'`)
	x.NoError(err)
	_, err = conn.ExecContext(ctx, q)
	x.ErrorContains(err, "statement timeout")
	_, err = conn.ExecContext(ctx, `RESET statement_timeout`)
	x.NoError(err)
}

// isValidIndex says PostgreSQL holds an index called `name` and uses it.
func isValidIndex(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	ok := false
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT i.indisvalid FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = $1`, name).Scan(&ok))

	return ok
}

func execOn(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()

	for _, q := range qs {
		_, err := db.ExecContext(t.Context(), q)
		require.NoError(t, err, q)
	}
}
