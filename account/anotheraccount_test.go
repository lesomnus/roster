package account_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAnAddressOfSomebodyWithAnotherAccountThereIsARefusalHereToo is the Login
// App's `TestAnAddressOfSomebodyWithAnotherAccountThereIsARefusal` at the
// other front door. Somebody arrives verified at an address that is proved to
// be a person's here, who already signs in through this provider by another
// account; roster refuses the link, and the app answered `500`, *cannot sign
// in*. It is a refusal about this arrival, and is said as one.
func TestAnAddressOfSomebodyWithAnotherAccountThereIsARefusalHereToo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := serve(t, account.Expected())
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	// Somebody who signs in through the directory already, whose address is
	// proved.
	dana, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "dana", Name: "Dana"}.Build())
	x.NoError(err)
	ref := rstr.HolderRef_builder{Id: dana.GetId()}.Build()
	proved(t, d, dana.GetId(), "dana@contoso.com")
	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder: ref, Provider: "example", Subject: "dana-at-the-directory",
	}.Build())
	x.NoError(err)

	// Another account at the directory, which says the same address of it.
	d.idp.Subject = "another-at-the-directory"
	d.idp.Claims = map[string]any{"email": "dana@contoso.com", "email_verified": true}

	code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusForbidden, code, "a refusal was answered as the app's own failure: %s", body)
	x.Contains(body, "another account", "the person is not told why")
	x.Contains(body, "an administrator")

	ids, err := d.ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: ref}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(ids.GetItems(), 1)
	x.Equal("dana-at-the-directory", ids.GetItems()[0].GetSubject(), "the account they sign in by was written over")
}
