package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestAGrantInASiteAndOneForAnAppReachWhatEitherReaches.
//
// Somebody bound roster's verbs on bindings in Seoul and `app-administrator`
// for Foo holds two grants, and each reaches what it reaches. The wall met
// them, and answered what both narrow to: the bindings of Seoul that were
// Foo's, which is fewer than either -- Foo's bindings, which are in no site,
// were not listed, and binding Foo's role was `NotFound`. Right is a list of
// Seoul's bindings and Foo's, each once, and a write let through where either
// grant reaches the row. Neither reaches past itself by the other: a binding
// of Seoul's role to somebody not of Seoul, the tenant's own, or Busan's, is
// refused as it was.
func TestAGrantInASiteAndOneForAnAppReachWhatEitherReaches(t *testing.T) {
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	park, lee, erin := b.holder(t, ctx, b.Contoso, "park"), b.holder(t, ctx, b.Contoso, "lee"), b.holder(t, ctx, b.Contoso, "erin")
	b.joins(t, ctx, park, seoul)
	b.joins(t, ctx, lee, busan)

	dana := b.fooAdmin(t, ctx, w, "dana")
	b.joins(t, ctx, dana, seoul)
	desk := b.siteRole(t, ctx, seoul, "seoul-desk",
		app.BindingService_List_FullMethodName, app.BindingService_Add_FullMethodName)
	b.binds(t, dana, desk, &seoul)

	// Rows of Seoul's, of Busan's and, from the world, of Bar's and the
	// tenant's.
	seoulBinding := b.bindsIn(t, ctx, desk, park, &seoul)
	busanBinding := b.bindsIn(t, ctx, b.siteRole(t, ctx, busan, "busan-desk", app.BindingService_List_FullMethodName), lee, &busan)

	c := clientsOf(served(t, b.Server))
	as := asOverTheWire(ctx, dana)

	t.Run("a list holds the site's and the app's, each once", func(t *testing.T) {
		x := require.New(t)

		bs, err := c.bindings.List(as, app.BindingListRequest_builder{Size: 100}.Build())
		x.NoError(err)
		got := idsOf(bs.GetItems())
		x.True(bytesIn(got, seoulBinding.Bytes()), "a binding of Seoul's was not listed")
		for _, v := range bs.GetItems() {
			r, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
				Ref: app.RoleRef_builder{Id: v.GetRole().GetId()}.Build(), Select: app.RoleSelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
			}.Build())
			x.NoError(err)
			ofFoo := string(r.GetApp().GetId()) == string(w.foo.Bytes()) || string(v.GetApp().GetId()) == string(w.foo.Bytes())
			inSeoul := string(v.GetSite().GetId()) == string(seoul.Bytes())
			x.True(ofFoo || inSeoul, "a binding listed is neither Foo's nor Seoul's")
		}
		x.Len(got, 4, "Seoul's two and Foo's two, each once: %v", got)
		x.False(bytesIn(got, busanBinding.Bytes()), "a binding of Busan's was listed")
		x.False(bytesIn(got, w.barBinding.Bytes()), "a binding of Bar's was listed")
		x.False(bytesIn(got, w.readerBinding.Bytes()), "a binding of the tenant's was listed")

		_, err = c.bindings.Get(as, app.BindingGetRequest_builder{Ref: app.BindingRef_builder{Id: busanBinding.Bytes()}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a binding of Busan's was read")
	})

	t.Run("a write is let through where either reaches the row", func(t *testing.T) {
		x := require.New(t)

		other := b.holder(t, ctx, b.Contoso, "kim")
		b.joins(t, ctx, other, seoul)
		_, err := c.bindings.Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: desk.Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
			Site:   app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		}.Build())
		x.NoError(err, "a binding of Seoul's")

		_, err = c.bindings.Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: w.everything.Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(),
		}.Build())
		x.NoError(err, "a binding of Foo's")
	})

	t.Run("and neither reaches past itself by the other", func(t *testing.T) {
		x := require.New(t)

		_, err := c.bindings.Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: w.reader.Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "a binding of the tenant's: %v", err)

		_, err = c.bindings.Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: desk.Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: lee.Bytes()}.Build(),
			Site:   app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "Seoul's role bound to somebody of Busan, whom Seoul's grant does not reach and Foo's does not make Seoul's: %v", err)

		_, err = c.bindings.Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: w.reader.Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: park.Bytes()}.Build(),
			Site:   app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "the tenant's role bound in Seoul, which Seoul's grant does not see: %v", err)
	})
}

// siteRole is a role of Contoso's in one site, made at the box.
func (b *built) siteRole(t *testing.T, ctx context.Context, site pdid.Id, alias string, methods ...string) pdid.Id {
	t.Helper()

	v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: site.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// bindsIn is a binding made at the box, and its identifier.
func (b *built) bindsIn(t *testing.T, ctx context.Context, role, who pdid.Id, site *pdid.Id) pdid.Id {
	t.Helper()

	req := app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}
	if site != nil {
		req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
	}
	v, err := b.Ungated.Binding().Add(ctx, req.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}
