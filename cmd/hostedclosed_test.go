package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// TestRostersOwnDoorReadsANameAsAFrontDoorDoes: which tenant a name is, at
// roster's own sign-in, is what `FrontService/WhoseHost` answers of the same
// name -- a closed tenant's included, and a request that names two.
//
// # What it was
//
// Two readers of one question. `WhoseHost` is what a front door asks, and it
// refuses a name whose tenant is closed, in those words. `cmd.Hosted` is what
// roster's own door asks, and it did not look: `AuthService/Offers` at a closed
// tenant's name went on answering, to anybody, whether it takes passwords and
// through which providers its people arrive.
//
// And a request that carried two names in `x-forwarded-host` was read by its
// first, where `roster-at` carrying two is refused (`keys.ArrivedAt`): which of
// two names a request is about is not something to guess, and the two readers
// guessed differently (#277).
func TestRostersOwnDoorReadsANameAsAFrontDoorDoes(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })

	for tenant, name := range map[string][]byte{"contoso.example": b.Contoso.Bytes(), "fabrikam.example": b.Fabrikam.Bytes()} {
		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: name}.Build(), Name: tenant,
		}.Build())
		x.NoError(err)
	}

	auth := app.NewAuthServiceClient(pdtest.Serve(t, b.grpc(t)))
	offers := func(md metadata.MD) error {
		_, err := auth.Offers(metadata.NewOutgoingContext(ctx, md), &app.AuthOffersRequest{})

		return err
	}

	x.NoError(offers(arrivingAt(t, "contoso.example")), "the control")

	t.Run("a request that names two tenants is about neither", func(t *testing.T) {
		x := require.New(t)

		for _, md := range []metadata.MD{
			metadata.Pairs("x-forwarded-host", "contoso.example", "x-forwarded-host", "fabrikam.example"),
			metadata.Pairs("x-forwarded-host", "fabrikam.example", "x-forwarded-host", "contoso.example"),
		} {
			x.Equal(codes.FailedPrecondition, status.Code(offers(md)), "%v", md)
		}

		// One name said twice is one name.
		x.NoError(offers(metadata.Pairs("x-forwarded-host", "contoso.example", "x-forwarded-host", "CONTOSO.example:443")))
	})

	t.Run("a closed tenant's name answers as it does to a front door", func(t *testing.T) {
		x := require.New(t)

		ref := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
		tn, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		_, err = b.Ungated.Tenant().Close(ctx, app.TenantCloseRequest_builder{Ref: ref, DateUpdated: tn.GetDateUpdated()}.Build())
		x.NoError(err)

		_, err = front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: "contoso.example"}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "the control: what a front door is told")

		err = offers(arrivingAt(t, "contoso.example"))
		x.Equal(codes.FailedPrecondition, status.Code(err), "a closed tenant still said how its people sign in: %v", err)

		_, err = auth.SignIn(metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example")),
			app.AuthSignInRequest_builder{Alias: "someone", Password: "correct horse battery staple"}.Build())
		x.Error(err)
	})
}
