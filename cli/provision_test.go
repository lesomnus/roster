package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/z"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestProvisioningTakesNothingThatIsSomebodys is the account app's start in
// three tenants, each with something already there.
//
// fabrikam has a person called `account`, holding the staff role; contoso has
// a role called `account`, bound to its people. Provisioning took both: the
// person was answered as the internet-facing account app with everything they
// held, and the role was rewritten to the app's methods for everybody bound to
// it. And any one tenant's refusal failed the whole run -- an init container --
// which is every tenant's sign-in down.
//
// northwind is the deployment that ran the app before #76: a holder holding a
// key of the app's own from then, still valid, which nothing rotates any more.
func TestProvisioningTakesNothingThatIsSomebodys(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	s := deployment(t)

	contoso := tenantCalled(t, s, "contoso")
	fabrikam := tenantCalled(t, s, "fabrikam")
	northwind := tenantCalled(t, s, "northwind")
	for _, v := range []struct {
		t    []byte
		name string
	}{{contoso, "contoso.example"}, {fabrikam, "fabrikam.example"}, {northwind, "northwind.example"}} {
		answersAt(t, s, v.t, v.name)
	}

	in := func(t []byte) *rstr.TenantRef { return rstr.TenantRef_builder{Id: t}.Build() }
	bind := func(role, who []byte) {
		_, err := s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role: rstr.RoleRef_builder{Id: role}.Build(), Holder: rstr.HolderRef_builder{Id: who}.Build(),
		}.Build())
		x.NoError(err)
	}

	// fabrikam: a person called account.
	person, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: in(fabrikam), Alias: "account"}.Build())
	x.NoError(err)
	staff, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: in(fabrikam), Alias: "staff", Methods: []string{"/hday.oasys.*/*"}}.Build())
	x.NoError(err)
	bind(staff.GetId(), person.GetId())

	// contoso: its own role called account, bound to erin.
	erin, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: in(contoso), Alias: "erin"}.Build())
	x.NoError(err)
	theirs, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: in(contoso), Alias: "account", Methods: []string{"/roster.MeService/Get"}}.Build())
	x.NoError(err)
	bind(theirs.GetId(), erin.GetId())

	// northwind: the app's holder from before, with the key it held then.
	before, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: in(northwind), Alias: "account"}.Build())
	x.NoError(err)
	_, err = mintNamed(ctx, s.Ungated, before.GetId(), accountProvisioned, []string{"/roster.MeService/Get"}, keys.PrefixTenant)
	x.NoError(err)

	_, n, err := provisionAccount(ctx, s, "", "")
	x.NoError(err, "one tenant's row failed the run for every tenant")
	x.Equal(1, n, "fronted a tenant whose rows are somebody's")

	nominated := func(t []byte) bool {
		vs, err := s.Ungated.Nomination().List(ctx, rstr.NominationListRequest_builder{
			Filters: []*rstr.NominationFilter{rstr.NominationFilter_builder{Tenant: in(t)}.Build()},
		}.Build())
		x.NoError(err)

		return len(vs.GetItems()) > 0
	}
	x.False(nominated(fabrikam), "the app was answered as fabrikam's person")
	x.False(nominated(contoso), "the app took contoso's role")
	x.True(nominated(northwind))

	got, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref:    rstr.RoleRef_builder{Id: theirs.GetId()}.Build(),
		Select: rstr.RoleSelect_builder{Methods: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal([]string{"/roster.MeService/Get"}, got.GetMethods(), "contoso's role was rewritten to the app's")

	_, err = s.Ungated.ApiKey().Get(ctx, rstr.ApiKeyGetRequest_builder{
		Ref: rstr.ApiKeyRef_builder{Slug: rstr.ApiKeyRefBySlug_builder{
			Holder: rstr.HolderRef_builder{Id: before.GetId()}.Build(), Alias: z.Ptr(accountProvisioned),
		}.Build()}.Build(),
		Select: rstr.ApiKeySelect_builder{}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "the key the app held before #76 still opens its holder")
}
