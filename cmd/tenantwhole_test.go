package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entbinding "github.com/lesomnus/roster/internal/ent/binding"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	enttenant "github.com/lesomnus/roster/internal/ent/tenant"
	app "github.com/lesomnus/roster/rstr"
)

// whatIsOf is what a tenant of an alias has, and whether there is one: its
// people, its roles and the bindings of them. A tenant `Add` made has one
// person, two roles and one binding; one it left part way has fewer.
func whatIsOf(t *testing.T, ctx context.Context, db *ent.Client, alias string) (there bool, people, roles, bindings int) {
	t.Helper()
	x := require.New(t)

	vs, err := db.Tenant.Query().Where(enttenant.AliasEQ(alias)).All(ctx)
	x.NoError(err)
	if len(vs) == 0 {
		return false, 0, 0, 0
	}
	x.Len(vs, 1)

	k := vs[0].Id
	people, err = db.Holder.Query().Where(entholder.TenantId(k)).Count(ctx)
	x.NoError(err)
	roles, err = db.Role.Query().Where(entrole.TenantId(k)).Count(ctx)
	x.NoError(err)
	bindings, err = db.Binding.Query().Where(entbinding.HasRoleWith(entrole.TenantId(k))).Count(ctx)
	x.NoError(err)

	return true, people, roles, bindings
}

// TestATenantMadeAtTheOperatorsPortIsWholeOrNotThere: `Tenant.Add` is five
// writes -- the tenant, who administers it, the role, the binding, and what
// everybody does to their own account -- and they are one transaction where
// the stack was built on a driver to begin one on. The data plane's two stacks
// are. The one `admin.addr` answers through was not, so there the five were
// made one after another, at the port where a customer is made.
//
// An operator who holds `TenantService` and `HolderService` and not everything
// is refused at the third write, since a tenant's first role hands out
// everything. It answered `PermissionDenied` and left the tenant and its
// `admin` with no role and no binding, and a second `Add`, by anybody,
// answered `AlreadyExists`: a tenant nobody could get into whose alias could
// not be used again. Right is nothing left, and an operator who does hold
// everything makes it.
func TestATenantMadeAtTheOperatorsPortIsWholeOrNotThere(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)

	// A second operator, narrower than the first.
	second := addHolder(t, ctx, s.Control, controlTenant(t, ctx, s), "second")
	boundOnControl(t, ctx, s, second.Bytes(), "customers", "/roster.TenantService/*", "/roster.HolderService/*")
	_, err := s.Control.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Holder: app.HolderRef_builder{Id: second.Bytes()}.Build(), Secret: []byte("a second operator's password"),
	}.Build())
	x.NoError(err)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	admin := app.NewTenantServiceClient(pdtest.Serve(t, g))
	as := func(alias, password string) context.Context {
		c := signIn(t, s, alias, password)
		x.NotNil(c, "%s did not sign in", alias)

		return metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))
	}

	_, err = admin.Add(as("second", "a second operator's password"), app.TenantAddRequest_builder{Alias: "halfco", Name: "Half Co"}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

	there, people, roles, bindings := whatIsOf(t, ctx, s.Ent, "halfco")
	x.False(there, "a refusal at the third write left a tenant: %d person(s), %d role(s), %d binding(s)", people, roles, bindings)

	// And made again, by somebody who holds what its first role hands out.
	_, err = admin.Add(as("admin", passwordFrom(t, out)), app.TenantAddRequest_builder{Alias: "halfco", Name: "Half Co"}.Build())
	x.NoError(err, "what a refused Add left is in the way of the next")

	there, people, roles, bindings = whatIsOf(t, ctx, s.Ent, "halfco")
	x.True(there)
	x.Equal([]int{1, 2, 1}, []int{people, roles, bindings}, "a tenant is made with somebody, two roles, and one of them bound")
}

// TestAFilesTenantIsWholeOrNotThere is the same at the other caller of that
// stack: a `resources:` file is applied through it, at every start. A write
// that failed part way -- a database that was busy, a process that stopped --
// left the tenant and its `admin` with no role, and the next start read the
// tenant as there and as the file says it (*same*), so nothing ever finished
// it. Right is nothing left, and the next start makes it.
func TestAFilesTenantIsWholeOrNotThere(t *testing.T) {
	const file = `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
`
	x := require.New(t)
	cdrv, cdsn := pdtest.DB(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	x.NoError(entmigrate.NewSchema(b.Control.Drv).Create(ctx))
	rs, err := cmd.ReadResources(declare(t, file))
	x.NoError(err)

	// The third write is the role, and it fails once.
	busy := true
	b.Ent.Role.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if busy && m.Op().Is(ent.OpCreate) {
				return nil, errors.New("the database was busy")
			}

			return next.Mutate(ctx, m)
		})
	})

	_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
	x.ErrorContains(err, "the database was busy")

	there, people, roles, bindings := whatIsOf(t, ctx, b.Ent, "newco")
	x.False(there, "a start that failed at the third write left a tenant: %d person(s), %d role(s), %d binding(s)", people, roles, bindings)

	busy = false
	got, err := cmd.ApplyResources(ctx, b.Server, rs, false)
	x.NoError(err)
	x.Equal([]string{"@newco"}, got.Added, "the next start did not make it: same=%v changed=%v", got.Same, got.Changed)

	there, people, roles, bindings = whatIsOf(t, ctx, b.Ent, "newco")
	x.True(there)
	x.Equal([]int{1, 2, 1}, []int{people, roles, bindings})
}

// TestTheOperatorsStackWritesAsOneWhatTheDataPlaneDoes: the stack `admin.addr`
// answers through was built without the driver and the lock the data plane's
// two are built on (`core.On`), and every rule of `server/core` that needs one
// either made its writes one at a time there, or refused. A tenant is the
// test above; these are the others, each as it was at that stack.
func TestTheOperatorsStackWritesAsOneWhatTheDataPlaneDoes(t *testing.T) {
	b := keyedFor(t, nil)
	ctx := t.Context()

	operated, err := cmd.Admin(b.Server)
	require.NoError(t, err)

	// An operator at that port, reaching one tenant.
	as := frame.Into(ctx, frame.New(b.Service, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	person := func(t *testing.T, alias string) pdid.Id {
		t.Helper()

		return addHolder(t, ctx, b.Server, b.Contoso, alias)
	}
	link := func(t *testing.T, who pdid.Id, provider, subject string) *app.Identity {
		t.Helper()
		v, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(), Provider: provider, Subject: subject,
		}.Build())
		require.NoError(t, err)

		return v
	}

	for _, v := range []struct{ name, issuer string }{
		{"github", "https://github.com"},
		{"okta", "https://contoso.okta.example"},
	} {
		_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: at, Name: v.name, Issuer: v.issuer, ClientId: "the-app",
		}.Build())
		require.NoError(t, err)
	}

	// `Holder.Provision` is a person, their account and their address: the
	// address was somebody's already, and the person and the account stayed.
	t.Run("somebody a directory would make is whole or not there", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0",
			ClientId: "the-app", SubjectClaim: "oid", Provisions: true,
		}.Build())
		x.NoError(err)
		erin := person(t, "erin")
		_, err = b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(), Address: "erin@contoso.example",
		}.Build())
		x.NoError(err)

		_, err = operated.Holder().Provision(as, app.HolderProvisionRequest_builder{
			Tenant: at, Alias: "gail", Name: "Gail", Provider: "entra",
			Subject: "2b3c4d5e-6f70-8192-a3b4-c5d6e7f8091a", Address: "erin@contoso.example",
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)

		n, err := b.Ent.Holder.Query().Where(entholder.TenantId(b.Contoso.Uuid()), entholder.AliasEQ("gail")).Count(ctx)
		x.NoError(err)
		x.Zero(n, "a refusal at the third write left the person the first one made")
	})

	// An address that leaves one row and arrives on another is three writes,
	// and was refused there outright: *this server cannot open a transaction*.
	t.Run("an address moves", func(t *testing.T) {
		const theirs = "ceo@contoso.example"
		x := require.New(t)

		alice := person(t, "alice")
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Address: theirs,
		}.Build())
		x.NoError(err)
		ceo := person(t, "ceo")
		who := link(t, ceo, "okta", "e1b2c3")

		v, err := operated.Email().Attest(as, app.EmailAttestRequest_builder{
			Holder:    app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
			Address:   theirs,
			VouchedBy: app.IdentityRef_builder{Id: who.GetId()}.Build(),
			Verified:  true,
		}.Build())
		x.NoError(err)
		x.Equal(ceo.Bytes(), v.GetHolder().GetId())
	})

	// And *nobody removes somebody's last way in* is a count, which two
	// operators asking at once each take before either writes: the lock that
	// makes them one caller asking twice was not taken there
	// (`TestTheLastWayInSurvivesTwoCallersAtOnce` is the data plane's).
	t.Run("and two operators at once do not take somebody's last two ways in", func(t *testing.T) {
		x := require.New(t)

		const people = 40

		lost := 0
		for i := range people {
			who := person(t, fmt.Sprintf("person-%d", i))
			vs := []*app.Identity{
				link(t, who, "github", fmt.Sprintf("42%d", i)),
				link(t, who, "okta", fmt.Sprintf("ok-%d", i)),
			}

			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, v := range vs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start

					_, _ = operated.Identity().Erase(as, app.IdentityRef_builder{Id: v.GetId()}.Build())
				}()
			}
			close(start)
			wg.Wait()

			left, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
				Filters: []*app.IdentityFilter{
					app.IdentityFilter_builder{Holder: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build(),
				},
			}.Build())
			x.NoError(err)
			if len(left.GetItems()) == 0 {
				lost++
			}
		}

		x.Zero(lost, "%d of %d people were left unable to sign in", lost, people)
	})
}
