package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
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
