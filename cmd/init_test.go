package cmd_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// inited runs `roster init` the way somebody at a shell does, and answers with
// what it printed and a connection to the deployment it left behind.
//
// The command rather than its parts, because what was wrong was not any one of
// them: every row it wrote was correct and the deployment was still unusable.
func inited(t *testing.T, args ...string) (*cmd.Server, string) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	// Both planes, because there is no other kind: `init` refuses a
	// configuration that names no control database. The switch this took was
	// how a deployment on `auth.Plain` got raised in a test, and `cmd.Build`
	// is what does that now.
	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	out := &bytes.Buffer{}
	k := cmd.NewCmdInit(&c)
	k.Writer = out

	x.NoError(k.Run(ctx, args), "init: %s", out)

	// A second server on the same database, since the command closed its own.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	return s, out.String()
}

// TestInitLeavesADeploymentThatWorks is the thing that was missing, and it is
// deliberately about what the operator can **do** rather than what rows exist.
//
// Before this, `init` wrote a tenant and a holder and printed "sign in as
// @contoso/admin". Every row was right. The admin could call one method --
// `MeService.Get`, which told them they held nothing -- and there was no way
// out, because writing the first role needs a binding that only writing the
// first role could give them.
//
// The plane it is about moved. `init` seeds the control plane and nothing else
// now -- a customer is an operator's act, over `admin.addr`, and
// `cmd/newcustomer_test.go` is that sequence end to end -- so the deadlock this
// was written for is the operator's deadlock, and the binding that has to
// actually work is theirs. The finding is the same one: rows that are all
// correct and a deployment nobody can use.
func TestInitLeavesADeploymentThatWorks(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)

	// The wildcard is said out loud where it is granted, in the words it was
	// granted in. A permission nobody reads about is one nobody remembers is
	// there, and a pattern printed as prose is one nobody can grep for.
	x.Contains(out, "/roster.*/*")
	x.Contains(out, "every Rpc roster serves")

	// And the customer that is not there, which is the half that used to read
	// as its opposite: this printed `sign in as: @contoso/admin`, and a data
	// plane holder gets no password and no key from anywhere here. It was a
	// true sentence only about a deployment on `auth.Plain`, which is the one
	// arrangement this command no longer makes.
	x.NotContains(out, "sign in as: @contoso/admin")
	x.Contains(out, "there are no customers yet")

	n, err := s.Ent.Tenant.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "init seeded a customer nobody asked for")

	// What the operator can do with what they were given, over the port they
	// were given it for.
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c, "the password init printed does not sign in")

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)

	admin := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	t.Run("the operator may make the first customer", func(t *testing.T) {
		x := require.New(t)

		tn, err := app.NewTenantServiceClient(admin).Add(as,
			app.TenantAddRequest_builder{Alias: "newco"}.Build())
		x.NoError(err, "the deployment cannot be administered by the person init named")

		h, err := app.NewHolderServiceClient(admin).Add(as, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Alias:  "admin",
		}.Build())
		x.NoError(err)

		// The write that used to be impossible from outside, and is the whole
		// of why `init` could stop doing it: `mayGrant` compares methods and
		// site rather than tenants, so a binding held in the **control** plane
		// reaches a tenant that did not exist a moment ago.
		r, err := app.NewRoleServiceClient(admin).Add(as, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Alias:   "everything",
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)

		_, err = app.NewBindingServiceClient(admin).Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build())
		x.NoError(err)
	})

	// The pattern itself, not what it expands to.
	//
	// A page evaluates it the same three ways the server does. An expansion
	// would be the methods that exist in **this** binary, so during a rolling
	// deploy two replicas would tell a page two different things about one
	// person.
	t.Run("Me answers with the pattern", func(t *testing.T) {
		x := require.New(t)

		wire := servedControl(t, s)

		v, err := app.NewMeServiceClient(wire).Get(
			metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value)),
			app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("ops", v.GetAlias())
		x.Equal([]string{"/roster.*/*"}, v.GetMethods())
	})
}

// TestInitSeedsAnOperator is the console's bootstrap: somebody in the control
// plane who can sign in, because a console cannot be the thing that creates the
// first person allowed to use it.
func TestInitSeedsAnOperator(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	x.NotNil(s.Control)

	// Shown once, and the only place it will ever be.
	x.Contains(out, "control plane")
	x.Contains(out, "password")
	x.Contains(out, "shown once")

	secret := passwordFrom(t, out)
	x.NotEmpty(secret)
	x.GreaterOrEqual(len(secret), 32)

	// It verifies against the control plane, which is what the console will do.
	v, err := s.Control.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  strPtr("ops"),
				Tenant: app.TenantRef_builder{Alias: strPtr("owner")}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err, "no operator was made")

	res, err := vouch.New(s.Control.Ungated, s.Control.Ungated).Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: v.GetId()}.Build(),
		Secret: []byte(secret),
	}.Build())
	x.NoError(err)
	x.True(res.GetOk(), "the password init printed does not verify")

	t.Run("and a wrong one does not", func(t *testing.T) {
		x := require.New(t)

		res, err := vouch.New(s.Control.Ungated, s.Control.Ungated).Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: v.GetId()}.Build(),
			Secret: []byte(secret + "x"),
		}.Build())
		x.NoError(err)
		x.False(res.GetOk())
	})

	t.Run("the password is not what was stored", func(t *testing.T) {
		x := require.New(t)

		c, err := s.Control.Ent.Credential.Query().First(ctx)
		x.NoError(err)
		x.NotContains(string(c.Secret), secret)
	})
}

// TestInitNeedsAControlPlane is the deployment this command will not make.
//
// It used to make it and print a note: no control plane, so `auth.Plain`, so
// every caller is whoever they type. That is a fine arrangement for a checkout
// and a bad one to be able to grow out of, because growing out of it is not a
// migration.
//
// `MeService.IssueKey` works under `Plain` -- a name is written, a frame is
// built, an `ApiKey` row lands on the data plane -- and nothing reads it,
// because `auth.Bearer` is not in the chain. An expiry is optional, so the row
// stays. Name a control plane afterwards and `auth.Seq` gains
// `keys.Store(control.Ungated, s.Ungated)`: every key minted while nobody was
// checking becomes a working credential, at once, issued by nobody.
//
// So the command refuses rather than warns. `Seed` does not, which is the line:
// a deployment raised by a Go call is a test or the Wasm sandbox, and `Plain`
// is what those are for.
func TestInitNeedsAControlPlane(t *testing.T) {
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}

	out := &bytes.Buffer{}
	k := cmd.NewCmdInit(&c)
	k.Writer = out

	err := k.Run(t.Context(), nil)
	x.Error(err, "a deployment that believes its callers was created without a word")
	x.ErrorContains(err, "control.db.driver",
		"the refusal does not name the field to fill in")

	t.Run("and nothing was written on the way to refusing", func(t *testing.T) {
		x := require.New(t)

		// Before the database is opened, which is what makes running this
		// again after filling the field in a first `init` rather than a
		// second. `init` twice is an error on purpose.
		s, err := cmd.Build(t.Context(), c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })
		x.NoError(s.Ent.Schema.Create(t.Context()))

		n, err := s.Ent.Tenant.Query().Count(t.Context())
		x.NoError(err)
		x.Zero(n, "a tenant was left behind by a command that refused")
	})
}

// TestNobodyGrantsEverythingWhoDoesNotHoldIt is the flag being subject to the
// rule every other grant is.
//
// It cannot be checked as a list -- "everything" is not the methods that exist
// today, which is the whole reason it is a flag -- so it is its own refusal,
// and both ways of handing it out have to make it.
func TestNobodyGrantsEverythingWhoDoesNotHoldIt(t *testing.T) {
	b, ctx := build(t)

	// Somebody who may write and bind roles, and holds nothing else.
	const write = "/roster.RoleService/Add"
	const bind = "/roster.BindingService/Add"
	const patch = "/roster.RoleService/Patch"

	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "admin-ish",
		Methods: []string{write, bind, patch},
	}.Build())
	require.NoError(t, err)
	b.binds(t, b.ContosoUser, mustId(t, r.GetId()), nil)

	as := framed(ctx, b.ContosoUser, b.Contoso)

	t.Run("not by writing such a role", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Role().Add(as, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:   "sneaky",
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("nor by patching one into it", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Walled.Role().Get(as, app.RoleGetRequest_builder{
			Ref: app.RoleRef_builder{Id: r.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		_, err = b.Walled.Role().Patch(as, app.RolePatchRequest_builder{
			Ref:         app.RoleRef_builder{Id: r.GetId()}.Build(),
			Methods:     []string{"/roster.*/*"},
			DateUpdated: v.GetDateUpdated(),
		}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	// The one that an empty list hides. Such a role carries no methods, so a
	// check that looked only at the column would find nothing to refuse and
	// hand out the widest role in the deployment.
	t.Run("nor by binding one somebody else wrote", func(t *testing.T) {
		x := require.New(t)

		w, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:   everythingAlias,
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)

		// The widest role in the deployment is a value in the column somebody
		// is checking. It used to be a flag beside an **empty** column, so a
		// check that read only `methods` found nothing to refuse -- which is
		// the whole reason the flag is gone.
		x.Equal([]string{"/roster.*/*"}, w.GetMethods())

		_, err = b.Walled.Binding().Add(as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: w.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}

const everythingAlias = "everything"

func strPtr(s string) *string { return &s }

// passwordFrom picks the secret out of what init printed, which is also a check
// that it is printed in a shape somebody can copy.
func passwordFrom(t *testing.T, out string) string {
	t.Helper()

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "password "); ok {
			return strings.TrimSpace(v)
		}
	}

	t.Fatalf("no password line in:\n%s", out)

	return ""
}

// framed is a call made straight at a stack, with the frame `gate.Decide`
// would have put there. `ApiKeyService` and these layers are reached in
// process, so there is no wire to carry one.
func framed(ctx context.Context, who pdid.Id, in pdid.Id) context.Context {
	return frame.Into(ctx, frame.New(who, in, frame.Whole()).WithScope(frame.Only(in)))
}

// TestNobodyWidensARoleTheyHold is the hole that was found while asking what
// the first operator should hold.
//
// `mayGrant` was wired to `Role.Add` and `Binding.Add`. `Role.Patch` went
// straight to the sink, so somebody holding only `RoleService/Patch` could add
// any method to any role they could see -- and everybody that role was ever
// granted to gained it, at once, without a binding being touched.
//
// It was unreachable by default, because deny-by-default means nobody holds
// `RoleService/Patch` until somebody grants it. So it opened for whichever
// deployment first delegated role editing, which is the deployment that most
// needed it closed.
func TestNobodyWidensARoleTheyHold(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const patch = "/roster.RoleService/Patch"
	const erase = "/roster.HolderService/Erase"

	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "editor",
		Methods: []string{patch},
	}.Build())
	x.NoError(err)
	b.binds(t, b.ContosoUser, mustId(t, r.GetId()), nil)

	as := framed(ctx, b.ContosoUser, b.Contoso)

	v, err := b.Walled.Role().Get(as, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{Id: r.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	_, err = b.Walled.Role().Patch(as, app.RolePatchRequest_builder{
		Ref:         app.RoleRef_builder{Id: r.GetId()}.Build(),
		Methods:     []string{patch, erase},
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.Error(err, "a role was widened to a method its editor does not hold")
	x.Equal(codes.PermissionDenied, status.Code(err))

	// And what they do hold they may still write, so the refusal is about the
	// method rather than about Patch being closed.
	_, err = b.Walled.Role().Patch(as, app.RolePatchRequest_builder{
		Ref:         app.RoleRef_builder{Id: r.GetId()}.Build(),
		Methods:     []string{patch},
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.NoError(err)
}

// TestAGivenPasswordIsTheOneThatSignsIn is the container's half of `init`.
//
// `roster init` generates a password and prints it once, which is right where
// there is a terminal and useless in an image that has to be up before anybody
// looks. So the image reads its environment and hands it over on a **pipe** —
// there is no `--password` flag and there will not be, because an argument is
// in the shell history and in the process list.
//
// What matters is that the thing typed into the console is the thing the
// environment said, which is one call to check and easy to get subtly wrong.
func TestAGivenPasswordIsTheOneThatSignsIn(t *testing.T) {
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
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	const given = "correct horse battery staple"

	v, err := cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops", Password: given})
	x.NoError(err)
	x.Equal(given, v.Password, "what was handed over is not what came back")

	// And it is what signs in, which is the only claim that matters.
	x.NotNil(signIn(t, s, "ops", given))
	x.Nil(signIn(t, s, "ops", "admin"), "the default signed in over a given one")
}

// TestTheFirstTenantCanBeGivenItsIdentifier.
//
// For the deployment that is not the only one who knows this organisation. An
// app served by this roster anchors its own rows on the identifier a credential
// carries, so the two have to agree about which tenant somebody is in.
//
// **What happens without it is nothing loud**, which is why this exists. Both
// sides come up, somebody signs in, and the app makes a second tenant because
// the identifier it was handed is not one it has -- two rows for one
// organisation, and no error anywhere. It was found that way: an app with its
// tenant written down as a constant, a person who signed in, and a third row in
// the table with a name nobody chose.
func TestTheFirstTenantCanBeGivenItsIdentifier(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	fresh := func(t *testing.T) *cmd.Server {
		t.Helper()

		drv, dsn := pdtest.DB(t)
		s, err := cmd.Build(ctx, cmd.Config{
			Db:    config.DbConfig{Driver: drv, Dsn: dsn},
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
		})
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })

		return s
	}

	// The shape an app writes down: a constant somebody composed, not one
	// roster minted.
	at := pdid.MustParse("00000000-0000-8000-8001-686461790000")

	s := fresh(t)

	v, err := cmd.Seed(ctx, s, cmd.Seeding{
		Tenant: "hday", Holder: "admin", Operator: "ops",
		TenantId: at,
	})
	x.NoError(err)
	x.Equal(at, v.Tenant, "the tenant was minted instead of taken")

	// The row and not just the answer.
	got, err := s.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref: app.TenantRef_builder{Alias: strPtr("hday")}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(at.Bytes(), got.GetId())

	t.Run("and nothing said still mints one", func(t *testing.T) {
		x := require.New(t)

		v, err := cmd.Seed(ctx, fresh(t), cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops"})
		x.NoError(err)
		x.NotEqual(pdid.Nil, v.Tenant)
		x.NotEqual(at, v.Tenant)
	})

	// An identifier of the wrong kind is refused, and by payday's minter rather
	// than by anything written here.
	t.Run("and one that names something else is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := cmd.Seed(ctx, fresh(t), cmd.Seeding{
			Tenant: "contoso", Holder: "admin", Operator: "ops",
			TenantId: pdid.New(2),
		})
		x.Error(err)
	})
}

// TestAnEmptyPasswordIsGeneratedInstead, so that a caller who has none is not
// silently given an empty one.
func TestAnEmptyPasswordIsGeneratedInstead(t *testing.T) {
	x := require.New(t)

	s, out := inited(t)
	x.NotEmpty(passwordFrom(t, out), "nothing was generated")
	_ = s
}

// TestInitLeavesTheTwoFilesTheTutorialNames is `roster init` against DSNs that
// are **paths**, which is what every real first run is and no other test does:
// the suite runs on pdtest's databases, so "two files appeared beside you" --
// the tutorial's own sentence -- was a claim nothing had ever watched happen.
// And the identifier init prints has to be the row it seeded, because writing
// it down is what the output tells an operator to do.
func TestInitLeavesTheTwoFilesTheTutorialNames(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	c := cmd.Config{
		Db: config.DbConfig{
			Driver: "sqlite3",
			Dsn:    "file:" + filepath.Join(dir, "roster.db") + "?_pragma=foreign_keys(1)",
		},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{
			Driver: "sqlite3",
			Dsn:    "file:" + filepath.Join(dir, "roster-control.db") + "?_pragma=foreign_keys(1)",
		}},
	}

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	for _, name := range []string{"roster.db", "roster-control.db"} {
		_, err := os.Stat(filepath.Join(dir, name))
		x.NoError(err, "%s did not appear", name)
	}

	// "holder ops is <id>" -- parsed the way an operator's eye does.
	m := regexp.MustCompile(`holder ops is (\S+)`).FindStringSubmatch(out)
	x.NotNil(m, "init did not print the operator's identifier:\n%s", out)

	id, err := pdid.Parse(m[1])
	x.NoError(err)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	v, err := s.Control.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: id.Bytes()}.Build(),
	}.Build())
	x.NoError(err, "the identifier init printed names nobody")
	x.Equal("ops", v.GetAlias())
}

// TestTheShippedConfigurationIsAFirstRun is `roster.yaml` as it ships, run the
// way the tutorial's first step runs it: copied beside you, `init`, and a
// server built on what came out.
//
// The file is the one artifact no compiler reads. Every field of it can drift
// from the code -- a renamed key, a broker this binary no longer links, a DSN
// pragma the driver stopped accepting -- and the first run of a new user is
// where that is discovered. The listeners' addresses are the one thing not
// exercised: real ports on a CI box are a flake, and which port is a
// deployment's own business anyway. `Ready` is the line `serve` refuses to
// start without, so building and passing it is "serve answers" in everything
// but the bind.
func TestTheShippedConfigurationIsAFirstRun(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	src, err := os.ReadFile("../roster.yaml")
	x.NoError(err)

	dir := t.TempDir()
	x.NoError(os.WriteFile(filepath.Join(dir, "roster.yaml"), src, 0o644))
	t.Chdir(dir) // the DSNs are relative, exactly as shipped

	var c cmd.Config
	root := cmd.Cmd(&c)
	root.Writer = io.Discard

	x.NoError(root.Run(ctx, []string{"--config", "roster.yaml", "init"}),
		"the shipped configuration does not init")

	for _, name := range []string{"roster.db", "roster-control.db"} {
		_, err := os.Stat(filepath.Join(dir, name))
		x.NoError(err, "%s did not appear", name)
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err, "the shipped configuration does not build")
	t.Cleanup(func() { s.Close() })

	x.NoError(s.Ready(ctx, c), "what init wrote is not what serve expects")
	x.NotNil(s.Control, "the shipped file must name a control plane; init would refuse one that does not")
}
