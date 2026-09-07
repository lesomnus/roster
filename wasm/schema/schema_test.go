package schema_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/wasm/schema"
)

const at = "schema.sql"

// head is what a reader opening the generated file needs to be told first.
const head = `-- Written by TestTheScriptIsThisSchema. Do not edit; regenerate.
--
-- One transaction, so the page pays for one commit rather than one per table.
BEGIN;

`

const tail = "\nCOMMIT;\n"

// TestTheScriptIsThisSchema is the one thing about a database written down in
// another file that can go wrong quietly.
//
// It creates the schema the way a process creates it and compares what SQLite
// then says it has against what this file holds. It is the reason the script
// can be trusted without a browser being opened.
//
//	PDTEST_UPDATE=1 go test ./wasm/schema/
//
// writes it again.
func TestTheScriptIsThisSchema(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	want := head + made(t, x, ctx) + tail

	if update() {
		x.NoError(os.WriteFile(at, []byte(want), 0o644))
		t.Logf("%s written", at)

		return
	}

	x.Equal(want, schema.Script,
		"%s is not the schema ent creates.\n\n    %s=1 go test ./wasm/schema/\n\nwrites it", at, pdtest.Update)
}

// TestTheScriptLoads says the file executes and leaves the tables the page will
// read.
//
// Loading it rather than trusting the text is the whole assertion: the SQL is
// generated, so nobody reads it, and a script that does not execute is a
// sandbox that shows an error where an app should be.
func TestTheScriptLoads(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "loaded.db"))
	x.NoError(err)
	t.Cleanup(func() { db.Close() })

	x.NoError(schema.Load(ctx, db))

	got, err := shape(ctx, db)
	x.NoError(err)
	x.Contains(got, "CREATE TABLE `holder`", "the script loaded but there is no holder table")
	x.Contains(got, "CREATE TABLE `tenant`")
}

// made is the schema a process arrives at, as SQLite describes it afterwards.
func made(t *testing.T, x *require.Assertions, ctx context.Context) string {
	t.Helper()

	s, err := cmd.Build(ctx, cmd.Config{
		// One connection, so that what is dumped is what one connection wrote.
		Db:    config.DbConfig{Driver: "sqlite3", Dsn: "file:" + filepath.Join(t.TempDir(), "made.db"), MaxOpenConns: 1},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))

	got, err := shape(ctx, s.Db)
	x.NoError(err)

	return got
}

// shape is every statement SQLite would need to make these tables again.
//
// Read out of `sqlite_master` rather than asked of ent, because what has to be
// reproduced is the database and not the intention -- an index ent creates as a
// side effect of an edge is in one and not the other.
//
// Tables before indexes and then by name, so that a diff of the file is a diff
// of the schema rather than of the order SQLite happened to answer in.
func shape(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
		ORDER BY CASE type WHEN 'table' THEN 0 ELSE 1 END, name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}

		b.WriteString(s)
		b.WriteString(";\n")
	}

	return b.String(), rows.Err()
}

func update() bool {
	v := os.Getenv(pdtest.Update)

	return v != "" && v != "0" && v != "false"
}
