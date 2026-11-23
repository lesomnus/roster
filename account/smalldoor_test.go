package account_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestWhatAStrangerIsToldOfATenant: `/providers` is what a sign-in page draws,
// for anybody, and says of the tenant what the page draws: its alias and its
// name.
//
// It answered the tenant's labels beside them. Labels are where a deployment
// puts what the schema does not name -- a plan, a billing reference, the file
// that declared the row (`roster.declared`) -- and the page reads none of them
// (#277).
func TestWhatAStrangerIsToldOfATenant(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	ctx := t.Context()

	ref := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()
	got, err := d.ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = d.ungated.Tenant().Patch(ctx, rstr.TenantPatchRequest_builder{
		Ref: ref, Labels: map[string]string{"billing/plan": "enterprise"}, DateUpdated: got.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/providers", "", nil)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"alias":"contoso"`)
	x.NotContains(body, "billing/plan")
	x.NotContains(body, "labels")
}

// TestTheAppsListsAreWhatItCalls: the two lists that say what the account
// app's key and a person's delegation may call name what is called, and no
// more.
//
// `Methods` -- what the page may ask roster as the person -- carried
// `EmailService/Claim`, under a comment saying it does not. No screen calls
// it through the page's road; for somebody whose own role names it, that road
// answered the token of a link to the browser. `Calls` carried
// `FrontService/WhereFrom`, which nothing in the app calls, in every tenant
// (#277). The app's own `/verify` goes on minting a link as itself, so the
// method stays on `Calls`.
func TestTheAppsListsAreWhatItCalls(t *testing.T) {
	x := require.New(t)

	x.NotContains(account.Methods, rstr.EmailService_Claim_FullMethodName)
	x.Contains(account.Calls, rstr.EmailService_Claim_FullMethodName)
	x.NotContains(account.Calls, rstr.FrontService_WhereFrom_FullMethodName)

	// And everything a person's delegation carries, the app's key may hand on.
	for _, m := range account.Methods {
		x.True(slices.Contains(account.Calls, m), "%s is asked as the person and the app's key does not carry it", m)
	}
}
