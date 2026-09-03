package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorSeesWhatSomebodyReaches is `Holder.Reaches`, the read the
// console's access panel draws beside a person: what they may call, as the
// gate would decide it, added up the one way the gate adds it up.
//
// Three paths into the union, one each: a binding written to the person, a
// role held in a team, and a binding written to a group they are in -- and a
// site-scoped one among them, so `sites` and `every_site` are both exercised.
// Then somebody with nothing, whose answer is empty rather than absent. All on
// the admin port, whose rules read the operator from the control plane and, for
// this one question, the person from the data plane (`cmd/admin.go`,
// `adminRules`).
//
// Beside it, the filters this panel lists by, which grew for it: sites by
// tenant, bindings by holder, team memberships by holder.
func TestAnOperatorSeesWhatSomebodyReaches(t *testing.T) {
	const (
		listPeople  = "/roster.HolderService/List"
		erasePeople = "/roster.HolderService/Erase"
		listSites   = "/roster.SiteService/List"
	)

	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)

	tenants := app.NewTenantServiceClient(conn)
	holders := app.NewHolderServiceClient(conn)
	sites := app.NewSiteServiceClient(conn)
	roles := app.NewRoleServiceClient(conn)
	bindings := app.NewBindingServiceClient(conn)
	teams := app.NewTeamServiceClient(conn)
	teamMemberships := app.NewTeamMembershipServiceClient(conn)
	groups := app.NewGroupServiceClient(conn)
	groupMemberships := app.NewGroupMembershipServiceClient(conn)

	tn, err := tenants.Add(as, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: tn.GetId()}.Build()

	other, err := tenants.Add(as, app.TenantAddRequest_builder{Alias: "fabrikam"}.Build())
	x.NoError(err)

	alice, err := holders.Add(as, app.HolderAddRequest_builder{Tenant: at, Alias: "alice"}.Build())
	x.NoError(err)
	her := app.HolderRef_builder{Id: alice.GetId()}.Build()

	// A site, and a second tenant's site, so the filter has something to leave out.
	seoul, err := sites.Add(as, app.SiteAddRequest_builder{Tenant: at, Alias: "seoul"}.Build())
	x.NoError(err)
	_, err = sites.Add(as, app.SiteAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(), Alias: "elsewhere",
	}.Build())
	x.NoError(err)
	inSeoul := app.SiteRef_builder{Id: seoul.GetId()}.Build()

	// Path one: a binding written to her, tenant-wide.
	reader, err := roles.Add(as, app.RoleAddRequest_builder{Tenant: at, Alias: "reader", Methods: []string{listPeople}}.Build())
	x.NoError(err)
	_, err = bindings.Add(as, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: reader.GetId()}.Build(), Holder: her,
	}.Build())
	x.NoError(err)

	// Path two: a role held in a team, which is in a site.
	lead, err := roles.Add(as, app.RoleAddRequest_builder{Tenant: at, Alias: "lead", Site: inSeoul, Methods: []string{erasePeople}}.Build())
	x.NoError(err)
	team, err := teams.Add(as, app.TeamAddRequest_builder{Tenant: at, Site: inSeoul, Alias: "platform"}.Build())
	x.NoError(err)
	_, err = teamMemberships.Add(as, app.TeamMembershipAddRequest_builder{
		Holder: her,
		Team:   app.TeamRef_builder{Id: team.GetId()}.Build(),
		Role:   app.RoleRef_builder{Id: lead.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// Path three: a binding written to a group she is in.
	surveyor, err := roles.Add(as, app.RoleAddRequest_builder{Tenant: at, Alias: "surveyor", Methods: []string{listSites}}.Build())
	x.NoError(err)
	group, err := groups.Add(as, app.GroupAddRequest_builder{Tenant: at, Alias: "everyone"}.Build())
	x.NoError(err)
	_, err = bindings.Add(as, app.BindingAddRequest_builder{
		Role:  app.RoleRef_builder{Id: surveyor.GetId()}.Build(),
		Group: app.GroupRef_builder{Id: group.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = groupMemberships.Add(as, app.GroupMembershipAddRequest_builder{
		Holder: her, Group: app.GroupRef_builder{Id: group.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	t.Run("the union, by every path, as patterns", func(t *testing.T) {
		x := require.New(t)

		v, err := holders.Reaches(as, app.HolderReachesRequest_builder{Ref: her}.Build())
		x.NoError(err)
		x.ElementsMatch([]string{listPeople, erasePeople, listSites}, v.GetMethods())

		// A tenant-wide binding is the whole width, and the team's site is
		// named beside it.
		x.True(v.GetEverySite(), "a binding with no site did not reach the whole tenant")
		x.Equal([][]byte{seoul.GetId()}, v.GetSites())
	})

	t.Run("and nothing is an answer, not an error", func(t *testing.T) {
		x := require.New(t)

		bob, err := holders.Add(as, app.HolderAddRequest_builder{Tenant: at, Alias: "bob"}.Build())
		x.NoError(err)

		v, err := holders.Reaches(as, app.HolderReachesRequest_builder{
			Ref: app.HolderRef_builder{Id: bob.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		x.Empty(v.GetMethods())
		x.Empty(v.GetSites())
		x.False(v.GetEverySite())
	})

	t.Run("the lists the panel draws are narrowed by what it asks for", func(t *testing.T) {
		x := require.New(t)

		vs, err := sites.List(as, app.SiteListRequest_builder{
			Filters: []*app.SiteFilter{app.SiteFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1, "a site of another tenant's was listed under this one")
		x.Equal("seoul", vs.GetItems()[0].GetAlias())

		bs, err := bindings.List(as, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: her}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(bs.GetItems(), 1, "a binding written to the group was listed as hers, or hers was not")

		// A listed row names its edges by identifier, which is what a panel
		// draws them from (and looks the alias up in the store); a list that
		// dropped them would be a table of blanks.
		x.Equal(reader.GetId(), bs.GetItems()[0].GetRole().GetId(), "the binding does not say which role")
		x.Equal(alice.GetId(), bs.GetItems()[0].GetHolder().GetId(), "the binding does not say whose")

		ts, err := teamMemberships.List(as, app.TeamMembershipListRequest_builder{
			Filters: []*app.TeamMembershipFilter{app.TeamMembershipFilter_builder{Holder: her}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(ts.GetItems(), 1)
		x.Equal(lead.GetId(), ts.GetItems()[0].GetRole().GetId(), "the membership does not say which role")
		x.Equal(team.GetId(), ts.GetItems()[0].GetTeam().GetId(), "the membership does not say which team")
	})
}
