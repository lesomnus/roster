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

// TestADeploymentKeyIsNarrowedToWhatAHostNominates is #36.
//
// One Login App instance fronting many tenants holds one credential, and the
// roster-hosted shape is an `rk_`: it resolves to a frame with no tenant, the
// policy hands it `frame.Everything`, and what keeps contoso's request out of
// fabrikam's rows is the app's own code. `docs/login.md` refuses an `rk_` front
// door for exactly that.
//
// `Host.acts_as` is what roster narrows it **to**: a request that says which
// name it arrived at is answered as the holder that name's tenant nominated.
func TestADeploymentKeyIsNarrowedToWhatAHostNominates(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, "/roster.*/*")
	ctx := t.Context()

	// The tenant's own: a name it answers at, and the holder it nominates.
	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "contoso.example",
		ActsAs: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// What the nominated holder may do, which is what the frame becomes: their
	// bindings, and nothing the key brought with it.
	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "fronts",
		Methods: []string{"/roster.MeService/Get", "/roster.HolderService/List"},
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// A second tenant, so that "every tenant" and "one tenant" are different
	// answers rather than the same one.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	_ = addHolder(t, ctx, b.Server, fabrikam, "somebody")

	me := app.NewMeServiceClient(b.Conn)
	holders := app.NewHolderServiceClient(b.Conn)

	bearer := metadata.Pairs("authorization", "Bearer "+b.Token)
	wide := metadata.NewOutgoingContext(ctx, bearer)
	at := metadata.NewOutgoingContext(ctx, metadata.Join(bearer,
		metadata.Pairs(keys.HeaderAt, "contoso.example")))

	// Without the header: the key, seeing every tenant -- which is the state
	// this exists to replace.
	vs, err := holders.List(wide, app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 4, "a deployment key saw fewer tenants than every one")

	t.Run("and with it, the holder that tenant nominated", func(t *testing.T) {
		x := require.New(t)

		v, err := me.Get(at, app.MeGetRequest_builder{}.Build())
		x.NoError(err, "the nomination did not answer")
		x.Equal(b.Who.Bytes(), v.GetId())
		x.Equal(b.Contoso.Bytes(), v.GetTenant())
	})

	t.Run("and the wall narrows what it reads", func(t *testing.T) {
		x := require.New(t)

		vs, err := holders.List(at, app.HolderListRequest_builder{}.Build())
		x.NoError(err)

		for _, h := range vs.GetItems() {
			x.Equal(b.Contoso.Bytes(), h.GetTenant().GetId(),
				"a narrowed call read a row from another tenant")
		}
		x.NotEmpty(vs.GetItems())
	})

	// Refused rather than answered as the key, which would hand back the wide
	// frame the caller was trying to narrow -- silently.
	// The nomination takes away rather than gives: the key could read every
	// tenant a moment ago, and what it holds here is one holder's bindings.
	t.Run("and it narrows rather than grants", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewTenantServiceClient(b.Conn).Add(at,
			app.TenantAddRequest_builder{Alias: "newco"}.Build())
		x.Error(err, "a narrowed call did something the holder it borrowed cannot")
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("and a name nothing claims is refused", func(t *testing.T) {
		x := require.New(t)

		nowhere := metadata.NewOutgoingContext(ctx, metadata.Join(bearer,
			metadata.Pairs(keys.HeaderAt, "nobody.example")))

		_, err := me.Get(nowhere, app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("and a name whose tenant nominated nobody is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: fabrikam.Bytes()}.Build(),
			Name:   "fabrikam.example",
		}.Build())
		x.NoError(err)

		unnamed := metadata.NewOutgoingContext(ctx, metadata.Join(bearer,
			metadata.Pairs(keys.HeaderAt, "fabrikam.example")))

		_, err = me.Get(unnamed, app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestAHostNominatesOnlyItsOwnTenantsHolder is the agreement the nomination
// needs, and the shape `agree.go` states for every other row that names two
// tenants: one that named somebody else's would hand a caller a frame in a
// tenant that never agreed to it.
func TestAHostNominatesOnlyItsOwnTenantsHolder(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t)

	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
		Name:   "fabrikam.example",

		// Contoso's.
		ActsAs: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.Error(err, "a host nominated another tenant's holder")
	x.Equal(codes.InvalidArgument, status.Code(err))

	t.Run("and its own is written", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Fabrikam, "front")
		v, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
			Name:   "fabrikam.example",
			ActsAs: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		got, err := b.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
			Ref:    app.HostRef_builder{Id: v.GetId()}.Build(),
			Select: app.HostSelect_builder{ActsAs: app.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(who.Bytes(), got.GetActsAs().GetId())
	})

}
