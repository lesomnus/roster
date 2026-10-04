package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// TestAnAppProvesWhoItIsToAnotherInTheSameTenant is #74.
//
// kamino and khala are both roster-hosted, both nominated in acme. kamino,
// calling khala about acme, has nothing khala can check as `@acme/kamino`: its
// `rk_` does not introspect on the data plane and `roster-at` is a header only
// roster reads. `Delegation.Exchange` mints a delegation **about the caller,
// issued to the audience**, so khala -- narrowed to acme itself -- introspects
// it and is told kamino, and nobody else is told anything.
func TestAnAppProvesWhoItIsToAnotherInTheSameTenant(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, app.TenantService_Get_FullMethodName)
	ctx := t.Context()
	acme := b.Contoso

	// kamino is the harness's key, answered in acme as `b.Who`.
	exchange := "/roster.DelegationService/Exchange"
	introspect := "/payday.TokenService/Introspect"
	nominates(t, b.Server, acme, b.Service, b.Who)
	permits(t, ctx, b, acme, b.Who, "kamino", exchange, introspect)

	// khala, another app, nominated in acme as its own holder.
	khalaKey, khalaToken := serviceKey(t, b.Server, "khala", introspect)
	khala := addHolder(t, ctx, b.Server, acme, "khala")
	nominates(t, b.Server, acme, khalaKey, khala)
	permits(t, ctx, b, acme, khala, "khala", introspect)

	// The harness calls its tenant contoso; acme is the name the issue uses.
	at := front.AtTenant("contoso")

	delegations := app.NewDelegationServiceClient(b.Conn)
	tokens := pdpb.NewTokenServiceClient(b.Conn)

	got, err := delegations.Exchange(arrivedAt(ctx, b.Token, at), app.DelegationExchangeRequest_builder{
		Audience: app.HolderRef_builder{Id: khala.Bytes()}.Build(),
		Methods:  []string{"/hday.khala.RobotService/Get"},
	}.Build())
	x.NoError(err)
	x.NotEmpty(got.GetToken())
	x.NotNil(got.GetDateExpires())

	ask := func(ctx context.Context) (*pdpb.TokenIntrospectResponse, error) {
		return tokens.Introspect(ctx, pdpb.TokenIntrospectRequest_builder{Token: got.GetToken()}.Build())
	}

	t.Run("the audience is told who sent it", func(t *testing.T) {
		x := require.New(t)

		v, err := ask(arrivedAt(ctx, khalaToken, at))
		x.NoError(err)
		x.Equal(b.Who.Bytes(), v.GetId(), "khala was told somebody other than kamino")
		x.Equal(acme.Bytes(), v.GetTenantId())
	})

	t.Run("and nobody else is told anything, the sender included", func(t *testing.T) {
		x := require.New(t)

		// The sender, narrowed to its own holder -- which may introspect, and is
		// not who the token was issued to.
		_, err := ask(arrivedAt(ctx, b.Token, at))
		x.Equal(codes.NotFound, status.Code(err))

		// The audience's key as itself, allowed to introspect and nobody in acme.
		_, err = ask(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+khalaToken)))
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("and a key as itself is nobody to name", func(t *testing.T) {
		_, err := delegations.Exchange(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+b.Token)),
			app.DelegationExchangeRequest_builder{
				Audience: app.HolderRef_builder{Id: khala.Bytes()}.Build(),
				Methods:  []string{"/hday.khala.RobotService/Get"},
			}.Build())
		require.Error(t, err)
	})

	t.Run("and an audience in another tenant is refused", func(t *testing.T) {
		elsewhere := addHolder(t, ctx, b.Server, add(t, ctx, b.Server, "fabrikam"), "khala")
		_, err := delegations.Exchange(arrivedAt(ctx, b.Token, at), app.DelegationExchangeRequest_builder{
			Audience: app.HolderRef_builder{Id: elsewhere.Bytes()}.Build(),
			Methods:  []string{"/hday.khala.RobotService/Get"},
		}.Build())
		require.Error(t, err)
		require.NotEqual(t, codes.OK, status.Code(err))
	})

	t.Run("and a token for nothing is not minted", func(t *testing.T) {
		_, err := delegations.Exchange(arrivedAt(ctx, b.Token, at), app.DelegationExchangeRequest_builder{
			Audience: app.HolderRef_builder{Id: khala.Bytes()}.Build(),
		}.Build())
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

}
