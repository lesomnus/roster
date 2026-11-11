package cmd_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/prove"
)

// A stack rebuilt on a transaction is the stack it was built as.
//
// An RPC that writes several rows as one runs them on a transaction of its
// own, and the stack is rebuilt on it (`enttx.Rebind`, `Core.WithDriver`).
// That rebuilt `server/core` from a list of what to carry over, and the list
// had fallen behind the options: the restore grace, the lock `only` takes, and
// whether everybody at the port is an operator stayed behind. Each is asked
// here inside a transaction the test opens, on SQLite and, with
// `PDTEST_POSTGRES`, on PostgreSQL.

// TestARestoreInsideATransactionKeepsTheGrace: `tenant.restore_within` is how
// long after a close a restore may undo it, and a transaction was a way past
// it -- the rebuilt stack had no window, which reads as *no limit*.
func TestARestoreInsideATransactionKeepsTheGrace(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.Tenant.RestoreWithin = time.Hour })

	// Somebody who may restore the whole tenant, made so while it is open.
	as := b.as(ctx, b.ContosoUser, b.Contoso)

	closes(t, ctx, b.Ungated, b.Contoso)
	x.NoError(b.Ent.Tenant.UpdateOneId(b.Contoso.Uuid()).SetDateClosed(time.Now().Add(-2 * time.Hour)).Exec(ctx))
	restore := app.TenantRestoreRequest_builder{Ref: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()}.Build()

	_, err := b.Walled.Tenant().Restore(as, restore)
	x.Equal(codes.FailedPrecondition, status.Code(err), "a close restored after its grace ran out: %v", err)

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	rebound, err := enttx.Rebind(b.Walled, drv)
	x.NoError(err)
	_, err = rebound.Tenant().Restore(as, restore)
	x.Equal(codes.FailedPrecondition, status.Code(err), "a transaction restored a close after its grace ran out: %v", err)
	x.NoError(tx.Rollback())

	v, err := b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
	x.NoError(err)
	x.NotNil(v.DateClosed, "the tenant is open again")
}

// TestTheLastWayInSurvivesTwoTransactionsAtOnce is
// `TestTheLastWayInSurvivesTwoCallersAtOnce` with each erase in a transaction
// of its own: the count `only` takes is a decision only under the write both
// callers make first, and on a rebuilt stack that write was not made.
func TestTheLastWayInSurvivesTwoTransactionsAtOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	door := b.holder(t, ctx, b.Contoso, "front-door")
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "front-door",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: door.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	as := b.as(ctx, door, b.Contoso)

	// One erase, in a transaction of its own, the stack rebuilt on it.
	erase := func(v *app.Identity) {
		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		if err != nil {
			return
		}
		s, err := enttx.Rebind(b.Walled, drv)
		if err != nil {
			_ = tx.Rollback()
			return
		}
		if _, err := s.Identity().Erase(as, app.IdentityRef_builder{Id: v.GetId()}.Build()); err != nil {
			_ = tx.Rollback()
			return
		}
		_ = tx.Commit()
	}

	const people = 40

	lost := 0
	for i := range people {
		who := b.holder(t, ctx, b.Contoso, fmt.Sprintf("person-%d", i))
		vs := []*app.Identity{
			b.identity(t, ctx, who, "github", fmt.Sprintf("gh-%d", i)),
			b.identity(t, ctx, who, "entra", fmt.Sprintf("en-%d", i)),
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, v := range vs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start

				erase(v)
			}()
		}
		close(start)
		wg.Wait()

		left, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{
				Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build()},
		}.Build())
		x.NoError(err)
		if len(left.GetItems()) == 0 {
			lost++
		}
	}

	x.Zero(lost, "%d of %d people were left unable to sign in by two transactions", lost, people)
}

// TestAStackRebuiltOnATransactionIsTheOperatorsStill: the admin port's stack is
// built knowing everybody at it is an operator (`core.Operated`), which is what
// lets an operator name one of the deployment's secrets -- and rebuilt on a
// transaction it forgot, so the same write inside one was refused. Nothing at
// that port opens one today; this is the stack's own promise, asked the way
// payday rebuilds it.
func TestAStackRebuiltOnATransactionIsTheOperatorsStill(t *testing.T) {
	x := require.New(t)
	b := keyedFor(t, nil)
	ctx := t.Context()

	operated, err := cmd.Admin(b.Server)
	x.NoError(err)

	// An operator at that port, reaching one tenant: not a frame over every
	// tenant, so what allows the write is the port's and nothing of theirs.
	as := frame.Into(ctx, frame.New(b.Service, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))
	add := func(s app.Server, name string) error {
		_, err := s.Connection().Add(as, app.ConnectionAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Name:   name, Issuer: "https://idp.example.test", ClientId: "app", SecretRef: "env:IDP_SECRET",
		}.Build())

		return err
	}

	x.NoError(add(operated, "outside"), "the operators' port could not name a deployment secret")

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	rebound, err := enttx.Rebind(operated, drv)
	x.NoError(err)
	x.NoError(add(rebound, "inside"), "rebuilt on a transaction, the operators' port could not name a deployment secret")
	x.NoError(tx.Commit())
}

// TestALockIsTakenOnTheTransactionItIsHanded: `cmd.Locking` and
// `cmd.Releasing` write on the transaction a call hands them, and wrote it into
// the variable every call shares -- so the next call, or a concurrent one,
// wrote on that transaction instead of its own. `go test -race` on PostgreSQL
// found it in `TestTheLastWayInSurvivesTwoCallersAtOnce`; this is the same
// thing without needing two calls to meet.
func TestALockIsTakenOnTheTransactionItIsHanded(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	who := b.holder(t, ctx, b.Contoso, "person")

	lock, release := cmd.Locking(b.Ent), cmd.Releasing(b.Ent)
	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	x.NoError(lock(ctx, drv, who))
	x.NoError(release(ctx, drv, "nobody-holds-this.example"))
	x.NoError(tx.Rollback())

	// Neither of the next is that call's: it is over.
	x.NoError(lock(ctx, nil, who), "a lock was taken on the transaction an earlier call was handed")
	x.NoError(release(ctx, nil, "nobody-holds-this.example"), "a name was let go of on the transaction an earlier call was handed")
}

// TestANameIsNotTakenInsideATransaction: a tenant taking a hostname lets go
// of it wherever another tenant holds it, which is a write past the wall, and
// this layer makes it only for a call that asks for that alone. Inside a
// transaction somebody else opened it refuses -- and says so, rather than that
// it cannot open a transaction, which there was not true.
func TestANameIsNotTakenInsideATransaction(t *testing.T) {
	const name = "contoso.example.com"

	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)

	c := claiming(t, b, b.ContosoUser, b.Contoso, name)
	published[prove.Record+"."+name] = []string{c.GetToken()}

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	rebound, err := enttx.Rebind(b.Walled, drv)
	x.NoError(err)
	_, err = rebound.Host().Add(b.as(ctx, b.ContosoUser, b.Contoso), app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   name,
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.ErrorContains(err, "inside a transaction")
	x.NoError(tx.Rollback())

	// And by itself, the same claim is taken.
	v, err := adding(t, b, b.ContosoUser, b.Contoso, name)
	x.NoError(err)
	x.NotNil(v.GetDateProved())
}
