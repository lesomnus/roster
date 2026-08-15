package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// built is roster with two customers in it, and somebody in each.
type built struct {
	*cmd.Server

	Acme     pdid.Id
	AcmeUser pdid.Id
	Hooli    pdid.Id

	// allowed is who has already been given the all-methods role, so that `as`
	// is idempotent the way a caller expects it to be.
	allowed map[pdid.Id]bool
}

func build(t *testing.T) (*built, context.Context) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	// SQLite unless PDTEST_POSTGRES names another. Everything roster generates
	// is SQL, and the two disagree in the directions that hide mistakes.
	drv, dsn := pdtest.DB(t)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	b := &built{Server: s}

	b.Acme = b.tenant(t, ctx, "acme")
	b.Hooli = b.tenant(t, ctx, "hooli")
	b.AcmeUser = b.holder(t, ctx, b.Acme, "someone")

	return b, ctx
}

// served is this app's data plane on a listener that is a channel, dialed.
//
// A helper because [cmd.Server.Grpc] answers with an error now -- a certificate
// that cannot be read is a server that must not start -- and unpacking that at
// twenty call sites would say the same thing twenty times. It takes the server
// rather than being a method so that it reads the same whatever the receiver in
// a given test is called.
func served(t *testing.T, s *cmd.Server, opts ...grpc.ServerOption) *grpc.ClientConn {
	t.Helper()

	g, err := s.Grpc(t.Context(), cmd.Config{}, opts...)
	require.NoError(t, err)

	return pdtest.Serve(t, g)
}

// servedControl is the same for the control plane.
func servedControl(t *testing.T, s *cmd.Server, opts ...grpc.ServerOption) *grpc.ClientConn {
	t.Helper()

	g, err := s.GrpcControl(t.Context(), cmd.Config{}, opts...)
	require.NoError(t, err)

	return pdtest.Serve(t, g)
}

// grpc is this app's served stack, built.
//
// A helper because [cmd.Server.Grpc] answers with an error now -- a certificate
// that cannot be read is a server that must not start -- and unpacking that at
// twenty call sites would say the same thing twenty times.
func (b *built) grpc(t *testing.T, opts ...grpc.ServerOption) *grpc.Server {
	t.Helper()

	g, err := b.Grpc(t.Context(), cmd.Config{}, opts...)
	require.NoError(t, err)

	return g
}

// grpcControl is the same for the control plane.
func (b *built) grpcControl(t *testing.T, opts ...grpc.ServerOption) *grpc.Server {
	t.Helper()

	g, err := b.GrpcControl(t.Context(), cmd.Config{}, opts...)
	require.NoError(t, err)

	return g
}

func (b *built) tenant(t *testing.T, ctx context.Context, alias string) pdid.Id {
	t.Helper()

	v, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

func (b *built) holder(t *testing.T, ctx context.Context, in pdid.Id, alias string) pdid.Id {
	t.Helper()

	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

func (b *built) site(t *testing.T, ctx context.Context, in pdid.Id, alias string) pdid.Id {
	t.Helper()

	v, err := b.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

func (b *built) identity(t *testing.T, ctx context.Context, of pdid.Id, provider, subject string) *app.Identity {
	t.Helper()

	v, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: of.Bytes()}.Build(),
		Provider: provider,
		Subject:  subject,
	}.Build())
	require.NoError(t, err)

	return v
}

// as is a request from somebody who may do things.
//
// It binds them an all-methods role first, because the policy denies by default
// and a test that skipped this would be testing the refusal rather than
// whatever it meant to. `roster init` does the same thing for a real
// deployment's first person.
//
// [built.asNobody] is the other half: somebody real, holding nothing.
func (b *built) as(ctx context.Context, actor, tenant pdid.Id) context.Context {
	b.mayAnything(actor, tenant)

	return b.asNobody(ctx, actor, tenant)
}

// asNobody is a request from somebody who holds no binding at all.
func (b *built) asNobody(ctx context.Context, actor, tenant pdid.Id) context.Context {
	f := frame.New(actor, tenant, frame.Whole()).WithScope(frame.Only(tenant))

	return frame.Into(ctx, f)
}

// mayAnything binds an all-methods role to somebody, once.
//
// Every RPC this app serves, listed off the server's own descriptors rather
// than written out -- a list written by hand is one that is right on the day it
// is written, and a method added tomorrow would silently be denied to every
// test.
func (b *built) mayAnything(actor, tenant pdid.Id) {
	if b.allowed == nil {
		b.allowed = map[pdid.Id]bool{}
	}
	if b.allowed[actor] {
		return
	}
	b.allowed[actor] = true

	ctx := context.Background()

	v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		Alias:   "everything-" + actor.String()[:8],
		Methods: []string{"/roster.*/*"},
	}.Build())
	if err != nil {
		panic(err)
	}

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: v.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: actor.Bytes()}.Build(),
	}.Build())
	if err != nil {
		panic(err)
	}
}

func mustId(t *testing.T, b []byte) pdid.Id {
	t.Helper()

	k, err := pdid.From(b)
	require.NoError(t, err)

	return k
}

// role is a role of this tenant's, made once per alias.
func (b *built) role(t *testing.T, ctx context.Context, alias string, methods ...string) pdid.Id {
	t.Helper()

	v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}
