package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestReachesIsAskedAtAnApp.
//
// What somebody may call is held at an app (`Role.app`), and `Reaches`
// answered the union of every role whatever app asked: an app with
// `reach.Methods` alone was told what somebody held at roster and at every
// other app, which no list of its own would show it. It is asked at an app:
// the caller itself unless the request says `app` or `roster`, and about
// another app or about roster only a caller who holds
// `/roster.BindingService/List` across the tenant is answered -- otherwise
// `PermissionDenied`, saying which was asked.
func TestReachesIsAskedAtAnApp(t *testing.T) {
	b, ctx := build(t)

	const atLoft = "/loft.BookingService/Cancel"

	loft := b.holder(t, ctx, b.Contoso, "loft")
	levyan := b.holder(t, ctx, b.Contoso, "levyan")
	asks := b.role(t, ctx, "asks", "/roster.HolderService/Reaches")
	b.binds(t, loft, asks, nil)
	b.binds(t, levyan, asks, nil)

	erin := b.holder(t, ctx, b.Contoso, "erin")
	b.binds(t, erin, b.role(t, ctx, "reader", getHolder), nil)
	b.binds(t, erin, b.roleAtApp(t, ctx, loft, "loft-staff", atLoft), nil)

	admin := b.holder(t, ctx, b.Contoso, "admin")

	holders := app.NewHolderServiceClient(served(t, b.Server))
	her := app.HolderRef_builder{Id: erin.Bytes()}.Build()
	ask := func(as pdid.Id, req *app.HolderReachesRequest) (*app.HolderReachesResponse, error) {
		return holders.Reaches(asOverTheWire(ctx, as), req)
	}
	of := func(who pdid.Id) *app.HolderRef { return app.HolderRef_builder{Id: who.Bytes()}.Build() }

	t.Run("with neither, an app is answered about itself", func(t *testing.T) {
		x := require.New(t)

		v, err := ask(loft, app.HolderReachesRequest_builder{Ref: her}.Build())
		x.NoError(err)
		x.Equal([]string{atLoft}, v.GetMethods())
		x.Equal([]string{atLoft}, v.GetEverywhere())
		x.True(v.GetEverySite())

		v, err = ask(loft, app.HolderReachesRequest_builder{Ref: her, App: of(loft)}.Build())
		x.NoError(err, "an app naming itself is asking about itself")
		x.Equal([]string{atLoft}, v.GetMethods())

		v, err = ask(levyan, app.HolderReachesRequest_builder{Ref: her}.Build())
		x.NoError(err)
		x.Empty(v.GetMethods(), "another app was answered what is held at the first")
		x.False(v.GetEverySite())
	})

	t.Run("at roster, or at another app, a caller who holds no list is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := ask(loft, app.HolderReachesRequest_builder{Ref: her, Roster: ptrTo(true)}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
		x.Equal("roster: what somebody may call at roster is answered to whoever holds /roster.BindingService/List across the tenant, "+
			"whose lists it adds up; an app asks about itself by naming neither", status.Convert(err).Message())

		_, err = ask(levyan, app.HolderReachesRequest_builder{Ref: her, App: of(loft)}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
		x.Equal("app: what somebody may call at another app is answered to whoever holds /roster.BindingService/List across the tenant, "+
			"whose lists it adds up; an app asks about itself by naming neither", status.Convert(err).Message())
	})

	t.Run("and one who does is answered", func(t *testing.T) {
		x := require.New(t)

		v, err := ask(admin, app.HolderReachesRequest_builder{Ref: her, Roster: ptrTo(true)}.Build())
		x.NoError(err)
		x.Equal([]string{getHolder}, v.GetMethods(), "at roster, of roster's roles")
		x.Equal([]string{getHolder}, v.GetEverywhere())

		v, err = ask(admin, app.HolderReachesRequest_builder{Ref: her, App: of(loft)}.Build())
		x.NoError(err)
		x.Equal([]string{atLoft}, v.GetMethods(), "at an app, of that app's")

		v, err = ask(admin, app.HolderReachesRequest_builder{Ref: her}.Build())
		x.NoError(err)
		x.Empty(v.GetMethods(), "a person asking about themselves as an app is answered of their own roles, which are none")
	})

	t.Run("and somebody suspended reaches nothing at any app", func(t *testing.T) {
		x := require.New(t)

		h, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: her, Select: app.HolderSelect_builder{DateUpdated: ptrTo(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: her, DateUpdated: h.GetDateUpdated()}.Build())
		x.NoError(err)

		for _, req := range []*app.HolderReachesRequest{
			app.HolderReachesRequest_builder{Ref: her, Roster: ptrTo(true)}.Build(),
			app.HolderReachesRequest_builder{Ref: her, App: of(loft)}.Build(),
		} {
			v, err := ask(admin, req)
			x.NoError(err)
			x.Empty(v.GetMethods())
		}
	})
}

func ptrTo[T any](v T) *T { return &v }
