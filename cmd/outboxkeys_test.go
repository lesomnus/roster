package cmd_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// TestTheQueueKeepsWhichRowAndNotWhatWasWritten: a row of the outbox kept the
// patch document of the write it was queued for, and nothing that reads the
// queue reads it: the drain hands on which row was written. A row waits until
// a `serve` drains it, with no window, and forgetting somebody takes nothing
// out of the queue; so a display name written at the box with no `serve`
// draining was in a row of `outbox` until one did. The queue keeps the method,
// by what, and the key, and the drain still publishes the write.
func TestTheQueueKeepsWhichRowAndNotWhatWasWritten(t *testing.T) {
	const name = "Dana Queued-Surname"

	// heldInQueue is how many rows of the queue hold `name`.
	heldInQueue := func(t *testing.T, b *built, ctx context.Context) int {
		t.Helper()
		rows := b.Ent.Outbox.Query().AllX(ctx)
		require.NotEmpty(t, rows, "nothing was queued, so this proves nothing")
		n := 0
		for _, v := range rows {
			if bytes.Contains(v.Patch, []byte(name)) {
				n++
			}
		}

		return n
	}
	rename := func(t *testing.T, ctx context.Context, h app.HolderServiceServer, who []byte) {
		t.Helper()
		ref := app.HolderRef_builder{Id: who}.Build()
		was, err := h.Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
		require.NoError(t, err)
		_, err = h.Update(ctx, app.HolderUpdateRequest_builder{
			Ref: ref, DateUpdated: was.GetDateUpdated(),
			Profile: app.Profile_builder{DisplayName: name}.Build(),
		}.Build())
		require.NoError(t, err)
	}

	t.Run("at the box", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t, func(c *cmd.Config) { *c = c.Queued() })
		ctx, stop := context.WithCancel(ctx)
		defer stop()

		dana := b.holder(t, ctx, b.Contoso, "dana")
		rename(t, ctx, b.Ungated.Holder(), dana.Bytes())
		x.Zero(heldInQueue(t, b, ctx), "a name written of somebody waits in the queue")

		// And what is queued is still published: a suspension at the box,
		// drained, is heard by a follower.
		conn := served(t, b.Server)
		b.mayAnything(b.ContosoUser, b.Contoso)
		as := asOverTheWire(ctx, b.ContosoUser)
		c := syncing(t, as, conn, b.holder(t, ctx, b.Contoso, "canary"))
		go func() { _ = pd.Drain(b.Ent, b.Watch.Broker(), 20*time.Millisecond).Spin(ctx) }()

		_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
			Ref: app.HolderRef_builder{Id: dana.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.NotNil(about(t, c, dana).GetDateDisabled(), "the drain did not publish what the queue kept")
	})

	t.Run("at a port", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t, func(c *cmd.Config) { c.Watch.Outbox = true })

		conn := served(t, b.Server)
		b.mayAnything(b.ContosoUser, b.Contoso)
		as := asOverTheWire(ctx, b.ContosoUser)
		dana := b.holder(t, ctx, b.Contoso, "dana")

		h := app.NewHolderServiceClient(conn)
		ref := app.HolderRef_builder{Id: dana.Bytes()}.Build()
		was, err := h.Get(as, app.HolderGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		_, err = h.Update(as, app.HolderUpdateRequest_builder{
			Ref: ref, DateUpdated: was.GetDateUpdated(),
			Profile: app.Profile_builder{DisplayName: name}.Build(),
		}.Build())
		x.NoError(err)
		x.Zero(heldInQueue(t, b, ctx), "a name written of somebody over the wire waits in the queue")
	})
}
