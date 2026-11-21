package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
)

// dryCounted is how many rows each plane holds of what a run of a file, or the
// provisioner it writes as, would add: the trail, people, roles, bindings,
// tenants and the rows a file declares.
func dryCounted(t *testing.T, c cmd.Config) map[string]int {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	out := map[string]int{}
	for plane, db := range map[string]*ent.Client{"data": s.Ent, "control": s.Control.Ent} {
		for what, n := range map[string]func(context.Context) (int, error){
			"audit":   db.Audit.Query().Count,
			"holder":  db.Holder.Query().Count,
			"role":    db.Role.Query().Count,
			"binding": db.Binding.Query().Count,
			"tenant":  db.Tenant.Query().Count,
			"host":    db.Host.Query().Count,
		} {
			v, err := n(ctx)
			x.NoError(err)
			out[plane+" "+what] = v
		}
	}

	return out
}

// TestADryRunWritesNothing: `roster resources apply --dry-run` writes no row
// and alters no table, on either plane.
//
// # What it was
//
// `ApplyResources` made the provisioner before it looked at `dry`, and the
// command migrated the data plane where `db.migrate` was on and never looked
// at the control plane. So a dry run on a deployment `init`ed and never served
// with a file added the holder `provisioner` and its binding to `everything`,
// two lines on the control plane's trail, under *dry run; nothing was
// written*; and on one nothing had started it made the data plane's tables and
// then failed on the control plane's.
//
// A dry run looks the provisioner up and never makes it, and asks of both
// planes what `Ready` asks without migrating: what somebody runs before a
// deployment does it for them must not be the first thing that does it.
func TestADryRunWritesNothing(t *testing.T) {
	const file = "resources:\n  - kind: Tenant\n    alias: contoso\n    name: Contoso\n  - kind: Host\n    tenant: contoso\n    name: contoso.example\n"

	t.Run("a deployment never served with a file", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)
		out, err := initRun(t, c)
		x.NoError(err, out)
		c.Db.Migrate, c.Control.Db.Migrate = true, true

		was := dryCounted(t, c)
		out, err = entities(t, &c, "resources", "apply", "--dry-run", "--file", declare(t, file)[0])
		x.NoError(err)
		x.Contains(out, "contoso")
		x.Equal(was, dryCounted(t, c), "a dry run wrote")

		_, err = cmd.ControlHolder(t.Context(), dryBuilt(t, c).Control, cmd.Provisioner)
		x.Error(err, "a dry run made the provisioner")
	})

	t.Run("a deployment nothing has started", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)
		c.Db.Migrate, c.Control.Db.Migrate = true, true

		_, err := entities(t, &c, "resources", "apply", "--dry-run", "--file", declare(t, file)[0])
		x.Error(err, "a dry run on no tables")

		s := dryBuilt(t, c)
		_, err = s.Ent.Tenant.Query().Count(t.Context())
		x.Error(err, "a dry run made the data plane's tables")
		_, err = s.Control.Ent.Tenant.Query().Count(t.Context())
		x.Error(err, "a dry run made the control plane's tables")
	})
}

// dryBuilt is the deployment `c` describes, closed when the test ends.
func dryBuilt(t *testing.T, c cmd.Config) *cmd.Server {
	t.Helper()
	s, err := cmd.Build(t.Context(), c)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	return s
}
