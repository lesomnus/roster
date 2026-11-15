package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestARoleOnATeamIsNotOneToBecomeSomebodyBy: whose credential somebody may
// write is whoever holds no more than they hold **by a binding**, where the
// binding is -- and a role they carry on a team, or in a group, is none of it.
//
// # What it was
//
// `mayReach` compared what the target holds with what the caller holds, and
// read both by the same function: everything, by any path, with a role held
// on a team counted across the whole tenant. That is the right way to read the
// **target**, where counting more refuses more. For the caller it is the
// other direction (#274):
//
//	Sam administers one site: `/roster.*/*`, bound there.
//	Sam may not write the tenant's administrator a password. Refused.
//	Sam puts himself on a team of his site, carrying his own role -- which
//	he may, holding it there.
//	Sam writes the tenant's administrator a password, signs in as them, and
//	binds himself the tenant's `everything`.
//
// And somebody who was only ever given a role on a team reached anybody's
// account at once, where the same role by a binding in the same site did not.
//
// So the caller's side is what they may pass on ([core.Granted]): bindings,
// each where it was made. It is the comparison `mayGrant` already makes, for
// the reason it gives -- a role held in one team is not one to wield across
// the tenant.
func TestARoleOnATeamIsNotOneToBecomeSomebodyBy(t *testing.T) {
	b, ctx := build(t)

	const issue = app.CredentialService_Issue_FullMethodName

	// The tenant's administrator, across it.
	chief := b.holder(t, ctx, b.Contoso, "chief")
	b.mayCall(t, ctx, chief, "chief", eraseHold, addIdentity)

	// And one site's: the same methods and the write of a way in, there.
	east := b.site(t, ctx, b.Contoso, "east")
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: east.Bytes()}.Build(),
		Alias:   "east-admin",
		Methods: []string{eraseHold, addIdentity, issue},
	}.Build())
	require.NoError(t, err)
	eastAdmin := mustId(t, role.GetId())

	sam := b.holder(t, ctx, b.Contoso, "sam")
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: eastAdmin.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: sam.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: east.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	ops := b.team(t, ctx, east, "east-ops")

	// link is somebody writing a way in onto the administrator: an account of
	// the caller's own at a provider, which is then the administrator's. A
	// provider each, since a person has one account at one.
	link := func(who pdid.Id, provider string) error {
		_, err := b.Walled.Identity().Add(b.asNobody(ctx, who, b.Contoso), app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: chief.Bytes()}.Build(),
			Provider: provider,
			Subject:  "an-account-of-the-callers-own",
		}.Build())

		return err
	}

	t.Run("a site's administrator does not reach the tenant's", func(t *testing.T) {
		x := require.New(t)

		err := link(sam, "github")
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("nor by carrying their own role onto a team of their site", func(t *testing.T) {
		x := require.New(t)

		// Which sam may do himself, holding the role there; written here as
		// the deployment, since what is asked is what it then lets him do.
		b.inTeam(t, ctx, sam, ops, eastAdmin)

		err := link(sam, "gitlab")
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a role held on a team was counted across the tenant, for whose account its holder may write a way into: %v", err)
	})

	t.Run("nor does somebody who holds it on a team alone", func(t *testing.T) {
		x := require.New(t)

		erin := b.holder(t, ctx, b.Contoso, "erin")
		b.inTeam(t, ctx, erin, ops, eastAdmin)

		err := link(erin, "google")
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and whoever holds as much across the tenant still does", func(t *testing.T) {
		x := require.New(t)

		peer := b.holder(t, ctx, b.Contoso, "peer")
		b.mayCall(t, ctx, peer, "peer", eraseHold, addIdentity)

		x.NoError(link(peer, "entra"))
	})

	// The other side is unchanged, and is the half that was right: somebody
	// who holds what they hold on a team is still somebody who holds it, to
	// whoever would write a way into their account.
	t.Run("and a role on a team still protects whoever holds it", func(t *testing.T) {
		x := require.New(t)

		lead := b.holder(t, ctx, b.Contoso, "lead")
		b.inTeam(t, ctx, lead, ops, b.role(t, ctx, "lead", eraseHold))

		desk := b.holder(t, ctx, b.Contoso, "desk")
		asDesk := b.mayCall(t, ctx, desk, "desk", addIdentity)

		_, err := b.Walled.Identity().Add(asDesk, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: lead.Bytes()}.Build(),
			Provider: "github",
			Subject:  "desk-1",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})
}
