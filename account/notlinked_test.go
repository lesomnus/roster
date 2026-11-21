package account_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestSomebodyTheAccountAppMayNotLinkIsToldWhoCan is the Login App's
// `TestSomebodyTheDoorMayNotLinkIsToldWhoCan` at the other front door, which
// asks the same policy the same way (`arrives.Known`). Somebody an operator
// entered and gave a role holds more than the app does, so the app may not
// write their first link, and that came back as its own failure: `500`,
// *cannot sign in*. It is a refusal, said where a stranger is told they were
// not invited: an administrator links the account, and then they sign in.
func TestSomebodyTheAccountAppMayNotLinkIsToldWhoCan(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := serve(t, account.Expected())
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	// Entered with an address, and bound one method no front door is.
	dana, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "dana", Name: "Dana"}.Build())
	x.NoError(err)
	ref := rstr.HolderRef_builder{Id: dana.GetId()}.Build()
	_, err = d.ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{Holder: ref, Address: "dana@contoso.com"}.Build())
	x.NoError(err)
	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: at, Alias: "reads-people", Methods: []string{rstr.HolderService_List_FullMethodName},
	}.Build())
	x.NoError(err)
	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: ref,
	}.Build())
	x.NoError(err)

	d.idp.Subject = "dana-at-the-directory"
	d.idp.Claims = map[string]any{"email": "dana@contoso.com", "email_verified": true}

	code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusForbidden, code, "a refusal was answered as the app's own failure: %s", body)
	x.Contains(body, "an administrator", "the person is not told who can link them")

	ids, err := d.ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: ref}.Build()},
	}.Build())
	x.NoError(err)
	x.Empty(ids.GetItems(), "the app wrote a way in for somebody who holds more than it")

	// An administrator links them, and they arrive.
	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder: ref, Provider: "example", Subject: "dana-at-the-directory",
	}.Build())
	x.NoError(err)

	b := d.browser(t, "contoso.test")
	code, body = b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code, body)
	code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"dana"`)
}
