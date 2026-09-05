package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestARestartKeepsEveryCredential is a deploy, from the callers' side.
//
// Every credential this deployment issues is a row, and every stop is a row or
// a stamp -- which is a design with one consequence worth a test of its own: a
// restart changes **nothing** for anybody. A key minted yesterday
// authenticates today, a console stays signed in across a deploy, and a
// suspension does not lift because the process that wrote it is gone.
//
// The suite proves each credential kind against a live server; almost nothing
// crosses a process boundary with one. The exception was `TestTheCliMints...`,
// and it is why this test exists: the control plane served every rk_ from a
// resolver wired at build time, so whether the wiring survives a **rebuild**
// is a question each port has to answer, not one of them.
func TestARestartKeepsEveryCredential(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s1, c, out := adminDeployment(t, nil)

	// A console signed in, in the first process.
	cookie := signIn(t, s1, "admin", passwordFrom(t, out))
	x.NotNil(cookie)

	// A customer with two people: alice, who stays; bob, who is suspended
	// before the restart and must still be suspended after it.
	newco := add(t, ctx, s1, "newco")
	alice := addHolder(t, ctx, s1, newco, "alice")
	bob := addHolder(t, ctx, s1, newco, "bob")

	rt := func(who pdid.Id, alias string) (string, *app.ApiKeyRef) {
		token, sum, err := keys.Mint(keys.PrefixTenant)
		x.NoError(err)

		v, err := s1.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Alias:   alias,
			Secret:  sum,
			Methods: []string{"/roster.MeService/Get"},
		}.Build())
		x.NoError(err)

		return token, app.ApiKeyRef_builder{Id: v.GetId()}.Build()
	}

	hers, _ := rt(alice, "hers")
	his, _ := rt(bob, "his")

	// And one the deployment took back: revoked rows must stay deleted, since
	// nothing anywhere re-seeds what a restart finds missing.
	gone, expendable := rt(alice, "expendable")

	// A service's key, in the control plane, allowed the call the whole
	// integration is built on.
	svc := addHolder(t, ctx, s1.Control, controlTenant(t, ctx, s1), "portal")
	rk, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = s1.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: svc.Bytes()}.Build(),
		Alias:   "production",
		Secret:  sum,
		Methods: []string{"/payday.TokenService/Introspect"},
	}.Build())
	x.NoError(err)

	// The two stops, written while the first process is alive.
	_, err = s1.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
		Ref: app.HolderRef_builder{Id: bob.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	_, err = s1.Ungated.ApiKey().Erase(ctx, expendable)
	x.NoError(err)

	// The restart. Same files, new everything else.
	x.NoError(s1.Close())

	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s2.Close() })

	data, err := s2.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	dconn := pdtest.Serve(t, data)

	control, err := s2.GrpcControl(ctx, cmd.Config{})
	x.NoError(err)
	cconn := pdtest.Serve(t, control)

	t.Run("a tenant key minted before it", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewMeServiceClient(dconn).Get(bearing(ctx, hers),
			app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("alice", v.GetAlias())
	})

	t.Run("a service key minted before it", func(t *testing.T) {
		x := require.New(t)

		// On both ports it is good for: the data plane, where an app holding
		// it introspects the tokens its callers present, and the control
		// plane, which is the port a deployment's own services call.
		as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+rk)

		v, err := pdpb.NewTokenServiceClient(dconn).Introspect(as,
			pdpb.TokenIntrospectRequest_builder{Token: hers}.Build())
		x.NoError(err, "the deploy signed a production service out")
		x.Equal(alice.Bytes(), v.GetId(), "about the holder, not the key")

		as = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+rk)
		_, err = pdpb.NewTokenServiceClient(cconn).Introspect(as,
			pdpb.TokenIntrospectRequest_builder{Token: rk}.Build())
		x.NoError(err)
	})

	t.Run("a console signed in before it", func(t *testing.T) {
		x := require.New(t)

		as := metadata.NewOutgoingContext(ctx,
			metadata.Pairs("cookie", cookie.Name+"="+cookie.Value))
		v, err := app.NewMeServiceClient(cconn).Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err, "a deploy signed every operator out")
		x.Equal("admin", v.GetAlias())
	})

	t.Run("a suspension written before it", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewMeServiceClient(dconn).Get(bearing(ctx, his),
			app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"a restart lifted a suspension")
	})

	t.Run("and a revocation written before it", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewMeServiceClient(dconn).Get(bearing(ctx, gone),
			app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"a restart resurrected a revoked key")
	})
}
