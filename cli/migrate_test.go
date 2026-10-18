package cli

import (
	"testing"

	atlas "ariga.io/atlas/sql/schema"
	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAColumnComesToATableThatHasRows is an upgrade, which no other test here
// is: every one of them migrates a database that is empty.
//
// Three columns came between two releases of one deployment --
// `connection.subject_claim`, `connection.provisions`, `holder.directory` --
// each NOT NULL and with no default, and SQLite will not add such a column to
// a table that has rows; Postgres will not either. The deployment's init
// container would not have finished. Each field declares its default now, as
// `(orm.field).default`, and the generator gives it to the column, so a row
// already there holds it -- and holds it in SQL, where NULL would not have
// been the empty string.
//
// So the rows are made, the columns taken away again to put the database where
// that deployment's was, and the migration asked to bring them back.
func TestAColumnComesToATableThatHasRows(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	s := deployment(t)

	tn := tenantCalled(t, s, "contoso")
	_, err := s.Ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
		Tenant:   rstr.TenantRef_builder{Id: tn}.Build(),
		Name:     "entra",
		Issuer:   "https://login.contoso.example",
		ClientId: "contoso-app",
		Scopes:   []string{"email", "profile"},
	}.Build())
	x.NoError(err)
	_, err = s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: tn}.Build(),
		Alias:  "erin",
	}.Build())
	x.NoError(err)

	for _, q := range []string{
		`ALTER TABLE "connection" DROP COLUMN "subject_claim"`,
		`ALTER TABLE "connection" DROP COLUMN "provisions"`,
		`ALTER TABLE "holder" DROP COLUMN "directory"`,
	} {
		_, err := s.Db.ExecContext(ctx, q)
		x.NoError(err, q)
	}

	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx), "a column came to a table with rows and was refused")

	// In SQL, where NULL is not the zero value.
	var (
		claim      *string
		provisions *bool
		directory  *string
	)
	x.NoError(s.Db.QueryRowContext(ctx, `SELECT "subject_claim", "provisions" FROM "connection" WHERE "name" = 'entra'`).Scan(&claim, &provisions))
	x.NoError(s.Db.QueryRowContext(ctx, `SELECT "directory" FROM "holder" WHERE "alias" = 'erin'`).Scan(&directory))
	x.NotNil(claim, "a connection made before the claim was named holds NULL rather than `sub`")
	x.Empty(*claim)
	x.NotNil(provisions)
	x.False(*provisions)
	x.NotNil(directory)
	x.Empty(*directory)

	// And the next start has nothing to do: the default is the schema's, so the
	// column is the shape the schema says.
	var planned []atlas.Change
	record := func(next schema.Differ) schema.Differ {
		return schema.DiffFunc(func(current, desired *atlas.Schema) ([]atlas.Change, error) {
			cs, err := next.Diff(current, desired)
			planned = cs

			return cs, err
		})
	}
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx, schema.WithDiffHook(record)))
	x.Empty(planned, "a database already migrated was planned to change again")
}

// TestAnIndexThisBuildReplacedIsDropped: an index an earlier build made and
// this one replaced goes when the database is migrated, and what replaced it
// is there (lesomnus/rove#122). Nothing listed as replaced is an index this
// build makes, or every migration would drop what it had just made.
func TestAnIndexThisBuildReplacedIsDropped(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	declared := map[string]bool{}
	for _, tb := range entmigrate.Tables {
		for _, i := range tb.Indexes {
			declared[i.Name] = true
		}
	}
	for _, v := range Replaced {
		x.False(declared[v], "%s is replaced, and this build declares it", v)
	}

	s := deployment(t)
	has := func(name string) bool {
		q := "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?"
		if s.Dialect == "postgres" {
			q = "SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1"
		}
		n := 0
		x.NoError(s.Db.QueryRowContext(ctx, q, name).Scan(&n))
		return n > 0
	}

	// Two the build before this one made: a list's, which began with the
	// time, and one declared (holder, kind, name) that had the edge last.
	for _, q := range []string{
		`CREATE INDEX team_date_created_id ON team (date_created, id)`,
		`CREATE INDEX credential_kind_name_holder_id ON credential (kind, name, holder_id)`,
	} {
		_, err := s.Db.ExecContext(ctx, q)
		x.NoError(err, q)
	}
	x.True(has("team_date_created_id"))

	x.NoError(Migrate(ctx, s))
	x.False(has("team_date_created_id"))
	x.False(has("credential_kind_name_holder_id"))
	x.True(has("team_tenant_id_date_created_id"))
	x.True(has("credential_holder_id_kind_name"))
}
