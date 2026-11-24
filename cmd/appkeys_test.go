package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
)

// An app reads and ends the keys that are for it (#567).
//
// `ApiKeyService`'s `Get`, `List` and `Erase` are among roster's verbs held for
// one app, and a key is an app's row when its `app` is that app: so the app,
// as itself, and its administrators list somebody's keys for it and end one,
// and an app's page shows a person their own keys for it.

// TestAnAppReadsAndEndsTheKeysThatAreForIt.
//
// A key was read by whose it is and by whoever holds `HolderService/SignsIn`
// across the tenant, and ended by whoever reaches its holder: Foo could not
// list the keys minted for it, of one person or of everybody, nor end one --
// each refused `PermissionDenied`. Foo's holder, bound `app-administrator` for
// itself, and Foo's administrator list the keys for Foo of one person and of
// everybody, read one, and end one that is somebody else's; they see none of
// roster's and none for Bar (`NotFound`, as a row they do not see), and end
// none of them (`PermissionDenied`, naming Foo). Somebody who holds the verbs
// for Bar sees and ends nothing of Foo's. Their own keys are theirs as ever.
// And a key Foo ended is none to Foo at the next `Introspect`.
func TestAnAppReadsAndEndsTheKeysThatAreForIt(t *testing.T) {
	b, ctx := build(t)
	w := b.appsWorld(t, ctx)
	b.forApp(t, ctx, w.foo, w.admin, w.foo) // what `roster app install` binds
	alice := b.fooAdmin(t, ctx, w, "alice")
	carol := b.holder(t, ctx, b.Contoso, "carol")
	b.forApp(t, ctx, carol, w.admin, w.bar)
	b.binds(t, w.foo, b.role(t, ctx, "foo-introspects", introspect), nil)

	erin := b.holder(t, ctx, b.Contoso, "erin")
	bob := b.holder(t, ctx, b.Contoso, "bob")
	_, erinsFoo := keyForApp(t, ctx, b.Server, erin, w.foo, "for-foo", "/foo.AssetService/Get")
	_, erinsBar := keyForApp(t, ctx, b.Server, erin, w.bar, "for-bar", "/bar.AssetService/Get")
	_, erinsOwn := keyForApp(t, ctx, b.Server, erin, pdid.Nil, "rosters", getHolder)
	_, alicesOwn := keyForApp(t, ctx, b.Server, alice, pdid.Nil, "alices", getHolder)

	conn := served(t, b.Server)
	keys := app.NewApiKeyServiceClient(conn)
	ofErin := []*app.ApiKeyFilter{app.ApiKeyFilter_builder{Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build()}.Build()}
	listed := func(t *testing.T, who pdid.Id, fs []*app.ApiKeyFilter) [][]byte {
		t.Helper()
		v, err := keys.List(asOverTheWire(ctx, who), app.ApiKeyListRequest_builder{Filters: fs, Size: 100}.Build())
		require.NoError(t, err)

		return idsOf(v.GetItems())
	}
	read := func(who, k pdid.Id) error {
		_, err := keys.Get(asOverTheWire(ctx, who), app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: k.Bytes()}.Build()}.Build())

		return err
	}
	end := func(who, k pdid.Id) error {
		_, err := keys.Erase(asOverTheWire(ctx, who), app.ApiKeyRef_builder{Id: k.Bytes()}.Build())

		return err
	}

	for name, who := range map[string]pdid.Id{"Foo's holder": w.foo, "Foo's administrator": alice} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			_, bobsFoo := keyForApp(t, ctx, b.Server, bob, w.foo, "for-foo-"+who.String(), "/foo.AssetService/Get")

			x.Equal([][]byte{erinsFoo.Bytes()}, listed(t, who, ofErin), "the keys for Foo of one person")
			all := listed(t, who, nil)
			x.Contains(all, erinsFoo.Bytes(), "the keys for Foo of everybody")
			x.Contains(all, bobsFoo.Bytes())
			x.False(bytesIn(all, erinsBar.Bytes()), "a key for Bar was listed")
			x.False(bytesIn(all, erinsOwn.Bytes()), "a key of roster's was listed")

			x.NoError(read(who, erinsFoo), "a key for Foo was not read")
			x.Equal(codes.NotFound, status.Code(read(who, erinsBar)), "a key for Bar was read")
			x.Equal(codes.NotFound, status.Code(read(who, erinsOwn)), "a key of roster's was read")

			err := end(who, erinsBar)
			x.Equal(codes.PermissionDenied, status.Code(err), "a key for Bar was ended: %v", err)
			x.Contains(status.Convert(err).Message(), "@contoso/foo")
			x.Equal(codes.PermissionDenied, status.Code(end(who, erinsOwn)), "a key of roster's was ended")
			x.NoError(end(who, bobsFoo), "somebody else's key for Foo was not ended")
		})
	}

	t.Run("their own as ever", func(t *testing.T) {
		x := require.New(t)

		x.Contains(listed(t, alice, nil), alicesOwn.Bytes(), "Foo's administrator did not list their own key")
		x.NoError(end(alice, alicesOwn), "Foo's administrator did not end their own key")
		x.False(bytesIn(listed(t, alice, nil), alicesOwn.Bytes()))
	})

	t.Run("and what holds the verbs for Bar, nothing of Foo's", func(t *testing.T) {
		x := require.New(t)

		x.Equal([][]byte{erinsBar.Bytes()}, listed(t, carol, ofErin))
		x.Equal(codes.NotFound, status.Code(read(carol, erinsFoo)))
		x.Equal(codes.PermissionDenied, status.Code(end(carol, erinsFoo)))
	})

	t.Run("and a key Foo ended is none to Foo", func(t *testing.T) {
		x := require.New(t)

		token, k := keyForApp(t, ctx, b.Server, erin, w.foo, "ended", "/foo.AssetService/Get")
		tokens := pdpb.NewTokenServiceClient(conn)
		ask := func() error {
			_, err := tokens.Introspect(asOverTheWire(ctx, w.foo), pdpb.TokenIntrospectRequest_builder{Token: token}.Build())

			return err
		}
		x.NoError(ask())
		x.NoError(end(w.foo, k))
		x.Equal(codes.NotFound, status.Code(ask()))
	})
}
