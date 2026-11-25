package cmd_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// listByAppRows is one app's role, group, binding held for it and key for
// it, made at the box.
type listByAppRows struct {
	role, group, binding, key pdid.Id
}

func (b *built) listByAppRows(t *testing.T, ctx context.Context, of pdid.Id, admin pdid.Id, name string) listByAppRows {
	t.Helper()
	x := require.New(t)

	r := listByAppRows{
		role:  b.roleAtApp(t, ctx, of, "listed-"+name+"-staff", "/"+name+".*/*"),
		group: b.appGroup(t, ctx, of, "listed-"+name+"-users"),
	}

	v, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: admin.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "listed-"+name+"-admin").Bytes()}.Build(),
		App:    app.HolderRef_builder{Id: of.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	r.binding = mustId(t, v.GetId())

	_, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	k, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Alias:   "listed-" + name + "-key",
		Secret:  sum,
		Methods: []string{"/" + name + ".Thing/Get"},
		App:     app.HolderRef_builder{Id: of.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	r.key = mustId(t, k.GetId())

	return r
}

// listByApp lists each of the four over `conn` as `as`, filtered by `of`
// where it is not nil, and answers the identifiers each answered.
func listByApp(t *testing.T, conn grpc.ClientConnInterface, as context.Context, of *app.HolderRef) (roles, groups, bindings, keys [][]byte) {
	t.Helper()
	x := require.New(t)

	rs, err := app.NewRoleServiceClient(conn).List(as, app.RoleListRequest_builder{Filters: listByAppFilter(of, func(h *app.HolderRef) *app.RoleFilter {
		return app.RoleFilter_builder{App: h}.Build()
	}), Size: 100}.Build())
	x.NoError(err)
	gs, err := app.NewGroupServiceClient(conn).List(as, app.GroupListRequest_builder{Filters: listByAppFilter(of, func(h *app.HolderRef) *app.GroupFilter {
		return app.GroupFilter_builder{App: h}.Build()
	}), Size: 100}.Build())
	x.NoError(err)
	bs, err := app.NewBindingServiceClient(conn).List(as, app.BindingListRequest_builder{Filters: listByAppFilter(of, func(h *app.HolderRef) *app.BindingFilter {
		return app.BindingFilter_builder{App: h}.Build()
	}), Size: 100}.Build())
	x.NoError(err)
	ks, err := app.NewApiKeyServiceClient(conn).List(as, app.ApiKeyListRequest_builder{Filters: listByAppFilter(of, func(h *app.HolderRef) *app.ApiKeyFilter {
		return app.ApiKeyFilter_builder{App: h}.Build()
	}), Size: 100}.Build())
	x.NoError(err)

	return idsOf(rs.GetItems()), idsOf(gs.GetItems()), idsOf(bs.GetItems()), idsOf(ks.GetItems())
}

func listByAppFilter[F any](of *app.HolderRef, f func(*app.HolderRef) F) []F {
	if of == nil {
		return nil
	}

	return []F{f(of)}
}

// TestAnAppsRowsAreListedByTheApp: `RoleService/List`, `GroupService/List`,
// `BindingService/List` and `ApiKeyService/List` take the app, and answer the
// rows whose `app` is that holder.
//
// None of the four took one (#567), so whoever wanted one app's roles read
// every role the wall showed them and picked. The filter means what the
// others mean, and narrows nothing more than the wall does: an app's
// administrator is answered that app's rows with it or without it, and
// nothing at all for another app's.
func TestAnAppsRowsAreListedByTheApp(t *testing.T) {
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	foo := b.listByAppRows(t, ctx, w.foo, w.admin, "foo")
	bar := b.listByAppRows(t, ctx, w.bar, w.admin, "bar")

	everybody := b.holder(t, ctx, b.Contoso, "lists-everything")
	b.mayAnything(everybody, b.Contoso)
	ofFoo := app.HolderRef_builder{Id: w.foo.Bytes()}.Build()
	ofBar := app.HolderRef_builder{Id: w.bar.Bytes()}.Build()
	conn := served(t, b.Server)

	t.Run("each of the four by app answers that app's rows and no others", func(t *testing.T) {
		x := require.New(t)
		as := asOverTheWire(ctx, everybody)

		all, _, _, _ := listByApp(t, conn, as, nil)
		x.True(bytesIn(all, bar.role.Bytes()) && bytesIn(all, w.reader.Bytes()), "no filter is everything the caller may see")

		roles, groups, bindings, ks := listByApp(t, conn, as, ofFoo)
		x.True(bytesIn(roles, foo.role.Bytes()), "Foo's role")
		x.True(bytesIn(roles, w.everything.Bytes()), "Foo's everything")
		x.True(bytesIn(groups, foo.group.Bytes()))
		x.Equal([][]byte{foo.binding.Bytes()}, bindings)
		x.Equal([][]byte{foo.key.Bytes()}, ks)
		for _, v := range [][]byte{bar.role.Bytes(), w.barRole.Bytes(), w.reader.Bytes(), w.admin.Bytes()} {
			x.False(bytesIn(roles, v), "a role not Foo's, listed by Foo")
		}
		for _, v := range [][]byte{bar.group.Bytes(), w.barGroup.Bytes(), w.employees.Bytes()} {
			x.False(bytesIn(groups, v), "a group not Foo's, listed by Foo")
		}

		// By alias, as a person names one.
		roles, _, _, _ = listByApp(t, conn, as, app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
			Alias: z.Ptr("bar"), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		}.Build()}.Build())
		x.True(bytesIn(roles, bar.role.Bytes()))
		x.False(bytesIn(roles, foo.role.Bytes()))
	})

	t.Run("an app's administrator filtering by another app is answered nothing", func(t *testing.T) {
		x := require.New(t)
		alice := b.fooAdmin(t, ctx, w, "lists-alice")
		as := asOverTheWire(ctx, alice)

		roles, groups, _, _ := listByApp(t, conn, as, nil)
		x.True(bytesIn(roles, foo.role.Bytes()))
		x.False(bytesIn(roles, bar.role.Bytes()), "Bar's role, to Foo's administrator")
		x.False(bytesIn(roles, w.reader.Bytes()), "the tenant's role, to Foo's administrator")
		x.False(bytesIn(groups, bar.group.Bytes()))

		filtered, fgroups, _, _ := listByApp(t, conn, as, ofFoo)
		x.ElementsMatch(roles, filtered, "the filter by their own app answers what no filter does")
		x.ElementsMatch(groups, fgroups)

		roles, groups, bindings, ks := listByApp(t, conn, as, ofBar)
		x.Empty(roles, "Foo's administrator, listing Bar's roles")
		x.Empty(groups)
		x.Empty(bindings)
		x.Empty(ks)
	})

	// An edge that is none has no filter: a reference naming nothing is
	// refused, as every list refuses one for an edge it takes (`site`).
	t.Run("roster's own is not a filter", func(t *testing.T) {
		_, err := app.NewRoleServiceClient(conn).List(asOverTheWire(ctx, everybody), app.RoleListRequest_builder{
			Filters: []*app.RoleFilter{app.RoleFilter_builder{App: app.HolderRef_builder{}.Build()}.Build()},
		}.Build())
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})
}

// TestTheBoxListsAnAppsRolesByTheApp: `roster role list` takes the filter in
// its request, as every list's filters are written.
func TestTheBoxListsAnAppsRolesByTheApp(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	for _, args := range [][]string{
		{"holder", "add", "@newco/foo"},
		{"role", "add", "@newco/foo-staff", `{"methods":["/foo.*/*"],"app":{"slug":{"alias":"foo","tenant":{"alias":"newco"}}}}`},
		{"role", "add", "@newco/plain", `{"methods":["/roster.HolderService/Get"]}`},
	} {
		_, said, err := boxRun(t, at(b.c, "admin"), "", args...)
		x.NoError(err, "%v: %s", args, said)
	}

	out, said, err := boxRun(t, at(b.c, "admin"), "", "role", "list", "-o", "json",
		`{"filters":[{"app":{"slug":{"alias":"foo","tenant":{"alias":"newco"}}}}]}`)
	x.NoError(err, said)

	var got struct {
		Items []struct{ Alias string }
	}
	x.NoError(json.Unmarshal([]byte(out), &got), out)
	aliases := []string{}
	for _, v := range got.Items {
		aliases = append(aliases, v.Alias)
	}
	x.Equal([]string{"foo-staff"}, aliases)
}
