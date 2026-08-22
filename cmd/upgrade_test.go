package cmd_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	"github.com/lesomnus/roster/internal/ent/predicate"
	app "github.com/lesomnus/roster/rstr"
)

// auditDomain is the column as a predicate, which is the one place a test has
// to widen a domain to what protobuf has: there is no uint8.
func auditDomain(d pdid.Domain) predicate.Audit { return entaudit.DomainEQ(uint32(d)) }

// TestADatabaseFromBeforeTheDomainColumnUpgrades.
//
// `Audit.domain` was added so that a retention policy could be per kind of
// thing, and adding a column to the one table that never stops growing is the
// change most likely to be met by a deployment that already has one.
//
// Two things have to hold and neither is obvious. The column has to arrive with
// a value for every row already there -- which is what its default is for, and
// what the hand-composed trail row in `cmd/admin.go` needed too. And a row
// written **before** it existed has to read as something a policy handles
// rather than as a kind it will treat wrongly: it reads as `pdid.Unknown`,
// which no entity may be registered as, so it falls to the deployment's default
// and never to a `by:` entry.
//
// Asserted by taking the column away and putting the database back through the
// migration, because that is what an upgrade is and nothing else here does it.
func TestADatabaseFromBeforeTheDomainColumnUpgrades(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Writes on the old shape: made first, then the column is removed from
	// under them, which is the state a deployment upgrades from.
	who := b.holder(t, ctx, b.Acme, "erin")

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(was)

	// The index goes first, because the column it is on cannot: an old database
	// has neither, and taking them away in the order the migration put them
	// there is the only way to get back to that shape.
	for _, q := range []string{
		"DROP INDEX audit_domain_date_created",
		"ALTER TABLE audit DROP COLUMN domain",
	} {
		var out sql.Result
		x.NoError(b.Drv.Exec(ctx, q, []any{}, &out),
			"%s: the old shape could not be arranged, so this proves nothing", q)
	}

	// The upgrade: what `roster serve` does with `db.migrate: true`, and what
	// `migrate.Check` refuses to start without.
	x.NoError(b.Ent.Schema.Create(ctx))

	t.Run("every row that was already there reads as no kind at all", func(t *testing.T) {
		x := require.New(t)

		vs, err := b.Ent.Audit.Query().All(ctx)
		x.NoError(err)
		x.Len(vs, was, "the upgrade lost rows")

		for _, v := range vs {
			x.Equal(pdid.Unknown, pdid.Domain(v.Domain),
				"a row written before the column has a kind it cannot have")
		}
	})

	t.Run("and a write after it carries one", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Provider: "github",
			Subject:  "gh-erin",
		}.Build())
		x.NoError(err)

		identity, ok := pdid.DomainOf("identity")
		x.True(ok)

		n, err := b.Ent.Audit.Query().Where(auditDomain(identity)).Count(ctx)
		x.NoError(err)
		x.NotZero(n, "the recorder stopped filling the column in after the upgrade")
	})
}
