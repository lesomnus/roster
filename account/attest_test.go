package account_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestTheAddressADirectoryHandedOverIsKeptHereToo is the account app's half of
// `login/provider_test.go`'s `TestTheAddressADirectoryHandedOverIsKept`: the
// same `arrives`, so what is pinned here is that this app may make the write.
//
// It could not. `Email.Attest` was in the Login App's list and not in
// `account.Calls`, which is what `roster account provision` binds and what
// these tests front two tenants on -- and `attest` takes a refusal as a
// deployment choosing not to store addresses, so every sign-in here ended
// with no address and nothing said.
func TestTheAddressADirectoryHandedOverIsKeptHereToo(t *testing.T) {
	d := serve(t, account.Invited())
	d.idp.Subject = "3001"

	signedIn := func(t *testing.T, claims map[string]any) {
		t.Helper()
		d.idp.Claims = claims
		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
		require.Equal(t, http.StatusOK, code, body)
	}
	address := func(t *testing.T) *rstr.Email {
		t.Helper()
		vs, err := d.ungated.Email().List(t.Context(), rstr.EmailListRequest_builder{
			Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{
				Holder: rstr.HolderRef_builder{Id: d.erin}.Build(),
			}.Build()},
		}.Build())
		require.NoError(t, err)
		require.Len(t, vs.GetItems(), 1, "the address the directory handed over was not written down")

		v, err := d.ungated.Email().Get(t.Context(), rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{Id: vs.GetItems()[0].GetId()}.Build(),
			Select: rstr.EmailSelect_builder{
				All:       proto.Bool(true),
				VouchedBy: rstr.IdentitySelect_builder{All: proto.Bool(true)}.Build(),
			}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}

	t.Run("unverified, so it is kept and not stamped", func(t *testing.T) {
		x := require.New(t)

		signedIn(t, map[string]any{"email": "Erin@Contoso.com"})
		v := address(t)
		x.Equal("erin@contoso.com", v.GetAddress())
		x.Equal("example", v.GetVouchedBy().GetProvider(), "the row does not say whose word it was")
		x.Nil(v.GetDateVerified())
	})

	t.Run("and stamped at the next sign-in that says it was checked", func(t *testing.T) {
		x := require.New(t)

		signedIn(t, map[string]any{"email": "erin@contoso.com", "email_verified": true})
		v := address(t)
		x.Equal("erin@contoso.com", v.GetAddress())
		x.NotNil(v.GetDateVerified(), "the directory's word that it checked was not written down")
	})
}
