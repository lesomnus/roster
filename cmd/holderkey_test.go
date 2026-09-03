package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestOneScreenShowsEveryWayIntoOneAccount.
//
// `SignsIn` exists because `ApiKeyService` and `CredentialService` are
// unregistered everywhere -- each has a generated `Get` that answers with a
// verifier -- and `IdentityService` narrows by the **tenant**, so a page that
// listed one person's ways in by reading and sifting would be reading every
// customer's to draw one.
//
// A **key** is a way in by the same definition as the other two: it resolves to
// its holder, so a call made with it is made as them. It was the one that this
// read did not answer, which meant a console could show what somebody signs in
// with and not what already signs in as them.
func TestOneScreenShowsEveryWayIntoOneAccount(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")
	b.identity(t, ctx, who, "github", "gh-erin")

	_, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   "ci",
		Secret:  sum,
		Methods: []string{"/roster.HolderService/List"},
	}.Build())
	x.NoError(err)

	// Somebody else's, which must not be in the answer.
	other := b.holder(t, ctx, b.Contoso, "somebody-else")

	_, sum2, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: other.Bytes()}.Build(),
		Alias:   "not-hers",
		Secret:  sum2,
		Methods: []string{"/roster.HolderService/List"},
	}.Build())
	x.NoError(err)

	res, err := b.Walled.Holder().SignsIn(b.asNobody(ctx, who, b.Contoso),
		app.HolderSignsInRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())
	x.NoError(err)

	x.Len(res.GetIdentities(), 1)
	x.Len(res.GetKeys(), 1, "the answer carries somebody else's keys, or none of hers")
	x.Equal("ci", res.GetKeys()[0].GetAlias())
	x.Equal([]string{"/roster.HolderService/List"}, res.GetKeys()[0].GetMethods())

	t.Run("and never the verifier", func(t *testing.T) {
		x := require.New(t)

		// There is no field for it, which is the statement rather than a
		// `Select` somebody could get wrong -- the same shape the credentials
		// beside it take.
		raw, err := proto.Marshal(res.GetKeys()[0])
		x.NoError(err)
		x.NotContains(string(raw), string(sum))
	})

	t.Run("and one of them can be ended", func(t *testing.T) {
		x := require.New(t)

		// `ApiKey.Erase`, by identifier: the layer reads the row through the
		// wall and holds its holder to `mayReach`, self passing.
		_, err := b.Walled.ApiKey().Erase(b.asNobody(ctx, who, b.Contoso),
			app.ApiKeyRef_builder{Id: res.GetKeys()[0].GetId()}.Build())
		x.NoError(err)

		now, err := b.Walled.Holder().SignsIn(b.asNobody(ctx, who, b.Contoso),
			app.HolderSignsInRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
		x.NoError(err)
		x.Empty(now.GetKeys())
	})

	t.Run("and never somebody wider's", func(t *testing.T) {
		x := require.New(t)

		// The line: a role naming `ApiKey.Erase` reaches anybody no wider than
		// the caller, and `mayReach` refuses anybody wider. So the other's key
		// is refused once the other holds more than erin does -- which is the
		// case that matters, a plain person ending an administrator's key.
		b.mayCall(t, ctx, other, "admin", "/roster.HolderService/Erase")

		vs, err := b.Walled.Holder().SignsIn(b.as(ctx, other, b.Contoso),
			app.HolderSignsInRequest_builder{
				Ref: app.HolderRef_builder{Id: other.Bytes()}.Build(),
			}.Build())
		x.NoError(err)
		x.Len(vs.GetKeys(), 1)

		_, err = b.Walled.ApiKey().Erase(b.asNobody(ctx, who, b.Contoso),
			app.ApiKeyRef_builder{Id: vs.GetKeys()[0].GetId()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a plain person ended an administrator's key")
	})
}
