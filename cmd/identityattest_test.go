package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/identity"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAnAccountAtAProviderIsAttestedThroughAFrontDoor: what somebody connects
// to their own account is written on the word of a front door that saw the
// provider say it is theirs, and on nobody else's.
//
// # What it was
//
// `IdentityService/Add` was in what everybody holds over their own account
// (`self-service`), and it takes no proof. With a key of their own, somebody
// bound nothing wrote an account under any provider's name and any subject
// onto their own row, live at once (answered `OK`; right is a refusal) -- a
// subject somebody else will one day sign in with, taken first, and at a
// connection that provisions later, who the directory finds its user by. The
// account page proved the account by the round trip to the provider and then
// wrote it with the same verb, so the proof was the page's and the verb asked
// for none (#286).
//
// So `IdentityService/Attest` is what everybody holds in its place, and it is
// taken only through a delegation whose app holds it across the tenant: that
// grant is the app vouching for the round trip roster cannot see.
func TestAnAccountAtAProviderIsAttestedThroughAFrontDoor(t *testing.T) {
	const password = "correct horse battery staple"

	b := keyFor(t)
	ctx := t.Context()
	x := require.New(t)

	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	bind := func(who pdid.Id, alias string, methods ...string) {
		r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Alias: alias, Methods: methods}.Build())
		x.NoError(err)
		_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
	}
	keyOf := func(who pdid.Id, alias string, methods ...string) string {
		token, sum, err := keys.Mint(keys.PrefixTenant)
		x.NoError(err)
		_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(), Alias: alias, Secret: sum, Methods: methods,
		}.Build())
		x.NoError(err)

		return token
	}

	// Two apps a person signs in through: a front door the tenant bound the
	// vouching grant across the tenant, and one it bound only a sign-in.
	door := addHolder(t, ctx, b.Server, b.Contoso, "door")
	bind(door, "a-front-door", verify, "/roster.VouchService/Delegate", app.IdentityService_Attest_FullMethodName)
	doorKey := keyOf(door, "door", "/roster.*/*")

	other := addHolder(t, ctx, b.Server, b.Contoso, "other")
	bind(other, "signs-in", verify, "/roster.VouchService/Delegate")
	otherKey := keyOf(other, "other", "/roster.*/*")

	// erin is bound nothing: what she does to her own account is the
	// tenant's `self-service` role.
	erin := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	x.NoError(setFor(ctx, b.Server, erin.Bytes(), password))
	mallory := addHolder(t, ctx, b.Server, b.Contoso, "mallory")

	through := func(t *testing.T, key string) func(req *app.IdentityAttestRequest) (*app.Identity, error) {
		t.Helper()

		res, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, key), app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: erin.Bytes()}.Build(),
			Secret:  []byte(password),
			Methods: []string{app.IdentityService_Attest_FullMethodName},
		}.Build())
		require.NoError(t, err)
		require.NotEmpty(t, res.GetToken())

		return func(req *app.IdentityAttestRequest) (*app.Identity, error) {
			return app.NewIdentityServiceClient(b.Conn).Attest(acting(ctx, key, res.GetToken()), req)
		}
	}
	attest := func(of pdid.Id, provider, subject string) *app.IdentityAttestRequest {
		return app.IdentityAttestRequest_builder{
			Holder: app.HolderRef_builder{Id: of.Bytes()}.Build(), Provider: provider, Subject: subject,
		}.Build()
	}
	accounts := func(of pdid.Id) int {
		n, err := b.Ent.Identity.Query().Where(identity.HasHolderWith(holder.IdEQ(of.Uuid())), identity.DateErasedIsNil()).Count(ctx)
		require.NoError(t, err)

		return n
	}

	hers := keyOf(erin, "her-own", app.IdentityService_Attest_FullMethodName, app.IdentityService_Add_FullMethodName)

	t.Run("with a key of her own, refused", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewIdentityServiceClient(b.Conn).Attest(bearing(ctx, hers), attest(erin, "github", "4242"))
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), "a front door attests")
		x.Zero(accounts(erin))
	})

	t.Run("and Add with it, which was OK, is a method she does not hold", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewIdentityServiceClient(b.Conn).Add(bearing(ctx, hers), app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(), Provider: "github", Subject: "4242",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.Zero(accounts(erin))
	})

	t.Run("through an app that does not hold the grant, refused", func(t *testing.T) {
		x := require.New(t)

		_, err := through(t, otherKey)(attest(erin, "github", "4242"))
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), "a front door attests")
		x.Zero(accounts(erin))
	})

	t.Run("through a front door that holds it, written", func(t *testing.T) {
		x := require.New(t)

		v, err := through(t, doorKey)(attest(erin, "github", "4242"))
		x.NoError(err)
		x.Equal("4242", v.GetSubject())
		x.Equal(1, accounts(erin))
	})

	t.Run("about somebody else, nobody she can see", func(t *testing.T) {
		x := require.New(t)

		_, err := through(t, doorKey)(attest(mallory, "github", "5151"))
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
		x.Zero(accounts(mallory))
	})

	t.Run("a second at one provider, as Add answers it", func(t *testing.T) {
		x := require.New(t)

		_, err := through(t, doorKey)(attest(erin, "github", "4343"))
		x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)
		x.Equal(1, accounts(erin))
	})
}
