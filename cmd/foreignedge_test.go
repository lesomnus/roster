package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
//	Alice may call Email.Add and Email.Get, in acme, and nothing else.
//	She adds an address of her own, vouched for by an identity of hooli's.
//	She reads her own row back, selecting through the edge.
//	She has hooli's provider subject, that person's name, and their tenant.
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
	outsider := b.holder(t, ctx, b.Hooli, "outsider")
	_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: outsider.Bytes()}.Build(),
		Name:             z.Ptr("Gavin Belson"),
		Desc:             z.Ptr("CEO, do not disclose"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	theirs := b.identity(t, ctx, outsider, "google", "hooli-secret-subject")

	// A clerk. Two methods, and neither of them looks like a way to read
	// another customer.
	b.binds(t, b.AcmeUser, b.role(t, ctx, "clerk",
		"/roster.EmailService/Add", "/roster.EmailService/Get"), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.AcmeUser)
	c := app.NewEmailServiceClient(conn)

	t.Run("by identifier", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Add(wire, app.EmailAddRequest_builder{
			Holder:    app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Address:   "alice@acme.example",
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
			Holder:  app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Address: "alice2@acme.example",
			VouchedBy: app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					TenantId: b.Hooli.Bytes(),
					Provider: z.Ptr("google"),
					Subject:  z.Ptr("hooli-secret-subject"),
				}.Build(),
			}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))

		// And the same answer for one that is not there, so nothing is learned
		// either way.
		_, err = c.Add(wire, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Address: "alice3@acme.example",
			VouchedBy: app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					TenantId: b.Hooli.Bytes(),
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
			Holder:  app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Address: "alice4@acme.example",
		}.Build())
		x.NoError(err)

		as := frame.Into(ctx,
			frame.New(b.AcmeUser, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))

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

		ours := b.identity(t, ctx, b.AcmeUser, "google", "acme-subject")

		_, err := c.Add(wire, app.EmailAddRequest_builder{
			Holder:    app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Address:   "alice5@acme.example",
			VouchedBy: app.IdentityRef_builder{Id: ours.GetId()}.Build(),
		}.Build())
		x.NoError(err, "the check refuses what it should allow")
	})
}
