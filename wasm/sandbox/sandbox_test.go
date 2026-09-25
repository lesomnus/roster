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
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
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
		Vouch:   cmd.VouchConfig{Password: cmd.PasswordConfig{MinLength: 5}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	_, err = cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "admin", Password: "admin"})
	x.NoError(err)

	op := &sandbox.Caller{}
	who := sandbox.Believe(op)
	a := sandbox.Auth(console.Auth(s.Control.Ungated, s.Control.Ent, s.Sessions), sandbox.TheOneTenant(s.Control.Ent), op)

	t.Run("nobody, before", func(t *testing.T) {
		x := require.New(t)

		_, err := who.Handle(ctx)
		x.ErrorIs(err, pdauth.ErrNoCredential)
	})

	t.Run("a wrong password is refused and remembers nobody", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignIn(ctx, app.AuthSignInRequest_builder{Alias: "admin", Password: "not it"}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))

		_, err = who.Handle(ctx)
		x.ErrorIs(err, pdauth.ErrNoCredential)
	})

	t.Run("the right one is the caller from then on", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignIn(ctx, app.AuthSignInRequest_builder{Alias: "admin", Password: "admin"}.Build())
		x.NoError(err)

		id, err := who.Handle(ctx)
		x.NoError(err)
		x.Equal("admin", id.Alias)
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

// TestTheSandboxSignsARosterUserIn is the user console's half of the same
// thing, and it has one more fake in it.
//
// The admin console's sign-in is about the control plane, which has one tenant,
// so nothing has to say which. The data plane has many and the answer is the
// name the browser arrived at -- which a message port does not carry either. So
// `sandbox.ArrivedAt` writes the name and `cmd.Hosted` resolves it, through the
// `Host` row the seed puts there rather than by being told the tenant.
//
// Which is what the third subtest is about: a sandbox whose seed forgot that
// row is refused here, in the words a deployment is refused in, rather than
// signing somebody in against whichever tenant the query happened to reach.
func TestTheSandboxSignsARosterUserIn(t *testing.T) {
	const at = "contoso.roster.example"

	x := require.New(t)
	ctx := context.Background()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		SignIn:  cmd.SignInConfig{Enabled: true},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Vouch:   cmd.VouchConfig{Password: cmd.PasswordConfig{MinLength: 5}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	seeded, err := cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "admin", Password: "admin"})
	x.NoError(err)

	// The two writes `wasm/main.go`'s `seed` makes beyond `Seed`: the name the
	// tenant answers at, and a way in for the person who administers it.
	_, err = s.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: seeded.Holder.Bytes()}.Build(),
		Secret: []byte("admin"),
	}.Build())
	x.NoError(err)

	user := &sandbox.Caller{}
	who := sandbox.Believe(user)
	hosted := sandbox.ArrivedAt(at, cmd.Hosted(s.Ent))
	a := sandbox.Auth(console.Auth(s.Ungated, s.Ent, s.People, console.WithTenant(hosted)), hosted, user)

	t.Run("a name nothing claims is refused, naming it", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignIn(ctx, app.AuthSignInRequest_builder{Alias: "admin", Password: "admin"}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.ErrorContains(err, at)

		_, err = who.Handle(ctx)
		x.ErrorIs(err, pdauth.ErrNoCredential)
	})

	_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: seeded.Tenant.Bytes()}.Build(),
		Name:   at,
	}.Build())
	x.NoError(err)

	t.Run("a wrong password is refused and remembers nobody", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignIn(ctx, app.AuthSignInRequest_builder{Alias: "admin", Password: "not it"}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))

		_, err = who.Handle(ctx)
		x.ErrorIs(err, pdauth.ErrNoCredential)
	})

	t.Run("the tenant's own administrator is the caller from then on", func(t *testing.T) {
		x := require.New(t)

		_, err := a.SignIn(ctx, app.AuthSignInRequest_builder{Alias: "admin", Password: "admin"}.Build())
		x.NoError(err)

		id, err := who.Handle(ctx)
		x.NoError(err)
		x.Equal("admin", id.Alias)
		x.Equal("contoso", id.Tenant)

		// And it is a **data plane** holder: the same alias is an operator on
		// the other database, and the frame this resolves to is the one the
		// wall narrows by.
		f, err := cmd.Resolver(s.Ungated, s.Control.Ungated).Resolve(ctx, id)
		x.NoError(err)
		x.Equal(seeded.Holder, f.Actor)
		x.Equal(seeded.Tenant, f.Tenant)
	})
}
