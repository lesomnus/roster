package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/me"
)

// connected is a provider one of the tenant's front doors signs people in
// through: a connection of that name, which is what makes an identity at it a
// way in.
func (b *built) connected(t *testing.T, ctx context.Context, in pdid.Id, names ...string) {
	t.Helper()

	for _, name := range names {
		_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(), Name: name,
			Issuer: "https://" + name + ".example.test", ClientId: "the-app",
		}.Build())
		require.NoError(t, err)
	}
}

// TestALastWayInIsOneThatWorks: *nobody removes their last way in* counted
// what would be left, and counted rows rather than ways in. Any other identity
// was one, whatever its provider -- a connection since erased, a name nobody
// ever connected -- and so was a password row in a tenant that takes no
// passwords, which `Vouch.Verify` refuses. Each erase went through, and left
// somebody holding nothing they could sign in by: the state the rule is there
// to refuse, reached by a count that was right about the wrong thing.
//
// What is left has to be a way in that works: an identity at a provider the
// tenant has a connection to, or a password where the tenant takes one
// (`config.password`).
func TestALastWayInIsOneThatWorks(t *testing.T) {
	b, ctx := build(t)
	b.connected(t, ctx, b.Contoso, "entra", "github")

	// As the person, which is who the rule is about; and by the button on
	// their own page, which ends at the same rule.
	erase := func(who pdid.Id, v *app.Identity) error {
		t.Helper()
		_, err := b.Walled.Identity().Erase(b.asNobody(ctx, who, b.Contoso), v.Ref())

		return err
	}
	unlink := func(who pdid.Id, v *app.Identity) error {
		t.Helper()
		s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))
		_, err := s.Unlink(b.asNobody(ctx, who, b.Contoso), app.MeUnlinkRequest_builder{Id: v.GetId()}.Build())

		return err
	}
	left := func(who pdid.Id) int {
		t.Helper()
		vs, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{
				Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build()},
		}.Build())
		require.NoError(t, err)

		return len(vs.GetItems())
	}

	t.Run("an identity at a provider no connection has is not one", func(t *testing.T) {
		x := require.New(t)
		who := b.holder(t, ctx, b.Contoso, "two-rows")
		live := b.identity(t, ctx, who, "entra", "sub-1")
		b.identity(t, ctx, who, "okta-we-left", "00u1")

		err := erase(who, live)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		err = unlink(who, live)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		x.Equal(2, left(who))
	})

	t.Run("nor one at a connection that was erased", func(t *testing.T) {
		x := require.New(t)
		b.connected(t, ctx, b.Contoso, "okta")
		who := b.holder(t, ctx, b.Contoso, "outlived")
		live := b.identity(t, ctx, who, "entra", "sub-2")
		b.identity(t, ctx, who, "okta", "00u2")

		c := b.connectionOf(t, ctx, b.Contoso, "okta")
		_, err := b.Ungated.Connection().Erase(ctx, app.ConnectionRef_builder{Id: c.GetId()}.Build())
		x.NoError(err)

		err = erase(who, live)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		x.Equal(2, left(who))
	})

	t.Run("one at another connection is", func(t *testing.T) {
		x := require.New(t)
		who := b.holder(t, ctx, b.Contoso, "two-doors")
		live := b.identity(t, ctx, who, "entra", "sub-3")
		b.identity(t, ctx, who, "github", "4242")

		x.NoError(erase(who, live))
		x.Equal(1, left(who))
	})

	t.Run("and the row that is no way in is theirs to remove", func(t *testing.T) {
		x := require.New(t)
		who := b.holder(t, ctx, b.Contoso, "tidying")
		b.identity(t, ctx, who, "entra", "sub-4")
		dead := b.identity(t, ctx, who, "okta-we-left", "00u4")

		x.NoError(erase(who, dead))
		x.Equal(1, left(who))
	})

	t.Run("a password is one where the tenant takes passwords", func(t *testing.T) {
		x := require.New(t)
		who := b.holder(t, ctx, b.Contoso, "password-on")
		only := b.identity(t, ctx, who, "entra", "sub-5")
		b.sets(t, ctx, who, "correct horse battery staple")

		x.NoError(erase(who, only))
	})

	t.Run("and not where it does not", func(t *testing.T) {
		x := require.New(t)
		at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
		tn, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
		x.NoError(err)
		_, err = b.Ungated.Tenant().Update(ctx, app.TenantUpdateRequest_builder{
			Ref: at, DateUpdated: tn.GetDateUpdated(),
			Config: app.TenantConfig_builder{Password: z.Ptr(false)}.Build(),
		}.Build())
		x.NoError(err)

		who := b.holder(t, ctx, b.Contoso, "password-off")
		only := b.identity(t, ctx, who, "entra", "sub-6")
		b.sets(t, ctx, who, "correct horse battery staple")

		err = erase(who, only)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		err = unlink(who, only)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		x.Equal(1, left(who))

		// The deployment is told the same: it is the person who would be left
		// outside, whoever asked.
		_, err = b.Ungated.Identity().Erase(ctx, only.Ref())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	})
}
