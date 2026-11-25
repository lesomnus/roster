package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/apikey"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/role"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/keys"
)

// appsRowsKinds are the four kinds of row an app has, each by its table and
// the column that names the app's holder, and how one of each is made for an
// app.
var appsRowsKinds = []struct {
	name, table, column string
	make                func(t *testing.T, ctx context.Context, b *built, of pdid.Id) pdid.Id
}{
	{"a role", role.Table, role.AppColumn, func(t *testing.T, ctx context.Context, b *built, of pdid.Id) pdid.Id {
		return b.roleAtApp(t, ctx, of, "rows-role-"+of.String()[:8], "/loft.*/*")
	}},
	{"a group", group.Table, group.AppColumn, func(t *testing.T, ctx context.Context, b *built, of pdid.Id) pdid.Id {
		v, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "rows-group-" + of.String()[:8],
			App:    app.HolderRef_builder{Id: of.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		return mustId(t, v.GetId())
	}},
	{"a binding", binding.Table, binding.AppColumn, func(t *testing.T, ctx context.Context, b *built, of pdid.Id) pdid.Id {
		v, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: b.role(t, ctx, "rows-for-an-app", app.RoleService_Get_FullMethodName).Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			App:    app.HolderRef_builder{Id: of.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		return mustId(t, v.GetId())
	}},
	{"a key", apikey.Table, apikey.AppColumn, func(t *testing.T, ctx context.Context, b *built, of pdid.Id) pdid.Id {
		_, sum, err := keys.Mint(keys.PrefixTenant)
		require.NoError(t, err)
		v, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Alias:   "rows-key-" + of.String()[:8],
			Secret:  sum,
			Methods: []string{"/loft.Thing/Get"},
			App:     app.HolderRef_builder{Id: of.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		return mustId(t, v.GetId())
	}},
}

// appsRowsOnDelete is what the database does to `table`'s rows when the row
// `column` names is deleted, as it says it: `RESTRICT`, `SET NULL`, `NO
// ACTION`.
func appsRowsOnDelete(t *testing.T, ctx context.Context, s *cmd.Server, table, column string) string {
	t.Helper()
	x := require.New(t)

	var (
		q    string
		args []any
	)
	switch s.Drv.Dialect() {
	case dialect.SQLite:
		q, args = `SELECT "from", on_delete FROM pragma_foreign_key_list(?)`, []any{table}
	case dialect.Postgres:
		q = `SELECT k.column_name, rc.delete_rule
FROM information_schema.referential_constraints rc
JOIN information_schema.key_column_usage k
  ON k.constraint_name = rc.constraint_name AND k.constraint_schema = rc.constraint_schema
WHERE k.table_schema = current_schema() AND k.table_name = $1`
		args = []any{table}
	default:
		t.Fatalf("no way to read a foreign key's action in %s", s.Drv.Dialect())
	}

	var rows entsql.Rows
	x.NoError(s.Drv.Query(ctx, q, args, &rows))
	defer rows.Close()

	said := ""
	for rows.Next() {
		var from, action string
		x.NoError(rows.Scan(&from, &action))
		if from == column {
			said = strings.ToUpper(action)
		}
	}
	x.NoError(rows.Err())
	x.NotEmpty(said, "%s.%s is no foreign key", table, column)

	return said
}

// TestAnAppsRowsKeepTheHolderItIsFrom: the holder an app is cannot be deleted
// while a role, a group, a binding or a key still names it as its app.
//
// Each of the four names its app by a nullable edge, since a row of roster's
// own names none, and a nullable edge was `ON DELETE SET NULL`: the holder's
// row deleted, an app's role would have become a role of roster's naming the
// app's methods, an app's group one of roster's, an app's binding one held
// for roster, and a key for the app one of roster's (#567). Nothing deletes a
// holder's row (`TestNoRoadThatDestroysAHolderLeavesARoleOfRosters`), so it
// was a hole nobody stood in. Right is that the database refuses it, `RESTRICT`
// on each, so that what an app's row means does not depend on every road
// keeping to that.
func TestAnAppsRowsKeepTheHolderItIsFrom(t *testing.T) {
	b, ctx := build(t)

	for _, k := range appsRowsKinds {
		t.Run(k.name, func(t *testing.T) {
			x := require.New(t)
			assert.Equal(t, "RESTRICT", appsRowsOnDelete(t, ctx, b.Server, k.table, k.column), "%s.%s", k.table, k.column)

			of := b.holder(t, ctx, b.Contoso, "rows-app-"+strings.ReplaceAll(strings.TrimPrefix(k.name, "a "), " ", "-"))
			row := k.make(t, ctx, b, of)

			// Underneath every server, as only the database's own words
			// would: the row is refused, and nothing moved.
			err := b.Ent.Holder.DeleteOneId(of.Uuid()).Exec(bare.TrustAdmission(ctx))
			x.Error(err, "the holder %s names as its app was deleted", k.name)

			_, err = b.Ent.Holder.Get(ctx, of.Uuid())
			x.NoError(err, "the holder's row is gone")
			x.Equal(1, appsRowsNaming(t, ctx, b.Server, k.table, k.column, row, of), "%s does not name the app as it did", k.name)
		})
	}

	t.Run("a holder nothing names as its app", func(t *testing.T) {
		x := require.New(t)

		alone := b.holder(t, ctx, b.Contoso, "rows-alone")
		x.NoError(b.Ent.Holder.DeleteOneId(alone.Uuid()).Exec(bare.TrustAdmission(ctx)))
	})
}

// TestAForgetFinishesForAPersonAndForAnAppsHolder: what destroys somebody
// (`server/forget`) blanks their row and deletes none, so the four rows that
// name an app's holder do not stand in its way, nor in a person's.
func TestAForgetFinishesForAPersonAndForAnAppsHolder(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	loft := b.holder(t, ctx, b.Contoso, "rows-loft")
	for _, k := range appsRowsKinds {
		k.make(t, ctx, b, loft)
	}
	erin := b.holder(t, ctx, b.Contoso, "rows-erin")
	b.binds(t, erin, b.roleAtApp(t, ctx, loft, "rows-loft-staff", "/loft.*/*"), nil)

	for _, who := range []pdid.Id{erin, loft} {
		_, err := b.Walled.Holder().Erase(b.asAtApp(ctx, b.holder(t, ctx, b.Contoso, "admin")), app.HolderRef_builder{Id: who.Bytes()}.Build())
		x.NoError(err)
		_, err = forget.Forget(ctx, b.Ent, who, trail.Policy{}, audit.Contents{}, nil)
		x.NoError(err, "a forget did not finish")

		_, err = b.Ent.Holder.Get(ctx, who.Uuid())
		x.NoError(err, "a forget deleted a holder's row")
	}
}

// appsRowsNaming is how many rows of `table` are `row` and name `of` in
// `column`: one, or none.
func appsRowsNaming(t *testing.T, ctx context.Context, s *cmd.Server, table, column string, row, of pdid.Id) int {
	t.Helper()
	x := require.New(t)

	q := `SELECT count(*) FROM "` + table + `" WHERE id = ? AND ` + column + ` = ?`
	if s.Drv.Dialect() == dialect.Postgres {
		q = strings.NewReplacer("id = ?", "id = $1", column+" = ?", column+" = $2").Replace(q)
	}

	var rows entsql.Rows
	x.NoError(s.Drv.Query(ctx, q, []any{row.Uuid(), of.Uuid()}, &rows))
	defer rows.Close()

	n := 0
	x.True(rows.Next())
	x.NoError(rows.Scan(&n))

	return n
}
