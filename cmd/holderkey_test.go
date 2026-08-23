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

	res, err := b.Walled.Holder().SignsIn(b.as(ctx, who, b.Contoso),
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

		_, err := b.Walled.Holder().RevokeKey(b.as(ctx, who, b.Contoso),
			app.HolderRevokeKeyRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
				Id:  res.GetKeys()[0].GetId(),
			}.Build())
		x.NoError(err)

		now, err := b.Walled.Holder().SignsIn(b.as(ctx, who, b.Contoso),
			app.HolderSignsInRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
		x.NoError(err)
		x.Empty(now.GetKeys())
	})

	t.Run("and never somebody else's", func(t *testing.T) {
		x := require.New(t)

		// A *which* within a *whose*: the reference says the person and the
		// identifier says the key, so pointing one at the other's is NotFound
		// rather than a revocation.
		vs, err := b.Walled.Holder().SignsIn(b.as(ctx, other, b.Contoso),
			app.HolderSignsInRequest_builder{
				Ref: app.HolderRef_builder{Id: other.Bytes()}.Build(),
			}.Build())
		x.NoError(err)
		x.Len(vs.GetKeys(), 1)

		_, err = b.Walled.Holder().RevokeKey(b.as(ctx, who, b.Contoso),
			app.HolderRevokeKeyRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
				Id:  vs.GetKeys()[0].GetId(),
			}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"a key was revoked through a person it does not belong to")
	})
}
