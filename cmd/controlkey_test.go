package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestTheControlPlaneAuthenticatesItsOwnKeys.
//
// The control plane is served with `auth.Bearer(keys.Store(control.Ungated,
// nil))`, which is the deployment saying its own keys are what reaches it. They
// were not: the resolver beside that handler is given `s.Control.Ungated`, and
// the control plane's `.Control` is nil -- that nil is what stops the recursion
// -- so `keyed` refused every key with "this deployment has no keys", from the
// plane the keys are in.
//
// Nothing caught it because nothing dialled `GrpcControl`. The port that exists
// to be called by a service was reached only by a cookie, in tests and in the
// admin port beside it.
//
// What it cost is every call a service makes to this plane: an app is told to
// hold a key from `roster key add`, and everything it presented that key to
// here answered `Unauthenticated: who is asking?`. (Introspecting a caller's
// `rt_` is `server.addr`'s TokenService -- this plane cannot see one; what is
// asserted here is that the key gets in at all.)
func TestTheControlPlaneAuthenticatesItsOwnKeys(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, c, _ := adminDeployment(t, nil)

	// A holder on the control plane to attribute the key to, in the owner
	// tenant `init` made.
	ts, err := s.Control.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{Size: 2}.Build())
	x.NoError(err)
	x.Len(ts.GetItems(), 1, "a control plane has one owner")

	svc, err := s.Control.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: ts.GetItems()[0].GetId()}.Build(),
		Alias:  "a-service",
	}.Build())
	x.NoError(err)

	token, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = s.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: svc.GetId()}.Build(),
		Alias:   "for-a-service",
		Secret:  sum,
		Methods: []string{"/payday.TokenService/Introspect"},
	}.Build())
	x.NoError(err)

	g, err := s.GrpcControl(ctx, c)
	x.NoError(err)
	x.NotNil(g)

	conn := pdtest.Serve(t, g)
	as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	// It asks about its own token -- the one thing this plane's TokenService
	// can answer about -- which is enough: what is being asserted is that the
	// caller got in at all.
	res, err := pdpb.NewTokenServiceClient(conn).Introspect(as, pdpb.TokenIntrospectRequest_builder{
		Token: token,
	}.Build())
	x.NoError(err, "the control plane refused a key that is in it")
	x.NotEmpty(res.GetId(), "introspection answered nothing about a key it accepted")
}

// TestACustomersKeyHasNoMeaningAtTheControlPort is the cell beside the one
// above: the port takes a cookie and an rk_, and a customer's rt_ is neither.
//
// `serve.go` decides it in one line -- the control plane's store is built with
// no tenant side -- and the refusal has to be `Unauthenticated`, not a
// resolution to somebody: an rt_ that resolved here would be a customer's
// person standing in the deployment's own rooms.
func TestACustomersKeyHasNoMeaningAtTheControlPort(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, c, _ := adminDeployment(t, nil)

	// A real tenant key, over in the data plane where they live.
	newco, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	who, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: newco.GetId()}.Build(),
		Alias:  "alice",
	}.Build())
	x.NoError(err)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	_, err = s.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.GetId()}.Build(),
		Alias:   "hers",
		Secret:  sum,
		Methods: []string{"/roster.MeService/Get"},
	}.Build())
	x.NoError(err)

	g, err := s.GrpcControl(ctx, c)
	x.NoError(err)

	conn := pdtest.Serve(t, g)
	as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	_, err = app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err),
		"a customer's key was somebody at the operators' port")
}
