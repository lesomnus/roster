package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestAReadOfWhatWasWrittenAboutSomebodyIsFiledUnderTheirTenant: what an
// operator reads of a customer's key, or of a grant made to one of its people,
// is on that customer's record.
//
// # What it was
//
// A row's tenant is read off the answer, and where the answer says none the row
// is filed under the reader's: so `cmd/record.go` reads it off the row instead,
// by its key (#280). It read it for the four kinds that held somebody's
// personal data when it was written -- a person, an address, an account at a
// provider, a number.
//
// The same part declared five more: what a key is called and its note, what a
// credential is called, and the labels of a binding, a relation and a
// nomination are words somebody wrote about a person, and a read of one is a
// row of the record from then on. Those five were not looked up. So an
// operator who read a customer's key with a `select` that left its holder out
// was filed under the operators' own tenant: the misfiling the lookup was
// written to end, back for exactly the kinds that had just begun to be
// recorded.
//
// A key and a credential say whose they are by their holder, a binding by its
// role, and a relation and a nomination by a column of their own.
func TestAReadOfWhatWasWrittenAboutSomebodyIsFiledUnderTheirTenant(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)
	who := adminCustomer(t, conn, as, "newco")

	owner := s.Control.Ent.Tenant.Query().OnlyIdX(ctx)
	newco := s.Ent.Holder.GetX(ctx, mustId(t, who).Uuid()).TenantId
	x.NotEqual(owner, newco)

	t.Run("a key, which says whose it is by its holder", func(t *testing.T) {
		x := require.New(t)
		K := app.NewApiKeyServiceClient(conn)

		issued, err := K.Issue(as, app.ApiKeyIssueRequest_builder{
			Holder: app.HolderRef_builder{Id: who}.Build(), Alias: "laptop",
			Methods: []string{app.MeService_Get_FullMethodName},
		}.Build())
		x.NoError(err)
		filed(t, ctx, s, "")

		v, err := K.Get(as, app.ApiKeyGetRequest_builder{
			Ref:    app.ApiKeyRef_builder{Id: issued.GetKey().GetId()}.Build(),
			Select: app.ApiKeySelect_builder{Alias: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("laptop", v.GetAlias())
		x.False(v.HasHolder(), "the answer says whose it is, so this asks nothing")

		rs := filed(t, ctx, s, app.ApiKeyService_Get_FullMethodName)
		x.Len(rs, 1)
		x.Equal(newco, rs[0].TenantId, "filed under the operators' own tenant, where no review by customer looks")
		x.Equal(owner, rs[0].ActorTenantId)
	})

	t.Run("a binding, which says whose it is by its role", func(t *testing.T) {
		x := require.New(t)
		B := app.NewBindingServiceClient(conn)

		role, err := app.NewRoleServiceClient(conn).Add(as, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: newco[:]}.Build(), Alias: "reader",
			Methods: []string{app.HolderService_List_FullMethodName},
		}.Build())
		x.NoError(err)
		bound, err := B.Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: who}.Build(),
			Labels: map[string]string{"why": "asked on the second"},
		}.Build())
		x.NoError(err)
		filed(t, ctx, s, "")

		v, err := B.Get(as, app.BindingGetRequest_builder{
			Ref:    app.BindingRef_builder{Id: bound.GetId()}.Build(),
			Select: app.BindingSelect_builder{Labels: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("asked on the second", v.GetLabels()["why"])

		rs := filed(t, ctx, s, app.BindingService_Get_FullMethodName)
		x.Len(rs, 1)
		x.Equal(newco, rs[0].TenantId, "filed under the operators' own tenant, where no review by customer looks")
	})
}
