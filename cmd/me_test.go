package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/lesomnus/roster/cmd"
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

	seoul := b.site(t, ctx, b.Acme, "seoul")
	team := b.team(t, ctx, seoul, "operators")

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Address: "someone@acme.example",
	}.Build())
	x.NoError(err)

	admin := b.role(t, ctx, "operator", getHolder)
	_, err = b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: admin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	conn := served(t, b.Server)

	v, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, b.AcmeUser),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err)

	x.Equal(b.AcmeUser.Bytes(), v.GetId())
	x.Equal(b.Acme.Bytes(), v.GetTenant())
	x.Equal("someone", v.GetAlias())

	x.Len(v.GetEmails(), 1)
	x.Equal("someone@acme.example", v.GetEmails()[0].GetAddress())

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

	b.binds(t, b.AcmeUser, b.role(t, ctx, "reader", getHolder), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.AcmeUser)

	v, err := app.NewMeServiceClient(conn).Get(wire, app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal([]string{getHolder}, v.GetMethods())
	x.True(v.GetEverySite(), "bound across the tenant, so narrowed by no site")

	// What it listed, the server allows.
	_, err = app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// What it did not, the server refuses.
	_, err = app.NewHolderServiceClient(conn).Erase(wire,
		app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build())
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

	v, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, b.AcmeUser),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(b.AcmeUser.Bytes(), v.GetId())
	x.Empty(v.GetMethods(), "somebody who holds nothing was told they hold something")
}

// TestMeAnswersHowSomebodySignsIn is item 7 of PLAN.md's list, for the one case
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

	b.identity(t, ctx, b.AcmeUser, "github", "1078")
	b.identity(t, ctx, b.AcmeUser, "entra", "8bf1e0a2")
	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	// Somebody else, in the same tenant, with an identity of their own -- so
	// that "their own" is a claim with something to be wrong about.
	other := b.holder(t, ctx, b.Acme, "somebody-else")
	b.identity(t, ctx, other, "github", "2049")

	v, err := me.New(b.Ent, cmd.Everything(b.Ent)).Get(
		b.as(ctx, b.AcmeUser, b.Acme), app.MeGetRequest_builder{}.Build())
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

	b.identity(t, ctx, b.AcmeUser, "github", "1078")
	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	ref := app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build()

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

		them := b.holder(t, ctx, b.Hooli, "erlich")
		b.identity(t, ctx, them, "github", "2049")

		admin := b.holder(t, ctx, b.Acme, "admin")
		b.mayAnything(admin, b.Acme)

		_, err := b.Walled.Holder().SignsIn(b.as(ctx, admin, b.Acme),
			app.HolderSignsInRequest_builder{
				Ref: app.HolderRef_builder{Id: them.Bytes()}.Build(),
			}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"one tenant read how another tenant's person signs in")
	})
}
