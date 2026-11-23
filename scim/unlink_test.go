package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// TestSomebodyDoesNotTakeThemselvesOutOfTheirDirectorysHands: a directory
// speaks for whoever has an account at the connection it provisions through,
// and finds them by it. That account was theirs to take away: *unlink*, on
// their own page, which is `IdentityService/Erase` of their own row and needs
// no role bound (`self-service`).
//
// # What it was
//
// Somebody the directory made, who also had a password, unlinked the account
// it made them by (OK). From then on they were nobody the directory spoke for:
// its `active: false` on the day they left was refused, `403`, and they went
// on signing in with the password; its lookup by their `externalId` found
// nobody (#278).
//
// Who has an account there is the directory's to say, for the write that takes
// one away as for the write that adds one
// (`TestADirectoryFindsNobodyByAnAccountTheyWroteThemselves`): it takes the
// verb held across the tenant by a binding. The directory suspends and deletes
// as before, which removes no account, and an administrator still takes one
// away.
func TestSomebodyDoesNotTakeThemselvesOutOfTheirDirectorysHands(t *testing.T) {
	d := stand(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	conn, err := grpc.NewClient(d.rpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	// made is somebody the directory made, with a password beside the account
	// it made them by, so that the account is not their last way in.
	made := func(t *testing.T, name, oid string) (string, pdid.Id, *rstr.Identity) {
		t.Helper()
		x := require.New(t)

		id := d.someone(t, name, oid)
		k, err := pdid.Parse(id)
		x.NoError(err)
		ref := rstr.HolderRef_builder{Id: k.Bytes()}.Build()
		_, err = d.s.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{Holder: ref, Secret: []byte("correct horse battery staple")}.Build())
		x.NoError(err)
		ids, err := d.s.Ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
			Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: ref}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(ids.GetItems(), 1)

		return id, k, ids.GetItems()[0]
	}

	t.Run("by unlinking the account it made them by", func(t *testing.T) {
		x := require.New(t)
		id, erin, theirs := made(t, "erin", erinOid)

		// As herself, by the button on her own page.
		as := metadata.AppendToOutgoingContext(ctx, "authorization",
			"Bearer "+d.mint(t, erin.Bytes(), "her-own", []string{rstr.IdentityService_Erase_FullMethodName}))
		_, err := rstr.NewIdentityServiceClient(conn).Erase(as, rstr.IdentityRef_builder{Id: theirs.GetId()}.Build())
		// Said and gone on from, so that a run where she did says what came
		// of it as well.
		if assert.Equal(t, codes.PermissionDenied, status.Code(err), "somebody took away the account their directory finds them by: %v", err) {
			assert.ErrorContains(t, err, "directory", "the refusal does not say whose these are")
		}

		// So the day she leaves, the directory says so and is heard.
		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
			"Operations": []map[string]any{{"op": "replace", "path": "active", "value": false}},
		})
		x.Equal(http.StatusOK, code, "the directory does not speak for somebody it made: %v", got)
		x.Equal(false, got["active"])
		x.NotNil(d.holder(t, id).GetDateDisabled())

		code, got, _ = d.call(t, d.key, http.MethodGet, filter("externalId", erinOid), nil)
		x.Equal(http.StatusOK, code)
		x.EqualValues(1, got["totalResults"])

		// And deletes her, which suspends her for good and removes no account.
		code, _, _ = d.call(t, d.key, http.MethodDelete, scim.Base+"/Users/"+id, nil)
		x.Equal(http.StatusNoContent, code)
		x.Equal(rstr.DirectoryStanding_DIRECTORY_STANDING_DELETED, d.holder(t, id).GetDirectoryStanding())
	})

	t.Run("and an administrator still takes one away", func(t *testing.T) {
		x := require.New(t)
		_, _, theirs := made(t, "fred", fredOid)

		// Somebody bound the verb across the tenant.
		desk, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "desk"}.Build())
		x.NoError(err)
		role, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
			Tenant: at, Alias: "unlinks-people", Methods: []string{rstr.IdentityService_Erase_FullMethodName},
		}.Build())
		x.NoError(err)
		_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: desk.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		as := metadata.AppendToOutgoingContext(ctx, "authorization",
			"Bearer "+d.mint(t, desk.GetId(), "the-desk", []string{rstr.IdentityService_Erase_FullMethodName}))

		res, err := rstr.NewClient(conn).Identity().Erase(as, rstr.IdentityRef_builder{Id: theirs.GetId()}.Build())
		x.NoError(err)
		x.True(res.GetErased())
	})
}
