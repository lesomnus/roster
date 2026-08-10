package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

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

// as is a request from somebody, with the scope a customer-facing server gives
// them: their own tenant and nothing else.
func (b *built) as(ctx context.Context, actor, tenant pdid.Id) context.Context {
	f := frame.New(actor, tenant, frame.Whole()).WithScope(frame.Only(tenant))

	return frame.Into(ctx, f)
}

func mustId(t *testing.T, b []byte) pdid.Id {
	t.Helper()

	k, err := pdid.From(b)
	require.NoError(t, err)

	return k
}
