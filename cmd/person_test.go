package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorFinishesAPerson is the rest of the person panel
// (`ts/src/people.tsx`), made as the panel makes it: on the admin port, with the
// operator's session, about one of a customer's people.
//
// Four writes the panel grew and the reads beside them. An address added and
// taken away -- `Email.Add` is a way in (the mailbox a reset goes to), so it
// runs `mayWriteAWayIn`, which an operator reaching the person passes; an
// identity unlinked from the operator's side, which is `Identity.Erase` and
// needs no escalation rule because taking a way in away is not adding one --
// but meets D42's rule like everybody else: the **last** way in is not taken
// away by anybody, operator included, because an operator who means to shut
// somebody out has `Disable` and does not need to strand them (`ts/plan.md`
// § I, verified here); a profile replaced whole through
// `Holder.Update`; and the person erased, softly, after which no read finds
// them and the trail still does.
func TestAnOperatorFinishesAPerson(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)

	tn, err := app.NewTenantServiceClient(conn).Add(as, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: tn.GetId()}.Build()

	holders := app.NewHolderServiceClient(conn)
	emails := app.NewEmailServiceClient(conn)
	identities := app.NewIdentityServiceClient(conn)

	erin, err := holders.Add(as, app.HolderAddRequest_builder{Tenant: at, Alias: "erin"}.Build())
	x.NoError(err)
	her := app.HolderRef_builder{Id: erin.GetId()}.Build()

	t.Run("an address, listed by whose it is, and taken away", func(t *testing.T) {
		x := require.New(t)

		v, err := emails.Add(as, app.EmailAddRequest_builder{Holder: her, Address: "erin@contoso.com"}.Build())
		x.NoError(err)
		x.Nil(v.GetDateVerified(), "an address an operator typed came back checked")

		vs, err := emails.List(as, app.EmailListRequest_builder{
			Filters: []*app.EmailFilter{app.EmailFilter_builder{Holder: her}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		x.Equal("erin@contoso.com", vs.GetItems()[0].GetAddress())

		_, err = emails.Erase(as, app.EmailRef_builder{Id: v.GetId()}.Build())
		x.NoError(err)

		vs, err = emails.List(as, app.EmailListRequest_builder{
			Filters: []*app.EmailFilter{app.EmailFilter_builder{Holder: her}.Build()},
		}.Build())
		x.NoError(err)
		x.Empty(vs.GetItems())
	})

	t.Run("a way in, unlinked from the operator's side", func(t *testing.T) {
		x := require.New(t)

		v, err := identities.Add(as, app.IdentityAddRequest_builder{
			Holder: her, Provider: "entra", Subject: "oid-9f3",
		}.Build())
		x.NoError(err)

		// Her only way in, and D42 refuses to take it -- from an operator as
		// from her. Locking somebody out is `Disable`; stranding them is not a
		// thing this deployment lets anybody do with a button.
		_, err = identities.Erase(as, app.IdentityRef_builder{Id: v.GetId()}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "the last way in was taken away")

		// With a password beside it, the identity is one of two and may go.
		_, err = app.NewCredentialServiceClient(conn).Set(as, app.CredentialSetRequest_builder{
			Ref: her, Secret: []byte("correct horse battery staple"),
		}.Build())
		x.NoError(err)

		_, err = identities.Erase(as, app.IdentityRef_builder{Id: v.GetId()}.Build())
		x.NoError(err, "an operator could not take a way in away")

		ways, err := holders.SignsIn(as, app.HolderSignsInRequest_builder{Ref: her}.Build())
		x.NoError(err)
		x.Empty(ways.GetIdentities(), "the unlinked identity is still a way in")
	})

	t.Run("a profile, replaced whole", func(t *testing.T) {
		x := require.New(t)

		row, err := holders.Get(as, app.HolderGetRequest_builder{Ref: her}.Build())
		x.NoError(err)

		v, err := holders.Update(as, app.HolderUpdateRequest_builder{
			Ref:         her,
			DateUpdated: row.GetDateUpdated(),
			Profile:     app.Profile_builder{DisplayName: "Erin", Department: "platform"}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("Erin", v.GetProfile().GetDisplayName())
		x.Equal("platform", v.GetProfile().GetDepartment())
		x.Equal("erin", v.GetAlias(), "an update touched the alias")

		// The version this page read is stale now, and a second save from it
		// is refused rather than applied to whatever the row became.
		_, err = holders.Update(as, app.HolderUpdateRequest_builder{
			Ref:         her,
			DateUpdated: row.GetDateUpdated(),
			Profile:     app.Profile_builder{DisplayName: "Somebody Else"}.Build(),
		}.Build())
		x.NotEqual(codes.OK, status.Code(err), "a stale version was applied")
	})

	t.Run("and erased, softly", func(t *testing.T) {
		x := require.New(t)

		_, err := holders.Erase(as, her)
		x.NoError(err)

		_, err = holders.Get(as, app.HolderGetRequest_builder{Ref: her}.Build())
		x.Equal(codes.NotFound, status.Code(err), "an erased person is still read")

		vs, err := holders.List(as, app.HolderListRequest_builder{
			Filters: []*app.HolderFilter{app.HolderFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		for _, h := range vs.GetItems() {
			x.NotEqual("erin", h.GetAlias(), "an erased person is still listed")
		}

		// The trail keeps what was done to them, which is what "soft" buys.
		trail, err := app.NewAuditServiceClient(conn).List(as, app.AuditListRequest_builder{
			Filters: []*app.AuditFilter{app.AuditFilter_builder{ObjectId: erin.GetId()}.Build()},
		}.Build())
		x.NoError(err)
		x.NotEmpty(trail.GetItems(), "the trail forgot an erased person")
	})
}
