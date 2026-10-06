package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// directory is a tenant whose `entra` connection names people by `oid` and is
// the one its directory provisions through, and the directory: a holder whose
// role is what a directory calls, asking.
func (b *built) directory(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	x := require.New(t)

	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant:       app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:         "entra",
		Issuer:       "https://login.microsoftonline.com/contoso/v2.0",
		ClientId:     "the-app",
		SubjectClaim: "oid",
		Provisions:   true,
	}.Build())
	x.NoError(err)

	who := b.holder(t, ctx, b.Contoso, "directory")
	role := b.role(t, ctx, "directory",
		app.HolderService_Provision_FullMethodName,
		app.HolderService_Deactivate_FullMethodName,
		app.HolderService_Activate_FullMethodName,
		app.HolderService_Get_FullMethodName,
		app.HolderService_Update_FullMethodName,
	)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	return b.asNobody(ctx, who, b.Contoso)
}

// provision is what a directory asks for one person.
func (b *built) provision(as context.Context, alias, subject, address string) (*app.Holder, error) {
	return b.Walled.Holder().Provision(as, app.HolderProvisionRequest_builder{
		Tenant:   app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:    alias,
		Name:     "Somebody " + alias,
		Profile:  app.Profile_builder{DisplayName: "Somebody " + alias, Department: "R&D"}.Build(),
		Provider: "entra",
		Subject:  subject,
		Address:  address,
	}.Build())
}

// TestADirectoryMakesPeopleAndOnlyNewOnes: `HolderService.Provision` makes a
// person, their identity at the connection the tenant provisions through, and
// their address -- unverified -- in one write or none. It reaches nobody who
// already exists, which is what makes it safe to hand a directory where
// `Identity.Add` is not.
func TestADirectoryMakesPeopleAndOnlyNewOnes(t *testing.T) {
	b, ctx := build(t)
	as := b.directory(t, ctx)

	t.Run("somebody, linked and addressed", func(t *testing.T) {
		x := require.New(t)
		h, err := b.provision(as, "erin", erinOid, "erin@contoso.example")
		x.NoError(err)
		x.Equal("erin", h.GetAlias())

		id := mustId(t, h.GetId())
		x.Equal(erinOid, idOfHolder(t, b, ctx, id).GetSubject())

		e, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{
			Ref: app.EmailRef_builder{At: app.EmailRefByAt_builder{
				TenantId: b.Contoso.Bytes(), Address: z.Ptr("erin@contoso.example"),
			}.Build()}.Build(),
			Select: app.EmailSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(h.GetId(), e.GetHolder().GetId())
		x.Nil(e.GetDateVerified(), "an address a directory hands over is written unverified")
	})

	t.Run("and a name somebody has takes a suffix", func(t *testing.T) {
		x := require.New(t)
		h, err := b.provision(as, "erin", fredOid, "erin.k@contoso.example")
		x.NoError(err)
		x.True(strings.HasPrefix(h.GetAlias(), "erin-"), h.GetAlias())
	})

	t.Run("an address somebody has refuses all of it", func(t *testing.T) {
		x := require.New(t)
		_, err := b.provision(as, "gail", "2b3c4d5e-6f70-8192-a3b4-c5d6e7f8091a", "erin@contoso.example")
		x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)
		_, err = b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Alias: z.Ptr("gail"), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			}.Build()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "a refused provision left a person behind")
	})

	t.Run("and so does a subject somebody has", func(t *testing.T) {
		_, err := b.provision(as, "hana", erinOid, "hana@contoso.example")
		require.Equal(t, codes.AlreadyExists, status.Code(err), "%v", err)
	})

	t.Run("not through a connection the tenant does not provision through", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: "google",
			Issuer: "https://accounts.google.com", ClientId: "the-app",
		}.Build())
		x.NoError(err)
		_, err = b.Walled.Holder().Provision(as, app.HolderProvisionRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "ivan",
			Provider: "google", Subject: "1234567890", Address: "ivan@contoso.example",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("and not a subject of the wrong shape", func(t *testing.T) {
		_, err := b.provision(as, "jane", "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", "jane@contoso.example")
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("and not in a tenant whose directory provisions nothing", func(t *testing.T) {
		_, err := b.Ungated.Holder().Provision(ctx, app.HolderProvisionRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(), Alias: "kim",
			Provider: "entra", Subject: erinOid,
		}.Build())
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	})
}

// idOfHolder is the one identity somebody has.
func idOfHolder(t *testing.T, b *built, ctx context.Context, of pdid.Id) *app.Identity {
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

// TestADirectoryLiftsOnlyItsOwnSuspension: a directory is one way, and
// restarting its provisioning says `active` for everybody -- so it lifts the
// suspensions it made and no other. An operator's `Disable` makes a
// suspension theirs, and a person the directory deleted is an operator's to
// bring back.
func TestADirectoryLiftsOnlyItsOwnSuspension(t *testing.T) {
	b, ctx := build(t)
	as := b.directory(t, ctx)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)

	h, err := b.provision(as, "erin", erinOid, "erin@contoso.example")
	require.NoError(t, err)
	ref := app.HolderRef_builder{Id: h.GetId()}.Build()

	read := func() *app.Holder {
		t.Helper()
		v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    ref,
			Select: app.HolderSelect_builder{DateDisabled: z.Ptr(true), Directory: z.Ptr(true)}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}
	deactivate := func(deleted bool) error {
		t.Helper()
		_, err := b.Walled.Holder().Deactivate(as, app.HolderDeactivateRequest_builder{Ref: ref, Deleted: deleted}.Build())

		return err
	}
	activate := func() error {
		t.Helper()
		_, err := b.Walled.Holder().Activate(as, app.HolderActivateRequest_builder{Ref: ref}.Build())

		return err
	}

	t.Run("its own, both ways", func(t *testing.T) {
		x := require.New(t)
		x.NoError(deactivate(false))
		v := read()
		x.NotNil(v.GetDateDisabled())
		x.Equal("inactive", v.GetDirectory())

		x.NoError(activate())
		v = read()
		x.Nil(v.GetDateDisabled())
		x.Empty(v.GetDirectory())
	})

	t.Run("not an operator's", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Walled.Holder().Disable(admin, app.HolderDisableRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		err = activate()
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.NotNil(read().GetDateDisabled(), "the directory lifted an operator's suspension")

		x.NoError(deactivate(false))
		x.Empty(read().GetDirectory(), "the directory took an operator's suspension for its own")

		_, err = b.Walled.Holder().Enable(admin, app.HolderEnableRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		x.Nil(read().GetDateDisabled())
	})

	t.Run("and an operator's Disable takes the directory's", func(t *testing.T) {
		x := require.New(t)
		x.NoError(deactivate(false))
		_, err := b.Walled.Holder().Disable(admin, app.HolderDisableRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		x.Empty(read().GetDirectory())

		err = activate()
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		_, err = b.Walled.Holder().Enable(admin, app.HolderEnableRequest_builder{Ref: ref}.Build())
		x.NoError(err)
	})

	t.Run("somebody deleted stays so until an operator says", func(t *testing.T) {
		x := require.New(t)
		x.NoError(deactivate(true))
		v := read()
		x.NotNil(v.GetDateDisabled())
		x.Equal("deleted", v.GetDirectory())

		err := activate()
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

		_, err = b.Walled.Holder().Enable(admin, app.HolderEnableRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		x.Nil(read().GetDateDisabled())
		x.Empty(read().GetDirectory())
	})

	t.Run("and only people who sign in through it", func(t *testing.T) {
		x := require.New(t)
		app_ := b.holder(t, ctx, b.Contoso, "kamino")
		_, err := b.Walled.Holder().Deactivate(as, app.HolderDeactivateRequest_builder{
			Ref: app.HolderRef_builder{Id: app_.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and the state is not something an Add carries", func(t *testing.T) {
		_, err := b.Walled.Holder().Add(admin, app.HolderAddRequest_builder{
			Tenant:    app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:     "ghost",
			Directory: "inactive",
		}.Build())
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})
}

// TestATenantProvisionsThroughOneConnection: a directory names the people it
// makes once, so one connection a tenant provisions through.
func TestATenantProvisionsThroughOneConnection(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	add := func(name string, provisions bool) error {
		t.Helper()
		_, err := b.Walled.Connection().Add(admin, app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: "https://idp.example/" + name, ClientId: "the-app", Provisions: provisions,
		}.Build())

		return err
	}
	set := func(name string, provisions bool) error {
		t.Helper()
		v := b.connectionOf(t, ctx, b.Contoso, name)
		_, err := b.Walled.Connection().Update(admin, app.ConnectionUpdateRequest_builder{
			Ref:         app.ConnectionRef_builder{Id: v.GetId()}.Build(),
			DateUpdated: v.GetDateUpdated(),
			Provisions:  z.Ptr(provisions),
		}.Build())

		return err
	}

	x := require.New(t)
	x.NoError(add("entra", true))
	x.Equal(codes.FailedPrecondition, status.Code(add("okta", true)))
	x.NoError(add("okta", false))
	x.Equal(codes.FailedPrecondition, status.Code(set("okta", true)))
	x.NoError(set("entra", true), "saying it again of the one that does is no second")
	x.NoError(set("entra", false))
	x.NoError(set("okta", true))
}
