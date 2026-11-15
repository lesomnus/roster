package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestASiteIsErasedWhenNothingIsInIt: a role, a binding, a team and a group
// may each be a site's, and a site that still has one is not erased.
//
// # What it was
//
// `Site.Erase` asked who was erasing and nothing of the site. What was in it
// stayed, naming a site that was gone, and went on counting: somebody bound
// there kept every method the binding carried, for a place nobody could see or
// administer any more (#274). A team erased takes its roles with it now; a
// site erased took nothing.
//
// Refused rather than taken with it. Erasing what is in a site because the
// site went is a great deal to undo for one call made by mistake, and counting
// its bindings as gone would leave rows that say something nothing reads. So
// what is in it is taken out first, by whoever decides each, and the refusal
// says what there is.
func TestASiteIsErasedWhenNothingIsInIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	busan := b.site(t, ctx, b.Contoso, "busan")
	at := app.SiteRef_builder{Id: busan.Bytes()}.Build()

	admin := b.holder(t, ctx, b.Contoso, "administers")
	as := b.as(ctx, admin, b.Contoso)
	erase := func() error {
		_, err := b.Walled.Site().Erase(as, at)

		return err
	}

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: contoso, Site: at, Alias: "busan-reader", Methods: []string{getHolder},
	}.Build())
	x.NoError(err)

	who := b.holder(t, ctx, b.Contoso, "bound-there")
	binding, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Site:   at,
	}.Build())
	x.NoError(err)

	team := b.team(t, ctx, busan, "busan-ops")

	group, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: contoso, Site: at, Alias: "busan-people",
	}.Build())
	x.NoError(err)

	// What is in it, taken out one at a time: each is enough to refuse it, and
	// the refusal names what is left.
	for _, step := range []struct {
		left string
		take func() error
	}{
		{"binding", func() error {
			_, err := b.Ungated.Binding().Erase(ctx, app.BindingRef_builder{Id: binding.GetId()}.Build())

			return err
		}},
		{"role", func() error {
			_, err := b.Ungated.Role().Erase(ctx, app.RoleRef_builder{Id: role.GetId()}.Build())

			return err
		}},
		{"team", func() error {
			_, err := b.Ungated.Team().Erase(ctx, app.TeamRef_builder{Id: team.Bytes()}.Build())

			return err
		}},
		{"group", func() error {
			_, err := b.Ungated.Group().Erase(ctx, app.GroupRef_builder{Id: group.GetId()}.Build())

			return err
		}},
	} {
		err := erase()
		x.Equal(codes.FailedPrecondition, status.Code(err), "with a %s still in it: %v", step.left, err)
		x.ErrorContains(err, step.left)

		x.NoError(step.take(), step.left)
	}

	x.NoError(erase(), "a site with nothing in it")

	// And the deployment's own work is held to it the same: it is about the
	// site, and not about who asks.
	t.Run("whoever asks", func(t *testing.T) {
		x := require.New(t)

		seoul := b.site(t, ctx, b.Contoso, "seoul")
		b.team(t, ctx, seoul, "seoul-ops")

		_, err := b.Ungated.Site().Erase(ctx, app.SiteRef_builder{Id: seoul.Bytes()}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	})
}
