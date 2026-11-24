package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAnErasedHoldersKeyStandsForNobody: a key whose holder is gone is refused
// as a key whose holder is suspended is.
//
// # What it was
//
// `server/keys` asks one thing of the holder a token hangs off -- `signable` --
// and it asked whether they are suspended and whether their tenant is closed.
// Not whether they are **erased**. A key is not erased with its holder: the row
// stays, the edge is immutable, and the lookup is by the verifier alone.
//
// So an operator who removed an app by erasing its holder on the control plane
// left its `rk_` answering on the data plane as before; and a product app that
// asked `TokenService/Introspect` about a tenant key of somebody erased was
// told the token is good, with the person's identifier and no tenant (#276).
// Suspending them stopped both. Erasing them, which is the stronger act, did
// not.
func TestAnErasedHoldersKeyStandsForNobody(t *testing.T) {
	const get = "/roster.HolderService/Get"

	t.Run("a deployment's key, its holder on the control plane erased", func(t *testing.T) {
		x := require.New(t)
		b := keyFor(t, listHolders)
		ctx := t.Context()

		holders := app.NewHolderServiceClient(b.Conn)
		_, err := holders.List(bearing(ctx, b.Token), app.HolderListRequest_builder{}.Build())
		x.NoError(err, "the control")

		_, err = b.Control.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: b.Service.Bytes()}.Build())
		x.NoError(err)

		_, err = holders.List(bearing(ctx, b.Token), app.HolderListRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"an app whose holder was erased is still answered: %v", err)
	})

	for _, tc := range []struct {
		what string
		// The token, and the app that asks about it: the one a key is for,
		// and the one a delegation was issued to.
		token func(t *testing.T, b *keyedBuilt) (string, context.Context)
	}{
		{"a tenant's key, asked about by the app it is for", func(t *testing.T, b *keyedBuilt) (string, context.Context) {
			foo := spenderFor(t, t.Context(), b)

			return mintSpent(t, t.Context(), b, b.Who, foo.id, "reader", []string{get}, time.Time{}), foo.as
		}},
		{"a delegation, asked about by the app it was given to", func(t *testing.T, b *keyedBuilt) (string, context.Context) {
			return delegates(t, t.Context(), b, b.Who, []string{get}, time.Hour), bearing(t.Context(), b.Token)
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			b := keyFor(t, introspect)
			ctx := t.Context()

			token, as := tc.token(t, b)
			c := pdpb.NewTokenServiceClient(b.Conn)
			ask := func() error {
				_, err := c.Introspect(as,
					pdpb.TokenIntrospectRequest_builder{Token: token}.Build())

				return err
			}
			x.NoError(ask(), "the control")

			_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: b.Who.Bytes()}.Build())
			x.NoError(err)

			x.Equal(codes.NotFound, status.Code(ask()),
				"somebody erased is still vouched for, so every app in front keeps letting them in")
		})
	}
}

// TestADelegationStopsWithItsAppsStandingInTheTenant: a call an app makes as
// somebody is refused once the app itself would be.
//
// # What it was
//
// A deployment key narrowed to a tenant is whoever that tenant nominated, and a
// tenant that suspends that holder has turned the app off there: the key as
// itself is refused, by the resolver, as every suspended caller is. A call that
// carries a delegation is not resolved to the nominee. `Acting` finds them to
// compare the delegation's issuer and answers as the **person**, so nothing on
// that path asked whether the nominee may still call -- and an app turned off
// in a tenant went on acting as everybody whose delegation it held, for as
// long as each lived (#276).
func TestADelegationStopsWithItsAppsStandingInTheTenant(t *testing.T) {
	x := require.New(t)

	// A narrowed key: nothing as itself.
	b := keyFor(t)
	ctx := t.Context()

	answersAt(t, b.Server, b.Contoso, "contoso.example")

	front := addHolder(t, ctx, b.Server, b.Contoso, "front")
	nominates(t, b.Server, b.Contoso, b.Service, front)

	// A delegation as a narrowed call mints one: issued to the nominee.
	token, _, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
		Holder:  b.Who,
		Issuer:  front.Bytes(),
		Methods: []string{"/roster.MeService/Get"},
		For:     time.Hour,
	})
	x.NoError(err)

	me := app.NewMeServiceClient(b.Conn)
	asSomebody := func() error {
		_, err := me.Get(
			metadata.AppendToOutgoingContext(arrivedAt(ctx, b.Token, "contoso.example"), keys.HeaderActing, token),
			app.MeGetRequest_builder{}.Build())

		return err
	}
	x.NoError(asSomebody(), "the control")

	ref := app.HolderRef_builder{Id: front.Bytes()}.Build()

	_, err = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.Equal(codes.Unauthenticated, status.Code(asSomebody()),
		"an app suspended in the tenant still acts as its people")

	// A suspension, so enabling them gives it back.
	_, err = b.Ungated.Holder().Enable(ctx, app.HolderEnableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.NoError(asSomebody())

	_, err = b.Ungated.Holder().Erase(ctx, ref)
	x.NoError(err)
	x.Equal(codes.Unauthenticated, status.Code(asSomebody()),
		"an app erased from the tenant still acts as its people")
}
