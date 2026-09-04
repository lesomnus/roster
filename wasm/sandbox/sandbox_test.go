package sandbox_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pdauth "github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/console"
	"github.com/lesomnus/roster/wasm/sandbox"
)

// TestTheSandboxSignsIn is `wasm/main.go` without the browser: the same
// `Build`, the same `Seed`, the same `console.Auth`, and every call made with a
// bare context -- which is what a message port hands a handler, there being no
// HTTP request to carry anything else.
//
// Here because the sandbox stopped signing in once with every other gate
// green, and the browser was the first thing to notice. `ts/e2e/sandbox.spec.ts`
// is the browser half; this is the half that runs in `scripts/test.sh`.
func TestTheSandboxSignsIn(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))
	_, err = cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops", Password: "sandboxed"})
	x.NoError(err)

	op := &sandbox.Operator{}
	who := sandbox.Believe(op)
	a := sandbox.Auth(console.Auth(s.Control.Ungated, s.Control.Ent, s.Sessions), s.Control.Ent, op)

	t.Run("nobody, before", func(t *testing.T) {
		x := require.New(t)

		_, err := who.Handle(ctx)
		x.ErrorIs(err, pdauth.ErrNoCredential)
	})

	t.Run("a wrong password is refused and remembers nobody", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignIn(ctx, app.AuthSignInRequest_builder{Alias: "ops", Password: "not it"}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))

		_, err = who.Handle(ctx)
		x.ErrorIs(err, pdauth.ErrNoCredential)
	})

	t.Run("the right one is the caller from then on", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignIn(ctx, app.AuthSignInRequest_builder{Alias: "ops", Password: "sandboxed"}.Build())
		x.NoError(err)

		id, err := who.Handle(ctx)
		x.NoError(err)
		x.Equal("ops", id.Alias)
		x.False(id.NamesNobody())

		// And the name resolves, through the same resolver the instance
		// serves with, to a frame -- which is the half a parsed name is not.
		f, err := cmd.Resolver(s.Control.Ungated, nil).Resolve(ctx, id)
		x.NoError(err)
		x.NotEqual(pdid.Nil, f.Actor)
	})

	t.Run("and a sign-out forgets", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignOut(ctx, app.AuthSignOutRequest_builder{}.Build())
		x.NoError(err)

		_, err = who.Handle(ctx)
		x.ErrorIs(err, pdauth.ErrNoCredential)
	})
}
