package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAnAppVouchesOnlyInThePersonsOwnTenant: the grant that vouches for an
// account is one held across the person's tenant. An app's holder of another
// tenant, bound `IdentityService/Attest` across its own, vouches for nobody
// here, and is refused as an app that does not hold it.
//
// No verb mints such a delegation today: `VouchService/Delegate` finds the
// person through the app's own wall, which holds its own tenant and no other.
// So the row is written through the unwalled server. The rule asks anyway,
// because a rule that stood on how another verb makes its rows would not be
// known to stand there by the next way a delegation comes to be made.
func TestAnAppVouchesOnlyInThePersonsOwnTenant(t *testing.T) {
	b := keyFor(t)
	ctx := t.Context()
	x := require.New(t)

	// A front door of a tenant, bound the vouching grant across that tenant,
	// and a key for it.
	door := func(tenant pdid.Id, alias string) (pdid.Id, string) {
		h := addHolder(t, ctx, b.Server, tenant, alias)
		r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(), Alias: alias,
			Methods: []string{app.IdentityService_Attest_FullMethodName},
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: h.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		token, sum, err := keys.Mint(keys.PrefixTenant)
		x.NoError(err)
		_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder: app.HolderRef_builder{Id: h.Bytes()}.Build(), Alias: alias, Secret: sum, Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)

		return h, token
	}

	erin := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	attest := func(by pdid.Id, key string, subject string) error {
		token, _, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
			Holder: erin, Issuer: by.Bytes(), Methods: []string{app.IdentityService_Attest_FullMethodName},
		})
		x.NoError(err)

		_, err = app.NewIdentityServiceClient(b.Conn).Attest(acting(ctx, key, token), app.IdentityAttestRequest_builder{
			Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(), Provider: "github", Subject: subject,
		}.Build())

		return err
	}

	theirs, theirKey := door(add(t, ctx, b.Server, "fabrikam"), "fabrikams-door")
	err := attest(theirs, theirKey, "4242")
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	x.Contains(status.Convert(err).Message(), "a front door attests")

	ours, ourKey := door(b.Contoso, "contosos-door")
	x.NoError(attest(ours, ourKey, "4242"))
}
