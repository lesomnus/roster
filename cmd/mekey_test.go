package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/me"
)

// The self-service half of a key, which `docs/operating.md` listed under *what
// is not here* for as long as the operator's half existed.
//
// An operator has had one since D51: the console lists somebody's keys beside
// their passwords and providers, mints one and revokes one. A person doing it
// for themselves had `IssueService` on the data plane and no read -- and could
// not have used `IssueService` anyway without a role that reached everybody in
// their tenant, which is the objection `MeService.Unlink`'s comment is built
// from.

// TestSomebodyMintsAKeyThatActsAsThem.
func TestSomebodyMintsAKeyThatActsAsThem(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "erin")
	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))
	as := b.as(ctx, who, b.Acme)

	v, err := s.IssueKey(as, app.MeIssueKeyRequest_builder{
		Alias:   "the-nightly-job",
		Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	x.NoError(err)

	// An `rt_` and never an `rk_`. Which kind this is is a fact about which
	// server answered rather than a field, and this server is the data plane --
	// `issue.proto`'s argument, arriving one service over.
	x.True(strings.HasPrefix(v.GetToken(), keys.PrefixTenant),
		"a customer's port minted a key of the deployment's own kind: %s", v.GetToken())
	x.Equal("the-nightly-job", v.GetKey().GetAlias())

	t.Run("and it is theirs, read off the frame", func(t *testing.T) {
		x := require.New(t)

		// There is no field that could have said otherwise, which is the whole
		// of what makes this the person's own rather than an operator's.
		u, err := b.Ent.ApiKey.Get(ctx, mustId(t, v.GetKey().GetId()).Uuid())
		x.NoError(err)

		owner, err := u.QueryHolder().Only(ctx)
		x.NoError(err)
		x.Equal(who.Uuid(), owner.ID)
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
		// to appear -- which is the statement D13 makes by not registering
		// `ApiKeyService`, said again where a page can hear it.
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

		_, err := s.RevokeKey(as, app.MeRevokeKeyRequest_builder{
			Id: v.GetKey().GetId(),
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
// The rule is not in `MeService` and must not be: it is `server/core`'s, over
// `ApiKey.Add`, and this reaches it by writing through the walled stack. A
// version of this method that reached for the database would compile, work,
// and hand every person in the deployment every method in it.
func TestNobodyMintsThemselvesAKeyWiderThanTheyAre(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const mine = "/roster.MeService/Get"
	const theirs = "/roster.HolderService/Erase"

	who := b.holder(t, ctx, b.Acme, "erin")
	as := b.mayCall(t, ctx, who, "modest", mine, app.MeService_IssueKey_FullMethodName)

	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))

	_, err := s.IssueKey(as, app.MeIssueKeyRequest_builder{
		Alias:   "reaching",
		Methods: []string{mine, theirs},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"somebody minted themselves a method they do not hold")

	t.Run("and the one they do hold still works", func(t *testing.T) {
		x := require.New(t)

		v, err := s.IssueKey(as, app.MeIssueKeyRequest_builder{
			Alias:   "modest",
			Methods: []string{mine},
		}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetToken())
	})
}

// TestARevokeIsAWhichAndNeverAWhose.
//
// The same care `Unlink` takes, for the same reason: the read that finds the
// key is narrowed by the caller before it is narrowed by the identifier, so one
// that is not theirs is `NotFound` -- told apart from nothing. Answered any
// other way, this is a question about whether somebody else's key exists.
func TestARevokeIsAWhichAndNeverAWhose(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))

	who := b.holder(t, ctx, b.Acme, "erin")
	other := b.holder(t, ctx, b.Acme, "sam")

	v, err := s.IssueKey(b.as(ctx, other, b.Acme), app.MeIssueKeyRequest_builder{
		Alias:   "sams",
		Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	x.NoError(err)

	_, err = s.RevokeKey(b.as(ctx, who, b.Acme), app.MeRevokeKeyRequest_builder{
		Id: v.GetKey().GetId(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	// And it is still there, which is the half a status code does not say.
	row, err := b.Ent.ApiKey.Get(ctx, mustId(t, v.GetKey().GetId()).Uuid())
	x.NoError(err)
	x.Nil(row.DateErased, "somebody revoked a key that was not theirs")
}

// TestAKeyThatAllowsNothingIsRefusedRatherThanMinted.
//
// `IssueKeyRequest.methods` states this and it matters more here than there: a
// page that defaulted an empty field to everything the person holds would mint
// a key as wide as they are every time somebody left it alone, and one that
// defaulted to nothing would mint keys that silently do not work.
func TestAKeyThatAllowsNothingIsRefusedRatherThanMinted(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "erin")
	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))
	as := b.as(ctx, who, b.Acme)

	_, err := s.IssueKey(as, app.MeIssueKeyRequest_builder{
		Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a key nobody named")

	_, err = s.IssueKey(as, app.MeIssueKeyRequest_builder{Alias: "empty"}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a key that opens no door")
}
