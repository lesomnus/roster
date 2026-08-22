package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// The two ways round `escalate.go` that the rule it states does not cover.
//
// Both are the same mistake in different places: a permission arrives by a
// path one reader of the rows walks and another does not, so the two answer
// differently about the same person. `cmd/policy.go` holds three such readers
// -- the gate's `of`, `Holds` and `Granted` -- and they are the ones that have
// to agree, because `server/core` asks two of them what the gate decided with
// the third.

// TestAttachingARoleIsGrantingIt.
//
// `TeamMembership.Add` names a role, and the gate unions the methods of a role
// somebody holds in a team into what they may ever call -- `policy.of` does it
// deliberately, because the gate is outermost and cannot know which team a
// call is about. So attaching a role **is** handing out its methods, and it
// was the one write that named a role and never asked `mayGrant`.
//
// What that cost is the shape `escalate.go` opens with, one service along:
//
//	Alice may call TeamMembership.Add and nothing else.
//	Alice attaches the tenant's admin role to herself, in any team.
//	Alice may now erase anybody.
//
// Two RPCs and no method she did not already hold -- from "Alice manages who
// is in what", which is the same sentence that made `Binding.Add` dangerous.
func TestAttachingARoleIsGrantingIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	mine := b.team(t, ctx, seoul, "mine")

	// Alice manages memberships across the tenant, and holds nothing else.
	b.binds(t, b.AcmeUser, b.role(t, ctx, "manager", addMember), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.AcmeUser)

	// The tenant's administrator role, which she does not hold.
	admin := b.role(t, ctx, "admin", eraseHold)

	_, err := app.NewTeamMembershipServiceClient(conn).Add(wire,
		app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
			Role:   app.RoleRef_builder{Id: admin.Bytes()}.Build(),
		}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she attached herself a role holding what she does not")
	x.Contains(status.Convert(err).Message(), eraseHold,
		"the refusal did not say which permission was the problem")

	// And the escalation the refusal exists to stop: had it been written, the
	// gate would let her through for the method the role names.
	_, err = app.NewHolderServiceClient(conn).Erase(wire,
		app.HolderRef_builder{Id: b.holder(t, ctx, b.Acme, "victim").Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she erased somebody, so the membership was written after all")
}

// TestWhatYouHoldYouMayAttach, so that what refused above was the escalation
// and not the method.
func TestWhatYouHoldYouMayAttach(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	mine := b.team(t, ctx, seoul, "mine")

	// She manages memberships and holds `Holder.Get` across the tenant.
	b.binds(t, b.AcmeUser, b.role(t, ctx, "manager", addMember, getHolder), nil)

	conn := served(t, b.Server)

	_, err := app.NewTeamMembershipServiceClient(conn).Add(asOverTheWire(ctx, b.AcmeUser),
		app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Acme, "newcomer").Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
			Role:   app.RoleRef_builder{Id: b.role(t, ctx, "reader", getHolder).Bytes()}.Build(),
		}.Build())
	x.NoError(err)
}

// TestAMembershipWithNoRoleIsStillAMembership.
//
// A membership may name no role at all -- it is how somebody is put in a team
// without being given anything -- and a check that refused those would have
// broken the thing the service is mostly used for.
func TestAMembershipWithNoRoleIsStillAMembership(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Acme, "seoul")
	mine := b.team(t, ctx, seoul, "mine")

	b.binds(t, b.AcmeUser, b.role(t, ctx, "manager", addMember), nil)

	conn := served(t, b.Server)

	_, err := app.NewTeamMembershipServiceClient(conn).Add(asOverTheWire(ctx, b.AcmeUser),
		app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Acme, "newcomer").Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
		}.Build())
	x.NoError(err)
}

// TestATeamWithNoSiteIsTheTenantsOwn.
//
// A team may sit in no site -- `team.proto` says empty is none -- and then the
// scope a role attached there is granted at is the tenant. So a caller whose
// own grant was made in a site does not cover it, which is the same asymmetry
// `mayGrant` applies to a binding written with no site.
func TestATeamWithNoSiteIsTheTenantsOwn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// A team of the tenant's own, with no site.
	v, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:  "floating",
	}.Build())
	x.NoError(err)
	mine := mustId(t, v.GetId())

	// She manages memberships across the tenant, and holds `Holder.Get` in
	// Seoul alone.
	seoul := b.site(t, ctx, b.Acme, "seoul")
	b.binds(t, b.AcmeUser, b.role(t, ctx, "manager", addMember), nil)
	b.binds(t, b.AcmeUser, b.role(t, ctx, "seoul-reader", getHolder), &seoul)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.AcmeUser)

	newcomer := b.holder(t, ctx, b.Acme, "newcomer")
	attach := func(team pdid.Id, role pdid.Id) error {
		_, err := app.NewTeamMembershipServiceClient(conn).Add(wire,
			app.TeamMembershipAddRequest_builder{
				Holder: app.HolderRef_builder{Id: newcomer.Bytes()}.Build(),
				Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
				Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
			}.Build())

		return err
	}

	reader := b.role(t, ctx, "reader", getHolder)

	// In the tenant's own team, her site-scoped grant does not reach.
	err = attach(mine, reader)
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a grant made in one site reached a team the whole tenant sees")

	// In Seoul's own team, it does.
	x.NoError(attach(b.team(t, ctx, seoul, "seoul-team"), reader))
}

// TestAPermissionHeldThroughAGroupIsStillHeld.
//
// `mayReach` reads what the **target** holds and lets the write through when
// that is nothing. So a reader of the rows that cannot see a path a permission
// arrives by does not refuse conservatively here -- it *allows*, and the
// person it allows writing is exactly the administrator the rule exists to
// protect.
//
// A group binding is such a path. The gate walks it (`policy.of`, and
// `TestAGroupCarriesItToo` pins that), and `Granted` did not, so a
// group-provisioned administrator read as holding nothing:
//
//	Ops may call Holder.List and nothing else.
//	Ops resets the password of an administrator whose role arrives by a group.
//	Ops signs in as them.
//
// The direction is what makes it worth a test of its own. The same blindness
// in `mayGrant` only ever refuses somebody who could have granted, which is a
// conversation; here it is silent.
func TestAPermissionHeldThroughAGroupIsStillHeld(t *testing.T) {
	b, ctx := build(t)

	// An administrator whose permission arrives only through a group.
	boss := b.holder(t, ctx, b.Acme, "boss")
	b.inGroup(t, ctx, boss, "admins", b.role(t, ctx, "admin", eraseHold))

	// And an operator who may only look.
	ops := b.holder(t, ctx, b.Acme, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", getHolder)

	v := b.operated()
	set := func(who pdid.Id) error {
		_, err := v.Set(asOps, app.VouchSetRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret: []byte("a new one"),
		}.Build())

		return err
	}

	t.Run("and may not be written by somebody narrower", func(t *testing.T) {
		x := require.New(t)

		err := set(boss)
		x.Equal(codes.PermissionDenied, status.Code(err),
			"an operator became a group-provisioned administrator in two operations")
		x.Contains(status.Convert(err).Message(), eraseHold)
	})

	t.Run("while somebody who really holds nothing still may be", func(t *testing.T) {
		x := require.New(t)

		x.NoError(set(b.holder(t, ctx, b.Acme, "joe")),
			"the fast path was lost, so every reset now pays for two reads")
	})
}

// TestAGroupHeldPermissionIsYoursToPassOn is the other direction of the same
// rows, and it is the reason the fix is one query rather than two answers.
//
// Somebody whose permissions arrive through a group may grant them, because
// they hold them: the gate lets them call the method on the strength of that
// binding, so a `mayGrant` that could not see it would refuse a grant the
// deployment had already decided they may make.
func TestAGroupHeldPermissionIsYoursToPassOn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.inGroup(t, ctx, b.AcmeUser, "managers",
		b.role(t, ctx, "manager", addBinding, addRole, getHolder))

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.AcmeUser)

	v, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{getHolder},
	}.Build())
	x.NoError(err)

	_, err = app.NewBindingServiceClient(conn).Add(wire, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: v.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.holder(t, ctx, b.Acme, "newcomer").Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// inGroup puts somebody in a group that holds a role, which is the other way a
// binding reaches a person.
func (b *built) inGroup(t *testing.T, ctx context.Context, who pdid.Id, alias string, role pdid.Id) {
	t.Helper()
	x := require.New(t)

	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:  app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Group: app.GroupRef_builder{Id: g.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
		Group:  app.GroupRef_builder{Id: g.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}
