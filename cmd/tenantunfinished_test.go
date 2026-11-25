package cmd_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"uuid"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	enthost "github.com/lesomnus/roster/internal/ent/host"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/front"
)

// swept is one pass of the tenants' sweep: it runs at once, is waited for by
// what it does, and then the loop is stopped.
func (b *built) swept(t *testing.T, ctx context.Context, within, hold time.Duration, done func() bool, why string) {
	t.Helper()

	ctx, stop := context.WithCancel(ctx)
	f := forget.SweepTenants(b.Ent, b.Ungated, within, hold, trail.Policy{}, audit.Contents{}, nil, time.Hour)
	over := make(chan struct{})
	go func() { defer close(over); _ = f(ctx) }()
	require.Eventually(t, done, 30*time.Second, 10*time.Millisecond, why)
	stop()
	<-over
}

// TestADestructionThatStoppedAfterItsLastPersonIsFinished: destroying a tenant
// is its people, each forgotten in a transaction of their own, and then one
// last transaction for every other row and the blanking of its own. The sweep
// found a destruction that had stopped by the people the tenant still had, so
// one whose last transaction failed -- a database that was busy, a process
// that stopped there -- was found by nothing: stamped destroyed for good, with
// its name, its roles and its hosts, and its hostname went on answering *this
// tenant is closed*. `forget.Unfinished` answered nobody and a pass of the
// sweep did nothing; right is the tenant, and a pass that finishes it.
//
// A stamped tenant is found by what is left of it: somebody not forgotten, a
// row that goes with a tenant, or a name on its own row. And one that is
// finished is found by nothing, so a pass does not walk every tenant a
// deployment ever destroyed.
func TestADestructionThatStoppedAfterItsLastPersonIsFinished(t *testing.T) {
	const name = "contoso.example.com"

	x := require.New(t)
	b, ctx := build(t)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: name}.Build())
	x.NoError(err)
	_, err = b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref: at, Name: z.Ptr("Contoso Ltd"), DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	closes(t, ctx, b.Ungated, b.Contoso)

	// The last transaction fails once, at the first row of a kind every tenant
	// has.
	busy := errors.New("the database was busy")
	fails := true
	b.Ent.Role.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if fails && m.Op().Is(ent.OpDelete) {
				return nil, busy
			}

			return next.Mutate(ctx, m)
		})
	})
	_, err = forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{}, nil)
	x.ErrorIs(err, busy)
	fails = false

	type left struct {
		name                 string
		people, roles, hosts int
	}
	leftOf := func() left {
		t.Helper()
		v, err := b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
		x.NoError(err)
		x.NotNil(v.DateDestroyed)

		out := left{name: v.Name}
		out.people, err = b.Ent.Holder.Query().Where(entholder.TenantId(b.Contoso.Uuid()), entholder.AliasNEQ("")).Count(ctx)
		x.NoError(err)
		out.roles, err = b.Ent.Role.Query().Where(entrole.TenantId(b.Contoso.Uuid())).Count(ctx)
		x.NoError(err)
		out.hosts, err = b.Ent.Host.Query().Where(enthost.TenantId(b.Contoso.Uuid())).Count(ctx)
		x.NoError(err)

		return out
	}
	was := leftOf()
	x.Zero(was.people, "somebody is not forgotten yet, which the sweep always found: this proves nothing")
	x.Equal("Contoso Ltd", was.name, "the row was blanked by a transaction that failed")
	x.NotZero(was.roles)
	x.NotZero(was.hosts)

	vs, err := forget.Unfinished(ctx, b.Ent)
	x.NoError(err)
	x.Equal([]pdid.Id{b.Contoso}, vs, "a tenant stamped destroyed, with its name, its roles and its hosts, is found by nothing")

	// With no window, where a pass destroys nothing an operator did not begin.
	b.swept(t, ctx, 0, time.Hour, func() bool { return leftOf() == left{} },
		"a pass of the sweep did not finish a destruction that stopped after its last person")

	_, err = front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: name}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a destroyed tenant's hostname still answers for it: %v", err)

	vs, err = forget.Unfinished(ctx, b.Ent)
	x.NoError(err)
	x.Empty(vs, "a destruction that finished is found again at every pass")

	// And what is written into it afterwards is a thing left of it: a
	// destroyed tenant holds nothing, so the next pass takes that too. Written
	// past the servers, whatever they come to say of such a write.
	x.NoError(b.Ent.Host.Create().
		SetId(uuid.New()).
		SetTenantId(b.Contoso.Uuid()).
		SetName("late." + name).
		SetDesc("").
		SetDateUpdated(time.Now()).
		SetDateCreated(time.Now()).Exec(bare.TrustAdmission(ctx)))
	vs, err = forget.Unfinished(ctx, b.Ent)
	x.NoError(err)
	x.Equal([]pdid.Id{b.Contoso}, vs, "a row written into a destroyed tenant is found by nothing")
	b.swept(t, ctx, 0, time.Hour, func() bool { return leftOf() == left{} },
		"a pass of the sweep left a row written into a destroyed tenant")
}

// TestAnAliasThatBeginsGoneIsFreedOnTheClock: a destroyed tenant keeps its
// alias for `tenant.alias_hold` and is then written `gone-<identifier>`, and
// the sweep told which were already freed by the prefix alone. An alias is
// anybody's to choose, that prefix included, so a tenant somebody named
// `gone-fishing` was never due: it kept the name for good, and only `roster
// forget --reuse` gave it back. Asked what is due once both holds are over,
// `forget.DueAliases` answered `contoso` and not `gone-fishing`; right is both.
//
// What says an alias was freed is the exact name freeing writes, which is that
// tenant's alone.
func TestAnAliasThatBeginsGoneIsFreedOnTheClock(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	fishing := b.tenant(t, ctx, "gone-fishing")
	for _, v := range []pdid.Id{fishing, b.Contoso} {
		closes(t, ctx, b.Ungated, v)
		_, err := forget.DestroyTenant(ctx, b.Ent, v, trail.Policy{}, audit.Contents{}, nil)
		x.NoError(err)
	}
	aliasOf := func(v pdid.Id) string {
		t.Helper()
		row, err := b.Ent.Tenant.Get(ctx, v.Uuid())
		x.NoError(err)

		return row.Alias
	}

	vs, err := forget.DueAliases(ctx, b.Ent, time.Now().Add(time.Minute))
	x.NoError(err)
	x.ElementsMatch([]pdid.Id{fishing, b.Contoso}, vs, "an alias somebody chose is taken for one already freed")

	b.swept(t, ctx, 0, time.Nanosecond, func() bool {
		return aliasOf(fishing) == forget.Gone(fishing) && aliasOf(b.Contoso) == forget.Gone(b.Contoso)
	}, "the hold ran out and a name was not released")

	_, err = b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "gone-fishing"}.Build())
	x.NoError(err, "a released name is free")

	vs, err = forget.DueAliases(ctx, b.Ent, time.Now().Add(time.Minute))
	x.NoError(err)
	x.Empty(vs, "a name released once is due again")
}
