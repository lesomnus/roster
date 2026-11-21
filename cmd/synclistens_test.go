package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestAStreamSaysWhenItListens: the sync stream answers its response header
// once roster has subscribed, and before anything was written, so a follower
// knows from when a write is heard by it. Nothing said so: payday subscribes
// inside the handler and the header went with the first event, so `Watch`
// returned to a client before roster listened, and a follower that forgot what
// it knew then kept an answer asked for in between over a write made in
// between. A write made once the header has come is heard, every time.
func TestAStreamSaysWhenItListens(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)

	who := b.holder(t, ctx, b.Contoso, "listened")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	h := app.NewHolderServiceClient(conn)

	for i := range 100 {
		// A bound on a header that does not come, which is the failure; the
		// test does not wait it out when it does come.
		sctx, stop := context.WithTimeout(as, 10*time.Second)
		out, err := app.NewSyncServiceClient(conn).Watch(sctx, app.SyncWatchRequest_builder{}.Build())
		x.NoError(err)

		md, err := out.Header()
		x.NoError(err)
		x.NotNil(md, "round %d: the stream ended, or its header never came, with nothing written", i)

		res, err := h.Invalidate(as, app.HolderInvalidateRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		v, err := out.Recv()
		x.NoError(err, "round %d: a write made once the stream listened", i)
		x.Equal(who.Bytes(), v.GetHolder(), "round %d", i)
		x.Equal(res.GetDateInvalidated().AsTime(), v.GetDateInvalidated().AsTime(), "round %d: the write made after the header", i)

		stop()
	}
}
