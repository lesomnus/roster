package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	app "github.com/lesomnus/roster/rstr"
)

// TestUpdateIsTheNarrowWrite is the method a caller is given, and the reason it
// exists rather than `Patch` being opened.
//
// `Patch` writes anything the schema holds, which is why payday closes it at
// the transport: *what a caller may change, and under what conditions, is not
// something a general write can be told*. This can be told, and what it says is
// two fields — both of them things a holder carries about itself, and neither
// of them anything the wall, the trail or a permission reads.
func TestUpdateIsTheNarrowWrite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const update = "/roster.HolderService/Update"

	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:  "editor",
		// `Patch` among them, which is what makes the last case worth running:
		// the general write is closed at the **transport**, so holding it
		// changes nothing.
		Methods: []string{update, getHolder, "/roster.HolderService/Patch"},
	}.Build())
	x.NoError(err)
	b.binds(t, b.AcmeUser, mustId(t, r.GetId()), nil)

	conn := served(t, b.Server)
	c := app.NewHolderServiceClient(conn)
	as := asOverTheWire(ctx, b.AcmeUser)

	me := app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build()

	was, err := c.Get(as, app.HolderGetRequest_builder{Ref: me}.Build())
	x.NoError(err)

	serial, err := anypb.New(app.TenantRef_builder{Alias: strPtr("arm-01")}.Build())
	x.NoError(err)

	t.Run("it writes the two fields", func(t *testing.T) {
		x := require.New(t)

		v, err := c.Update(as, app.HolderUpdateRequest_builder{
			Ref:         me,
			Profile:     app.Profile_builder{DisplayName: "Ada Lovelace"}.Build(),
			Data:        serial,
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		x.Equal("Ada Lovelace", v.GetProfile().GetDisplayName())

		// Read back rather than trusting the answer: an Update echoes what it
		// was given, so a value that was never stored still looks right.
		got, err := c.Get(as, app.HolderGetRequest_builder{Ref: me}.Build())
		x.NoError(err)
		x.Equal("Ada Lovelace", got.GetProfile().GetDisplayName())
		x.Equal(serial.GetTypeUrl(), got.GetData().GetTypeUrl())
	})

	// Sending neither changes neither, so a caller updating a profile does not
	// have to know what is in `data` and cannot erase it by not knowing.
	t.Run("and leaves what it was not given", func(t *testing.T) {
		x := require.New(t)

		got, err := c.Get(as, app.HolderGetRequest_builder{Ref: me}.Build())
		x.NoError(err)

		_, err = c.Update(as, app.HolderUpdateRequest_builder{
			Ref:         me,
			Profile:     app.Profile_builder{DisplayName: "Ada Byron"}.Build(),
			DateUpdated: got.GetDateUpdated(),
		}.Build())
		x.NoError(err)

		now, err := c.Get(as, app.HolderGetRequest_builder{Ref: me}.Build())
		x.NoError(err)
		x.Equal("Ada Byron", now.GetProfile().GetDisplayName())
		x.Equal(serial.GetTypeUrl(), now.GetData().GetTypeUrl(), "the data was erased by a profile write")
	})

	// The version is carried through, or a write against a row that has moved
	// is applied to whatever it became.
	t.Run("and refuses a row that has moved", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Update(as, app.HolderUpdateRequest_builder{
			Ref:         me,
			Profile:     app.Profile_builder{DisplayName: "Somebody Else"}.Build(),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.Error(err, "a stale version was written anyway")
	})

	// And what makes it narrow. This caller **holds** `Patch` -- it is in their
	// role above -- and is still refused, because the general write is closed at
	// the transport rather than by a permission. So `Update` is not a smaller
	// door into the same room; it is the only door.
	t.Run("Patch is still not a way around it", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Patch(as, app.HolderPatchRequest_builder{
			Ref:   me,
			Alias: strPtr("somebody-else"),
		}.Build())
		x.Error(err)
		x.Equal(codes.Unimplemented, status.Code(err),
			"a caller who holds Patch reached it")
	})

	// It is a method like any other, so a caller without it is refused by the
	// gate rather than by anything this file wrote.
	t.Run("and somebody who does not hold it may not call it", func(t *testing.T) {
		x := require.New(t)

		other := b.holder(t, ctx, b.Acme, "nobody")

		_, err := c.Update(asOverTheWire(ctx, other), app.HolderUpdateRequest_builder{
			Ref:     me,
			Profile: app.Profile_builder{DisplayName: "Nope"}.Build(),
		}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}
