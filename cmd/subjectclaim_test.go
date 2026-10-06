package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// An Entra object id: what `oid` is, and the shape a connection naming its
// people by it holds every subject to.
const (
	erinOid = "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
	fredOid = "1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f809"
)

// connectionOf is the tenant's connection of that name, read by the deployment.
func (b *built) connectionOf(t *testing.T, ctx context.Context, in pdid.Id, name string) *app.Connection {
	t.Helper()

	v, err := b.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref: app.ConnectionRef_builder{At: app.ConnectionRefByAt_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
			Name:   z.Ptr(name),
		}.Build()}.Build(),
		Select: app.ConnectionSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	require.NoError(t, err)

	return v
}

// TestAConnectionNamesItsPeopleByOneClaim: `Connection.subject_claim` is a claim
// a front door knows how to read, and nothing else. A tenant moves its
// connection forward -- `sub` to `oid` -- and not back: everybody already moved
// would be a stranger at their next sign-in. Back is the deployment's.
func TestAConnectionNamesItsPeopleByOneClaim(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	add := func(as context.Context, s app.Server, name, claim string) error {
		t.Helper()
		_, err := s.Connection().Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
			SubjectClaim: claim,
		}.Build())

		return err
	}
	update := func(as context.Context, s app.Server, name, claim string) error {
		t.Helper()
		v := b.connectionOf(t, ctx, b.Contoso, name)
		_, err := s.Connection().Update(as, app.ConnectionUpdateRequest_builder{
			Ref:          app.ConnectionRef_builder{Id: v.GetId()}.Build(),
			DateUpdated:  v.GetDateUpdated(),
			SubjectClaim: z.Ptr(claim),
		}.Build())

		return err
	}

	t.Run("a claim no front door reads is refused", func(t *testing.T) {
		x := require.New(t)
		err := add(admin, b.Walled, "upn", "preferred_username")
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		err = add(ctx, b.Ungated, "upn", "email")
		x.Equal(codes.InvalidArgument, status.Code(err), "the deployment is held to the claims a front door reads too: %v", err)
	})

	t.Run("a tenant names its people by oid", func(t *testing.T) {
		x := require.New(t)
		x.NoError(add(admin, b.Walled, "entra", "oid"))
		x.Equal("oid", b.connectionOf(t, ctx, b.Contoso, "entra").GetSubjectClaim())
	})

	t.Run("and does not go back", func(t *testing.T) {
		x := require.New(t)
		err := update(admin, b.Walled, "entra", "sub")
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		err = update(admin, b.Walled, "entra", "")
		x.Equal(codes.PermissionDenied, status.Code(err), "empty is `sub`: %v", err)
		x.Equal("oid", b.connectionOf(t, ctx, b.Contoso, "entra").GetSubjectClaim())
	})

	t.Run("which the deployment may", func(t *testing.T) {
		x := require.New(t)
		x.NoError(update(ctx, b.Ungated, "entra", ""))
		x.Empty(b.connectionOf(t, ctx, b.Contoso, "entra").GetSubjectClaim())
	})

	t.Run("and forward is the tenant's again", func(t *testing.T) {
		require.NoError(t, update(admin, b.Walled, "entra", "oid"))
	})

	t.Run("saying the claim it has is no move", func(t *testing.T) {
		require.NoError(t, update(admin, b.Walled, "entra", "oid"))
	})
}

// TestAnIdentityMovesToTheClaimItsConnectionNames: `IdentityService.Resubject`
// moves a person's identity to the subject their connection now names -- the
// same row, the same person -- one way, once, and only to the claim's shape.
func TestAnIdentityMovesToTheClaimItsConnectionNames(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
	}.Build())
	require.NoError(t, err)

	erin := b.holder(t, ctx, b.Contoso, "erin")
	fred := b.holder(t, ctx, b.Contoso, "fred")

	// What Entra's v2 `sub` looks like: pairwise, base64url, no dashes.
	was := b.identity(t, ctx, erin, "entra", "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ")
	b.identity(t, ctx, fred, "entra", "BBBBBBBBBBBBBBBBBBBBBIkzqFVrSaSaFHy782bbtaQ")

	move := func(ref []byte, subject string) (*app.Identity, error) {
		t.Helper()

		return b.Walled.Identity().Resubject(admin, app.IdentityResubjectRequest_builder{
			Ref:     app.IdentityRef_builder{Id: ref}.Build(),
			Subject: z.Ptr(subject),
		}.Build())
	}
	idOf := func(of pdid.Id) *app.Identity {
		t.Helper()
		vs, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{
				Holder: app.HolderRef_builder{Id: of.Bytes()}.Build(),
			}.Build()},
		}.Build())
		require.NoError(t, err)
		require.Len(t, vs.GetItems(), 1)

		return vs.GetItems()[0]
	}

	t.Run("not while the connection names people by sub", func(t *testing.T) {
		_, err := move(was.GetId(), erinOid)
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	})

	c := b.connectionOf(t, ctx, b.Contoso, "entra")
	_, err = b.Ungated.Connection().Update(ctx, app.ConnectionUpdateRequest_builder{
		Ref:          app.ConnectionRef_builder{Id: c.GetId()}.Build(),
		DateUpdated:  c.GetDateUpdated(),
		SubjectClaim: z.Ptr("oid"),
	}.Build())
	require.NoError(t, err)

	t.Run("not to something that is not an oid", func(t *testing.T) {
		_, err := move(was.GetId(), "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaZ")
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("to the oid, on the same row and the same person", func(t *testing.T) {
		x := require.New(t)
		v, err := move(was.GetId(), erinOid)
		x.NoError(err)
		x.Equal(was.GetId(), v.GetId())

		got := idOf(erin)
		x.Equal(erinOid, got.GetSubject())
		x.Equal(was.GetId(), got.GetId())
	})

	t.Run("and once", func(t *testing.T) {
		_, err := move(was.GetId(), fredOid)
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "a person's oid does not change: %v", err)
		require.Equal(t, erinOid, idOf(erin).GetSubject())
	})

	t.Run("not to somebody else's oid", func(t *testing.T) {
		_, err := move(idOf(fred).GetId(), erinOid)
		require.Equal(t, codes.AlreadyExists, status.Code(err), "%v", err)
	})

	t.Run("and a new identity there is an oid too", func(t *testing.T) {
		x := require.New(t)
		gail := b.holder(t, ctx, b.Contoso, "gail")
		_, err := b.Walled.Identity().Add(admin, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: gail.Bytes()}.Build(),
			Provider: "entra",
			Subject:  "CCCCCCCCCCCCCCCCCCCCCIkzqFVrSaSaFHy782bbtaQ",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "a pairwise sub where the oid belongs: %v", err)

		_, err = b.Walled.Identity().Add(admin, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: gail.Bytes()}.Build(),
			Provider: "entra",
			Subject:  "2b3c4d5e-6f70-8192-a3b4-c5d6e7f8091a",
		}.Build())
		x.NoError(err)
	})

	t.Run("a provider no connection fronts keeps whatever subjects it says", func(t *testing.T) {
		b.identity(t, ctx, b.holder(t, ctx, b.Contoso, "hana"), "ldap", "uid=hana")
	})
}

// TestAMoveIsAWayInAndIsHeldToIt: the new subject is a way into the person like
// any other, so a caller narrower than them -- a front door, signing an
// administrator in -- may not move their identity. The front door signs them
// in by the row they had instead (`arrives.Known`).
func TestAMoveIsAWayInAndIsHeldToIt(t *testing.T) {
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
		SubjectClaim: "oid",
	}.Build())
	require.NoError(t, err)

	// Written by the deployment before the connection named `oid`, which is
	// what every row of a connection that moved looks like.
	legacy := func(of pdid.Id, sub string) *app.Identity {
		t.Helper()
		v, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: of.Bytes()}.Build(),
			Provider: "local",
			Subject:  sub,
		}.Build())
		require.NoError(t, err)
		_, err = b.Ungated.Identity().Patch(ctx, app.IdentityPatchRequest_builder{
			Ref:              app.IdentityRef_builder{Id: v.GetId()}.Build(),
			Provider:         z.Ptr("entra"),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		require.NoError(t, err)

		return v
	}

	// A front door: a holder of the tenant's whose role is what it calls.
	door := b.holder(t, ctx, b.Contoso, "door")
	role := b.role(t, ctx, "door",
		app.IdentityService_Get_FullMethodName, app.IdentityService_Resubject_FullMethodName)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: door.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
	as := b.asNobody(ctx, door, b.Contoso)

	move := func(ref []byte, subject string) error {
		t.Helper()
		_, err := b.Walled.Identity().Resubject(as, app.IdentityResubjectRequest_builder{
			Ref:     app.IdentityRef_builder{Id: ref}.Build(),
			Subject: z.Ptr(subject),
		}.Build())

		return err
	}

	t.Run("not somebody wider than the caller", func(t *testing.T) {
		v := legacy(b.ContosoUser, "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ")
		err := move(v.GetId(), erinOid)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("somebody who holds nothing beyond it", func(t *testing.T) {
		v := legacy(b.holder(t, ctx, b.Contoso, "fred"), "BBBBBBBBBBBBBBBBBBBBBIkzqFVrSaSaFHy782bbtaQ")
		require.NoError(t, move(v.GetId(), fredOid))
	})
}
