package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
)

// TestASuspendedPersonIsSuspendedInFrontOfEveryApp.
//
// D26's table says where `date_disabled` is enforced -- `cmd.Resolver`, on the
// grounds that it is *where every credential that resolves to a holder arrives:
// a session, an `rt_`, a delegation*. That is true of every credential arriving
// **here**, and a product app's is not one of them. custody is handed an `rt_`
// or an `rd_` and asks `TokenService/Introspect` what it stands for, which is
// exactly what `operating.md` tells an app in front to ask.
//
// So suspending somebody stopped them signing in and stopped them calling
// roster, and left them working in every app in front of it until the token
// expired -- which for a key is possibly never. The one act whose whole purpose
// is *this person, not now* did not reach where the person actually is.
//
// Refused as `NotFound`, which is what this Rpc answers for every token it will
// not vouch for. An app hearing it stops trusting the string and sends the
// person to authenticate again, which is where they are told -- the same
// reading `cmd.Resolver` gives by answering `ErrNoCredential` rather than a
// permission error.
func TestASuspendedPersonIsSuspendedInFrontOfEveryApp(t *testing.T) {
	const get = "/roster.HolderService/Get"

	for _, tc := range []struct {
		what  string
		token func(t *testing.T, b *keyedBuilt) string
	}{
		{"a tenant key", func(t *testing.T, b *keyedBuilt) string {
			return mintFor(t, t.Context(), b, b.Who, "reader", []string{get}, time.Time{})
		}},
		{"a delegation", func(t *testing.T, b *keyedBuilt) string {
			return delegates(t, t.Context(), b, b.Who, []string{get}, time.Hour)
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			b := keyFor(t, introspect)
			ctx := t.Context()

			token := tc.token(t, b)
			c := pdpb.NewTokenServiceClient(b.Conn)
			ask := func() error {
				_, err := c.Introspect(bearing(ctx, b.Token),
					pdpb.TokenIntrospectRequest_builder{Token: token}.Build())

				return err
			}

			// It works while they are here.
			x.NoError(ask())

			_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			}.Build())
			x.NoError(err)

			x.Equal(codes.NotFound, status.Code(ask()),
				"a suspended person's token still introspected, so every app in front kept letting them in")

			// And enabling them gives it back, because a suspension is not an
			// erasure: the row is still theirs and so is the token. This is the
			// half that says the refusal reads the column rather than voiding
			// anything.
			_, err = b.Ungated.Holder().Enable(ctx, app.HolderEnableRequest_builder{
				Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			}.Build())
			x.NoError(err)
			x.NoError(ask())
		})
	}
}
