package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

const (
	issueKey    = "/roster.ApiKeyService/Issue"
	listHolder  = "/roster.HolderService/List"
	eraseHolder = "/roster.HolderService/Erase"
)

// TestACustomerMintsTheirOwnKeyOverTheWire.
//
// The gap the roadmap carried under P5 for as long as there was a roadmap:
// *not done, minting an `rt_` over the wire*. The half that **answers** was
// finished long ago -- a tenant key resolves to its holder and
// `TokenService/Introspect` serves it to product apps -- and the half that
// issues was a shell on the box, which is not a thing a customer has.
//
// It hands out nothing new, which is what makes it safe to offer at all: an
// `rt_` resolves to a person and is narrowed by what that person may do, so a
// key is at most a second copy of a credential they already hold, and less,
// since it names methods.
func TestACustomerMintsTheirOwnKeyOverTheWire(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// She may mint keys, and may list the people in her tenant. Nothing else.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "self-serve", issueKey, listHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	res, err := app.NewApiKeyServiceClient(conn).Issue(wire, app.ApiKeyIssueRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Alias:   "ci",
		Methods: []string{listHolder},
	}.Build())
	x.NoError(err)

	x.True(strings.HasPrefix(res.GetToken(), keys.PrefixTenant),
		"the customer-facing port minted a key of the deployment's own kind: %q", res.GetToken())
	x.NotNil(res.GetKey())

	// Answered once, and what is stored is a hash. The row that comes back
	// beside the token has its verifier cleared like every other answer.
	x.Empty(res.GetKey().GetSecret(), "the row came back carrying the verifier")

	t.Run("and the row is hers, with what she named", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ent.ApiKey.Query().WithHolder().Only(ctx)
		x.NoError(err)
		x.Equal(b.ContosoUser.Uuid(), v.Edges.Holder.Id)
		x.Equal([]string{listHolder}, v.Methods)
		x.Equal("ci", v.Alias)

		// The hash and not the token. What was answered with is the only time
		// the string existed anywhere but in the caller's hands.
		x.NotEmpty(v.Secret)
		x.NotContains(string(v.Secret), res.GetToken())
	})

	// That the token then **resolves to her** -- one tenant, her permissions,
	// not the deployment's -- is `TestATenantKeyIsTheirsAndNotTheDeploymentS`,
	// which is the same property asked of a key minted at a shell. It needs a
	// deployment that reads keys at all, and this one is `auth.Plain`: with no
	// `control:` there is nowhere to keep them, so nothing here would read one
	// back. That is the sandbox caveat `auth.Plain` carries everywhere and not
	// a fact about this service.
}

// TestNobodyMintsAKeyWiderThanThemselves.
//
// The first of the two rules a key is held to, and it is not written in the
// issuer: minting goes through the **walled** server, so `core.ApiKey.Add`
// runs. *Nobody hands out a method they do not hold* -- a key is the most
// direct grant there is, since whoever holds the string can call whatever the
// column says.
//
// Over the wire and not in process, because what this is really asserting is
// that the new door is behind the same rules the old one was. A service that
// reached the ungated server would pass every check by having no frame.
func TestNobodyMintsAKeyWiderThanThemselves(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.binds(t, b.ContosoUser, b.role(t, ctx, "self-serve", issueKey, listHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	_, err := app.NewApiKeyServiceClient(conn).Issue(wire, app.ApiKeyIssueRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Alias:   "wider",
		Methods: []string{listHolder, eraseHolder},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she minted a key holding what she does not")
	x.Contains(status.Convert(err).Message(), eraseHolder)
}

// TestNobodyMintsAKeyOnSomebodyElsesAccount.
//
// The second rule, and the one that makes this safe to serve to customers at
// all. A key **resolves to its holder**, so calls made with it are made as
// them: minting one on the administrator's row carrying only a method the
// minter holds is a credential for the administrator, written by somebody who
// could not otherwise reach them.
//
// `core/apikey.go` records exactly this as the finding the methods check alone
// left open, one door over. This is the same door reached from the wire.
func TestNobodyMintsAKeyOnSomebodyElsesAccount(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// The administrator, who may erase people.
	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.binds(t, boss, b.role(t, ctx, "admin", eraseHolder), nil)

	// Alice, who may mint keys and list people, and cannot reach the boss.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "self-serve", issueKey, listHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	_, err := app.NewApiKeyServiceClient(conn).Issue(wire, app.ApiKeyIssueRequest_builder{
		// Only a method she holds, which is what makes this the interesting
		// case: the methods check passes and the key is still an account.
		Holder:  app.HolderRef_builder{Id: boss.Bytes()}.Build(),
		Alias:   "not-hers",
		Methods: []string{listHolder},
	}.Build())
	x.Error(err, "she minted a credential that acts as somebody she cannot reach")
	x.Equal(codes.PermissionDenied, status.Code(err))
}

// TestAKeyIsNotMintedIntoAnotherTenant.
//
// The wall, asserted at this door because a new service is a new place for it
// to have been forgotten. It answers NotFound rather than PermissionDenied,
// which is the wall working as a wall: what a caller cannot see does not exist,
// and telling them apart would say whether somebody is there.
func TestAKeyIsNotMintedIntoAnotherTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	stranger := b.holder(t, ctx, b.Fabrikam, "stranger")
	b.binds(t, b.ContosoUser, b.role(t, ctx, "self-serve", issueKey, listHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	_, err := app.NewApiKeyServiceClient(conn).Issue(wire, app.ApiKeyIssueRequest_builder{
		Holder:  app.HolderRef_builder{Id: stranger.Bytes()}.Build(),
		Alias:   "across",
		Methods: []string{listHolder},
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestTheTwoPlanesNameAHolderTheirOwnWay.
//
// `service` creates a holder by being named, which is right where there is one
// tenant and wrong where there are many: a call that made a person by
// mentioning them is a way to write rows into somebody else's tenant by typo.
//
// So each plane takes one form and refuses the other, and refuses a request
// that gave both -- the refusal `vouch.refOf` makes about a person named two
// ways, because a caller that filled in both has not decided which it means.
func TestTheTwoPlanesNameAHolderTheirOwnWay(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.binds(t, b.ContosoUser, b.role(t, ctx, "self-serve", issueKey, listHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)
	c := app.NewApiKeyServiceClient(conn)

	_, err := c.Issue(wire, app.ApiKeyIssueRequest_builder{
		Service: "made-up-by-mentioning",
		Alias:   "ci",
		Methods: []string{listHolder},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(status.Convert(err).Message(), "holder:")

	_, err = c.Issue(wire, app.ApiKeyIssueRequest_builder{
		Service: "both",
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Alias:   "ci",
		Methods: []string{listHolder},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(status.Convert(err).Message(), "two ways")

	_, err = c.Issue(wire, app.ApiKeyIssueRequest_builder{
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Alias:  "ci",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(status.Convert(err).Message(), "opens no door")
}
