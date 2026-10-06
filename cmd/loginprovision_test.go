package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// What `roster login provision` writes, and where the bound on it is.
//
// # What this was about, and what changed under it
//
// It was written for a defect with one sentence in it: *a delegation is the
// intersection of what the key allows and what the holder may do*, so a key and
// a role that disagree allow the narrower of the two. `enrol: enrolling` put
// `HolderService.Add` on the role and left it off the key, and the first person
// a directory vouched for reached the end of a whole sign-in and was refused at
// the one write that makes them somebody --
//
//	enrol …@…: PermissionDenied: /roster.HolderService/Add: this credential is not for that
//
// Nothing local saw it. `kustomize build` renders, `pd doctor` is about the
// schema, and the flow's own tests write with `Ungated`, which has no key.
//
// #36 moved where that bound is, and the assertion moved with it rather than
// being dropped. The app holds one **deployment** key now and narrows it per
// request with `roster-at`, and `keys.At` answers as the nominated holder with
// `frame.Whole()` -- the key's own method list is not carried through. So the two
// lists are deliberately **different**, and what each one has to be is what this
// pins:
//
//	the key's        the three reads made before the tenant is known
//	the role's       everything a call inside a flow does, widened by `enrolling`
//
// A key as wide as the role would be a key that could do all of it **without**
// naming a tenant, which is the wide frame a `Nomination` exists to take away.
func provisioned(t *testing.T, enrol string) (key, role []string) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	out := t.TempDir()

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Login:   cmd.LoginConfig{Enrol: enrol},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)

	// The name this tenant answers at, which is what `provision` walks: a
	// customer that registered one is a customer this app fronts (#42), so there
	// is no list of tenants anywhere for it to read.
	_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Name:   "contoso.example.com",
	}.Build())
	x.NoError(err)
	s.Close()

	x.NoError(cli.NewCmdLogin(&c).Run(ctx, []string{"provision", "--out", out}))

	// One file and not one per tenant, which is the whole of the change: an
	// `rk_` is the control plane's, so it needs no customer to exist and a first
	// start has no cycle to break.
	b, err := os.ReadFile(filepath.Join(out, "login-app.key"))
	x.NoError(err)
	x.True(strings.HasPrefix(strings.TrimSpace(string(b)), "rk_"),
		"the Login App's key is not a deployment key: %q", string(b))

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	return allowedByKey(t, ctx, s), allowedByRole(t, ctx, s, "contoso")
}

// allowedByKey is what the one deployment key allows, off the control plane.
func allowedByKey(t *testing.T, ctx context.Context, s *cmd.Server) []string {
	t.Helper()
	x := require.New(t)

	vs, err := s.Control.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1, "provision left more than one key, or none")

	return vs.GetItems()[0].GetMethods()
}

// allowedByRole is what the nominated holder may do, and that the name points at
// them.
func allowedByRole(t *testing.T, ctx context.Context, s *cmd.Server, tenant string) []string {
	t.Helper()
	x := require.New(t)

	at := app.TenantRef_builder{Alias: proto.String(tenant)}.Build()
	role, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{
			Slug: app.RoleRefBySlug_builder{Alias: proto.String("login-app"), Tenant: at}.Build(),
		}.Build(),
		Select: app.RoleSelect_builder{Methods: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)

	who, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{Alias: proto.String("login-app"), Tenant: at}.Build(),
		}.Build(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	x.NoError(err)

	// And the nomination, which is what makes any of it reachable: without it a
	// request carrying `roster-at` for this tenant's name is refused outright
	// rather than answered as the key (`server/keys/at.go`). Found by the
	// control-plane holder the key hangs off, which is the one `provision` named.
	borrower, err := cmd.HolderNamed(ctx, s.Control, "login-app")
	x.NoError(err)
	n, err := s.Ungated.Nomination().Get(ctx, app.NominationGetRequest_builder{
		Ref: app.NominationRef_builder{Borrower: app.NominationRefByBorrower_builder{
			Tenant:     at,
			BorrowerId: borrower.Bytes(),
		}.Build()}.Build(),
		Select: app.NominationSelect_builder{ActsAs: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err, "provision nominated nobody for its key in this tenant, so no flow can reach it")
	x.Equal(who.GetId(), n.GetActsAs().GetId(),
		"the tenant nominated somebody other than the holder provision wrote")

	return role.GetMethods()
}

func TestTheProvisionedRoleAllowsWhatThePolicyAsksFor(t *testing.T) {
	const add = "/roster.HolderService/Add"

	t.Run("invited does not make people", func(t *testing.T) {
		x := require.New(t)

		_, role := provisioned(t, "")
		x.NotContains(role, add)

		_, role = provisioned(t, "invited")
		x.NotContains(role, add)
	})

	// `expected` matches somebody an operator entered and makes nobody, so it
	// needs no more than the base list either.
	t.Run("and neither does expected", func(t *testing.T) {
		_, role := provisioned(t, "expected")
		require.NotContains(t, role, add)
	})

	// The one line that widens it, and the defect this file was written for.
	t.Run("enrolling does, because it has to", func(t *testing.T) {
		_, role := provisioned(t, "enrolling")
		require.Contains(t, role, add)
	})

	// And the method that fills a profile, which is held whatever the policy:
	// whether to fill is each tenant's to say (`TenantProfile`), and this is
	// the grant that writes only blanks -- not `Update`, which rewrites.
	t.Run("filling a blank is held always, and rewriting never", func(t *testing.T) {
		x := require.New(t)
		const fill = "/roster.HolderService/Fill"

		key, role := provisioned(t, "")
		x.Contains(role, fill)
		x.NotContains(role, "/roster.HolderService/Update", "the grant that rewrites a profile, given instead")
		x.NotContains(key, fill)
	})
}

// TestTheProvisionedKeyIsNarrowerThanTheRole is the bound that replaced *the two
// lists must match*.
//
// The key may make the three reads that work out whose flow this is, and nothing
// else. Everything a flow actually does is the nominated holder's role, reached
// only by a request that **names a tenant** -- so a key as wide as the role would
// be one that could do all of it with the wide frame a `Nomination` exists to
// take away.
func TestTheProvisionedKeyIsNarrowerThanTheRole(t *testing.T) {
	x := require.New(t)

	key, role := provisioned(t, "enrolling")

	x.ElementsMatch(cli.LoginResolving, key)
	x.NotContains(key, "/roster.HolderService/Add")
	x.NotContains(key, "/roster.VouchService/Verify",
		"the key can verify a password without naming a tenant, which is the frame this removes")

	// And the role is the wider of the two, which is the direction that has to
	// hold: it is reached only through a nomination.
	x.Greater(len(role), len(key))
}

// TestProvisionMigratesBothPlanes is the first start of a fresh deployment, and
// it is here because CI found it and nothing local did.
//
// This command is an **init container**: it runs before the server has opened
// either database, on a volume with no tables. `ready` migrated the data plane,
// which was the whole of what the per-tenant keys touched -- and #36 put the key
// on a control-plane holder, so the first start failed in the init container on
// `no such table: tenant`, the pod never became ready, and what the rig reported
// was `timed out waiting for the condition` about a Deployment.
//
// The other tests here create both schemas first, which is why they were green.
// This one deliberately does not.
func TestProvisionMigratesBothPlanes(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	out := t.TempDir()

	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true},
		},
	}

	// Nothing has created a table on either plane, which is the state this
	// command exists for.
	x.NoError(cli.NewCmdLogin(&c).Run(ctx, []string{"provision", "--out", out}))

	b, err := os.ReadFile(filepath.Join(out, "login-app.key"))
	x.NoError(err)
	x.True(strings.HasPrefix(strings.TrimSpace(string(b)), "rk_"))

	// And no names to nominate on, which is said and not refused: a fresh volume
	// has no customers, and refusing here would be a deployment that cannot come
	// up because it has not come up.
}

// TestProvisionAgainBindsNothingTwiceAndLeavesOtherAppsAlone is two defects the
// walk over names had, found on a running deployment.
//
// It bound the role once per **name** on every start, and nothing refused a
// second identical binding: one holder had sixty-three of them. And it wrote
// the nomination onto each name, which could hold one app -- so any other
// roster-hosted app nominated at the same name lost it at the Login App's next
// restart.
func TestProvisionAgainBindsNothingTwiceAndLeavesOtherAppsAlone(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	out := t.TempDir()

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true}},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))

	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: tn.GetId()}.Build()

	// Two names, which is what multiplied the bindings.
	for _, name := range []string{"one.contoso.example", "two.contoso.example"} {
		_, err := s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: name}.Build())
		x.NoError(err)
	}

	// Another app the roster operator runs, nominated in the same tenant.
	product, err := cmd.HolderNamed(ctx, s.Control, "product")
	x.NoError(err)
	itsHolder, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "product"}.Build())
	x.NoError(err)
	_, err = s.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
		Tenant: at, BorrowerId: product.Bytes(),
		ActsAs: app.HolderRef_builder{Id: itsHolder.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	s.Close()

	for range 3 {
		x.NoError(cli.NewCmdLogin(&c).Run(ctx, []string{"provision", "--out", out}))
	}

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	who, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{Alias: proto.String("login-app"), Tenant: at}.Build(),
		}.Build(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	x.NoError(err)

	bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
		Filters: []*app.BindingFilter{app.BindingFilter_builder{
			Holder: app.HolderRef_builder{Id: who.GetId()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(bs.GetItems(), 1, "three starts over two names bound the role more than once")

	n, err := s.Ungated.Nomination().Get(ctx, app.NominationGetRequest_builder{
		Ref: app.NominationRef_builder{Borrower: app.NominationRefByBorrower_builder{
			Tenant: at, BorrowerId: product.Bytes(),
		}.Build()}.Build(),
		Select: app.NominationSelect_builder{ActsAs: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(itsHolder.GetId(), n.GetActsAs().GetId(), "provision took another app's nomination")
}

// TestAFrontDoorsOwnRowsAreDeclared is the Login App's rows in a tenant: the
// holder, its role, the binding and the nomination `login provision` writes.
//
// They are the configuration's -- written because the deployment turned the
// front door on, and rewritten at every start -- so they are read-only to
// anybody reaching them through a port. A tenant administrator ending the
// nomination from the user console was everybody's sign-in stopping until the
// next deploy; disabling the holder was the same, for good. Turning one off is
// taking it out of the configuration and then erasing what is left as the
// deployment, which is what a shell on the box is.
func TestAFrontDoorsOwnRowsAreDeclared(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true}},
	}
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: tn.GetId()}.Build()
	_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: "contoso.example"}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	x.NoError(cli.NewCmdLogin(&c).Run(ctx, []string{"provision", "--out", t.TempDir()}))

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	who, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String("login-app"), Tenant: at}.Build()}.Build(),
		Select: app.HolderSelect_builder{Labels: proto.Bool(true), DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	role, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String("login-app"), Tenant: at}.Build()}.Build(),
		Select: app.RoleSelect_builder{Labels: proto.Bool(true), DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
		Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who.GetId()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(bs.GetItems(), 1)
	ns, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(ns.GetItems(), 1)

	x.Equal("config: login", who.GetLabels()[cmd.Declared])
	x.Equal("config: login", role.GetLabels()[cmd.Declared])
	x.Equal("config: login", ns.GetItems()[0].GetLabels()[cmd.Declared])

	// The tenant's administrator, at a port: narrowed to their tenant.
	admin, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "boss"}.Build())
	x.NoError(err)
	as := frame.Into(ctx, frame.New(cmd.MustFrom(admin.GetId()), cmd.MustFrom(tn.GetId()), frame.Whole()).WithScope(frame.Only(cmd.MustFrom(tn.GetId()))))

	refused := func(err error, what string) {
		t.Helper()
		x.Equal(codes.FailedPrecondition, status.Code(err), "%s: %v", what, err)
	}

	_, err = s.Ungated.Nomination().Erase(as, app.NominationRef_builder{Id: ns.GetItems()[0].GetId()}.Build())
	refused(err, "ending the Login App's nomination")
	_, err = s.Ungated.Holder().Disable(as, app.HolderDisableRequest_builder{
		Ref: app.HolderRef_builder{Id: who.GetId()}.Build(), DateUpdated: who.GetDateUpdated(),
	}.Build())
	refused(err, "disabling its holder")
	_, err = s.Ungated.Holder().Erase(as, app.HolderRef_builder{Id: who.GetId()}.Build())
	refused(err, "erasing its holder")
	_, err = s.Ungated.Role().Patch(as, app.RolePatchRequest_builder{
		Ref: app.RoleRef_builder{Id: role.GetId()}.Build(), Methods: []string{}, DateUpdated: role.GetDateUpdated(),
	}.Build())
	refused(err, "narrowing its role")
	_, err = s.Ungated.Role().Erase(as, app.RoleRef_builder{Id: role.GetId()}.Build())
	refused(err, "erasing its role")
	_, err = s.Ungated.Binding().Erase(as, app.BindingRef_builder{Id: bs.GetItems()[0].GetId()}.Build())
	refused(err, "erasing its binding")

	// And the deployment, which has the configuration too, may.
	_, err = s.Ungated.Nomination().Erase(ctx, app.NominationRef_builder{Id: ns.GetItems()[0].GetId()}.Build())
	x.NoError(err, "the deployment could not erase what it declared")
}
