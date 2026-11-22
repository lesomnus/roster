package cmd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestIntrospectSaysOneThingOfATokenThatIsNone: every token `Introspect` will
// not vouch for is answered in one code and one sentence.
//
// # What it was
//
// The code was one and the words were three: a key never minted, or revoked,
// was `NotFound: ApiKey not found`, the generated read's; an expired one `no
// such key`; one whose holder is suspended `no such token`. `Introspect` says
// of itself that expired, revoked and never-existed told apart are an oracle
// for *this string was a real key once*, and an app holding the method read
// which by the message. The door says one sentence for all of them, and so
// does this, the door's.
func TestIntrospectSaysOneThingOfATokenThatIsNone(t *testing.T) {
	const get = "/roster.HolderService/Get"

	b := keyFor(t, introspect)
	ctx := t.Context()

	for name, token := range map[string]func(t *testing.T) string{
		"a key nobody minted": func(*testing.T) string {
			return keys.PrefixTenant + strings.Repeat("A", 43)
		},
		"a delegation nobody minted": func(*testing.T) string {
			return keys.PrefixDelegation + strings.Repeat("A", 43)
		},
		"a key revoked": func(t *testing.T) string {
			token := mintFor(t, ctx, b, b.Who, "revoked", []string{get}, time.Time{})
			_, err := b.Ungated.ApiKey().Erase(ctx, app.ApiKeyRef_builder{Secret: keys.Sum(token)}.Build())
			require.NoError(t, err)

			return token
		},
		"a key expired": func(t *testing.T) string {
			return mintFor(t, ctx, b, b.Who, "expired", []string{get}, time.Now().Add(-time.Hour))
		},
		"a key whose holder is suspended": func(t *testing.T) string {
			who := addHolder(t, ctx, b.Server, b.Contoso, "suspended")
			token := mintFor(t, ctx, b, who, "suspended", []string{get}, time.Time{})
			_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
			require.NoError(t, err)

			return token
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pdpb.NewTokenServiceClient(b.Conn).Introspect(bearing(ctx, b.Token),
				pdpb.TokenIntrospectRequest_builder{Token: token(t)}.Build())
			require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
			require.Equal(t, "no such token", status.Convert(err).Message())
		})
	}
}
