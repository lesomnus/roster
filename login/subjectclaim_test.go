package login_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// Entra's two names for one person: the `sub` it issues this app alone, and the
// `oid` everything else that knows them says.
const (
	erinSub = "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ"
	erinOid = "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
)

// namesBy moves contoso's connection to a claim, as the deployment does.
func (d *deployment) namesBy(t *testing.T, name, claim string) {
	t.Helper()
	x := require.New(t)

	tn, err := d.s.Ungated.Tenant().Get(t.Context(), rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)
	c, err := d.s.Ungated.Connection().Get(t.Context(), rstr.ConnectionGetRequest_builder{
		Ref: rstr.ConnectionRef_builder{At: rstr.ConnectionRefByAt_builder{
			Tenant: rstr.TenantRef_builder{Id: tn.GetId()}.Build(),
			Name:   proto.String(name),
		}.Build()}.Build(),
		Select: rstr.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Connection().Update(t.Context(), rstr.ConnectionUpdateRequest_builder{
		Ref:          rstr.ConnectionRef_builder{Id: c.GetId()}.Build(),
		DateUpdated:  c.GetDateUpdated(),
		SubjectClaim: proto.String(claim),
	}.Build())
	x.NoError(err)
}

// subjectOf is the subject of the one identity somebody has.
func (d *deployment) subjectOf(t *testing.T, who pdid.Id) string {
	t.Helper()

	vs, err := d.s.Ungated.Identity().List(t.Context(), rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{
			Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build()},
	}.Build())
	require.NoError(t, err)
	require.Len(t, vs.GetItems(), 1)

	return vs.GetItems()[0].GetSubject()
}

// TestAConnectionNamingOidMovesPeopleAtTheirNextSignIn: a connection moved to
// `oid` finds everybody who signed in before by the pairwise `sub` their
// identity was keyed by, and moves that identity to their `oid` with the token
// that carries both -- once. After that the `oid` is how they are found, and
// is what a directory provisioning them says.
func TestAConnectionNamingOidMovesPeopleAtTheirNextSignIn(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serve(t)
	erin := d.who["contoso"]

	d.connect(t, p, "entra")
	_, err := d.s.Ungated.Identity().Add(t.Context(), rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: erin.Bytes()}.Build(), Provider: "entra", Subject: erinSub,
	}.Build())
	x.NoError(err)
	d.namesBy(t, "entra", "oid")

	p.Subject = erinSub
	p.Claims = map[string]any{"oid": erinOid}

	for i, challenge := range []string{"c1", "c2"} {
		d.hydra.raise(challenge, "contoso-web")
		res := d.through(t, d.browser(t), challenge, "entra")
		x.Equal(http.StatusSeeOther, res.StatusCode, "sign-in %d", i+1)

		subject, _ := d.hydra.told()
		x.Equal(erin.String(), subject, "sign-in %d was somebody else", i+1)
		x.Equal(erinOid, d.subjectOf(t, erin), "sign-in %d left the identity where it was", i+1)
	}
}

// TestSomebodyWiderThanTheFrontDoorSignsInByTheRowTheyHad: the move is a way in
// written, and a front door may not write one into somebody who holds more
// than it does. They are signed in by the row the token's own `sub` names,
// exactly as before the connection moved, and an operator moves them.
func TestSomebodyWiderThanTheFrontDoorSignsInByTheRowTheyHad(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serve(t)
	erin := d.who["contoso"]

	tn, err := d.s.Ungated.Tenant().Get(t.Context(), rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)
	wide, err := d.s.Ungated.Role().Add(t.Context(), rstr.RoleAddRequest_builder{
		Tenant:  rstr.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "wider-than-the-door",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Binding().Add(t.Context(), rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: wide.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: erin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	d.connect(t, p, "entra")
	_, err = d.s.Ungated.Identity().Add(t.Context(), rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: erin.Bytes()}.Build(), Provider: "entra", Subject: erinSub,
	}.Build())
	x.NoError(err)
	d.namesBy(t, "entra", "oid")

	p.Subject = erinSub
	p.Claims = map[string]any{"oid": erinOid}

	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "entra")
	x.Equal(http.StatusSeeOther, res.StatusCode)

	subject, _ := d.hydra.told()
	x.Equal(erin.String(), subject)
	x.Equal(erinSub, d.subjectOf(t, erin), "the front door wrote a way into somebody wider than itself")
}

// TestATokenWithoutTheNamedClaimIsRefused: a connection naming `oid` and a token
// without one -- Entra, asked without the `profile` scope -- is a sign-in
// refused, not a sign-in by `sub`: under the other claim it is somebody else.
func TestATokenWithoutTheNamedClaimIsRefused(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serve(t)
	erin := d.who["contoso"]

	d.connect(t, p, "entra")
	_, err := d.s.Ungated.Identity().Add(t.Context(), rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: erin.Bytes()}.Build(), Provider: "entra", Subject: erinSub,
	}.Build())
	x.NoError(err)
	d.namesBy(t, "entra", "oid")

	p.Subject = erinSub

	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "entra")
	x.Equal(http.StatusBadRequest, res.StatusCode)
	x.Equal(erinSub, d.subjectOf(t, erin))
}
