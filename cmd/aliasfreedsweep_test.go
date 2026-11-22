package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// TestAnAliasFreedByTheSweepIsCalledWhatTheCommandCallsIt: a tenant's alias
// freed is one act on the trail, `roster.alias.free`, whether `roster forget`
// frees it or the sweep does once its hold is over.
//
// # What it was
//
// `roster forget --reuse` names its line `roster.alias.free` (#487), and the
// sweep's line for the same write was filed under the generated method that
// wrote it, `/roster.TenantService/Patch`, as any change of a tenant is. Who
// reads the trail for when a name came free found the command's and not the
// sweep's.
func TestAnAliasFreedByTheSweepIsCalledWhatTheCommandCallsIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	closes(t, ctx, b.Ungated, b.Contoso)
	_, err := forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)
	before, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	seen := map[string]bool{}
	for _, v := range before {
		seen[v.Id.String()] = true
	}

	gone := forget.Gone(b.Contoso)
	b.swept(t, ctx, 0, time.Nanosecond, func() bool {
		v, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		}.Build())
		return err == nil && v.GetAlias() == gone
	}, "the sweep did not free the alias")

	vs, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	var actions []string
	for _, v := range vs {
		if !seen[v.Id.String()] {
			actions = append(actions, v.Action)
		}
	}
	x.Contains(actions, forget.AliasFreed, "the sweep's line is called %v", actions)
	x.NotContains(actions, app.TenantService_Patch_FullMethodName)
	x.Equal("roster.alias.free", forget.AliasFreed, "the name `roster forget` gives the same act")
}
