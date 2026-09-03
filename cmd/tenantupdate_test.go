package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorEditsWhatACustomerSaysAboutItself is `Tenant.Update`: name, a
// note and labels, under the version read -- and never the alias, which every
// reference and every host resolves through.
func TestAnOperatorEditsWhatACustomerSaysAboutItself(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)
	tenants := app.NewTenantServiceClient(conn)

	tn, err := tenants.Add(as, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	ref := app.TenantRef_builder{Id: tn.GetId()}.Build()

	v, err := tenants.Update(as, app.TenantUpdateRequest_builder{
		Ref:         ref,
		DateUpdated: tn.GetDateUpdated(),
		Name:        ptr("Contoso Ltd"),
		Desc:        ptr("the first customer"),
		Labels:      map[string]string{"brand": "Contoso", "support": "help@contoso.com"},
	}.Build())
	x.NoError(err)
	x.Equal("Contoso Ltd", v.GetName())
	x.Equal("contoso", v.GetAlias(), "an update touched the alias")
	x.Equal("Contoso", v.GetLabels()["brand"])

	// The version this page read is stale now, and a second save from it is
	// refused rather than applied to whatever the row became.
	_, err = tenants.Update(as, app.TenantUpdateRequest_builder{
		Ref: ref, DateUpdated: tn.GetDateUpdated(), Name: ptr("Somebody Else"),
	}.Build())
	x.NotEqual(codes.OK, status.Code(err), "a stale version was applied")

	// Leaving labels out leaves them as they are; naming a name alone changes
	// the name alone.
	w, err := tenants.Update(as, app.TenantUpdateRequest_builder{
		Ref: ref, DateUpdated: v.GetDateUpdated(), Name: ptr("Contoso Limited"),
	}.Build())
	x.NoError(err)
	x.Equal("Contoso Limited", w.GetName())
	x.Equal("Contoso", w.GetLabels()["brand"], "labels were dropped by an update that did not name them")
	x.Equal("the first customer", w.GetDesc())
}
