package cmd_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// face is an image somebody might hand over: a square PNG of one colour.
func face(t *testing.T, side int, c color.Color) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := range side {
		for x := range side {
			m.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, m))

	return b.Bytes()
}

// pictured is somebody's picture as it is stored, read past every layer that
// would cut it down.
func (b *built) pictured(t *testing.T, ctx context.Context, who pdid.Id) (*app.Profile, []uint32) {
	t.Helper()
	v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	sizes := []uint32{}
	for _, r := range v.GetPortrait().GetRenditions() {
		sizes = append(sizes, r.GetSize())
	}

	return v.GetProfile(), sizes
}

// grantee is somebody in contoso holding exactly these methods.
func (b *built) grantee(t *testing.T, ctx context.Context, alias string, methods ...string) pdid.Id {
	t.Helper()
	who := b.holder(t, ctx, b.Contoso, alias)
	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	require.NoError(t, err)
	b.binds(t, who, mustId(t, r.GetId()), nil)

	return who
}

const fill = "/roster.HolderService/Fill"

// TestFillWritesOnlyTheBlanks is the grant a front door is given: what a
// directory says about somebody goes where their profile has nothing, and
// nowhere else, and the caller learns one bit about the row it wrote to.
func TestFillWritesOnlyTheBlanks(t *testing.T) {
	b, ctx := build(t)
	c := app.NewHolderServiceClient(served(t, b.Server))
	door := asOverTheWire(ctx, b.grantee(t, ctx, "login-app", fill))
	ref := func(who pdid.Id) *app.HolderRef { return app.HolderRef_builder{Id: who.Bytes()}.Build() }

	t.Run("a blank profile is filled, the picture both ways", func(t *testing.T) {
		x := require.New(t)
		dana := b.holder(t, ctx, b.Contoso, "dana")

		// The name first, and the answer that decides whether an image is
		// worth fetching.
		res, err := c.Fill(door, app.HolderFillRequest_builder{Ref: ref(dana), DisplayName: "Dana Scully"}.Build())
		x.NoError(err)
		x.True(res.GetPictureless())

		res, err = c.Fill(door, app.HolderFillRequest_builder{
			Ref:     ref(dana),
			Picture: "https://lh3.example/dana=s96-c",
			Image:   face(t, 200, color.NRGBA{200, 80, 80, 255}),
		}.Build())
		x.NoError(err)
		x.False(res.GetPictureless())

		p, sizes := b.pictured(t, ctx, dana)
		x.Equal("Dana Scully", p.GetDisplayName())
		x.Equal("https://lh3.example/dana=s96-c", p.GetPicture())
		x.Equal([]uint32{32, 64, 128}, sizes)
	})

	t.Run("and what is there stays", func(t *testing.T) {
		x := require.New(t)
		erin := b.holder(t, ctx, b.Contoso, "erin")
		_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
			Ref:              ref(erin),
			Profile:          app.Profile_builder{DisplayName: "Erin H.", Department: "robots"}.Build(),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)

		_, err = c.Fill(door, app.HolderFillRequest_builder{Ref: ref(erin), DisplayName: "Erin Hart"}.Build())
		x.NoError(err)
		_, err = c.Fill(door, app.HolderFillRequest_builder{Ref: ref(erin), Image: face(t, 64, color.Black)}.Build())
		x.NoError(err)

		p, sizes := b.pictured(t, ctx, erin)
		x.Equal("Erin H.", p.GetDisplayName(), "a name somebody chose was replaced")
		x.Equal("robots", p.GetDepartment(), "the rest of the profile was lost")
		x.Equal([]uint32{32, 64}, sizes)

		// A second picture for somebody who has one is not kept.
		res, err := c.Fill(door, app.HolderFillRequest_builder{Ref: ref(erin), Image: face(t, 200, color.White)}.Build())
		x.NoError(err)
		x.False(res.GetPictureless())
		_, sizes = b.pictured(t, ctx, erin)
		x.Equal([]uint32{32, 64}, sizes, "a picture was replaced by a sign-in")
	})

	// A URL somebody typed is their picture, though roster has no copy of it.
	// A portrait from their directory would be a second one.
	t.Run("a picture somebody chose counts", func(t *testing.T) {
		x := require.New(t)
		finn := b.holder(t, ctx, b.Contoso, "finn")
		_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
			Ref:              ref(finn),
			Profile:          app.Profile_builder{Picture: "https://example.com/finn.jpg"}.Build(),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)

		res, err := c.Fill(door, app.HolderFillRequest_builder{Ref: ref(finn)}.Build())
		x.NoError(err)
		x.False(res.GetPictureless())

		_, err = c.Fill(door, app.HolderFillRequest_builder{Ref: ref(finn), Image: face(t, 200, color.White)}.Build())
		x.NoError(err)
		p, sizes := b.pictured(t, ctx, finn)
		x.Equal("https://example.com/finn.jpg", p.GetPicture())
		x.Empty(sizes)
	})

	// Refused whole: a name filled and a picture refused would be a call that
	// did something and answered that it had not.
	t.Run("what is not a picture is refused, and nothing is written", func(t *testing.T) {
		x := require.New(t)
		gus := b.holder(t, ctx, b.Contoso, "gus")

		for _, req := range []*app.HolderFillRequest{
			app.HolderFillRequest_builder{Ref: ref(gus), DisplayName: "Gus", Image: []byte("<svg/>")}.Build(),
			app.HolderFillRequest_builder{Ref: ref(gus), DisplayName: "Gus", Picture: "javascript:alert(1)"}.Build(),
			app.HolderFillRequest_builder{Ref: ref(gus), DisplayName: "Gus", Picture: "http://example.com/gus.jpg"}.Build(),
		} {
			_, err := c.Fill(door, req)
			x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		}

		p, sizes := b.pictured(t, ctx, gus)
		x.Empty(p.GetDisplayName())
		x.Empty(sizes)
	})

	// And what makes it the narrow grant: holding it is not holding `Update`
	// or `Get`.
	t.Run("it reads and writes nothing else", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Update(door, app.HolderUpdateRequest_builder{
			Ref: ref(b.ContosoUser), Profile: app.Profile_builder{DisplayName: "Mallory"}.Build(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
		_, err = c.Get(door, app.HolderGetRequest_builder{Ref: ref(b.ContosoUser)}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("a declared row is the file's", func(t *testing.T) {
		x := require.New(t)
		v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "app",
			Labels: map[string]string{cmd.Declared: "config: login"},
		}.Build())
		x.NoError(err)

		_, err = c.Fill(door, app.HolderFillRequest_builder{Ref: ref(mustId(t, v.GetId())), DisplayName: "App"}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})
}

// TestPortrayKeepsAnImageOrTakesItAway, and the profile's URL goes either way:
// the picture is one thing, and once it is this image a URL saying where some
// other picture is describes somebody else's.
func TestPortrayKeepsAnImageOrTakesItAway(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	c := app.NewHolderServiceClient(served(t, b.Server))
	as := asOverTheWire(ctx, b.grantee(t, ctx, "editor", "/roster.HolderService/Portray", getHolder))
	who := b.holder(t, ctx, b.Contoso, "hana")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              ref,
		Profile:          app.Profile_builder{DisplayName: "Hana", Picture: "https://example.com/hana.jpg"}.Build(),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	read := func() *app.Holder {
		v, err := c.Get(as, app.HolderGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		return v
	}

	v := read()
	v, err = c.Portray(as, app.HolderPortrayRequest_builder{
		Ref: ref, DateUpdated: v.GetDateUpdated(), Image: face(t, 300, color.NRGBA{20, 120, 60, 255}),
	}.Build())
	x.NoError(err)
	p, sizes := b.pictured(t, ctx, who)
	x.Equal([]uint32{32, 64, 128}, sizes)
	x.Empty(p.GetPicture(), "the URL of another picture was left beside this one")
	x.Equal("Hana", p.GetDisplayName(), "the rest of the profile was lost")

	t.Run("under the version it read", func(t *testing.T) {
		x := require.New(t)
		_, err := c.Portray(as, app.HolderPortrayRequest_builder{Ref: ref, Image: face(t, 64, color.White)}.Build())
		x.Error(err, "written with no version")

		stale := v.GetDateUpdated()
		_, err = c.Portray(as, app.HolderPortrayRequest_builder{Ref: ref, DateUpdated: read().GetDateUpdated()}.Build())
		x.NoError(err)
		_, err = c.Portray(as, app.HolderPortrayRequest_builder{
			Ref: ref, DateUpdated: stale, Image: face(t, 64, color.White),
		}.Build())
		x.Error(err, "written against a row that had moved")
	})

	t.Run("and nothing is taken away", func(t *testing.T) {
		x := require.New(t)
		_, sizes := b.pictured(t, ctx, who)
		x.Empty(sizes)

		_, err := c.Portray(as, app.HolderPortrayRequest_builder{
			Ref: ref, DateUpdated: read().GetDateUpdated(), Image: []byte("GIF89a, or so it says"),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})
}

// TestAProfileNamingAnotherPictureTakesThePortraitAway: a screen that prefers
// roster's copy would otherwise go on drawing the picture somebody had just
// replaced.
func TestAProfileNamingAnotherPictureTakesThePortraitAway(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	c := app.NewHolderServiceClient(served(t, b.Server))
	as := asOverTheWire(ctx, b.grantee(t, ctx, "editor",
		"/roster.HolderService/Portray", "/roster.HolderService/Update", getHolder))
	who := b.holder(t, ctx, b.Contoso, "ines")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	read := func() *app.Holder {
		v, err := c.Get(as, app.HolderGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		return v
	}
	update := func(p *app.Profile) error {
		_, err := c.Update(as, app.HolderUpdateRequest_builder{Ref: ref, Profile: p, DateUpdated: read().GetDateUpdated()}.Build())

		return err
	}

	_, err := c.Portray(as, app.HolderPortrayRequest_builder{
		Ref: ref, DateUpdated: read().GetDateUpdated(), Image: face(t, 200, color.White),
	}.Build())
	x.NoError(err)

	x.NoError(update(app.Profile_builder{DisplayName: "Ines"}.Build()))
	_, sizes := b.pictured(t, ctx, who)
	x.Len(sizes, 3, "a profile write that named no other picture took the portrait")

	x.Equal(codes.InvalidArgument, status.Code(update(app.Profile_builder{Picture: "javascript:alert(1)"}.Build())))
	_, sizes = b.pictured(t, ctx, who)
	x.Len(sizes, 3)

	x.NoError(update(app.Profile_builder{Picture: "https://example.com/ines.jpg"}.Build()))
	p, sizes := b.pictured(t, ctx, who)
	x.Equal("https://example.com/ines.jpg", p.GetPicture())
	x.Empty(sizes)

	// A value from before the URL was checked is left to whoever next changes
	// it, so a form that sends it back unchanged still saves.
	_, err = b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              ref,
		Profile:          app.Profile_builder{Picture: "a picture"}.Build(),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	x.NoError(update(app.Profile_builder{DisplayName: "Ines", Picture: "a picture"}.Build()))
}

// TestAPortraitIsNotHandedOver: what is on a row is what roster rendered.
func TestAPortraitIsNotHandedOver(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	c := app.NewHolderServiceClient(served(t, b.Server))
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	_, err := c.Add(as, app.HolderAddRequest_builder{
		Tenant: at, Alias: "jo",
		Portrait: app.Portrait_builder{Renditions: []*app.Rendition{
			app.Rendition_builder{Size: 32, Uri: "data:image/jpeg;base64,AAAA"}.Build(),
		}}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))

	_, err = c.Add(as, app.HolderAddRequest_builder{
		Tenant: at, Alias: "jo", Profile: app.Profile_builder{Picture: "javascript:alert(1)"}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestAPageCarriesOneSizeOfEach: a hundred people at every size is a megabyte a
// page drawing them small has no use for, so a page carries one, and `Get`
// carries all of them.
func TestAPageCarriesOneSizeOfEach(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := served(t, b.Server)
	c := app.NewHolderServiceClient(conn)
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)

	people := []pdid.Id{}
	for _, alias := range []string{"pat-kim", "pat-lee"} {
		who := b.holder(t, ctx, b.Contoso, alias)
		_, err := c.Fill(as, app.HolderFillRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Image: face(t, 256, color.White),
		}.Build())
		x.NoError(err)
		people = append(people, who)
	}
	paged := func(t *testing.T, vs []*app.Holder) {
		t.Helper()
		n := 0
		for _, v := range vs {
			if !v.HasPortrait() {
				continue
			}
			n++
			require.Len(t, v.GetPortrait().GetRenditions(), 1)
			require.Equal(t, uint32(64), v.GetPortrait().GetRenditions()[0].GetSize())
		}
		require.Equal(t, len(people), n)
	}

	t.Run("List", func(t *testing.T) {
		res, err := c.List(as, app.HolderListRequest_builder{}.Build())
		require.NoError(t, err)
		paged(t, res.GetItems())
	})
	t.Run("Search", func(t *testing.T) {
		res, err := c.Search(as, app.HolderSearchRequest_builder{Q: z.Ptr("pat-")}.Build())
		require.NoError(t, err)
		paged(t, res.GetItems())
	})
	t.Run("Watch", func(t *testing.T) {
		fs := []*app.HolderFilter{}
		for _, who := range people {
			fs = append(fs, app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build())
		}
		w, err := c.Watch(as, app.HolderWatchRequest_builder{Filters: fs}.Build())
		require.NoError(t, err)
		first, err := w.Recv()
		require.NoError(t, err)
		vs := []*app.Holder{}
		for _, i := range first.GetItems() {
			vs = append(vs, i.GetValue())
		}
		paged(t, vs)
	})
	t.Run("Get", func(t *testing.T) {
		v, err := c.Get(as, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: people[0].Bytes()}.Build()}.Build())
		require.NoError(t, err)
		require.Len(t, v.GetPortrait().GetRenditions(), 3)
	})
}
