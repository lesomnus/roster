package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestADelegationIsWithinWhatItsCallerMayCall: an app is handed a delegation
// for the methods it may itself call, and for no other.
//
// # What it was
//
// `mayDelegate` refused a list wider than the caller's own, and what it read
// for *the caller's own* was the grant on the frame: the method list of the
// credential the call arrived with. That is the bound of a deployment key
// calling as itself, which holds nothing else. It is not the bound of a
// **holder**. A deployment key sent with `roster-at` is answered as whoever the
// tenant nominated, and the grant on that frame is `frame.Whole()` -- *whatever
// that holder may do, which their bindings decide on every call* (`keys.At`).
// The gate asks the bindings; this rule never did.
//
// So the front door a tenant installed with `VouchService/Verify` and
// `VouchService/Delegate` -- which is what `roster app install` writes -- was
// handed a delegation for any list at all, `/roster.*/*` among them, for
// whoever's password it had just checked: seen on a served deployment as an
// app bound two methods reading every role of the tenant as its administrator
// (#276). `account.Calls` says the bound it was built on -- *a delegation is
// never wider than the key that asked for it* -- and for a narrowed key
// nothing held it.
//
// It asks the gate what the gate would be asked: may this caller call that.
func TestADelegationIsWithinWhatItsCallerMayCall(t *testing.T) {
	const password = "correct horse battery staple"

	// A narrowed key: nothing as itself.
	b := keyFor(t)
	ctx := t.Context()
	x := require.New(t)

	answersAt(t, b.Server, b.Contoso, "contoso.example")

	// Who the app is in contoso, and the two methods a front door is bound.
	front := addHolder(t, ctx, b.Server, b.Contoso, "front")
	nominates(t, b.Server, b.Contoso, b.Service, front)

	bind := func(who []byte, alias string, methods ...string) {
		r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:   alias,
			Methods: methods,
		}.Build())
		x.NoError(err)

		_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: who}.Build(),
		}.Build())
		x.NoError(err)
	}
	bind(front.Bytes(), "fronts", "/roster.VouchService/Verify", "/roster.VouchService/Delegate")

	// And somebody who administers the tenant, signing in through it.
	bind(b.Who.Bytes(), "administers", "/roster.*/*")
	x.NoError(setFor(ctx, b.Server, b.Who.Bytes(), password))

	vouch := app.NewVouchServiceClient(b.Conn)
	delegate := func(t *testing.T, as string, methods ...string) (*app.VouchDelegateResponse, error) {
		t.Helper()

		call := arrivedAt(ctx, as, "contoso.example")
		if as != b.Token {
			call = bearing(ctx, as)
		}

		return vouch.Delegate(call, app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret:  []byte(password),
			Methods: methods,
		}.Build())
	}

	t.Run("not a method the app is not bound", func(t *testing.T) {
		x := require.New(t)

		for _, m := range []string{"/roster.HolderService/Erase", "/roster.RoleService/List", "/roster.*/*", "/roster.VouchService/*"} {
			res, err := delegate(t, b.Token, m)
			x.Equal(codes.PermissionDenied, status.Code(err), "%s: %v", m, err)
			x.Empty(res.GetToken(), "%s: an app bound two methods was handed a delegation for it", m)
		}
	})

	t.Run("and not one beside a method it is", func(t *testing.T) {
		x := require.New(t)

		_, err := delegate(t, b.Token, "/roster.VouchService/Verify", "/roster.HolderService/Erase")
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("what it is bound, it hands on", func(t *testing.T) {
		x := require.New(t)

		res, err := delegate(t, b.Token, "/roster.VouchService/Verify")
		x.NoError(err)
		x.True(res.GetVerified().GetOk())
		x.NotEmpty(res.GetToken())
	})

	// What everybody calls about themselves is nobody's binding and is not
	// refused for want of one: `MeService/Get` is what a Login App's delegation
	// is (`login.Methods`), and its role does not name it.
	t.Run("and what nobody needs a role for", func(t *testing.T) {
		x := require.New(t)

		res, err := delegate(t, b.Token, "/roster.MeService/Get")
		x.NoError(err)
		x.NotEmpty(res.GetToken())

		me, err := app.NewMeServiceClient(b.Conn).Get(
			metadata.AppendToOutgoingContext(arrivedAt(ctx, b.Token, "contoso.example"), keys.HeaderActing, res.GetToken()),
			app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal(b.Who.Bytes(), me.GetId())
	})

	// A tenant's own key is its holder narrowed by its list, so it is held to
	// both: the list was always asked, and the holder never.
	t.Run("a tenant's key is held to its holder as to its list", func(t *testing.T) {
		x := require.New(t)

		mint := func(alias string, methods ...string) string {
			token, sum, err := keys.Mint(keys.PrefixTenant)
			x.NoError(err)

			_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
				Holder:  app.HolderRef_builder{Id: front.Bytes()}.Build(),
				Alias:   alias,
				Secret:  sum,
				Methods: methods,
			}.Build())
			x.NoError(err)

			return token
		}

		// A list wider than its holder.
		wide := mint("wide", "/roster.*/*")
		_, err := delegate(t, wide, "/roster.HolderService/Erase")
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		res, err := delegate(t, wide, "/roster.VouchService/Verify")
		x.NoError(err)
		x.NotEmpty(res.GetToken())

		// And a list narrower than its holder, which is the bound it had.
		narrow := mint("narrow", "/roster.VouchService/Delegate")
		_, err = delegate(t, narrow, "/roster.VouchService/Verify")
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})
}
