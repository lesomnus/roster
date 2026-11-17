package login_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAnAddressOfSomebodyWithAnotherAccountThereIsARefusal: under `expected`
// and `enrolling` a proved address names its holder to whoever arrives
// verified at it, and that holder may already sign in through the same
// provider, by another account. roster refuses the link -- a second account
// at one provider is a link that found the wrong row -- and it is right to:
// either their account at the directory was made again, or the address is
// somebody else's now, and which is not a front door's to guess.
//
// That refusal came back as the app's own failure too: `502`, *this login is
// not working*, at every try. It is a refusal about this arrival, as the one
// `TestSomebodyTheDoorMayNotLinkIsToldWhoCan` holds is: the person is told
// there is another account, and that an administrator says which is theirs.
func TestAnAddressOfSomebodyWithAnotherAccountThereIsARefusal(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	p := idptest.New(t, "login-app")
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Expected() })
	d.connect(t, p, "entra")

	// Somebody who signs in through the directory already, whose address is
	// proved.
	dana, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(), Alias: "dana", Name: "Dana",
	}.Build())
	x.NoError(err)
	ref := rstr.HolderRef_builder{Id: dana.GetId()}.Build()
	v, err := d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{Holder: ref, Address: "dana@contoso.example"}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Email().Patch(ctx, rstr.EmailPatchRequest_builder{
		Ref: rstr.EmailRef_builder{Id: v.GetId()}.Build(), DateVerified: timestamppb.Now(), DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{Holder: ref, Provider: "entra", Subject: "dana-at-entra"}.Build())
	x.NoError(err)

	// Another account at the directory, which says the same address of it.
	p.Subject = "another-at-entra"
	p.Claims = map[string]any{"email": "dana@contoso.example", "email_verified": true}

	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "entra")
	x.Equal(http.StatusForbidden, res.StatusCode, "a refusal was answered as the app's own failure")
	said, err := io.ReadAll(res.Body)
	x.NoError(err)
	x.Contains(string(said), "another account", "the person is not told why")
	x.Contains(string(said), "an administrator")

	_, told := d.hydra.said()
	x.False(told, "hydra was told about a sign-in that was refused")
	ids, err := d.s.Ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: ref}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(ids.GetItems(), 1)
	x.Equal("dana-at-entra", ids.GetItems()[0].GetSubject(), "the account they sign in by was written over")

	// And the account they had signs them in as it did.
	p.Subject = "dana-at-entra"
	d.hydra.raise("c2", "contoso-web")
	res = d.through(t, d.browser(t), "c2", "entra")
	x.Equal(http.StatusSeeOther, res.StatusCode)
}
