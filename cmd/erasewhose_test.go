package cmd_test

import (
	"testing"

	"github.com/lesomnus/payday/frame"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAWayInIsTakenAwayByWhoeverCouldWriteIt: an address and an identity are
// erased by whoever may write one onto that person, and by the person.
//
// # What it was
//
// Writing a way into somebody's account is held to [core.mayReach] -- the
// writer holds everything they do -- and erasing one was asked nothing: the
// generated `Erase`, behind the role that names it. So somebody holding
// `Email.Erase` and nothing else erased the administrator's address, and then
// wrote it onto their own row, which is the write no rule refuses (#274). The
// administrator's mail-borne way back was gone, and the address was somebody
// else's to prove.
//
// A key and a delegation were already erased by whoever reaches their holder
// (`coreApiKey.reaches`). These two are that.
func TestAWayInIsTakenAwayByWhoeverCouldWriteIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	admin := b.holder(t, ctx, b.Contoso, "admin")
	b.mayAnything(admin, b.Contoso)
	b.sets(t, ctx, admin, "the one the admin chose")

	low := b.holder(t, ctx, b.Contoso, "low")
	b.sets(t, ctx, low, "the one low chose")

	desk := b.holder(t, ctx, b.Contoso, "desk")
	b.sets(t, ctx, desk, "the one the desk chose")
	as := b.mayCall(t, ctx, desk, "takes-ways-in-away",
		app.EmailService_Erase_FullMethodName,
		app.IdentityService_Erase_FullMethodName,
	)

	type ways struct {
		email    *app.EmailRef
		identity *app.IdentityRef
	}
	of := func(who []byte, alias string) ways {
		ref := app.HolderRef_builder{Id: who}.Build()

		e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: alias + "@contoso.example"}.Build())
		x.NoError(err)
		i, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{Holder: ref, Provider: "door-of-" + alias, Subject: "subject-of-" + alias}.Build())
		x.NoError(err)

		return ways{
			email:    app.EmailRef_builder{Id: e.GetId()}.Build(),
			identity: app.IdentityRef_builder{Id: i.GetId()}.Build(),
		}
	}

	t.Run("not of somebody who holds what the caller does not", func(t *testing.T) {
		x := require.New(t)
		theirs := of(admin.Bytes(), "admin")

		_, err := b.Walled.Email().Erase(as, theirs.email)
		x.Equal(codes.PermissionDenied, status.Code(err), "the administrator's address: %v", err)
		_, err = b.Walled.Identity().Erase(as, theirs.identity)
		x.Equal(codes.PermissionDenied, status.Code(err), "the administrator's identity: %v", err)

		// And each is still there.
		_, err = b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{Ref: theirs.email}.Build())
		x.NoError(err)
		_, err = b.Ungated.Identity().Get(ctx, app.IdentityGetRequest_builder{Ref: theirs.identity}.Build())
		x.NoError(err)
	})

	t.Run("of somebody who holds nothing the caller does not", func(t *testing.T) {
		x := require.New(t)
		theirs := of(low.Bytes(), "low")

		e, err := b.Walled.Email().Erase(as, theirs.email)
		x.NoError(err)
		x.True(e.GetErased())
		i, err := b.Walled.Identity().Erase(as, theirs.identity)
		x.NoError(err)
		x.True(i.GetErased())
	})

	t.Run("and their own, whatever they hold", func(t *testing.T) {
		x := require.New(t)
		theirs := of(admin.Bytes(), "admin-again")

		// The rule passes for the caller's own row, as the rule about writing
		// one does. (Being let call the method at all is the gate's.)
		own := frame.Into(ctx, frame.New(admin, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))

		e, err := b.Walled.Email().Erase(own, theirs.email)
		x.NoError(err)
		x.True(e.GetErased())
		i, err := b.Walled.Identity().Erase(own, theirs.identity)
		x.NoError(err)
		x.True(i.GetErased())
	})

	t.Run("and what is not there is erased by nobody, as it always was", func(t *testing.T) {
		x := require.New(t)

		e, err := b.Walled.Email().Erase(as, app.EmailRef_builder{Id: admin.Bytes()}.Build())
		x.NoError(err)
		x.False(e.GetErased())
		i, err := b.Walled.Identity().Erase(as, app.IdentityRef_builder{Id: admin.Bytes()}.Build())
		x.NoError(err)
		x.False(i.GetErased())
	})
}

// TestATeamIsErasedByWhoeverCouldHaveGivenWhatItGives: erasing a team takes
// from each of its people the role they hold in it, and is held to what taking
// one of them out is.
//
// # What it was
//
// `TeamMembership.Erase` asks whether the caller may change that team and
// whether they hold what the role there gave (`takeaway.go`). `Team.Erase` was
// asked nothing -- and a team erased hands out nothing, so erasing one is
// every one of those at once. Somebody holding `Team.Erase` and nothing else
// took from a team's people what they could not have taken from one of them
// (#274).
func TestATeamIsErasedByWhoeverCouldHaveGivenWhatItGives(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	suspends := b.role(t, ctx, "suspends", app.HolderService_Disable_FullMethodName)

	team := b.team(t, ctx, seoul, "on-call")
	lead := b.holder(t, ctx, b.Contoso, "lead")
	b.inTeam(t, ctx, lead, team, suspends)

	clerk := b.holder(t, ctx, b.Contoso, "clerk")
	as := b.mayCall(t, ctx, clerk, "erases-teams", app.TeamService_Erase_FullMethodName)

	_, err := b.Walled.Team().Erase(as, app.TeamRef_builder{Id: team.Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	x.ErrorContains(err, app.HolderService_Disable_FullMethodName)

	_, err = b.Ungated.Team().Get(ctx, app.TeamGetRequest_builder{Ref: app.TeamRef_builder{Id: team.Bytes()}.Build()}.Build())
	x.NoError(err, "a team refused its erase is gone")

	// A team that gives nobody anything is theirs to erase, as the role says.
	empty := b.team(t, ctx, seoul, "nobody-yet")
	v, err := b.Walled.Team().Erase(as, app.TeamRef_builder{Id: empty.Bytes()}.Build())
	x.NoError(err)
	x.True(v.GetErased())

	// And whoever holds what the team gives, where it gives it, erases it.
	chief := b.holder(t, ctx, b.Contoso, "chief")
	wide := b.mayCall(t, ctx, chief, "erases-teams-and-suspends",
		app.TeamService_Erase_FullMethodName, app.HolderService_Disable_FullMethodName)

	v, err = b.Walled.Team().Erase(wide, app.TeamRef_builder{Id: team.Bytes()}.Build())
	x.NoError(err)
	x.True(v.GetErased())

	// Somebody bound nowhere does not see a site's team, and is answered as
	// they were: nothing erased, and nothing said of whether it is there.
	other := b.team(t, ctx, seoul, "another")
	v, err = b.Walled.Team().Erase(b.asNobody(ctx, b.holder(t, ctx, b.Contoso, "passer-by"), b.Contoso),
		app.TeamRef_builder{Id: other.Bytes()}.Build())
	x.NoError(err)
	x.False(v.GetErased())

	_, err = b.Ungated.Team().Get(ctx, app.TeamGetRequest_builder{Ref: app.TeamRef_builder{Id: other.Bytes()}.Build()}.Build())
	x.NoError(err)

	// And somebody in the site who holds nothing for the team is refused.
	seen := b.holder(t, ctx, b.Contoso, "sees-seoul")
	b.binds(t, seen, b.role(t, ctx, "reads-teams", app.TeamService_Get_FullMethodName), &seoul)
	_, err = b.Walled.Team().Erase(b.asNobody(ctx, seen, b.Contoso), app.TeamRef_builder{Id: other.Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
}

// TestWhoAGroupIsShownToIsTheTenantsToSay: a `GroupExposure` puts a group's
// name in the tokens of a relying party that grants on it, and writing or
// erasing one is held across the tenant.
//
// # What it was
//
// `group.proto` says a group reaches a client *because a tenant's
// administrator said it does*, and nothing held the write to one: a group may
// be a site's, the wall narrows its exposures with it, and somebody bound in
// that site made a group there, called it what a relying party grants on, and
// showed it to that party -- or took a group of the site out of a party's
// tokens, and with it what its people could do there. What a name means at a
// relying party is not a site's to say, as a connection or a hostname is not
// (`tenantwide.go`).
func TestWhoAGroupIsShownToIsTheTenantsToSay(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	shows := b.role(t, ctx, "shows-groups",
		app.GroupExposureService_Add_FullMethodName, app.GroupExposureService_Erase_FullMethodName)

	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		Alias:  "cluster-admins",
	}.Build())
	x.NoError(err)
	group := app.GroupRef_builder{Id: g.GetId()}.Build()

	// Somebody who holds the two writes in the group's own site, and no wider.
	local := b.holder(t, ctx, b.Contoso, "seoul-admin")
	b.binds(t, local, shows, &seoul)
	inSite := b.asNobody(ctx, local, b.Contoso)

	_, err = b.Walled.GroupExposure().Add(inSite, app.GroupExposureAddRequest_builder{Group: group, Client: "kube"}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	x.ErrorContains(err, "the whole tenant")

	// And across the tenant.
	wide := b.holder(t, ctx, b.Contoso, "tenant-admin")
	b.binds(t, wide, shows, nil)
	across := b.asNobody(ctx, wide, b.Contoso)

	shown, err := b.Walled.GroupExposure().Add(across, app.GroupExposureAddRequest_builder{Group: group, Client: "kube"}.Build())
	x.NoError(err)

	ref := app.GroupExposureRef_builder{Id: shown.GetId()}.Build()
	_, err = b.Walled.GroupExposure().Erase(inSite, ref)
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

	_, err = b.Ungated.GroupExposure().Get(ctx, app.GroupExposureGetRequest_builder{Ref: ref}.Build())
	x.NoError(err, "an exposure refused its erase is gone")

	gone, err := b.Walled.GroupExposure().Erase(across, ref)
	x.NoError(err)
	x.True(gone.GetErased())
}
