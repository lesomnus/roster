package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/payday/auth"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

const getHolder = "/roster.HolderService/Get"

// binds grants a role to somebody, in a site or across the tenant.
func (b *built) binds(t *testing.T, who pdid.Id, role pdid.Id, site *pdid.Id) {
	t.Helper()

	req := app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}
	if site != nil {
		req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
	}

	_, err := b.Ungated.Binding().Add(t.Context(), req.Build())
	require.NoError(t, err)
}

// TestSomebodyWithNoBindingMayCallNothing, which is the only defensible default
// for a store of people.
//
// The alternative is that adding the first role **takes away** permissions
// everybody silently had -- a change nobody can review, because there is no
// before-state written down anywhere.
func TestSomebodyWithNoBindingMayCallNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	ctx = asOverTheWire(ctx, b.AcmeUser)

	_, err := app.NewHolderServiceClient(conn).Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
}

// TestARoleIsWhatOpensIt, and only the methods it names.
func TestARoleIsWhatOpensIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.binds(t, b.AcmeUser, b.role(t, ctx, "reader", getHolder), nil)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	ctx = asOverTheWire(ctx, b.AcmeUser)

	v, err := app.NewHolderServiceClient(conn).Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("someone", v.GetAlias())

	// And nothing else it does not name.
	_, err = app.NewHolderServiceClient(conn).Erase(ctx,
		app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
}

// TestAGroupCarriesItToo, which is what a group is for: the binding is written
// once and the membership changes.
func TestAGroupCarriesItToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:  "readers",
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:  app.RoleRef_builder{Id: b.role(t, ctx, "reader", getHolder).Bytes()}.Build(),
		Group: app.GroupRef_builder{Id: g.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	wire := asOverTheWire(ctx, b.AcmeUser)

	get := func() error {
		_, err := app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		}.Build())

		return err
	}

	// Not a member yet.
	x.Equal(codes.PermissionDenied, status.Code(get()))

	_, err = b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Group:  app.GroupRef_builder{Id: g.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	x.NoError(get())
}

// TestABindingInASiteNarrowsTheSecondAxis is what `pd.Grouped` was generated
// for and what nothing had answered until now.
//
// A binding with a site is that site and no other. A binding without one is the
// tenant's whole width. Both are read out of the same rows the permission check
// reads, which is the point: what narrows a query and what permits a call are
// one set of facts.
func TestABindingInASiteNarrowsTheSecondAxis(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	frankfurt := b.site(t, ctx, b.Acme, "frankfurt")
	b.team(t, ctx, seoul, "operators")
	b.team(t, ctx, frankfurt, "operators")

	// Bound in Seoul only.
	b.binds(t, b.AcmeUser, b.role(t, ctx, "reader", "/roster.TeamService/List"), &seoul)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	wire := asOverTheWire(ctx, b.AcmeUser)

	vs, err := app.NewTeamServiceClient(conn).List(wire, app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1, "a caller bound in one site saw another's team")

	// Somebody bound across the tenant sees both.
	other := b.holder(t, ctx, b.Acme, "wide")
	b.binds(t, other, b.role(t, ctx, "wide-reader", "/roster.TeamService/List"), nil)

	vs, err = app.NewTeamServiceClient(conn).List(asOverTheWire(ctx, other),
		app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2)
}

// TestARoleDoesNotCrossTheWall. A binding narrows within a tenant and there is
// no row that widens past one.
func TestARoleDoesNotCrossTheWall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.binds(t, b.AcmeUser, b.role(t, ctx, "reader", "/roster.TeamService/List"), nil)

	b.team(t, ctx, b.site(t, ctx, b.Acme, "ours"), "ours")
	theirs := b.team(t, ctx, b.site(t, ctx, b.Hooli, "theirs"), "theirs")

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))

	vs, err := app.NewTeamServiceClient(conn).List(asOverTheWire(ctx, b.AcmeUser),
		app.TeamListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1)

	for _, v := range vs.GetItems() {
		x.NotEqual(theirs.Bytes(), v.GetId(), "a role reached into another tenant")
	}
}

// asOverTheWire is a call made as somebody, the way `auth.Plain` takes one.
func asOverTheWire(ctx context.Context, who pdid.Id) context.Context {
	return auth.PlainProvider(who.String()).Provide(ctx)
}

const addMember = "/roster.TeamMembershipService/Add"

// TestATeamAdministratorManagesTheirOwnTeam, and no other.
//
// This is the half `gate.Policy` cannot answer. It sees the actor, their
// tenant, the actor's own row and the method -- and never what the call is
// about. So the gate lets a team's administrator through for the method at all,
// and `server/core` refuses the wrong team, and the two are one answer in two
// places.
func TestATeamAdministratorManagesTheirOwnTeam(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	mine := b.team(t, ctx, seoul, "mine")
	yours := b.team(t, ctx, seoul, "yours")

	admin := b.role(t, ctx, "team-admin", addMember)

	// Alice administers `mine`, and holds no binding at all.
	_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: admin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	wire := asOverTheWire(ctx, b.AcmeUser)

	somebody := b.holder(t, ctx, b.Acme, "newcomer")
	add := func(team pdid.Id) error {
		_, err := app.NewTeamMembershipServiceClient(conn).Add(wire,
			app.TeamMembershipAddRequest_builder{
				Holder: app.HolderRef_builder{Id: somebody.Bytes()}.Build(),
				Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
			}.Build())

		return err
	}

	// Her own team: allowed.
	x.NoError(add(mine))

	// The one next to it, in the same site and the same tenant: refused.
	err = add(yours)
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a team administrator added somebody to a team they do not administer")
}

// TestABindingReachesEveryTeam, so that what refused above was the team and not
// the method.
func TestABindingReachesEveryTeam(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	yours := b.team(t, ctx, seoul, "yours")

	b.binds(t, b.AcmeUser, b.role(t, ctx, "staffer", addMember), nil)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	somebody := b.holder(t, ctx, b.Acme, "newcomer")

	_, err := app.NewTeamMembershipServiceClient(conn).Add(asOverTheWire(ctx, b.AcmeUser),
		app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: somebody.Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: yours.Bytes()}.Build(),
		}.Build())
	x.NoError(err)
}

const (
	addBinding = "/roster.BindingService/Add"
	addRole    = "/roster.RoleService/Add"
	eraseHold  = "/roster.HolderService/Erase"
)

// TestNobodyGrantsWhatTheyDoNotHold is the hole this closes, written as the two
// RPCs it used to take.
//
// Being allowed to write bindings was being allowed everything: write a role
// holding anything, bind it to yourself, done. Two calls, from a permission an
// administrator grants without hesitating -- "Alice manages who is in what".
func TestNobodyGrantsWhatTheyDoNotHold(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Alice manages memberships, and nothing else.
	b.binds(t, b.AcmeUser, b.role(t, ctx, "manager", addBinding, addRole), nil)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	wire := asOverTheWire(ctx, b.AcmeUser)

	// She cannot write a role holding what she does not hold.
	_, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   "sneaky",
		Methods: []string{eraseHold},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she wrote a role holding what she does not")

	// Nor bind one somebody else wrote.
	theirs := b.role(t, ctx, "eraser", eraseHold)
	_, err = app.NewBindingServiceClient(conn).Add(wire, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: theirs.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she bound herself a role holding what she does not")
}

// TestWhatYouHoldYouMayPassOn, so that what refused above was the escalation
// and not the method.
func TestWhatYouHoldYouMayPassOn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.binds(t, b.AcmeUser, b.role(t, ctx, "manager", addBinding, addRole, getHolder), nil)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	wire := asOverTheWire(ctx, b.AcmeUser)

	// A role holding a subset of hers.
	v, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{getHolder},
	}.Build())
	x.NoError(err)

	// And bound to somebody else.
	other := b.holder(t, ctx, b.Acme, "newcomer")
	_, err = app.NewBindingServiceClient(conn).Add(wire, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: v.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// TestATeamRoleIsNotYoursToHandOut.
//
// What counts is what somebody holds **wide**. A role held in one team is
// scoped to that team, and binding it across the tenant would widen a scope
// rather than pass on a permission.
func TestATeamRoleIsNotYoursToHandOut(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	mine := b.team(t, ctx, seoul, "mine")

	// She may write bindings, and holds `Holder.Erase` only inside one team.
	b.binds(t, b.AcmeUser, b.role(t, ctx, "manager", addBinding), nil)

	_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: b.role(t, ctx, "team-eraser", eraseHold).Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))

	_, err = app.NewBindingServiceClient(conn).Add(asOverTheWire(ctx, b.AcmeUser),
		app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: b.role(t, ctx, "wide-eraser", eraseHold).Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a role held in one team was bound across the tenant")
}

// TestARoleMayNameAServiceOrAPackage is what a pattern buys, which is the whole
// reason it replaced a boolean.
//
// Between "one method" and "everything" there was nothing. A role meaning
// "manage holders" was eight lines that grew a ninth the day a method was
// added, and nobody noticed until somebody needed it.
func TestARoleMayNameAServiceOrAPackage(t *testing.T) {
	b, ctx := build(t)

	roleOf := func(t *testing.T, alias string, ms ...string) pdid.Id {
		t.Helper()

		v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
			Alias:   alias,
			Methods: ms,
		}.Build())
		require.NoError(t, err)

		return mustId(t, v.GetId())
	}

	t.Run("a service pattern reaches its own methods", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Acme, "holder-admin")
		b.binds(t, who, roleOf(t, "holders", "/roster.HolderService/*"), nil)

		conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
		as := asOverTheWire(ctx, who)

		_, err := app.NewHolderServiceClient(conn).List(as,
			app.HolderListRequest_builder{}.Build())
		x.NoError(err)

		// And stops at the service boundary, which is the part a list of
		// method names would have got right by accident and a glob has to get
		// right on purpose.
		_, err = app.NewTeamServiceClient(conn).List(as,
			app.TeamListRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("a method pattern reaches across services", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Acme, "reader")
		b.binds(t, who, roleOf(t, "read-only", "/roster.*/List"), nil)

		conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
		as := asOverTheWire(ctx, who)

		for _, call := range []func() error{
			func() error {
				_, err := app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
				return err
			},
			func() error {
				_, err := app.NewTeamServiceClient(conn).List(as, app.TeamListRequest_builder{}.Build())
				return err
			},
		} {
			x.NoError(call())
		}

		// A different method of a service it does reach.
		_, err := app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	// And the pattern is still only ever an attenuation of the wall: it says
	// which methods and never which tenants.
	t.Run("a package pattern does not cross the wall", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Acme, "everything-here")
		b.binds(t, who, roleOf(t, "all-of-roster", "/roster.*/*"), nil)

		conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))

		v, err := app.NewHolderServiceClient(conn).List(asOverTheWire(ctx, who),
			app.HolderListRequest_builder{}.Build())
		x.NoError(err)

		for _, h := range v.GetItems() {
			x.NotEqual(b.Hooli.Bytes(), h.GetTenant().GetId())
		}
	})
}

// TestASiteAdministratorStaysInTheirSite is the rule `role.proto` states and
// nothing enforced.
//
// The schema says it outright -- *a role in a site may only be bound in that
// site, and it is the whole of what keeps somebody who administers one site
// from writing a rule that lands outside it* -- and until this, nothing read
// that sentence. Found by an adversarial pass over something else entirely.
//
// The escalation was two RPCs, used no method the attacker did not already
// hold, and nothing refused or logged: bound to a Seoul role **in Seoul**, bind
// that same role to yourself with no site, and the second axis answers "every
// site" for you afterwards.
func TestASiteAdministratorStaysInTheirSite(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const listTeams = "/roster.TeamService/List"
	const bind = "/roster.BindingService/Add"
	const writeRole = "/roster.RoleService/Add"

	seoul, err := b.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:  "seoul",
	}.Build())
	x.NoError(err)
	site := mustId(t, seoul.GetId())

	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: seoul.GetId()}.Build(),
		Alias:   "seoul-admin",
		Methods: []string{listTeams, bind, writeRole},
	}.Build())
	x.NoError(err)
	b.binds(t, b.AcmeUser, mustId(t, r.GetId()), &site)

	as := frame.Into(ctx, frame.New(b.AcmeUser, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))

	// She really does hold those methods, in Seoul. Without this the refusals
	// below could be her holding nothing at all.
	t.Run("she may act in her own site", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Team().List(as, app.TeamListRequest_builder{}.Build())
		x.NoError(err)
	})

	t.Run("her role cannot be bound outside its site", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Binding().Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			// No site, which is the whole tenant.
		}.Build())
		x.Error(err, "a site administrator bound their role across the tenant")
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	// And the other way round: what is held in a site is not hers to write into
	// a tenant-wide role either. `bindableIn` closes the path she took; this
	// closes the question.
	t.Run("nor written into a tenant-wide role", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Role().Add(as, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
			Alias:   "mine-everywhere",
			Methods: []string{listTeams},
		}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("what she holds in a site is not what she may pass on", func(t *testing.T) {
		x := require.New(t)

		held, err := cmd.Granted(b.Ent)(ctx, b.AcmeUser)
		x.NoError(err)
		x.Empty(held, "a site-scoped binding was offered as something to grant")
	})

	// The rule is about the **role**, not about who is asking, so it holds for
	// somebody who legitimately holds everything.
	//
	// This is the case `Granted` cannot answer: a tenant operator really does
	// hold those methods tenant-wide, so `mayGrant` agrees and only the role's
	// own site refuses. Without it, the schema's rule is enforced by nobody
	// whenever the person asking is allowed to ask.
	t.Run("not even by somebody who holds the whole tenant", func(t *testing.T) {
		x := require.New(t)

		boss := b.holder(t, ctx, b.Acme, "boss")
		everywhere, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
			Alias:   "tenant-operator",
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)
		b.binds(t, boss, mustId(t, everywhere.GetId()), nil)

		theirs := frame.Into(ctx,
			frame.New(boss, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))

		// They may bind it where it belongs.
		_, err = b.Walled.Binding().Add(theirs, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Site:   app.SiteRef_builder{Id: seoul.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		// And not outside it, however much they hold.
		_, err = b.Walled.Binding().Add(theirs, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: boss.Bytes()}.Build(),
		}.Build())
		x.Error(err, "a role of one site was bound across the tenant")
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	// A role belonging to no site is this schema's ClusterRole and is bindable
	// anywhere in its tenant. Narrowing is free; widening is what needs
	// permission, and that asymmetry is the rule rather than a gap in it.
	t.Run("a role of no site is still bindable anywhere", func(t *testing.T) {
		x := require.New(t)

		w, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
			Alias:   "tenant-reader",
			Methods: []string{listTeams},
		}.Build())
		x.NoError(err)

		bob := b.holder(t, ctx, b.Acme, "bob")

		_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: w.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		// And in a site, which is narrowing.
		_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: w.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(),
			Site:   app.SiteRef_builder{Id: seoul.GetId()}.Build(),
		}.Build())
		x.NoError(err)
	})
}
