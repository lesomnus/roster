package cmd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/me"
)

// The self-service half of a key, as the line has it: no verb of its own. A
// person calls `ApiKey.Issue` and `Holder.RevokeKey` with their **own**
// reference -- the operator's verbs -- and what keeps the button safe is
// `server/core`'s rule over every grant (nobody hands out a method they do not
// hold), not a method shaped so that it cannot be pointed at anybody else.
// `Me.Get` still lists them, because reading your own record is the one thing
// that must work with no role.

// TestSomebodyMintsAKeyThatActsAsThem.
func TestSomebodyMintsAKeyThatActsAsThem(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")
	own := app.HolderRef_builder{Id: who.Bytes()}.Build()
	as := b.as(ctx, who, b.Contoso)
	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))

	v, err := b.Walled.ApiKey().Issue(as, app.ApiKeyIssueRequest_builder{
		Holder:  own,
		Alias:   "the-nightly-job",
		Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	x.NoError(err)

	// An `rt_` and never an `rk_`. Which kind this is is a fact about which
	// server answered rather than a field, and this server is the data plane.
	x.True(strings.HasPrefix(v.GetToken(), keys.PrefixTenant),
		"a customer's port minted a key of the deployment's own kind: %s", v.GetToken())
	x.Equal("the-nightly-job", v.GetKey().GetAlias())
	x.Empty(v.GetKey().GetSecret(), "the row came back with its verifier")

	t.Run("and it is theirs, because that is the row they named", func(t *testing.T) {
		x := require.New(t)

		u, err := b.Ent.ApiKey.Get(ctx, mustId(t, v.GetKey().GetId()).Uuid())
		x.NoError(err)

		owner, err := u.QueryHolder().Only(ctx)
		x.NoError(err)
		x.Equal(who.Uuid(), owner.Id)
	})

	t.Run("and their own page lists it", func(t *testing.T) {
		x := require.New(t)

		res, err := s.Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Len(res.GetKeys(), 1)
		x.Equal("the-nightly-job", res.GetKeys()[0].GetAlias())
		x.Equal([]string{app.MeService_Get_FullMethodName}, res.GetKeys()[0].GetMethods())
	})

	t.Run("and the secret is nowhere in it", func(t *testing.T) {
		x := require.New(t)

		// A key is readable exactly once. `SignInKey` has the fields written
		// out, so there is no `Select` to get wrong and nowhere for a verifier
		// to appear.
		res, err := s.Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)

		row, err := b.Ent.ApiKey.Get(ctx, mustId(t, v.GetKey().GetId()).Uuid())
		x.NoError(err)
		x.NotEmpty(row.Secret, "nothing was stored to check it against")
		x.NotContains(res.String(), string(row.Secret))
		x.NotContains(res.String(), v.GetToken())
	})

	t.Run("and they can revoke it", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Holder().RevokeKey(as, app.HolderRevokeKeyRequest_builder{
			Ref: own,
			Id:  v.GetKey().GetId(),
		}.Build())
		x.NoError(err)

		res, err := s.Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Empty(res.GetKeys())
	})
}

// TestNobodyMintsThemselvesAKeyWiderThanTheyAre, which is what makes a
// self-service button safe rather than a way to hand out permissions.
//
// The rule is `server/core`'s, over `ApiKey.Add`, and `Issue` reaches it by
// writing through the layer -- so it holds for a person naming their own row
// exactly as it holds for an operator naming somebody else's.
func TestNobodyMintsThemselvesAKeyWiderThanTheyAre(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const mine = "/roster.MeService/Get"
	const theirs = "/roster.HolderService/Erase"

	who := b.holder(t, ctx, b.Contoso, "erin")
	own := app.HolderRef_builder{Id: who.Bytes()}.Build()
	as := b.mayCall(t, ctx, who, "modest", mine, app.ApiKeyService_Issue_FullMethodName)

	_, err := b.Walled.ApiKey().Issue(as, app.ApiKeyIssueRequest_builder{
		Holder:  own,
		Alias:   "reaching",
		Methods: []string{mine, theirs},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"somebody minted themselves a method they do not hold")

	t.Run("and the one they do hold still works", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Walled.ApiKey().Issue(as, app.ApiKeyIssueRequest_builder{
			Holder:  own,
			Alias:   "modest",
			Methods: []string{mine},
		}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetToken())
	})
}

// TestARevokeIsAWhichAndNeverAWhose.
//
// `Holder.RevokeKey` is a *which* within a *whose*: the reference says the
// person, the identifier says the key, and one that is not theirs is
// `NotFound` -- told apart from nothing, so this cannot be used to ask whether
// somebody else's key exists.
func TestARevokeIsAWhichAndNeverAWhose(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")
	other := b.holder(t, ctx, b.Contoso, "sam")

	v, err := b.Walled.ApiKey().Issue(b.as(ctx, other, b.Contoso), app.ApiKeyIssueRequest_builder{
		Holder:  app.HolderRef_builder{Id: other.Bytes()}.Build(),
		Alias:   "sams",
		Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	x.NoError(err)

	_, err = b.Walled.Holder().RevokeKey(b.as(ctx, who, b.Contoso), app.HolderRevokeKeyRequest_builder{
		Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Id:  v.GetKey().GetId(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	// And it is still there, which is the half a status code does not say.
	row, err := b.Ent.ApiKey.Get(ctx, mustId(t, v.GetKey().GetId()).Uuid())
	x.NoError(err)
	x.Nil(row.DateErased, "somebody revoked a key that was not theirs")
}

// TestAKeyThatAllowsNothingIsRefusedRatherThanMinted.
//
// A page that defaulted an empty field to everything the person holds would
// mint a key as wide as they are every time somebody left it alone, and one
// that defaulted to nothing would mint keys that silently do not work.
func TestAKeyThatAllowsNothingIsRefusedRatherThanMinted(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")
	own := app.HolderRef_builder{Id: who.Bytes()}.Build()
	as := b.as(ctx, who, b.Contoso)

	_, err := b.Walled.ApiKey().Issue(as, app.ApiKeyIssueRequest_builder{
		Holder:  own,
		Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a key nobody named")

	_, err = b.Walled.ApiKey().Issue(as, app.ApiKeyIssueRequest_builder{
		Holder: own,
		Alias:  "empty",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a key that opens no door")
}

// TestSelfServiceOverTheWireWithHerOwnKey is self-service under the credential
// a person actually holds, which the layer tests beside this stand in for with
// a hand-built frame.
//
// Three sentences, one caller. Somebody with **no role** still reads their own
// record -- `Me.Get` is waived, which is only worth anything if the whole
// served stack agrees -- and it answers `methods` as empty, because nothing is
// what they may do. Minting themselves more than they are is refused through
// the same stack, on the operator's verb named about their own row. And a key
// that allows nothing is refused rather than minted.
func TestSelfServiceOverTheWireWithHerOwnKey(t *testing.T) {
	ctx := t.Context()

	b := keyFor(t, verify)

	const issue = "/roster.ApiKeyService/Issue"
	const listHolders = "/roster.HolderService/List"

	t.Run("somebody with no role still reads themselves", func(t *testing.T) {
		x := require.New(t)

		nobody := addHolder(t, ctx, b.Server, b.Contoso, "norole")
		hers := mintFor(t, ctx, b, nobody, "her-laptop",
			[]string{"/roster.MeService/Get"}, time.Time{})

		v, err := app.NewMeServiceClient(b.Conn).Get(bearing(ctx, hers),
			app.MeGetRequest_builder{}.Build())
		x.NoError(err, "the waiver did not survive the served stack")
		x.Equal("norole", v.GetAlias())
		x.Empty(v.GetMethods(), "somebody who holds nothing was told otherwise")
	})

	t.Run("and cannot mint themselves more than they are", func(t *testing.T) {
		x := require.New(t)

		permits(t, ctx, b, b.Contoso, b.Who, "issuer", issue)
		hers := mintFor(t, ctx, b, b.Who, "her-minter", []string{issue}, time.Time{})

		own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
		c := app.NewApiKeyServiceClient(b.Conn)

		_, err := c.Issue(bearing(ctx, hers), app.ApiKeyIssueRequest_builder{
			Holder:  own,
			Alias:   "wider",
			Methods: []string{issue, listHolders},
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"somebody minted a key naming a method they do not hold")

		_, err = c.Issue(bearing(ctx, hers), app.ApiKeyIssueRequest_builder{
			Holder: own,
			Alias:  "for-nothing",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err),
			"a key that allows nothing was minted rather than refused")

		v, err := c.Issue(bearing(ctx, hers), app.ApiKeyIssueRequest_builder{
			Holder:  own,
			Alias:   "exactly-her",
			Methods: []string{issue},
		}.Build())
		x.NoError(err, "the two refusals above are rules, not a broken door")
		x.True(strings.HasPrefix(v.GetToken(), keys.PrefixTenant))
	})
}
