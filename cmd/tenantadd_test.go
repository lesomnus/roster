package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// TestATenantIsMadeWithSomebodyWhoCanAdministerIt is #32 § B.
//
// A customer was four writes, all of them a roster operator's, and what the
// four left between the first and the last was a tenant nobody could do
// anything in. The only way to finish one was the operator reaching inside it
// through the port that waives two rules (#26). `Tenant.Add` is the act now.
func TestATenantIsMadeWithSomebodyWhoCanAdministerIt(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t)

	tn, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{
		Alias: "newco",
		Name:  "Newco Ltd",
	}.Build())
	x.NoError(err)
	x.Equal("newco", tn.GetAlias())

	// Named rather than answered with: a caller that has just made a tenant
	// knows both halves of `@newco/admin`, so there is nothing for the verb
	// that answers with a row to carry.
	_, err = b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(core.Administers),
				Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err, "a tenant was made with nobody who could administer it")

	t.Run("and the role it wrote is the pattern, bound to them", func(t *testing.T) {
		x := require.New(t)

		r, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref: app.RoleRef_builder{
				Slug: app.RoleRefBySlug_builder{
					Alias:  z.Ptr(core.Everyverb),
					Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal([]string{core.EveryMethod}, r.GetMethods())

		vs, err := b.Ungated.Binding().List(ctx, app.BindingListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems(), "the role it wrote is bound to nobody")
	})

	// The way in is the verb that answers with a secret, and it needs nothing
	// this one did not already say.
	t.Run("and a password is the call beside it", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
			Holder: app.HolderRef_builder{
				Slug: app.HolderRefBySlug_builder{
					Alias:  z.Ptr(core.Administers),
					Tenant: app.TenantRef_builder{Alias: z.Ptr("newco")}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEmpty(res.GetSecret())
	})

	// The transaction: the tenant is written first, so a refusal after it is
	// what would leave a tenant nobody can get into -- the shape `roster init`
	// was in before #20.
	//
	// Tested by a refusal that comes after a write. This asked again for
	// `newco`, which is refused at the first write, the tenant's own alias:
	// nothing had been written for a transaction to take back, so it passed
	// with the five made one at a time as well -- which they were, at the
	// operators' port (#279; `TestATenantMadeAtTheOperatorsPortIsWholeOrNotThere`).
	// Somebody who holds nothing is refused at the second, the tenant's
	// `admin`, once the tenant is written; and that it was written is counted,
	// so that a rule which one day refuses them sooner fails this rather than
	// emptying it again.
	t.Run("and a refusal after the first write leaves nothing", func(t *testing.T) {
		x := require.New(t)

		written := 0
		b.Ent.Tenant.Use(func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				if m.Op().Is(ent.OpCreate) {
					written++
				}

				return next.Mutate(ctx, m)
			})
		})

		_, err := b.Ungated.Tenant().Add(b.asNobody(ctx, b.ContosoUser, b.Contoso), app.TenantAddRequest_builder{
			Alias: "halfco",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.Equal(1, written, "refused before the tenant was written, which asks nothing of a transaction")

		there, people, roles, bindings := whatIsOf(t, ctx, b.Ent, "halfco")
		x.False(there, "a refusal after the tenant was written left it: %d person(s), %d role(s), %d binding(s)", people, roles, bindings)

		// And nothing is in the way of making it.
		_, err = b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "halfco"}.Build())
		x.NoError(err)
	})
}

// TestTheControlPlaneMakesItsOwnFirstHolder is the other half of the same
// decision: there, the one tenant is the deployment itself and its first holder
// is `roster init`'s, named by `--operator` and given a password in one act.
func TestTheControlPlaneMakesItsOwnFirstHolder(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, _ := inited(t)

	vs, err := s.Control.Ent.Holder.Query().All(ctx)
	x.NoError(err)
	x.Len(vs, 1, "the control plane's tenant was given an administrator of its own")
	x.Equal("admin", vs[0].Alias)
}
