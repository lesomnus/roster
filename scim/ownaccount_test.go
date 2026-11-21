package scim_test

import (
	"net/http"
	"testing"

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

// TestADirectoryFindsNobodyByAnAccountTheyWroteThemselves: a directory finds
// its user by `externalId` through the identity at the connection it
// provisions through, and that identity was anybody's to write onto their own
// row, with no proof the subject is theirs (`self-service` names
// `IdentityService/Add`).
//
// # What it was
//
// Somebody who holds nothing wrote a colleague's object id onto their own row
// there. From then on they were who the directory meant by that colleague: a
// lookup by the colleague's `externalId` was answered their row, creating the
// colleague was refused as taken (`409`), and what the directory then wrote
// about the colleague -- a name, `active: false`, a place in a group it grants
// through -- was written onto them. Each answered `200`, and the colleague had
// no row at all (#278).
//
// At the connection a tenant's directory provisions through, who has an
// account is the directory's to say: it writes them as it makes people, and
// one for somebody already here takes a grant across the tenant -- a front
// door's, which saw them sign in, or an administrator's. It is not somebody's
// own to write, which their own write anywhere else still is.
func TestADirectoryFindsNobodyByAnAccountTheyWroteThemselves(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	// Another directory the tenant offers, which nothing provisions through.
	_, err := d.s.Ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
		Tenant: at, Name: "github", Issuer: "https://github.com", ClientId: "the-app",
	}.Build())
	x.NoError(err)

	// Mallory: somebody of the tenant who holds nothing, calling the one verb
	// with a key of her own -- hers to call by `self-service`, bound to nobody.
	h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "mallory", Name: "Mallory"}.Build())
	x.NoError(err)
	mallory, err := pdid.From(h.GetId())
	x.NoError(err)
	own := rstr.HolderRef_builder{Id: h.GetId()}.Build()
	key := d.mint(t, h.GetId(), "her-own", []string{rstr.IdentityService_Add_FullMethodName})

	conn, err := grpc.NewClient(d.rpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	defer conn.Close()
	as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+key)
	hers := rstr.NewClient(conn).Identity()

	// Erin's object id at the directory, which is not Mallory's.
	_, err = hers.Add(as, rstr.IdentityAddRequest_builder{Holder: own, Provider: "entra", Subject: erinOid}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "somebody wrote themselves an account at the connection their directory provisions through: %v", err)
	x.ErrorContains(err, "directory", "the refusal does not say whose these are to write")

	t.Run("so the directory's user is the one it makes", func(t *testing.T) {
		x := require.New(t)

		code, got, _ := d.call(t, d.key, http.MethodGet, filter("externalId", erinOid), nil)
		x.Equal(http.StatusOK, code)
		x.EqualValues(0, got["totalResults"], "the directory was answered somebody for an id it has made nobody by: %v", got["Resources"])

		code, got, _ = d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
		x.Equal(http.StatusCreated, code, "%v", got)
		made, _ := got["id"].(string)
		x.NotEqual(mallory.String(), made)

		code, got, _ = d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+made, map[string]any{
			"Operations": []map[string]any{{"op": "replace", "path": "active", "value": false}},
		})
		x.Equal(http.StatusOK, code, "%v", got)

		her := d.holder(t, mallory.String())
		x.Nil(her.GetDateDisabled(), "what the directory said of its own user was written onto somebody else")
		x.Empty(her.GetDirectory())
		x.Empty(her.GetProfile().GetDisplayName())
	})

	t.Run("and an account anywhere else is still somebody's own to write", func(t *testing.T) {
		_, err := hers.Add(as, rstr.IdentityAddRequest_builder{Holder: own, Provider: "github", Subject: "4242"}.Build())
		require.NoError(t, err)
	})

	t.Run("and one there for somebody already here takes a grant across the tenant", func(t *testing.T) {
		x := require.New(t)

		// Somebody bound the verb in the tenant, as a front door's holder is
		// and an administrator is.
		desk, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: "desk"}.Build())
		x.NoError(err)
		role, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
			Tenant: at, Alias: "links-people", Methods: []string{rstr.IdentityService_Add_FullMethodName},
		}.Build())
		x.NoError(err)
		_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: desk.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		theirs := metadata.AppendToOutgoingContext(ctx, "authorization",
			"Bearer "+d.mint(t, desk.GetId(), "the-desk", []string{rstr.IdentityService_Add_FullMethodName}))

		_, err = rstr.NewClient(conn).Identity().Add(theirs, rstr.IdentityAddRequest_builder{
			Holder: own, Provider: "entra", Subject: fredOid,
		}.Build())
		x.NoError(err)
	})
}
