package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestANameErasedAnswersNobodyAndANameMovedAnswersWhoHasIt: a sign-in on the
// data plane is about the tenant that has the name the request arrived at --
// has it now.
//
// # What it was
//
// `cmd.Hosted` read `Host` by name and took the first row, and did not ask
// whether the row was erased. An erase keeps the row. So a name a tenant gave
// up went on signing that tenant's people in; and a name that moved to another
// tenant went on checking passwords against the one before -- whose name it now
// was could not sign in at it, and whose it had been still could (#274).
//
// `FrontService.WhoseHost` reads the same name through the server and was
// right all along, so the two said different things of one name.
func TestANameErasedAnswersNobodyAndANameMovedAnswersWhoHasIt(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })

	const name = "two.example"

	// Somebody of each tenant, under the same alias, with a password each.
	password := func(who pdid.Id) string {
		res, err := b.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		return res.GetSecret()
	}
	contosos := password(b.ContosoUser)
	fabrikams := password(b.holder(t, ctx, b.Fabrikam, "someone"))

	auth := app.NewAuthServiceClient(pdtest.Serve(t, b.grpc(t)))
	signIn := func(secret string) error {
		at := metadata.NewOutgoingContext(ctx, arrivingAt(t, name))
		_, err := auth.SignIn(at, app.AuthSignInRequest_builder{Alias: "someone", Password: secret}.Build())

		return err
	}

	host, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   name,
	}.Build())
	x.NoError(err)
	x.NoError(signIn(contosos), "the tenant's own person, at the tenant's own name")

	gone, err := b.Ungated.Host().Erase(ctx, app.HostRef_builder{Id: host.GetId()}.Build())
	x.NoError(err)
	x.True(gone.GetErased())

	t.Run("a name erased is one nothing claims", func(t *testing.T) {
		x := require.New(t)

		err := signIn(contosos)
		x.Equal(codes.FailedPrecondition, status.Code(err), "a name its tenant gave up still signed its people in: %v", err)
		x.ErrorContains(err, name)
	})

	t.Run("and a name that moved is its new tenant's", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
			Name:   name,
		}.Build())
		x.NoError(err)

		x.NoError(signIn(fabrikams), "whose name it is now could not sign in at it")

		err = signIn(contosos)
		x.Equal(codes.Unauthenticated, status.Code(err), "whose name it was still could: %v", err)
	})
}
