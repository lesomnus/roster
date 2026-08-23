package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnEdgeIsNotAWayThroughTheWall.
//
// The widest thing found in this app, and it needed a clerk rather than an
// administrator.
//
// The gate checks the edges an `Add` hangs off, which is what keeps a row from
// being planted in a tenant the caller cannot see. It checked the **path to the
// tenant** and nothing else, and payday said why: *an edge pointing at some
// other row in another tenant is a different question -- referential, not
// tenancy.*
//
// It is not a different question, because an edge is a **read**. `Email` has
// `vouched_by`, an `Identity` that is not the path to anybody's tenant, and
// `EmailSelect` nests `IdentitySelect`, which nests `HolderSelect`, which nests
// `TenantSelect`. So:
//
//	Alice may call Email.Add and Email.Get, in contoso, and nothing else.
//	She adds an address of her own, vouched for by an identity of fabrikam's.
//	She reads her own row back, selecting through the edge.
//	She has fabrikam's provider subject, that person's name, and their tenant.
//
// Three hops, from two methods, with no permission anybody would question.
//
// Fixed in payday, because the gate is generated and every app on it had the
// same hole: `lesomnus/payday@7d19dea`. Pinned here and asserted here, because
// the leak was found here and a property that holds only because of how
// somebody else emits a layer is one that stops holding without anything in
// this repository changing.
func TestAnEdgeIsNotAWayThroughTheWall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Somebody in the other tenant, with something worth not leaking.
	outsider := b.holder(t, ctx, b.Fabrikam, "outsider")
	_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: outsider.Bytes()}.Build(),
		Name:             z.Ptr("Gavin Belson"),
		Desc:             z.Ptr("CEO, do not disclose"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	theirs := b.identity(t, ctx, outsider, "google", "fabrikam-secret-subject")

	// A clerk. Two methods, and neither of them looks like a way to read
	// another customer.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "clerk",
		"/roster.EmailService/Add", "/roster.EmailService/Get"), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)
	c := app.NewEmailServiceClient(conn)

	t.Run("by identifier", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Add(wire, app.EmailAddRequest_builder{
			Holder:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address:   "alice@contoso.example",
			VouchedBy: app.IdentityRef_builder{Id: theirs.GetId()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"an edge reached another tenant's row")
	})

	t.Run("and by subject, which needs no identifier at all", func(t *testing.T) {
		x := require.New(t)

		// The other half, and the worse one: `Pick` applies erasure and no
		// scope, so this form needs nothing but the tenant identifier -- which
		// `FrontService.WhoseHost` hands out unwalled, on purpose -- and it
		// answers differently for a subject that exists. An oracle before it is
		// a leak.
		_, err := c.Add(wire, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: "alice2@contoso.example",
			VouchedBy: app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					TenantId: b.Fabrikam.Bytes(),
					Provider: z.Ptr("google"),
					Subject:  z.Ptr("fabrikam-secret-subject"),
				}.Build(),
			}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))

		// And the same answer for one that is not there, so nothing is learned
		// either way.
		_, err = c.Add(wire, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: "alice3@contoso.example",
			VouchedBy: app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					TenantId: b.Fabrikam.Bytes(),
					Provider: z.Ptr("google"),
					Subject:  z.Ptr("nobody-here-at-all"),
				}.Build(),
			}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"the two answers differ, so this is an existence oracle")
	})

	t.Run("and it cannot be moved there afterwards", func(t *testing.T) {
		x := require.New(t)

		// `/Patch` is closed at the transport, so this goes through the walled
		// stack directly -- which is what a deployment with
		// `allow_general_writes` serves, and what `roster email patch` reaches
		// at a shell, since the local CLI installs no `closed` interceptor.
		v, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: "alice4@contoso.example",
		}.Build())
		x.NoError(err)

		as := frame.Into(ctx,
			frame.New(b.ContosoUser, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))

		_, err = b.Walled.Email().Patch(as, app.EmailPatchRequest_builder{
			Ref:              app.EmailRef_builder{Id: v.GetId()}.Build(),
			VouchedBy:        app.IdentityRef_builder{Id: theirs.GetId()}.Build(),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"an edge was moved onto another tenant's row")
	})

	t.Run("and an edge inside the wall still works", func(t *testing.T) {
		x := require.New(t)

		ours := b.identity(t, ctx, b.ContosoUser, "google", "contoso-subject")

		_, err := c.Add(wire, app.EmailAddRequest_builder{
			Holder:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address:   "alice5@contoso.example",
			VouchedBy: app.IdentityRef_builder{Id: ours.GetId()}.Build(),
		}.Build())
		x.NoError(err, "the check refuses what it should allow")
	})
}

// TestNobodyAssertsThatTheirOwnAddressWasChecked.
//
// `05792f6` closed this class one entity over -- *a stamp is not a field a
// caller writes* -- for `Identity.tenant_id`, with `immutable: true`. That word
// is the right one there and the wrong one here: `immutable` takes a field out
// of the **patch** request and leaves it in `Add`, and what `date_verified`
// wants is the other way round. Neither generator can say it, so it is a layer.
//
// Nothing reads the column yet, which is what makes this cheap to close and the
// reason to close it now. Its whole stated job is to decide whether an address
// may be **trusted** -- `email.proto` says an unverified provider address must
// not be trusted to link accounts -- so the day something reads it is the day
// every value already there was written by whoever could write the row.
//
// Which is wider than it sounds: `mayReach` passes for any target holding
// nothing the caller lacks, so a support desk whose whole job is contact
// details writes addresses for nearly everybody in the tenant.
func TestNobodyAssertsThatTheirOwnAddressWasChecked(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.binds(t, b.ContosoUser, b.role(t, ctx, "desk", "/roster.EmailService/Add"), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)
	c := app.NewEmailServiceClient(conn)

	_, err := c.Add(wire, app.EmailAddRequest_builder{
		Holder:       app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address:      "alice@contoso.example",
		DateVerified: timestamppb.Now(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err),
		"a caller asserted that their own address had been checked")
	x.Contains(status.Convert(err).Message(), "date_verified")

	t.Run("and the same write without the claim is fine", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Add(wire, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: "alice@contoso.example",
		}.Build())
		x.NoError(err)
	})

	t.Run("and the deployment's own work is not a request", func(t *testing.T) {
		x := require.New(t)

		// No frame, which is `init`, a seed, or a server writing through the
		// unwalled stack -- the same opt-out `mayGrant` and `mayJoin` take.
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:       app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address:      "seeded@contoso.example",
			DateVerified: timestamppb.Now(),
		}.Build())
		x.NoError(err)
	})
}
