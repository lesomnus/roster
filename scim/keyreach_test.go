package scim_test

// What a directory's key reaches, past the people it makes and signs in: what
// the rules page says a tenant gives a directory when it provisions one
// (`docs/usage/rules.md` § *What a directory may do*). Each of these is
// decided -- `HolderService/Update` asks no reach -- and was said nowhere a
// tenant reads; the tests hold what the page now says.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// aliased is somebody of the tenant by alias, by their id at the endpoint.
func (d *deployment) aliased(t *testing.T, alias string) string {
	t.Helper()
	v, err := d.s.Ungated.Holder().Get(t.Context(), rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias: proto.String(alias), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	require.NoError(t, err)
	k, err := pdid.From(v.GetId())
	require.NoError(t, err)

	return k.String()
}

// TestADirectorysKeyWritesTheProfileOfAnybodyNoFileDeclares: the key writes
// the profile of anybody of the tenant whose row no file declares -- the
// tenant's administrator among them, whom it neither made nor signs in -- and
// who they report to. A declared row it does not.
func TestADirectorysKeyWritesTheProfileOfAnybodyNoFileDeclares(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	admin := d.aliased(t, "admin")
	erin := d.someone(t, "erin", erinOid)

	code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+admin, patchOf(
		map[string]any{"op": "replace", "path": "displayName", "value": "Renamed By The Directory"},
		map[string]any{"op": "replace", "path": enterprise + ":department", "value": "Nowhere"},
		map[string]any{"op": "replace", "path": enterprise + ":manager", "value": erin},
	))
	x.Equal(http.StatusOK, code, "%v", got)
	h := d.holder(t, admin)
	x.Equal("Renamed By The Directory", h.GetProfile().GetDisplayName())
	x.Equal("Nowhere", h.GetProfile().GetDepartment())
	x.NotEmpty(d.relationsOf(t, admin), "who they report to is kept")

	door, err := d.s.Ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "login-app",
		Labels: map[string]string{cmd.Declared: "config: login"},
	}.Build())
	x.NoError(err)
	k, err := pdid.From(door.GetId())
	x.NoError(err)
	code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+k.String(), patchOf(
		map[string]any{"op": "replace", "path": "displayName", "value": "Renamed By The Directory"},
	))
	x.NotEqual(http.StatusOK, code, "a declared row is the file's: %v", got)
	x.Empty(d.holder(t, k.String()).GetProfile().GetDisplayName())
}

// TestADirectorysKeyPutsAnybodyOfTheTenantInTheGroupsItMakes: the key makes
// groups, manages the ones its holder made, puts anybody of the tenant in them
// -- the administrator among them -- and erases them with what was bound to
// them.
func TestADirectorysKeyPutsAnybodyOfTheTenantInTheGroupsItMakes(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	admin := d.aliased(t, "admin")
	erin := d.someone(t, "erin", erinOid)

	code, got := d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops Team", "externalId": opsOid})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)

	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(
		map[string]any{"op": "add", "path": "members", "value": []map[string]any{{"value": admin}, {"value": erin}}},
	))
	x.Equal(http.StatusNoContent, code, "%v", got)
	in := d.membersOf(t, id)
	x.Contains(in, "admin", "somebody it neither made nor signs in")
	x.Contains(in, "erin")

	// What an administrator binds to it goes with it.
	k, err := pdid.Parse(id)
	x.NoError(err)
	reads, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "auditor",
		Methods: []string{rstr.AuditService_List_FullMethodName},
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: reads.GetId()}.Build(), Group: rstr.GroupRef_builder{Id: k.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	who, err := pdid.Parse(erin)
	x.NoError(err)
	holds := func() []string {
		t.Helper()
		v, err := d.s.Ungated.Holder().Reaches(ctx, rstr.HolderReachesRequest_builder{
			Ref: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		return v.GetMethods()
	}
	x.Contains(holds(), rstr.AuditService_List_FullMethodName)

	code, got = d.group(t, http.MethodDelete, "/"+id, nil)
	x.Equal(http.StatusNoContent, code, "%v", got)
	code, _ = d.group(t, http.MethodGet, "/"+id, nil)
	x.Equal(http.StatusNotFound, code)
	x.NotContains(holds(), rstr.AuditService_List_FullMethodName)
}

// TestADirectorysKeyReadsEveryPersonsAddressesAccountsAndNumbers: presented
// to roster itself, which is what a key that leaked is, the key reads every
// address, every account at every provider and every number of the tenant --
// the administrator's among them.
func TestADirectorysKeyReadsEveryPersonsAddressesAccountsAndNumbers(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	admin, err := pdid.Parse(d.aliased(t, "admin"))
	x.NoError(err)
	of := rstr.HolderRef_builder{Id: admin.Bytes()}.Build()

	_, err = d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{Holder: of, Address: "admin@contoso.example"}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{Holder: of, Provider: "github", Subject: "4242"}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Phone().Add(ctx, rstr.PhoneAddRequest_builder{Holder: of, Number: "+82 2-123-4567", Kind: "work"}.Build())
	x.NoError(err)

	conn, err := grpc.NewClient(d.rpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })
	cl := rstr.NewClient(conn)
	as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+d.key)

	es, err := cl.Email().List(as, rstr.EmailListRequest_builder{}.Build())
	x.NoError(err)
	addresses := []string{}
	for _, v := range es.GetItems() {
		addresses = append(addresses, v.GetAddress())
	}
	x.Contains(addresses, "admin@contoso.example")

	is, err := cl.Identity().List(as, rstr.IdentityListRequest_builder{}.Build())
	x.NoError(err)
	subjects := []string{}
	for _, v := range is.GetItems() {
		subjects = append(subjects, v.GetProvider()+":"+v.GetSubject())
	}
	x.Contains(subjects, "github:4242")

	ps, err := cl.Phone().List(as, rstr.PhoneListRequest_builder{
		Filters: []*rstr.PhoneFilter{rstr.PhoneFilter_builder{Holder: of}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(ps.GetItems(), 1)
}

// TestTheDirectoryIsWhoeverHoldsItsVerbs: nothing ties what a directory may do
// to the key `roster scim provision` minted. Another holder bound the same
// role, with a key of the same list, makes people and suspends one the
// directory made. The one thing it may not is change a group the directory's
// holder made, since only whoever made a group manages it.
func TestTheDirectoryIsWhoeverHoldsItsVerbs(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "other"}.Build())
	x.NoError(err)
	role, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: at, Alias: "other", Methods: scim.Bound()}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	other := d.mint(t, h.GetId(), "other", scim.Methods)

	made := d.someone(t, "erin", erinOid)
	code, got, _ := d.call(t, other, http.MethodPatch, scim.Base+"/Users/"+made, patchOf(
		map[string]any{"op": "replace", "path": "active", "value": false},
	))
	x.Equal(http.StatusOK, code, "%v", got)
	x.NotNil(d.holder(t, made).GetDateDisabled())

	body := erin()
	body["userName"], body["externalId"] = "fred@contoso.example", fredOid
	body["emails"] = []map[string]any{{"primary": true, "type": "work", "value": "fred@contoso.example"}}
	code, got, _ = d.call(t, other, http.MethodPost, scim.Base+"/Users", body)
	x.Equal(http.StatusCreated, code, "%v", got)

	code, got = d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops Team", "externalId": opsOid})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	code, got, _ = d.call(t, other, http.MethodPatch, scim.Base+"/Groups/"+id, patchOf(
		map[string]any{"op": "replace", "path": "displayName", "value": "Theirs"},
	))
	x.Equal(http.StatusForbidden, code, "%v", got)
}
