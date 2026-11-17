package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// taking is what the two tests below share: a tenant with two sites, its
// administrator on the wire, and the calls that take a row away.
type taking struct {
	b     *built
	ctx   context.Context
	conn  *grpc.ClientConn
	admin context.Context

	seoul, busan pdid.Id
}

func takingAt(t *testing.T) *taking {
	t.Helper()
	b, ctx := build(t)

	k := &taking{b: b, ctx: ctx, conn: served(t, b.Server)}
	b.mayAnything(b.ContosoUser, b.Contoso)
	k.admin = asOverTheWire(ctx, b.ContosoUser)
	k.seoul, k.busan = b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")

	return k
}

// role is a role of the tenant's, of a site or of none.
func (k *taking) role(t *testing.T, alias string, site *pdid.Id, methods ...string) pdid.Id {
	t.Helper()

	req := app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: k.b.Contoso.Bytes()}.Build(), Alias: alias, Methods: methods,
	}
	if site != nil {
		req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
	}
	v, err := k.b.Ungated.Role().Add(k.ctx, req.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// bound is a binding of a role to somebody or to a group, in a site or in
// none.
func (k *taking) bound(t *testing.T, role pdid.Id, to pdid.Id, site *pdid.Id) *app.BindingRef {
	t.Helper()

	req := app.BindingAddRequest_builder{Role: roleRef(role)}
	if to.Domain() == pd.GroupDomain {
		req.Group = app.GroupRef_builder{Id: to.Bytes()}.Build()
	} else {
		req.Holder = app.HolderRef_builder{Id: to.Bytes()}.Build()
	}
	if site != nil {
		req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
	}
	v, err := k.b.Ungated.Binding().Add(k.ctx, req.Build())
	require.NoError(t, err)

	return app.BindingRef_builder{Id: v.GetId()}.Build()
}

// placed is somebody's place on a team, carrying a role.
func (k *taking) placed(t *testing.T, who, team, role pdid.Id) *app.TeamMembershipRef {
	t.Helper()

	v, err := k.b.Ungated.TeamMembership().Add(k.ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
		Role:   roleRef(role),
	}.Build())
	require.NoError(t, err)

	return app.TeamMembershipRef_builder{Id: v.GetId()}.Build()
}

// group is a group nobody manages, of a site or of none, and somebody's place
// in it.
func (k *taking) group(t *testing.T, alias string, site *pdid.Id, who pdid.Id, role *app.RoleRef) (pdid.Id, *app.GroupMembershipRef) {
	t.Helper()

	req := app.GroupAddRequest_builder{Tenant: app.TenantRef_builder{Id: k.b.Contoso.Bytes()}.Build(), Alias: alias}
	if site != nil {
		req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
	}
	g, err := k.b.Ungated.Group().Add(k.ctx, req.Build())
	require.NoError(t, err)

	m, err := k.b.Ungated.GroupMembership().Add(k.ctx, app.GroupMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Group:  app.GroupRef_builder{Id: g.GetId()}.Build(),
		Role:   role,
	}.Build())
	require.NoError(t, err)

	return mustId(t, g.GetId()), app.GroupMembershipRef_builder{Id: m.GetId()}.Build()
}

// desk is somebody bound these methods in one site, and nothing else
// anywhere, on the wire.
func (k *taking) desk(t *testing.T, alias string, site pdid.Id, methods ...string) context.Context {
	t.Helper()

	who := k.b.holder(t, k.ctx, k.b.Contoso, alias)
	k.b.boundIn(t, k.ctx, who, site, alias, methods...)

	return asOverTheWire(k.ctx, who)
}

func (k *taking) unbind(as context.Context, ref *app.BindingRef) (bool, error) {
	v, err := app.NewBindingServiceClient(k.conn).Erase(as, ref)

	return v.GetErased(), err
}

func (k *taking) displace(as context.Context, ref *app.TeamMembershipRef) (bool, error) {
	v, err := app.NewTeamMembershipServiceClient(k.conn).Erase(as, ref)

	return v.GetErased(), err
}

func (k *taking) disband(as context.Context, team pdid.Id) (bool, error) {
	v, err := app.NewTeamServiceClient(k.conn).Erase(as, app.TeamRef_builder{Id: team.Bytes()}.Build())

	return v.GetErased(), err
}

func (k *taking) takeOut(as context.Context, ref *app.GroupMembershipRef) (bool, error) {
	v, err := app.NewGroupMembershipServiceClient(k.conn).Erase(as, ref)

	return v.GetErased(), err
}

// eraseRole, eraseGroup and eraseSite are the tenant's administrator's.
func (k *taking) eraseRole(t *testing.T, role pdid.Id) {
	t.Helper()
	_, err := app.NewRoleServiceClient(k.conn).Erase(k.admin, roleRef(role))
	require.NoError(t, err)
}

func (k *taking) eraseGroup(t *testing.T, group pdid.Id) {
	t.Helper()
	_, err := app.NewGroupServiceClient(k.conn).Erase(k.admin, app.GroupRef_builder{Id: group.Bytes()}.Build())
	require.NoError(t, err)
}

func (k *taking) eraseSite(site pdid.Id) error {
	_, err := app.NewSiteServiceClient(k.conn).Erase(k.admin, app.SiteRef_builder{Id: site.Bytes()}.Build())

	return err
}

// TestWhatGrantsNothingAnyMoreIsTakenAway: a binding of a role that was
// erased, a place in a group that was, and a place on a team that was give
// nobody anything, and each is taken away by whoever may erase where it is.
//
// # What it was
//
// An erase asks what the row gave before it takes it (`takeaway.go`), and read
// that through the role or the group the row names. One erased since comes
// back as its reference alone, so the erase of such a binding, and of a place
// in such a group, answered `Unknown: not a payday identifier: is 0 bytes,
// want 16`: to the tenant's administrator, and at the box. A team somebody
// held a since-erased role in answered `NotFound: Role not found` for its own
// erase. And a site counts every binding that names it, so one whose last
// binding was of an erased role said *take them out first* for good (#279).
//
// A row that gives nothing takes nothing from anybody by going, so the rule
// about taking away has nothing to ask about it. What is left is whether its
// caller may erase there at all, which the gate and the wall answered before
// this was reached.
func TestWhatGrantsNothingAnyMoreIsTakenAway(t *testing.T) {
	k := takingAt(t)
	b, ctx := k.b, k.ctx

	const (
		unbind   = app.BindingService_Erase_FullMethodName
		displace = app.TeamMembershipService_Erase_FullMethodName
	)
	park := b.holder(t, ctx, b.Contoso, "park")

	t.Run("a binding of a role that was erased, by the tenant's administrator", func(t *testing.T) {
		x := require.New(t)
		role := k.role(t, "reads-people", nil, getHolder)
		binding := k.bound(t, role, park, nil)
		k.eraseRole(t, role)

		erased, err := k.unbind(k.admin, binding)
		x.NoError(err)
		x.True(erased)
	})

	t.Run("and at the box", func(t *testing.T) {
		x := require.New(t)
		role := k.role(t, "reads-people", nil, getHolder)
		binding := k.bound(t, role, park, nil)
		k.eraseRole(t, role)

		v, err := b.Ungated.Binding().Erase(ctx, binding)
		x.NoError(err)
		x.True(v.GetErased())
	})

	// Somebody who holds the erase in Seoul and nothing else there, and
	// somebody who holds it in Busan.
	seoulDesk := k.desk(t, "seoul-desk", k.seoul, unbind, displace)
	busanDesk := k.desk(t, "busan-desk", k.busan, unbind, displace)

	t.Run("in a site, by whoever may erase there and nobody of another", func(t *testing.T) {
		x := require.New(t)

		// A role of the site's and one of the whole tenant's, each bound
		// there and each erased since.
		for _, of := range []*pdid.Id{&k.seoul, nil} {
			role := k.role(t, "writes-people", of, app.HolderService_Update_FullMethodName)
			binding := k.bound(t, role, park, &k.seoul)
			k.eraseRole(t, role)

			_, err := k.unbind(busanDesk, binding)
			x.Equal(codes.NotFound, status.Code(err), "%v", err)

			erased, err := k.unbind(seoulDesk, binding)
			x.NoError(err, "it names what they do not hold, and it gives nobody that any more")
			x.True(erased)
		}
	})

	t.Run("a binding to a group that was erased", func(t *testing.T) {
		x := require.New(t)
		role := k.role(t, "seoul-writer", &k.seoul, app.HolderService_Update_FullMethodName)
		group, _ := k.group(t, "seoul-crew", &k.seoul, park, nil)
		binding := k.bound(t, role, group, &k.seoul)

		// While there is a group it gives everybody in it what the role
		// names, and is taken away by somebody who holds that there.
		_, err := k.unbind(seoulDesk, binding)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		k.eraseGroup(t, group)
		erased, err := k.unbind(seoulDesk, binding)
		x.NoError(err)
		x.True(erased)
	})

	t.Run("and then the site it was the last thing in", func(t *testing.T) {
		x := require.New(t)
		incheon := b.site(t, ctx, b.Contoso, "incheon")
		role := k.role(t, "incheon-reader", &incheon, getHolder)
		binding := k.bound(t, role, park, &incheon)
		k.eraseRole(t, role)

		// It is still a row that names the site, and is still counted: what
		// is in a site is taken out, and not passed over.
		err := k.eraseSite(incheon)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		x.ErrorContains(err, "a binding")

		_, err = k.unbind(k.admin, binding)
		x.NoError(err)
		x.NoError(k.eraseSite(incheon), "a site with nothing in it")
	})

	t.Run("a place in a group that was erased", func(t *testing.T) {
		x := require.New(t)
		for _, at := range []struct {
			alias string
			take  func(*app.GroupMembershipRef) (bool, error)
		}{
			{"auditors", func(ref *app.GroupMembershipRef) (bool, error) { return k.takeOut(k.admin, ref) }},
			{"at-the-box", func(ref *app.GroupMembershipRef) (bool, error) {
				v, err := b.Ungated.GroupMembership().Erase(ctx, ref)

				return v.GetErased(), err
			}},
		} {
			group, place := k.group(t, at.alias, nil, park, nil)
			k.eraseGroup(t, group)

			erased, err := at.take(place)
			x.NoError(err, at.alias)
			x.True(erased, at.alias)
		}
	})

	t.Run("a team where somebody holds a role erased since", func(t *testing.T) {
		x := require.New(t)
		team := b.team(t, ctx, k.seoul, "day-shift")
		role := k.role(t, "shift-lead", nil, getHolder)
		k.placed(t, park, team, role)
		k.eraseRole(t, role)

		erased, err := k.disband(k.admin, team)
		x.NoError(err, "a team's erase said a role is missing")
		x.True(erased)
	})

	t.Run("a place on a team that was erased", func(t *testing.T) {
		x := require.New(t)
		team := b.team(t, ctx, k.seoul, "night-shift")
		role := k.role(t, "seoul-shift-lead", &k.seoul, app.HolderService_Update_FullMethodName)
		place := k.placed(t, park, team, role)

		// While there is a team the place carries the role, and is taken
		// away by somebody who holds that there.
		_, err := k.displace(seoulDesk, place)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		_, err = k.disband(k.admin, team)
		x.NoError(err)
		erased, err := k.displace(seoulDesk, place)
		x.NoError(err)
		x.True(erased)
	})

	// An app's nomination is not a grant, and is the same shape: its erase
	// ends what the app minted as its nominee, read the nominee to do it, and
	// answered `NotFound: Holder not found` for one erased since -- who
	// answers as nobody already (`keys.signable`), so there is nothing to end.
	t.Run("a nomination of somebody who was erased", func(t *testing.T) {
		x := require.New(t)
		for _, at := range []struct {
			alias string
			take  func(*app.NominationRef) (*app.NominationEraseResponse, error)
		}{
			{"reports", func(ref *app.NominationRef) (*app.NominationEraseResponse, error) {
				return app.NewNominationServiceClient(k.conn).Erase(k.admin, ref)
			}},
			{"at-the-box", func(ref *app.NominationRef) (*app.NominationEraseResponse, error) {
				return b.Ungated.Nomination().Erase(ctx, ref)
			}},
		} {
			nominee := b.holder(t, ctx, b.Contoso, at.alias)
			n := seededNomination(t, b, nominee, at.alias)
			_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: nominee.Bytes()}.Build())
			x.NoError(err)

			v, err := at.take(app.NominationRef_builder{Id: n.GetId()}.Build())
			x.NoError(err, at.alias)
			x.True(v.GetErased(), at.alias)
		}
	})
}

// TestWhatARowGaveIsReadFromTheRowAndNotAsItsTakerSeesIt: an erase is held to
// what the row gave, and that is read from the rows the gate reads, whoever
// is erasing.
//
// # What it was
//
// It was read as the caller reads the role, and a role of the whole tenant is
// in no site: somebody who holds the erase in one site does not see it. So
// for them a binding made in their own site of such a role answered `Unknown`
// where a role erased since did, a team somebody held one in answered
// `NotFound: Role not found`, and a place on a team or a manager's place in a
// group that carried one was taken away with nothing asked -- a role that did
// not come back was read as one that gave nothing, and whether it is there is
// not what its taker can see of it (#279).
//
// Nobody takes away what they could not have given, and what was given is
// what the row hands whoever it names: asked of the rows, as what a group
// hands out already was (`cmd.Joining`).
func TestWhatARowGaveIsReadFromTheRowAndNotAsItsTakerSeesIt(t *testing.T) {
	k := takingAt(t)
	b, ctx := k.b, k.ctx

	const reads, writes = app.HolderService_Get_FullMethodName, app.HolderService_Update_FullMethodName

	// Two roles of the whole tenant, and somebody of Seoul who holds every
	// erase there and what one of the two names.
	reader, writer := k.role(t, "reader", nil, reads), k.role(t, "writer", nil, writes)
	desk := k.desk(t, "seoul-desk", k.seoul,
		app.BindingService_Erase_FullMethodName,
		app.TeamMembershipService_Erase_FullMethodName,
		app.TeamService_Erase_FullMethodName,
		app.GroupMembershipService_Erase_FullMethodName,
		reads,
	)
	park, choi := b.holder(t, ctx, b.Contoso, "park"), b.holder(t, ctx, b.Contoso, "choi")

	t.Run("a binding made in their site", func(t *testing.T) {
		x := require.New(t)

		_, err := k.unbind(desk, k.bound(t, writer, park, &k.seoul))
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.ErrorContains(err, writes)

		erased, err := k.unbind(desk, k.bound(t, reader, park, &k.seoul))
		x.NoError(err, "what they hold there")
		x.True(erased)
	})

	t.Run("a place on a team of their site", func(t *testing.T) {
		x := require.New(t)
		team := b.team(t, ctx, k.seoul, "day-shift")

		_, err := k.displace(desk, k.placed(t, park, team, writer))
		x.Equal(codes.PermissionDenied, status.Code(err), "a role its taker cannot see was read as one that gave nothing: %v", err)

		erased, err := k.displace(desk, k.placed(t, choi, team, reader))
		x.NoError(err)
		x.True(erased)
	})

	t.Run("and the team, which is each of its places at once", func(t *testing.T) {
		x := require.New(t)
		team := b.team(t, ctx, k.seoul, "night-shift")
		wide := k.placed(t, park, team, writer)
		k.placed(t, choi, team, reader)

		_, err := k.disband(desk, team)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.ErrorContains(err, writes)

		_, err = k.displace(k.admin, wide)
		x.NoError(err)
		erased, err := k.disband(desk, team)
		x.NoError(err, "a team's erase said a role is missing")
		x.True(erased)
	})

	t.Run("a manager's place in a group of their site", func(t *testing.T) {
		x := require.New(t)

		// A manager's role is held across the tenant by whoever appoints one
		// (`mayAppoint`), and taken away by whoever holds it so.
		manages := k.role(t, "group-manager", nil, app.GroupMembershipService_Add_FullMethodName)
		_, managers := k.group(t, "seoul-crew", &k.seoul, park, roleRef(manages))
		_, members := k.group(t, "seoul-staff", &k.seoul, choi, nil)

		_, err := k.takeOut(desk, managers)
		x.Equal(codes.PermissionDenied, status.Code(err), "a role its taker cannot see was read as one that gave nothing: %v", err)

		erased, err := k.takeOut(desk, members)
		x.NoError(err)
		x.True(erased)
	})
}

// TestAnOperatorTakesAwayWhatGrantsNothingToo is the first test above at the
// operators' port, where what a caller holds is the control plane's to say
// and what a customer's row gives is the data plane's (`adminRules`): a
// binding of an erased role answered `Unknown` there as well.
func TestAnOperatorTakesAwayWhatGrantsNothingToo(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)

	who := app.HolderRef_builder{Id: adminCustomer(t, conn, as, "contoso")}.Build()
	of, err := app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{
		Ref: who, Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)

	role, err := app.NewRoleServiceClient(conn).Add(as, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: of.GetTenant().GetId()}.Build(), Alias: "reads-people", Methods: []string{getHolder},
	}.Build())
	x.NoError(err)
	binding, err := app.NewBindingServiceClient(conn).Add(as, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: role.GetId()}.Build(), Holder: who,
	}.Build())
	x.NoError(err)
	_, err = app.NewRoleServiceClient(conn).Erase(as, app.RoleRef_builder{Id: role.GetId()}.Build())
	x.NoError(err)

	v, err := app.NewBindingServiceClient(conn).Erase(as, app.BindingRef_builder{Id: binding.GetId()}.Build())
	x.NoError(err)
	x.True(v.GetErased())
}
