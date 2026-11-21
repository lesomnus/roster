package login_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAFrontDoorLinksSomebodyAtTheConnectionTheirDirectoryProvisionsThrough:
// at the connection a tenant's directory provisions through, an account is
// not somebody's own to write (`TestADirectoryFindsNobodyByAnAccountTheyWroteThemselves`),
// and is still a front door's. Somebody an operator entered before the
// directory provisioned anybody arrives through that connection, is matched by
// their address, and is linked by the door that saw the provider sign them in
// -- which is how somebody a directory matched and did not make comes to be
// one it speaks for (`docs/scim.md`). The door holds the write by a binding
// across the tenant, and what it links is the provider's own word.
func TestAFrontDoorLinksSomebodyAtTheConnectionTheirDirectoryProvisionsThrough(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	p := idptest.New(t, "login-app")
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Expected() })
	d.connect(t, p, "entra")

	tn, err := d.s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)
	c, err := d.s.Ungated.Connection().Get(ctx, rstr.ConnectionGetRequest_builder{
		Ref: rstr.ConnectionRef_builder{At: rstr.ConnectionRefByAt_builder{
			Tenant: rstr.TenantRef_builder{Id: tn.GetId()}.Build(), Name: proto.String("entra"),
		}.Build()}.Build(),
		Select: rstr.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Connection().Update(ctx, rstr.ConnectionUpdateRequest_builder{
		Ref: rstr.ConnectionRef_builder{Id: c.GetId()}.Build(), DateUpdated: c.GetDateUpdated(),
		Provisions: proto.Bool(true),
	}.Build())
	x.NoError(err)

	dana := d.entered(t, "dana", "dana@contoso.example")
	p.Subject = "dana-at-entra"
	p.Claims = map[string]any{"email": "dana@contoso.example", "email_verified": true}

	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "entra")
	x.Equal(http.StatusSeeOther, res.StatusCode)

	subject, _ := d.hydra.told()
	x.Equal(dana.String(), subject)
	x.Equal("dana-at-entra", d.subjectOf(t, dana), "the door did not link the account it saw sign in")
}
