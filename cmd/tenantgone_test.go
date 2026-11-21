package cmd_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entconnection "github.com/lesomnus/roster/internal/ent/connection"
	entgroup "github.com/lesomnus/roster/internal/ent/group"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	enthost "github.com/lesomnus/roster/internal/ent/host"
	enthostproof "github.com/lesomnus/roster/internal/ent/hostproof"
	entmaildomain "github.com/lesomnus/roster/internal/ent/maildomain"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	entnomination "github.com/lesomnus/roster/internal/ent/nomination"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	entsite "github.com/lesomnus/roster/internal/ent/site"
	entteam "github.com/lesomnus/roster/internal/ent/team"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// under is how many rows hang off a tenant, by kind: every kind whose row
// names its tenant itself. A destroyed tenant has none of any.
func under(t *testing.T, ctx context.Context, db *ent.Client, in pdid.Id) map[string]int {
	t.Helper()
	x := require.New(t)

	k := in.Uuid()
	n := map[string]int{}
	var err error
	n["Holder"], err = db.Holder.Query().Where(entholder.TenantId(k), entholder.AliasNEQ("")).Count(ctx)
	x.NoError(err)
	n["Site"], err = db.Site.Query().Where(entsite.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["Team"], err = db.Team.Query().Where(entteam.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["Role"], err = db.Role.Query().Where(entrole.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["Group"], err = db.Group.Query().Where(entgroup.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["Host"], err = db.Host.Query().Where(enthost.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["MailDomain"], err = db.MailDomain.Query().Where(entmaildomain.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["HostProof"], err = db.HostProof.Query().Where(enthostproof.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["Nomination"], err = db.Nomination.Query().Where(entnomination.TenantId(k)).Count(ctx)
	x.NoError(err)
	n["Connection"], err = db.Connection.Query().Where(entconnection.TenantId(k)).Count(ctx)
	x.NoError(err)

	return n
}

// TestNothingIsWrittenIntoATenantThatIsGone: a destroyed tenant is a row that
// keeps an identifier for the trail and, for a while, an alias so that nobody
// else is that name. Nothing asked whether a tenant was one before writing
// into it. Nobody of a closed tenant gets in, so the callers left are the
// deployment's own: an operator at `admin.addr`, and a command at the box.
// For both, `HolderService/Add`, `Host.Add`, `Site.Add` and `Role.Add` naming
// a destroyed tenant answered OK, and so did `TenantService/Update`, which
// wrote a name and settings back onto the blank row. A person added there was
// forgotten by the next sweep; a host was not, and went on answering for a
// tenant that is gone.
//
// Right is a refusal that says so, for every kind of row that names its tenant
// and for what a tenant says about itself. The kinds are read off the schema,
// so that the next one added is asked too.
//
// A closed tenant is not this. Closing shuts its people out and destroys
// nothing, and whoever runs the deployment still writes in one, on purpose:
// it is how a customer is put right before it is restored.
func TestNothingIsWrittenIntoATenantThatIsGone(t *testing.T) {
	b := keyedFor(t, nil)
	ctx := t.Context()

	// The two ways in that are left: the box, which is the unwalled server on
	// a pipe with nobody framed (`cli/entity.go`, `pipe`), and `admin.addr`,
	// as an operator who holds everything.
	box := grpc.NewServer()
	app.RegisterServer(box, b.Ungated)

	op := addHolder(t, ctx, b.Control, controlTenant(t, ctx, b.Server), "operator")
	boundOnControl(t, ctx, b.Server, op.Bytes(), "operators", "/roster.*/*")
	_, err := b.Control.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref: app.HolderRef_builder{Id: op.Bytes()}.Build(), Secret: []byte("an operator's own password"),
	}.Build())
	require.NoError(t, err)
	k := signIn(t, b.Server, "operator", "an operator's own password")
	require.NotNil(t, k, "the operator did not sign in")
	admin, err := b.GrpcAdmin(ctx, b.Config)
	require.NoError(t, err)

	ways := []struct {
		name, word string
		conn       grpc.ClientConnInterface
		as         context.Context
	}{
		{"at the box", "box", pdtest.Serve(t, box), ctx},
		{"at admin.addr", "admin", pdtest.Serve(t, admin), metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", k.Name+"="+k.Value))},
	}

	// One write for every kind of row that names its tenant. `who` is somebody
	// of the tenant, for the one kind that also names a person.
	type write func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, who *app.HolderRef) error
	adds := map[string]write{
		"Holder": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewHolderServiceClient(c).Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: name}.Build())
			return err
		},
		"Site": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewSiteServiceClient(c).Add(ctx, app.SiteAddRequest_builder{Tenant: at, Alias: name}.Build())
			return err
		},
		"Team": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewTeamServiceClient(c).Add(ctx, app.TeamAddRequest_builder{Tenant: at, Alias: name}.Build())
			return err
		},
		"Role": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewRoleServiceClient(c).Add(ctx, app.RoleAddRequest_builder{
				Tenant: at, Alias: name, Methods: []string{app.HolderService_Get_FullMethodName},
			}.Build())
			return err
		},
		"Group": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewGroupServiceClient(c).Add(ctx, app.GroupAddRequest_builder{Tenant: at, Alias: name}.Build())
			return err
		},
		"Host": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewHostServiceClient(c).Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: name + ".bolt.example"}.Build())
			return err
		},
		"MailDomain": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewMailDomainServiceClient(c).Add(ctx, app.MailDomainAddRequest_builder{Tenant: at, Name: name + ".mail.bolt.example"}.Build())
			return err
		},
		"HostProof": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewHostProofServiceClient(c).Add(ctx, app.HostProofAddRequest_builder{Tenant: at, Name: name + ".claim.bolt.example"}.Build())
			return err
		},
		"Connection": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, _ *app.HolderRef) error {
			_, err := app.NewConnectionServiceClient(c).Add(ctx, app.ConnectionAddRequest_builder{
				Tenant: at, Name: name, Issuer: "https://" + name + ".idp.example", ClientId: "the-app",
			}.Build())
			return err
		},
		"Nomination": func(c grpc.ClientConnInterface, ctx context.Context, at *app.TenantRef, name string, who *app.HolderRef) error {
			_, err := app.NewNominationServiceClient(c).Add(ctx, app.NominationAddRequest_builder{
				Tenant: at, Name: name, BorrowerId: pdid.New(b.Service.Domain()).Bytes(), ActsAs: who,
			}.Build())
			return err
		},
	}

	// Every kind the schema has, and none this forgot: an `Add` that takes
	// the tenant it writes into.
	t.Run("every kind of row that names its tenant is asked", func(t *testing.T) {
		var named []string
		protoregistry.GlobalFiles.RangeFiles(func(f protoreflect.FileDescriptor) bool {
			if f.Package() != "roster" {
				return true
			}
			for i := range f.Messages().Len() {
				m := f.Messages().Get(i)
				kind, ok := strings.CutSuffix(string(m.Name()), "AddRequest")
				if !ok {
					continue
				}
				if fd := m.Fields().ByName("tenant"); fd != nil && fd.Message() != nil && fd.Message().FullName() == "roster.TenantRef" {
					named = append(named, kind)
				}
			}

			return true
		})
		slices.Sort(named)

		asked := make([]string, 0, len(adds))
		for kind := range adds {
			asked = append(asked, kind)
		}
		slices.Sort(asked)

		require.NotEmpty(t, named, "the walk found no kind at all, so it holds nothing")
		require.Equal(t, named, asked, "a kind of row names its tenant and is not asked here whether that tenant is gone")
	})

	// A customer with a row of every kind, then closed, then destroyed.
	bolt := add(t, ctx, b.Server, "bolt")
	at := app.TenantRef_builder{Id: bolt.Bytes()}.Build()
	_, err = b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref: at, Name: z.Ptr("Bolt Ltd"), DateUpdatedForce: z.Ptr(true),
	}.Build())
	require.NoError(t, err)
	somebody := addHolder(t, ctx, b.Server, bolt, "somebody")
	closes(t, ctx, b.Ungated, bolt)

	t.Run("a closed tenant is still the deployment's to write in", func(t *testing.T) {
		who := app.HolderRef_builder{Id: somebody.Bytes()}.Build()
		for _, w := range ways {
			for kind, write := range adds {
				name := strings.ToLower(kind) + "-" + w.word
				require.NoError(t, write(w.conn, w.as, at, name, who), "%s, %s", kind, w.name)
			}

			v, err := app.NewTenantServiceClient(w.conn).Get(w.as, app.TenantGetRequest_builder{Ref: at}.Build())
			require.NoError(t, err)
			_, err = app.NewTenantServiceClient(w.conn).Update(w.as, app.TenantUpdateRequest_builder{
				Ref: at, Desc: z.Ptr("closed, " + w.name), DateUpdated: v.GetDateUpdated(),
			}.Build())
			require.NoError(t, err, "%s", w.name)
		}
	})

	_, err = forget.DestroyTenant(ctx, b.Ent, bolt, trail.Policy{}, audit.Contents{}, nil)
	require.NoError(t, err)
	for kind, n := range under(t, ctx, b.Ent, bolt) {
		require.Zero(t, n, "a destroyed tenant still has its %s, so this proves nothing", kind)
	}

	for _, w := range ways {
		t.Run("nothing is added under it "+w.name, func(t *testing.T) {
			for kind, write := range adds {
				err := write(w.conn, w.as, at, "late", nil)
				if kind == "Nomination" {
					// It names somebody of the tenant as well, and a
					// destroyed tenant has nobody: refused for that first.
					require.Error(t, err, "%s: written into a tenant that is gone", kind)

					continue
				}
				require.Equal(t, codes.FailedPrecondition, status.Code(err), "%s: %v", kind, err)
				require.ErrorContains(t, err, "destroyed", "%s is refused, and not told why", kind)
			}
			for kind, n := range under(t, ctx, b.Ent, bolt) {
				require.Zero(t, n, "a %s was written into a tenant that is gone", kind)
			}
		})

		t.Run("and it says nothing about itself again "+w.name, func(t *testing.T) {
			x := require.New(t)

			v, err := app.NewTenantServiceClient(w.conn).Get(w.as, app.TenantGetRequest_builder{Ref: at}.Build())
			x.NoError(err)
			_, err = app.NewTenantServiceClient(w.conn).Update(w.as, app.TenantUpdateRequest_builder{
				Ref: at, Name: z.Ptr("Bolt Ltd"), Labels: map[string]string{"tier": "gold"},
				Config:      app.TenantConfig_builder{FrontDoor: "https://account.bolt.example"}.Build(),
				DateUpdated: v.GetDateUpdated(),
			}.Build())
			x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
			x.ErrorContains(err, "destroyed")

			row, err := b.Ent.Tenant.Get(ctx, bolt.Uuid())
			x.NoError(err)
			x.Empty(row.Name, "a name was written back onto a destroyed row")
			x.Empty(row.Labels)
			x.Empty(row.Config.GetFrontDoor())
		})
	}

	// The general write is served at no port, and a command at the box
	// reaches it: `roster tenant patch`.
	t.Run("nor by the general write at the box", func(t *testing.T) {
		x := require.New(t)

		for name, req := range map[string]*app.TenantPatchRequest{
			"name":   app.TenantPatchRequest_builder{Ref: at, Name: z.Ptr("Bolt Ltd"), DateUpdatedForce: z.Ptr(true)}.Build(),
			"desc":   app.TenantPatchRequest_builder{Ref: at, Desc: z.Ptr("back"), DateUpdatedForce: z.Ptr(true)}.Build(),
			"labels": app.TenantPatchRequest_builder{Ref: at, Labels: map[string]string{"tier": "gold"}, DateUpdatedForce: z.Ptr(true)}.Build(),
			"config": app.TenantPatchRequest_builder{
				Ref: at, Config: app.TenantConfig_builder{FrontDoor: "https://account.bolt.example"}.Build(), DateUpdatedForce: z.Ptr(true),
			}.Build(),
		} {
			_, err := app.NewTenantServiceClient(ways[0].conn).Patch(ways[0].as, req)
			x.Equal(codes.FailedPrecondition, status.Code(err), "%s: %v", name, err)
			x.ErrorContains(err, "destroyed", name)
		}
	})

	// What the deployment itself still writes on the row is what a
	// destruction is made of: a hold, which keeps what the trail has of it and
	// stops one that is under way, and the alias given back.
	t.Run("and what ends a tenant is still written on its row", func(t *testing.T) {
		x := require.New(t)

		x.NoError(forget.Hold(ctx, b.Ungated, bolt, "a claim that arrived afterwards"))
		x.NoError(forget.Lift(ctx, b.Ungated, bolt))
		x.NoError(forget.FreeAlias(ctx, b.Ungated, bolt))

		row, err := b.Ent.Tenant.Get(ctx, bolt.Uuid())
		x.NoError(err)
		x.Equal(forget.Gone(bolt), row.Alias)
	})
}

// TestAFileDoesNotBringATenantBack: a `resources:` file is applied at every
// start, and it found its tenant by alias and looked at nothing else. So a
// file that still declared a tenant somebody had closed and destroyed wrote
// the name, the settings, a connection and a host back onto the destroyed
// row: *changed=[@newco] added=[@newco/entra newco.example]*, on a row with no
// people that no sweep visits again. Once its alias was given back, the next
// start made a second `newco` and stopped at the host the first one held, and
// `serve` exits on that.
//
// Right is that a tenant which is closed or destroyed is left exactly as it
// is, with nothing of it or under it written, and that the log says at each
// start which file still declares it. The server starts.
func TestAFileDoesNotBringATenantBack(t *testing.T) {
	const file = `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
    config:
      front_door: https://account.newco.example
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/contoso/v2.0
    client_id: the-app
    secret_ref: env:ENTRA
  - kind: Host
    tenant: newco
    name: newco.example
  - kind: MailDomain
    tenant: newco
    name: newco.example
  - kind: Tenant
    alias: other
    name: Other
`
	x := require.New(t)
	cdrv, cdsn := pdtest.DB(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	x.NoError(entmigrate.NewSchema(b.Control.Drv).Create(ctx))
	paths := declare(t, file)
	rs, err := cmd.ReadResources(paths)
	x.NoError(err)

	got, err := cmd.ApplyResources(ctx, b.Server, rs, false)
	x.NoError(err)
	x.Len(got.Added, 5)

	v, err := b.Ent.Tenant.Query().All(ctx)
	x.NoError(err)
	var newco pdid.Id
	for _, w := range v {
		if w.Alias == "newco" {
			newco = pdid.Id(w.Id)
		}
	}

	// applied is one start: what it did, and what it said.
	applied := func(dry bool) (cmd.Applied, string, error) {
		said := &bytes.Buffer{}
		got, err := cmd.ApplyResources(log.Into(ctx, slog.New(slog.NewJSONHandler(said, nil))), b.Server, rs, dry)

		return got, said.String(), err
	}
	left := []string{"@newco", "@newco/entra", "@newco/newco.example", "@newco/newco.example"}

	// Closed at the box, as a declared tenant is: no port closes one.
	closes(t, ctx, b.Ungated, newco)

	t.Run("closed, it is left as it is", func(t *testing.T) {
		x := require.New(t)

		// Somebody edits the file meanwhile, which a start would write.
		edited, err := cmd.ReadResources(declare(t, strings.ReplaceAll(file, "name: Newco", "name: Newco Ltd")))
		x.NoError(err)
		got, err := cmd.ApplyResources(ctx, b.Server, edited, false)
		x.NoError(err)
		x.Equal(left, got.Left)
		x.Empty(got.Changed, "a closed tenant was written to by its file")

		row, err := b.Ent.Tenant.Get(ctx, newco.Uuid())
		x.NoError(err)
		x.Equal("Newco", row.Name)

		_, said, err := applied(false)
		x.NoError(err)
		x.Contains(said, `"level":"WARN"`)
		x.Contains(said, "closed")
		x.Contains(said, paths[0], "the log does not say which file still declares it")
	})

	_, err = forget.DestroyTenant(ctx, b.Ent, newco, trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)

	t.Run("destroyed, nothing of it or under it is written", func(t *testing.T) {
		x := require.New(t)

		got, said, err := applied(false)
		x.NoError(err, "a file that still declares a destroyed tenant stops the start")
		x.Equal(left, got.Left)
		x.Empty(got.Added, "a file wrote under a tenant that is gone")
		x.Empty(got.Changed, "a file wrote a destroyed tenant back")
		x.Equal([]string{"@other"}, got.Same, "the rest of the file was not applied")

		row, err := b.Ent.Tenant.Get(ctx, newco.Uuid())
		x.NoError(err)
		x.Empty(row.Name)
		x.Empty(row.Labels)
		x.Empty(row.Config.GetFrontDoor())
		for kind, n := range under(t, ctx, b.Ent, newco) {
			x.Zero(n, "a %s was written under a tenant that is gone", kind)
		}

		x.Contains(said, `"level":"WARN"`, "nothing a person watches says the file still declares it")
		x.Contains(said, "destroyed")
		x.Contains(said, "newco")
		x.Contains(said, paths[0], "the log does not say which file to take it out of")

		// And a dry run says the same, and not that it would write.
		got, _, err = applied(true)
		x.NoError(err)
		x.Equal(left, got.Left)
		x.Empty(got.Added)
		x.Empty(got.Changed)
	})

	// The alias given back, a year on or with `--reuse`: the file names
	// nobody, and what it declares is made, as a new tenant. The start is not
	// stopped by a host the destroyed row was handed back.
	t.Run("and once its alias is free the start is not stopped", func(t *testing.T) {
		x := require.New(t)

		x.NoError(forget.FreeAlias(ctx, b.Ungated, newco))
		got, _, err := applied(false)
		x.NoError(err)
		x.Empty(got.Left)
		x.Equal([]string{"@newco", "@newco/entra", "@newco/newco.example", "@newco/newco.example"}, got.Added)

		for kind, n := range under(t, ctx, b.Ent, newco) {
			x.Zero(n, "a %s was written under the tenant that is gone", kind)
		}
	})
}

// TestADeclaredTenantIsNotRenamedAtTheBox: a file finds its tenant by alias.
// A declared tenant's alias is not changed at any port -- `Update` refuses a
// declared row, and never writes an alias -- and was changed at the box, by
// the general write a command there reaches (`roster tenant patch`). The next
// start then found nobody of the old alias, made a second tenant, and stopped
// at the first name both declare.
//
// A command at the box writes a declared row because whoever is there has the
// file too. For an alias that is not enough, since it is the one field the
// file is found by: so it is refused while the row is declared, and said how.
func TestADeclaredTenantIsNotRenamedAtTheBox(t *testing.T) {
	const file = `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
  - kind: Host
    tenant: newco
    name: newco.example
`
	x := require.New(t)
	cdrv, cdsn := pdtest.DB(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	x.NoError(entmigrate.NewSchema(b.Control.Drv).Create(ctx))
	paths := declare(t, file)
	rs, err := cmd.ReadResources(paths)
	x.NoError(err)
	_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
	x.NoError(err)

	rename := func(from, to string) error {
		_, err := b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
			Ref: app.TenantRef_builder{Alias: z.Ptr(from)}.Build(), Alias: z.Ptr(to), DateUpdatedForce: z.Ptr(true),
		}.Build())

		return err
	}

	err = rename("newco", "newco-ltd")
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	x.ErrorContains(err, paths[0], "the refusal does not say which file declares it")

	n, err := b.Ent.Tenant.Query().Count(ctx)
	x.NoError(err)
	got, err := cmd.ApplyResources(ctx, b.Server, rs, false)
	x.NoError(err, "the next start stops")
	x.Empty(got.Added, "the next start made a second tenant")
	now, err := b.Ent.Tenant.Query().Count(ctx)
	x.NoError(err)
	x.Equal(n, now)

	t.Run("its other fields are the box's to write, as they were", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
			Ref: app.TenantRef_builder{Alias: z.Ptr("newco")}.Build(), Desc: z.Ptr("by hand"), DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)

		// And the alias it has is no change.
		x.NoError(rename("newco", "newco"))
	})

	t.Run("and one no file declares is renamed", func(t *testing.T) {
		x := require.New(t)

		x.NoError(rename("contoso", "contoso-ltd"))

		// Which a declared one becomes by the label coming off its row.
		_, err := b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
			Ref:    app.TenantRef_builder{Alias: z.Ptr("newco")}.Build(),
			Labels: map[string]string{"was-declared-in": "resources.yaml"}, DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)
		x.NoError(rename("newco", "newco-ltd"))
	})
}

// TestAFrontDoorIsNotAnsweredInATenantThatIsGone: `roster login provision`,
// and `serve` with the Login App in it, write the app's holder, its role, a
// binding and a nomination in every tenant that has a name. A tenant whose
// destruction is under way still has its names -- they go last -- so it was
// handed a new person, by the deployment itself, at every start. With writes
// into a destroyed tenant refused, the same walk would stop there instead,
// and a provisioner that stops is every tenant's sign-in down. It says the
// tenant is destroyed and passes it by, as it passes one whose rows of that
// name are somebody else's.
func TestAFrontDoorIsNotAnsweredInATenantThatIsGone(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))

	named := func(alias string) pdid.Id {
		tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
		x.NoError(err)
		_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Name: alias + ".example.com",
		}.Build())
		x.NoError(err)

		return mustId(t, tn.GetId())
	}
	contoso, bolt := named("contoso"), named("bolt")

	// A destruction under way: stamped, its people forgotten, and its names
	// still there, which is where one an operator holds waits.
	closes(t, ctx, s.Ungated, bolt)
	x.NoError(s.Ent.Tenant.UpdateOneId(bolt.Uuid()).SetDateDestroyed(time.Now()).Exec(ctx))
	who, err := s.Ent.Holder.Query().Where(entholder.TenantId(bolt.Uuid())).Ids(ctx)
	x.NoError(err)
	for _, h := range who {
		_, err := forget.Forget(ctx, s.Ent, pdid.Id(h), trail.Policy{}, audit.Contents{}, nil)
		x.NoError(err)
	}
	x.NoError(s.Close())

	x.NoError(cli.NewCmdLogin(cfg.New(cmd.Name, &c), &c).Run(ctx, []string{"provision", "--out", t.TempDir()}),
		"one tenant being destroyed, and the Login App is provisioned in none")

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	living := func(in pdid.Id) int {
		n, err := s.Ent.Holder.Query().Where(entholder.TenantId(in.Uuid()), entholder.AliasNEQ(""), entholder.DateErasedIsNil()).Count(ctx)
		x.NoError(err)

		return n
	}
	x.Zero(living(bolt), "somebody was made in a tenant that is being destroyed")
	x.Equal(2, living(contoso), "the tenant beside it was not provisioned: its administrator, and the app")
}
