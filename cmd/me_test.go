package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/lesomnus/roster/cmd"
	entidentity "github.com/lesomnus/roster/internal/ent/identity"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/me"
	"github.com/lesomnus/roster/server/vouch"
)

// TestMeAnswersAboutTheCaller, and takes nothing to say who that is.
//
// The absence of a subject field is what makes it safe to read unwalled: it
// cannot be pointed at somebody else, so there is no narrowing the missing
// argument has not already done.
func TestMeAnswersAboutTheCaller(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	team := b.team(t, ctx, seoul, "operators")

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)

	admin := b.role(t, ctx, "operator", getHolder)
	_, err = b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: admin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	conn := served(t, b.Server)

	v, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, b.ContosoUser),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err)

	x.Equal(b.ContosoUser.Bytes(), v.GetId())
	x.Equal(b.Contoso.Bytes(), v.GetTenant())
	x.Equal("someone", v.GetAlias())

	x.Len(v.GetEmails(), 1)
	x.Equal("someone@contoso.example", v.GetEmails()[0].GetAddress())

	// The team, with the site it is in -- a team's name means nothing without
	// one, since `operators` exists in every site and names different people.
	x.Len(v.GetTeams(), 1)
	x.Equal("operators", v.GetTeams()[0].GetAlias())
	x.Equal("seoul", v.GetTeams()[0].GetSiteAlias())
	x.Equal("operator", v.GetTeams()[0].GetRole())

	// And the union the gate enforces, which is the part a page cannot work
	// out for itself.
	x.Contains(v.GetMethods(), getHolder)
	x.False(v.GetEverySite())
	x.Len(v.GetSites(), 1, "in a team, so narrowed to its site")
}

// TestMeSaysWhatTheGateSays, so that what a page shows and what the server
// allows cannot drift.
func TestMeSaysWhatTheGateSays(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.binds(t, b.ContosoUser, b.role(t, ctx, "reader", getHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	v, err := app.NewMeServiceClient(conn).Get(wire, app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal([]string{getHolder}, v.GetMethods())
	x.True(v.GetEverySite(), "bound across the tenant, so narrowed by no site")

	// What it listed, the server allows.
	_, err = app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// What it did not, the server refuses.
	_, err = app.NewHolderServiceClient(conn).Erase(wire,
		app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
}

// TestMeNeedsSomebody. It is about the caller, so there has to be one.
func TestMeNeedsSomebody(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := served(t, b.Server)

	_, err := app.NewMeServiceClient(conn).Get(ctx, app.MeGetRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err))
}

// TestSomebodyWithNothingCanStillAskWhatTheyHave.
//
// Requiring a role to learn that you have none is a deployment where somebody
// just given an account cannot be told what it is for -- and where the page
// that would say so is the one that cannot load.
func TestSomebodyWithNothingCanStillAskWhatTheyHave(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := served(t, b.Server)

	v, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, b.ContosoUser),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(b.ContosoUser.Bytes(), v.GetId())
	x.Empty(v.GetMethods(), "somebody who holds nothing was told they hold something")
}

// TestMeAnswersHowSomebodySignsIn is item 7 of the twelve, for the one case
// that is safe to answer: somebody's own.
//
// Neither half could be read any other way. `IdentityService` narrows by the
// **tenant**, so a person reading their own identities through it reads their
// whole tenant's and filters -- which is the leak D17 named and D23 exists to
// remove, and it is the first thing a self-service screen reaches for.
// `CredentialService` is not registered at all, because its generated `Get`
// answers with the verifier.
//
// This message takes no subject, so there is nothing to point at anybody else,
// which is the same property that lets `cmd.Policy` waive a binding for it.
func TestMeAnswersHowSomebodySignsIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.identity(t, ctx, b.ContosoUser, "github", "1078")
	b.identity(t, ctx, b.ContosoUser, "entra", "8bf1e0a2")
	b.sets(t, ctx, b.ContosoUser, "correct horse battery staple")

	// Somebody else, in the same tenant, with an identity of their own -- so
	// that "their own" is a claim with something to be wrong about.
	other := b.holder(t, ctx, b.Contoso, "somebody-else")
	b.identity(t, ctx, other, "github", "2049")

	v, err := me.New(b.Ent, cmd.Everything(b.Ent)).Get(
		b.as(ctx, b.ContosoUser, b.Contoso), app.MeGetRequest_builder{}.Build())
	x.NoError(err)

	var providers []string
	for _, i := range v.GetIdentities() {
		providers = append(providers, i.GetProvider())
		x.NotEmpty(i.GetId(), "a screen with a remove button has nothing to name")
		x.NotEqual("2049", i.GetSubject(), "somebody else's identity was answered with")
	}
	x.ElementsMatch([]string{"github", "entra"}, providers)

	x.Len(v.GetCredentials(), 1)
	x.Equal(vouch.KindPassword, v.GetCredentials()[0].GetKind())

	// And the thing that must never be here. There is no field to ask for --
	// which is a compile-time fact as much as a runtime one -- so what this
	// pins is that nothing else on the message is carrying it either.
	raw, err := protojson.Marshal(v)
	x.NoError(err)
	x.NotContains(string(raw), "argon2", "the verifier reached a page")
	x.NotContains(string(raw), "secret")
}

// TestAnOperatorSeesHowSomebodyElseSignsIn is the other half of item 7, and it
// is the same answer asked the ordinary way.
//
// `MeService` is safe to read through no wall because it takes nothing: it
// cannot be pointed at anybody else. This takes a subject, so it goes behind
// the wall and needs a role that names it — and it is what an operator's list
// of "who is here and how do they get in" is drawn from.
func TestAnOperatorSeesHowSomebodyElseSignsIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.identity(t, ctx, b.ContosoUser, "github", "1078")
	b.sets(t, ctx, b.ContosoUser, "correct horse battery staple")

	ref := app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build()

	v, err := b.Ungated.Holder().SignsIn(ctx, app.HolderSignsInRequest_builder{Ref: ref}.Build())
	x.NoError(err)

	x.Len(v.GetIdentities(), 1)
	x.Equal("github", v.GetIdentities()[0].GetProvider())
	x.Len(v.GetCredentials(), 1)
	x.Equal(vouch.KindPassword, v.GetCredentials()[0].GetKind())

	// And nothing that could be presented. The fields are written out, so the
	// verifier is absent rather than deselected.
	raw, err := protojson.Marshal(v)
	x.NoError(err)
	x.NotContains(string(raw), "argon2")
	x.NotContains(string(raw), "secret")

	// The wall decides whether this holder is one the caller may see at all,
	// and somebody outside the tenant is NotFound rather than an empty list --
	// which would say "here, and with no way in" about a person the caller
	// cannot see.
	t.Run("and the wall answers first", func(t *testing.T) {
		x := require.New(t)

		them := b.holder(t, ctx, b.Fabrikam, "erlich")
		b.identity(t, ctx, them, "github", "2049")

		admin := b.holder(t, ctx, b.Contoso, "admin")
		b.mayAnything(admin, b.Contoso)

		_, err := b.Walled.Holder().SignsIn(b.as(ctx, admin, b.Contoso),
			app.HolderSignsInRequest_builder{
				Ref: app.HolderRef_builder{Id: them.Bytes()}.Build(),
			}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"one tenant read how another tenant's person signs in")
	})
}

// TestSomebodyRemovesTheirOwnWayInAndNobodyElseS is D24 §4's write, and the
// shape that keeps it safe is the one that keeps `Get` safe.
//
// `Identity` narrows by the **tenant**, so a person removing their own through
// that service would be doing it with a permission that reaches everybody
// else's — which is the leak D17 named and D23 exists to remove, arriving on
// the one screen it is most tempting on. This takes an identifier and refuses
// one that is not theirs, so the argument is a *which* and never a *whose*.
func TestSomebodyRemovesTheirOwnWayInAndNobodyElseS(t *testing.T) {
	b, ctx := build(t)

	mine := b.identity(t, ctx, b.ContosoUser, "github", "1078")
	b.identity(t, ctx, b.ContosoUser, "entra", "8bf1e0a2")

	them := b.holder(t, ctx, b.Contoso, "somebody-else")
	theirs := b.identity(t, ctx, them, "github", "2049")

	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))
	as := b.as(ctx, b.ContosoUser, b.Contoso)

	t.Run("their own goes", func(t *testing.T) {
		x := require.New(t)

		_, err := s.Unlink(as, app.MeUnlinkRequest_builder{Id: mine.GetId()}.Build())
		x.NoError(err)

		v, err := s.Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Len(v.GetIdentities(), 1)
		x.Equal("entra", v.GetIdentities()[0].GetProvider())
	})

	// Not refused — **not found**, which is the same answer a row that was never
	// there gets. Told apart, this would say whether somebody else's identity
	// exists.
	t.Run("and somebody else's is not there to remove", func(t *testing.T) {
		x := require.New(t)

		_, err := s.Unlink(as, app.MeUnlinkRequest_builder{Id: theirs.GetId()}.Build())
		x.Equal(codes.NotFound, status.Code(err))

		// The live ones. An unlink is a soft erase, so a count over the whole
		// table would still see the one that went.
		n, err := b.Ent.Identity.Query().Where(entidentity.DateErasedIsNil()).Count(ctx)
		x.NoError(err)
		x.Equal(2, n, "one person removed another's way in")
	})

	// And the rule in the layer holds, so the button cannot lock somebody out
	// of their own account.
	t.Run("and the last one stays", func(t *testing.T) {
		x := require.New(t)

		v, err := s.Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Len(v.GetIdentities(), 1)

		_, err = s.Unlink(as, app.MeUnlinkRequest_builder{Id: v.GetIdentities()[0].GetId()}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})
}

// TestSomebodySignsThemselvesOutOfEverything is D26's self-service half.
//
// `HolderService.Invalidate` takes a subject and is an operator's; this takes
// nothing and is the person's own. Neither needs a role, and that is the
// decision rather than an oversight: requiring one means somebody who has just
// been given an account cannot sign themselves out of a session they no longer
// trust, which is the moment they most want to.
func TestSomebodySignsThemselvesOutOfEverything(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))
	as := b.as(ctx, b.ContosoUser, b.Contoso)

	res, err := s.SignOutEverywhere(as, app.MeSignOutEverywhereRequest_builder{}.Build())
	x.NoError(err)
	x.NotNil(res.GetDateInvalidated(), "nothing came back to compare a session against")

	v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Select: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.NotNil(v.GetDateInvalidated())

	// And it is the caller's own row and nobody else's, which the missing
	// subject is the whole of.
	them := b.holder(t, ctx, b.Contoso, "somebody-else")
	other, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: them.Bytes()}.Build(),
		Select: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Nil(other.GetDateInvalidated())
}
