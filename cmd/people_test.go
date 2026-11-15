package cmd_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// calling is a request as somebody, of this method: what a call that arrived
// at a port carries, and what the wall narrows by (`cmd.Calling`).
func (b *built) calling(ctx context.Context, who pdid.Id, method string) context.Context {
	return cmd.Calling(b.asNobody(ctx, who, b.Contoso), method)
}

// boundIn binds somebody a role of these methods in one site.
func (b *built) boundIn(t *testing.T, ctx context.Context, who pdid.Id, site pdid.Id, alias string, methods ...string) {
	t.Helper()

	v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: site.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	require.NoError(t, err)
	b.binds(t, who, mustId(t, v.GetId()), &site)
}

// TestAGrantMadeInASiteReachesThePeopleOfThatSite is the regional helpdesk.
//
// kim is bound a helpdesk's role **in Seoul**. park is of Seoul, lee of Busan,
// and choi is of Seoul and administers the tenant. kim resets park's password,
// does not see lee, and is refused choi's -- and does not say who is of Seoul.
//
// # What it was
//
// A person carries no site, and what carries none was narrowed by nothing: a
// role naming `HolderService/*`, bound in one site, listed every person of the
// tenant, read every key, and suspended its administrator (#274). Who is in a
// site was a row nothing read (`SiteMembership`).
func TestAGrantMadeInASiteReachesThePeopleOfThatSite(t *testing.T) {
	b, ctx := build(t)

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")

	park, lee, choi := b.holder(t, ctx, b.Contoso, "park"), b.holder(t, ctx, b.Contoso, "lee"), b.holder(t, ctx, b.Contoso, "choi")
	b.joins(t, ctx, park, seoul)
	b.joins(t, ctx, lee, busan)
	b.joins(t, ctx, choi, seoul)
	b.mayAnything(choi, b.Contoso)

	const (
		issue  = app.CredentialService_Issue_FullMethodName
		list   = app.HolderService_List_FullMethodName
		get    = app.HolderService_Get_FullMethodName
		update = app.HolderService_Update_FullMethodName
		add    = app.HolderService_Add_FullMethodName
		erase  = app.HolderService_Erase_FullMethodName
		mails  = app.EmailService_List_FullMethodName
		places = app.SiteMembershipService_Add_FullMethodName
		leaves = app.SiteMembershipService_Erase_FullMethodName
	)
	kim := b.holder(t, ctx, b.Contoso, "kim")
	b.boundIn(t, ctx, kim, seoul, "helpdesk", issue, list, get, update, add, erase, mails, places, leaves)

	ref := func(k pdid.Id) *app.HolderRef { return app.HolderRef_builder{Id: k.Bytes()}.Build() }
	for _, who := range []pdid.Id{park, lee, choi} {
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref(who), Address: who.String() + "@contoso.example"}.Build())
		require.NoError(t, err)
	}

	t.Run("they see the people of their site, and themselves", func(t *testing.T) {
		x := require.New(t)

		vs, err := b.Walled.Holder().List(b.calling(ctx, kim, list), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		var got []string
		for _, v := range vs.GetItems() {
			got = append(got, v.GetAlias())
		}
		slices.Sort(got)
		x.Equal([]string{"choi", "kim", "park"}, got)

		_, err = b.Walled.Holder().Get(b.calling(ctx, kim, get), app.HolderGetRequest_builder{Ref: ref(lee)}.Build())
		x.Equal(codes.NotFound, status.Code(err), "somebody of another site: %v", err)

		// And what is theirs with them.
		es, err := b.Walled.Email().List(b.calling(ctx, kim, mails), app.EmailListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(es.GetItems(), 2, "the addresses of park and choi, and not of lee")
	})

	t.Run("and write a way in for one of them, who holds no more than they do", func(t *testing.T) {
		x := require.New(t)

		v, err := b.operated().Issue(b.calling(ctx, kim, issue), app.CredentialIssueRequest_builder{Ref: ref(park)}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetSecret())

		_, err = b.operated().Issue(b.calling(ctx, kim, issue), app.CredentialIssueRequest_builder{Ref: ref(lee)}.Build())
		x.Equal(codes.NotFound, status.Code(err), "somebody of another site: %v", err)

		// Of their site, and the tenant's administrator: seen, and not theirs.
		_, err = b.operated().Issue(b.calling(ctx, kim, issue), app.CredentialIssueRequest_builder{Ref: ref(choi)}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("a person is made and erased by the tenant, and not by a site", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Holder().Add(b.calling(ctx, kim, add), app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "made-in-seoul",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.ErrorContains(err, "the whole tenant")

		_, err = b.Walled.Holder().Erase(b.calling(ctx, kim, erase), ref(park))
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		_, err = b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref(park)}.Build())
		x.NoError(err)
	})

	t.Run("and who is of a site is the tenant's to say", func(t *testing.T) {
		x := require.New(t)

		// The write that would put lee within kim's reach, by kim.
		_, err := b.Walled.SiteMembership().Add(b.calling(ctx, kim, places), app.SiteMembershipAddRequest_builder{
			Holder: ref(lee), Site: app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		}.Build())
		x.NotEqual(codes.OK, status.Code(err), "somebody bound in a site put somebody of another site into theirs")

		// Nor does kim take park out of it.
		in, err := b.Ungated.SiteMembership().List(ctx, app.SiteMembershipListRequest_builder{
			Filters: []*app.SiteMembershipFilter{app.SiteMembershipFilter_builder{Holder: ref(park)}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(in.GetItems(), 1)
		_, err = b.Walled.SiteMembership().Erase(b.calling(ctx, kim, leaves), app.SiteMembershipRef_builder{Id: in.GetItems()[0].GetId()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		// The tenant's administrator says it, and then lee is of Seoul.
		_, err = b.Walled.SiteMembership().Add(b.calling(ctx, choi, places), app.SiteMembershipAddRequest_builder{
			Holder: ref(lee), Site: app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		_, err = b.Walled.Holder().Get(b.calling(ctx, kim, get), app.HolderGetRequest_builder{Ref: ref(lee)}.Build())
		x.NoError(err)
	})
}

// TestAGrantReachesWhereItWasMadeWhateverElseIsHeld: a call is narrowed to
// where its caller holds **the method it is**.
//
// # What it was
//
// The wall asked which sites a caller's holdings reached, all of them
// together, and narrowed by nothing once anything was held across the tenant.
// So somebody bound a role that reads teams across the tenant, and another
// that erases them in Seoul, erased Busan's (#274): the reading role showed
// them the team, and the gate counts a method held anywhere.
func TestAGrantReachesWhereItWasMadeWhateverElseIsHeld(t *testing.T) {
	b, ctx := build(t)

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	ours, theirs := b.team(t, ctx, seoul, "seoul-team"), b.team(t, ctx, busan, "busan-team")

	const (
		list    = app.TeamService_List_FullMethodName
		erase   = app.TeamService_Erase_FullMethodName
		disable = app.HolderService_Disable_FullMethodName
	)
	kim := b.holder(t, ctx, b.Contoso, "kim")
	b.binds(t, kim, b.role(t, ctx, "sees-teams", list, app.TeamService_Get_FullMethodName), nil)
	b.boundIn(t, ctx, kim, seoul, "seoul-teams", erase, disable)

	lee := b.holder(t, ctx, b.Contoso, "lee")
	b.joins(t, ctx, lee, busan)

	team := func(k pdid.Id) *app.TeamRef { return app.TeamRef_builder{Id: k.Bytes()}.Build() }
	there := func(k pdid.Id) bool {
		t.Helper()
		_, err := b.Ungated.Team().Get(ctx, app.TeamGetRequest_builder{Ref: team(k)}.Build())
		if status.Code(err) == codes.NotFound {
			return false
		}
		require.NoError(t, err)

		return true
	}

	t.Run("what is held across the tenant reaches all of it", func(t *testing.T) {
		x := require.New(t)

		vs, err := b.Walled.Team().List(b.calling(ctx, kim, list), app.TeamListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 2)
	})

	t.Run("and what is held in a site reaches that site, in the same hands", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Walled.Team().Erase(b.calling(ctx, kim, erase), team(theirs))
		x.NoError(err)
		x.False(v.GetErased(), "a role held in Seoul erased a team of Busan's")
		x.True(there(theirs))

		_, err = b.Walled.Holder().Disable(b.calling(ctx, kim, disable), app.HolderDisableRequest_builder{
			Ref: app.HolderRef_builder{Id: lee.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a role held in Seoul suspended somebody of Busan: %v", err)

		v, err = b.Walled.Team().Erase(b.calling(ctx, kim, erase), team(ours))
		x.NoError(err)
		x.True(v.GetErased())
	})

	t.Run("so a tenant that wants its people seen everywhere binds the read across it", func(t *testing.T) {
		x := require.New(t)

		// kim reads people across the tenant, and suspends them in Seoul:
		// lee of Busan is seen, and is not kim's to suspend. Somebody of no
		// site at all is the same.
		const get = app.HolderService_Get_FullMethodName
		b.binds(t, kim, b.role(t, ctx, "directory", get, app.HolderService_List_FullMethodName), nil)
		nowhere := b.holder(t, ctx, b.Contoso, "of-no-site")

		for _, who := range []pdid.Id{lee, nowhere} {
			ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

			_, err := b.Walled.Holder().Get(b.calling(ctx, kim, get), app.HolderGetRequest_builder{Ref: ref}.Build())
			x.NoError(err, "a read held across the tenant did not reach them")

			_, err = b.Walled.Holder().Disable(b.calling(ctx, kim, disable), app.HolderDisableRequest_builder{Ref: ref}.Build())
			x.Equal(codes.NotFound, status.Code(err), "a write held in Seoul reached them: %v", err)
		}
	})

	t.Run("at a port as it is here", func(t *testing.T) {
		x := require.New(t)

		// The same call arriving: the interceptor says which method it is.
		conn := served(t, b.Server)
		v, err := app.NewTeamServiceClient(conn).Erase(asOverTheWire(ctx, kim), team(theirs))
		x.NoError(err)
		x.False(v.GetErased(), "a role held in Seoul erased a team of Busan's, over the wire")
		x.True(there(theirs))

		vs, err := app.NewTeamServiceClient(conn).List(asOverTheWire(ctx, kim), app.TeamListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1, "what is left of the two: read across the tenant")
	})
}

// TestTheTrailIsTheTenantsAsAWhole: a line of the trail is about whatever was
// written, in whichever site, and is read by whoever holds the read across the
// tenant.
//
// It is in no site, so it was narrowed by nothing: somebody bound in one site
// read every write of every other -- a role of another site and what it
// allows, by its id (#274).
func TestTheTrailIsTheTenantsAsAWhole(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const trail = app.AuditService_List_FullMethodName
	seoul := b.site(t, ctx, b.Contoso, "seoul")

	local := b.holder(t, ctx, b.Contoso, "local")
	b.boundIn(t, ctx, local, seoul, "seoul-auditor", trail)
	wide := b.holder(t, ctx, b.Contoso, "auditor")
	b.binds(t, wide, b.role(t, ctx, "auditor", trail), nil)

	may := func(who pdid.Id) error {
		return cmd.Policy(b.Ent).May(frame.Into(ctx, frame.New(who, b.Contoso, frame.Whole())),
			gate.Call{Actor: who, Tenant: b.Contoso, Action: trail})
	}

	err := may(local)
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	x.ErrorContains(err, "the whole tenant")

	x.NoError(may(wide))
}

// TestAPlaceInATeamIsReadWhereTheTeamIs: a `TeamMembership` names no site and
// is of a team that does, and is read where the team is -- and a team or a
// group of no site is the tenant's to make.
//
// Somebody bound in one site read who is in which team of another, holding
// which role; and made a team of no site, which they then could not see
// (#274).
func TestAPlaceInATeamIsReadWhereTheTeamIs(t *testing.T) {
	b, ctx := build(t)

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	ours, theirs := b.team(t, ctx, seoul, "seoul-team"), b.team(t, ctx, busan, "busan-team")
	role := b.role(t, ctx, "member-of-something", app.MeService_Get_FullMethodName)

	park, lee := b.holder(t, ctx, b.Contoso, "park"), b.holder(t, ctx, b.Contoso, "lee")
	b.inTeam(t, ctx, park, ours, role)
	b.inTeam(t, ctx, lee, theirs, role)

	const (
		list   = app.TeamMembershipService_List_FullMethodName
		teams  = app.TeamService_Add_FullMethodName
		groups = app.GroupService_Add_FullMethodName
	)
	kim := b.holder(t, ctx, b.Contoso, "kim")
	b.boundIn(t, ctx, kim, seoul, "seoul-admin", list, teams, groups)

	t.Run("a place in a team of another site is not read", func(t *testing.T) {
		x := require.New(t)

		vs, err := b.Walled.TeamMembership().List(b.calling(ctx, kim, list), app.TeamMembershipListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		x.Equal(ours.Bytes(), vs.GetItems()[0].GetTeam().GetId())
	})

	t.Run("and somebody's own place is theirs to read, wherever it is", func(t *testing.T) {
		x := require.New(t)

		// lee holds nothing that reads memberships anywhere: what is narrowed
		// is the reach of a grant, and never somebody's own.
		vs, err := b.Walled.TeamMembership().List(b.calling(ctx, lee, list), app.TeamMembershipListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		x.Equal(theirs.Bytes(), vs.GetItems()[0].GetTeam().GetId())
	})

	t.Run("a team or a group of no site is made across the tenant", func(t *testing.T) {
		x := require.New(t)
		at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
		in := app.SiteRef_builder{Id: seoul.Bytes()}.Build()

		_, err := b.Walled.Team().Add(b.calling(ctx, kim, teams), app.TeamAddRequest_builder{Tenant: at, Alias: "of-no-site"}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = b.Walled.Group().Add(b.calling(ctx, kim, groups), app.GroupAddRequest_builder{Tenant: at, Alias: "of-no-site"}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		// In their own site, as they were bound.
		_, err = b.Walled.Team().Add(b.calling(ctx, kim, teams), app.TeamAddRequest_builder{Tenant: at, Site: in, Alias: "of-seoul"}.Build())
		x.NoError(err)
	})
}

// TestAManagerOfAGroupReachesWhoTheyPutInIt: a manager's role, held in a
// group, reaches where the group is -- the tenant, for a group of no site --
// for what it names, which is what changes that group's members.
//
// Narrowed by the method, a manager who holds nothing else would otherwise see
// nobody to put in: the role is no binding, and names no site of its own.
func TestAManagerOfAGroupReachesWhoTheyPutInIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	g := groups{b: b, admin: b.as(ctx, b.ContosoUser, b.Contoso)}

	const join = app.GroupMembershipService_Add_FullMethodName

	G := g.add(t, "of-the-tenant")
	manager := b.holder(t, ctx, b.Contoso, "manager")
	_, err := g.join(g.admin, manager, G, g.manages(t, ctx))
	x.NoError(err)

	// Holding nothing through a binding, not even the sight of the group.
	anybody := b.holder(t, ctx, b.Contoso, "anybody")
	_, err = g.join(b.calling(ctx, manager, join), anybody, G, nil)
	x.NoError(err, "a manager who holds nothing else could not put somebody into their group")

	// And not into one they do not manage, which is the layer's as it was.
	other := g.add(t, "another")
	_, err = g.join(b.calling(ctx, manager, join), anybody, other, nil)
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
}
