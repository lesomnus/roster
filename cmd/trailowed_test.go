package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/pd"
)

// TestWhatTheTrailOwesIsSaid (payday#115): `roster trail owed` says what is
// owed of the trail's erasures and not paid -- how many, and since when -- and
// how many of them a legal hold keeps, by tenant, which is what waits for the
// hold to lift. With nothing owed it says that.
func TestWhatTheTrailOwesIsSaid(t *testing.T) {
	x := require.New(t)
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) { c = *cc })

	x.Contains(stdoutOf(t, cli.Cmd(&c), "trail", "owed"), "nothing of the trail's erasures is owed")

	// Two owed, and one of them kept by contoso's hold, as a payment under a
	// hold leaves it.
	store := pd.TrailStore(b.Ent)
	x.NoError(store.Owe(ctx, []pdid.Id{pdid.New(pd.HolderDomain), pdid.New(pd.HolderDomain)}))
	vs, err := store.Owed(ctx, trail.Cursor{}, 10)
	x.NoError(err)
	x.Len(vs, 2)
	x.NoError(store.Kept(ctx, vs[0].Key, []pdid.Id{b.Contoso}))

	out := stdoutOf(t, cli.Cmd(&c), "trail", "owed")
	x.Contains(out, "2 erasure(s) owed, the oldest since ")
	x.Contains(out, "1 kept by a legal hold of @contoso (", "what a hold keeps is not said by tenant")
}
