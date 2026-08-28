package cmd_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// The one thing that did not cross replicas, and does now.
//
// The statelessness review, D33, found everything else already externalised --
// sessions, keys, delegations, lockouts, all rows re-read per request -- and
// one exception: a client watching against one process never heard about a
// write that landed on another, on a stream that stayed open and looked
// healthy.
//
// `watch.broker: postgres` is the answer, and it needs no second piece of
// infrastructure: `LISTEN`/`NOTIFY` on the database the rows are already in.

// TestAConsoleWatchingOneReplicaHearsTheOther.
//
// Two servers on one database is two replicas for every purpose this is about.
// Nothing connects them but the database, which is the point.
func TestAConsoleWatchingOneReplicaHearsTheOther(t *testing.T) {
	x := require.New(t)

	dsn := os.Getenv(pdtest.Postgres)
	if dsn == "" {
		t.Skipf("%s is not set; a broker that crosses processes needs the database that carries it", pdtest.Postgres)
	}

	ctx := t.Context()
	drv, at := pdtest.DB(t)
	db := config.DbConfig{Driver: drv, Dsn: at}

	// Named, and with no address of its own: the rows' own database is the
	// answer for this broker rather than a fallback.
	w := config.WatchConfig{Broker: brokerpg.Name}

	one, err := cmd.Build(ctx, cmd.Config{Db: db, Watch: w})
	x.NoError(err)
	t.Cleanup(func() { one.Close() })
	x.NoError(one.Ent.Schema.Create(ctx))

	two, err := cmd.Build(ctx, cmd.Config{Db: db, Watch: w})
	x.NoError(err)
	t.Cleanup(func() { two.Close() })

	// `LISTEN` is a round trip and `Build` answers before it has necessarily
	// landed. A publish that beats it is lost, which is the guarantee and not
	// a defect.
	time.Sleep(500 * time.Millisecond)

	tenant, err := one.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)

	who, err := one.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "watched",
	}.Build())
	x.NoError(err)

	actor := mustId(t, who.GetId())
	at0 := mustId(t, tenant.GetId())

	// Written once, because both servers read the same rows -- which is the
	// premise being tested one level down.
	role, err := one.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: at0.Bytes()}.Build(),
		Alias:   "everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	_, err = one.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: actor.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// The stream, on the first server.
	here := served(t, one)
	as := asOverTheWire(ctx, actor)

	me := app.HolderRef_builder{Id: who.GetId()}.Build()
	c := watching(t, as, here, app.HolderWatchRequest_builder{
		Filters: []*app.HolderFilter{app.HolderFilter_builder{Ref: me}.Build()},
	}.Build())

	arrives(t, c) // the snapshot

	// The write, on the **second** server, over its own connection: what
	// publishes is the interceptor, so a direct call writes the row and tells
	// nobody.
	there := served(t, two)

	_, err = app.NewHolderServiceClient(there).Disable(asOverTheWire(ctx, actor),
		app.HolderDisableRequest_builder{Ref: me}.Build())
	x.NoError(err)

	v := arrives(t, c)
	x.NotNil(v.GetDateDisabled(),
		"a write on the other replica did not reach this stream")
}
