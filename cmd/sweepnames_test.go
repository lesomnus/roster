package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestTheSweepOfAttemptsNamesTheRowItCollected: a sign-in's attempt the sweep
// collected is on the trail as `roster.continuation.expire`, and a link as
// `roster.link.expire`, each named for its row as thirteen of the trail's own
// names are.
//
// Both were `roster.vouch.expire`, named for the package that writes the line:
// two kinds of row under one name, which a reader of a trail told apart by
// reading `server/vouch`.
func TestTheSweepOfAttemptsNamesTheRowItCollected(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build()
	past := timestamppb.New(time.Now().Add(-time.Minute))

	attempt, err := b.Ungated.Continuation().Add(ctx, app.ContinuationAddRequest_builder{
		Holder: who, IssuedTo: who.GetId(), Secret: sweepable(t), DateExpires: past,
	}.Build())
	x.NoError(err)
	link, err := b.Ungated.Link().Add(ctx, app.LinkAddRequest_builder{
		Holder: who, IssuedTo: who.GetId(), Secret: sweepable(t), DateExpires: past,
	}.Build())
	x.NoError(err)

	cs, err := cmd.Config{}.Audit.Contents()
	x.NoError(err)
	n, err := vouch.Collect(ctx, b.Ent, cs)
	x.NoError(err)
	x.Equal(2, n, "the sweep collected the two")

	for _, tc := range []struct {
		row  string
		id   []byte
		name string
	}{
		{"an attempt", attempt.GetId(), "roster.continuation.expire"},
		{"a link", link.GetId(), "roster.link.expire"},
	} {
		vs := linesAbout(t, ctx, b.Ent, mustId(t, tc.id))
		x.NotEmpty(vs, "the trail says nothing of %s that went", tc.row)
		x.Equal(tc.name, vs[len(vs)-1].Action, "the line that says %s went", tc.row)
	}
}
