package login_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestSomebodyTheDoorMayNotLinkIsToldWhoCan: under `expected` and `enrolling`
// somebody an operator entered is matched by their address at their first
// sign-in and linked by the front door's own `IdentityService/Add`, which is
// held to the rule every way in is: nobody writes one for somebody who holds
// more than they do. A front door holds little, so anybody entered and given a
// role -- every administrator an operator invites -- was refused the link. The
// refusal came back as a failure of the app: `502`, *this login is not
// working*, with an error in the log, at every try, and nothing on the page or
// in the docs to say what would end it.
//
// The rule stays; a front door that linked whoever it matched would be a way
// to become anybody whose address a directory will vouch for. What changes is
// the answer. It is a refusal and not a fault: the person is told, where a
// stranger is told they were not invited, that an administrator has to link
// the account, and once one has they sign in.
func TestSomebodyTheDoorMayNotLinkIsToldWhoCan(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	p := idptest.New(t, "login-app")
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Expected() })
	d.connect(t, p, "entra")

	// Entered with an address, and bound one method no front door is.
	dana := d.entered(t, "dana", "dana@contoso.example")
	tn, err := d.s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)
	role, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "reads-people",
		Methods: []string{rstr.HolderService_List_FullMethodName},
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: dana.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	p.Subject = "dana-at-entra"
	p.Claims = map[string]any{"email": "dana@contoso.example", "email_verified": true}

	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "entra")
	x.Equal(http.StatusForbidden, res.StatusCode, "a refusal was answered as the app's own failure")
	said, err := io.ReadAll(res.Body)
	x.NoError(err)
	x.Contains(string(said), "an administrator", "the person is not told who can link them")

	_, told := d.hydra.said()
	x.False(told, "hydra was told about a sign-in that was refused")
	ids, err := d.s.Ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: rstr.HolderRef_builder{Id: dana.Bytes()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	x.Empty(ids.GetItems(), "the front door wrote a way in for somebody who holds more than it")

	// An administrator links them, with what the log said, and they arrive.
	_, err = d.s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: dana.Bytes()}.Build(), Provider: "entra", Subject: "dana-at-entra",
	}.Build())
	x.NoError(err)

	d.hydra.raise("c2", "contoso-web")
	res = d.through(t, d.browser(t), "c2", "entra")
	x.Equal(http.StatusSeeOther, res.StatusCode)
	subject, _ := d.hydra.told()
	x.Equal(dana.String(), subject)
}
