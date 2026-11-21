package cmd_test

import (
	"bytes"
	"image"
	"image/png"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/portrait"
)

// strip is an image w pixels wide and a few high: the least there is to
// render of that width.
func strip(t *testing.T, w int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewGray(image.Rect(0, 0, w, 8))))

	return b.Bytes()
}

// TestAPictureIsNoLargerThanTheWireSays: somebody's picture is theirs to set
// with no role (`HolderService/Portray`, in `self-service`), and what roster
// does with one is decode it whole and scale it, in the serving process. The
// wire took an image of 4096 pixels a side, and one of those cost a process up
// to 269 MiB to render (`server/portrait`, where the measuring is). It takes
// 2048 a side, from whoever sends it: a person about their own row, and a
// front door filling a blank from a directory. One wider is refused with the
// number, and nothing is written.
func TestAPictureIsNoLargerThanTheWireSays(t *testing.T) {
	b, ctx := build(t)
	c := app.NewHolderServiceClient(served(t, b.Server))
	ref := func(who pdid.Id) *app.HolderRef { return app.HolderRef_builder{Id: who.Bytes()}.Build() }
	most := strconv.Itoa(portrait.MaxSide)

	t.Run("from somebody about their own row", func(t *testing.T) {
		x := require.New(t)
		dana := b.holder(t, ctx, b.Contoso, "dana")
		as := asOverTheWire(ctx, dana)
		read := func() *app.Holder {
			t.Helper()
			v, err := c.Get(as, app.HolderGetRequest_builder{Ref: ref(dana)}.Build())
			x.NoError(err)

			return v
		}

		_, err := c.Portray(as, app.HolderPortrayRequest_builder{
			Ref: ref(dana), DateUpdated: read().GetDateUpdated(), Image: strip(t, 4096),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "an image as wide as the old most was taken: %v", err)
		x.Contains(status.Convert(err).Message(), most, "the refusal does not say what the most is")
		_, sizes := b.pictured(t, ctx, dana)
		x.Empty(sizes, "an image refused was kept")

		_, err = c.Portray(as, app.HolderPortrayRequest_builder{
			Ref: ref(dana), DateUpdated: read().GetDateUpdated(), Image: strip(t, portrait.MaxSide+1),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		_, err = c.Portray(as, app.HolderPortrayRequest_builder{
			Ref: ref(dana), DateUpdated: read().GetDateUpdated(), Image: strip(t, portrait.MaxSide),
		}.Build())
		x.NoError(err, "the most a picture may be is refused")
		_, sizes = b.pictured(t, ctx, dana)
		x.Equal([]uint32{8}, sizes)
	})

	t.Run("and from a front door filling a blank", func(t *testing.T) {
		x := require.New(t)
		door := asOverTheWire(ctx, b.grantee(t, ctx, "login-app", fill))
		erin := b.holder(t, ctx, b.Contoso, "erin")

		_, err := c.Fill(door, app.HolderFillRequest_builder{Ref: ref(erin), Image: strip(t, portrait.MaxSide+1)}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), most)

		res, err := c.Fill(door, app.HolderFillRequest_builder{Ref: ref(erin), Image: strip(t, portrait.MaxSide)}.Build())
		x.NoError(err)
		x.False(res.GetPictureless())
	})
}
