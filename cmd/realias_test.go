package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/roster/rstr"
)

// What a person is written as, changed.
//
// # Why it is a method and not a field on `Update`
//
// Roles are lists of methods, so a separate name is the only way a deployment
// grants one without the other -- and these are not the same permission.
// `Update` writes what a holder carries about itself and nothing the wall, the
// trail or a permission reads; this writes the **index** every reference
// resolves through.
func TestSomebodyIsWrittenAsSomethingElse(t *testing.T) {
	const realias = "/roster.HolderService/Realias"

	read := func(t *testing.T, b *built, who []byte) *app.Holder {
		t.Helper()
		v, err := b.Ungated.Holder().Get(t.Context(), app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who}.Build(),
			Select: app.HolderSelect_builder{Alias: proto.Bool(true), DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}

	t.Run("the alias moves and the identifier does not", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		who := b.holder(t, ctx, b.Contoso, "alice")

		was := read(t, b, who.Bytes())
		v, err := b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{
			Ref:         app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Alias:       proto.String("alice-kim"),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		x.Equal("alice-kim", v.GetAlias())

		// The whole reason an alias may move at all: it is a convenience for
		// people typing, and the reference is the identifier -- which is what a
		// token carries and what the trail names.
		x.Equal(who.Bytes(), v.GetId())
	})

	// Not a rule this method wrote: the grammar payday holds every alias to,
	// and the unique index that was there before this existed.
	t.Run("a name that is not an alias, and one somebody has", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		who := b.holder(t, ctx, b.Contoso, "alice")
		b.holder(t, ctx, b.Contoso, "bob")

		was := read(t, b, who.Bytes())
		_, err := b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{
			Ref:         app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Alias:       proto.String("Alice Kim"),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))

		_, err = b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{
			Ref:         app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Alias:       proto.String("bob"),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err))
	})

	// Unlike `Disable` and the two beside it, this replaces a value the caller
	// read -- so two editors reaching two names is exactly what a lost update
	// is, and the version is not optional.
	t.Run("a stale version is refused", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		who := b.holder(t, ctx, b.Contoso, "alice")

		was := read(t, b, who.Bytes())
		_, err := b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Alias: proto.String("alice-kim"),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.NoError(err)

		_, err = b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Alias: proto.String("alice-lee"),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.Error(err, "a write against a row that had moved was applied")
	})

	// The decision, written down because it looks like a bug: the old alias is
	// free the moment this returns, and `@contoso/alice` may later be somebody
	// else. Holding it instead would mean a name can never be reused, which is
	// a second trap rather than a fix for the first.
	t.Run("the old alias comes free, and the person did not move", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		alice := b.holder(t, ctx, b.Contoso, "alice")

		was := read(t, b, alice.Bytes())
		_, err := b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{
			Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Alias: proto.String("alice-kim"),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
		x.NoError(err)

		somebodyElse := b.holder(t, ctx, b.Contoso, "alice")
		x.NotEqual(alice, somebodyElse)

		// And the first one is untouched, which is the whole answer: what a
		// token carries and what the trail names did not move.
		still := read(t, b, alice.Bytes())
		x.Equal("alice-kim", still.GetAlias())
	})

	// The point of the separate name: a role may grant one and not the other.
	t.Run("it is granted apart from Update", func(t *testing.T) {
		x := require.New(t)
		b := keyFor(t, "/roster.HolderService/Update")
		ctx := t.Context()
		as := bearing(ctx, b.Token)
		c := app.NewHolderServiceClient(b.Conn)

		_, err := c.Realias(as, app.HolderRealiasRequest_builder{
			Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(), Alias: proto.String("renamed"),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "Update carried Realias with it")

		// And the other way: the method a deployment did grant.
		b2 := keyFor(t, realias, "/roster.HolderService/Get")
		as2 := bearing(ctx, b2.Token)
		c2 := app.NewHolderServiceClient(b2.Conn)

		got, err := c2.Get(as2, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: b2.Who.Bytes()}.Build(),
			Select: app.HolderSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = c2.Realias(as2, app.HolderRealiasRequest_builder{
			Ref: app.HolderRef_builder{Id: b2.Who.Bytes()}.Build(), Alias: proto.String("renamed"),
			DateUpdated: got.GetDateUpdated(),
		}.Build())
		x.NoError(err)
	})
}
