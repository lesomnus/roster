package cmd_test

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// entities runs one of the built entity commands the way somebody at a shell
// does, and answers with what it printed.
//
// Through `cmd.Cmd` and not through the command in isolation, because the whole
// question is whether the configuration is there by the time the connection is
// opened -- and what puts it there is a handler on the root.
func entities(t *testing.T, c *cmd.Config, args ...string) (string, error) {
	t.Helper()

	out := &bytes.Buffer{}
	root := cmd.Cmd(c)
	root.Writer = out

	err := root.Run(t.Context(), args)

	return out.String(), err
}

// seeded is a deployment with two tenants and somebody in the first, written
// through the same server the commands will read.
func seeded(t *testing.T) *cmd.Config {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	c := &cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}

	s, err := cmd.Build(ctx, *c)
	x.NoError(err)
	x.NoError(s.Ent.Schema.Create(ctx))

	b := &built{Server: s}
	b.Acme = b.tenant(t, ctx, "acme")
	b.Hooli = b.tenant(t, ctx, "hooli")
	admin := b.holder(t, ctx, b.Acme, "admin")

	// What `roster init` binds: the whole of what this app serves. Without it
	// the admin is somebody who can call one method and be told they hold
	// nothing, which is the state `init` exists to avoid -- and a test that
	// reaches this deployment over the wire would be testing that refusal
	// rather than what it meant to.
	b.mayAnything(admin, b.Acme)

	// Closed, because each command opens a server of its own on this database
	// -- which is the thing being tested.
	x.NoError(s.Close())

	return c
}

// TestTenantLs is what an operator asks first, and the answer is every tenant.
//
// Behind the wall it would be none: the wall narrows a read to the tenants the
// caller may see, and an operator is not in one. These commands run on
// `Server.Ungated` for exactly that reason, and this is the assertion that says
// so rather than leaving it to the comment.
func TestTenantLs(t *testing.T) {
	x := require.New(t)

	c := seeded(t)

	out, err := entities(t, c, "tenant", "ls")
	x.NoError(err)

	x.Contains(out, "acme")
	x.Contains(out, "hooli")
}

// TestHolderGetNamesSomebodyBySlug, which is the form a person types.
func TestHolderGetNamesSomebodyBySlug(t *testing.T) {
	x := require.New(t)

	c := seeded(t)

	out, err := entities(t, c, "holder", "get", "@acme/admin")
	x.NoError(err)
	x.Contains(out, "admin")
}

// TestAnEntityCommandWritesThroughTheWholeStack.
//
// An `add` goes through the layers, the minter and the trail a request goes
// through -- so a row written here is a row written the same way, and reading it
// back is the assertion. A command that reached the ent client directly would be
// a second way to write a row, and the first thing a second way does is disagree
// with the first.
func TestAnEntityCommandWritesThroughTheWholeStack(t *testing.T) {
	x := require.New(t)

	c := seeded(t)

	_, err := entities(t, c, "tenant", "add", "@initech")
	x.NoError(err)

	out, err := entities(t, c, "tenant", "ls")
	x.NoError(err)
	x.Contains(out, "initech")
}

// TestTheCommandsAreWhatTheSchemaDeclared.
//
// Nothing in this repository lists them. `identity` has a `ls` because
// `Identity` declared `list:`; an entity that declared none has no `ls` and
// nobody wrote that down twice.
func TestTheCommandsAreWhatTheSchemaDeclared(t *testing.T) {
	x := require.New(t)

	root := cmd.Cmd(&cmd.Config{})

	at := func(name string) *xli.Command {
		for _, v := range root.Commands {
			if v.Name == name {
				return v
			}
		}

		return nil
	}

	for _, name := range []string{"tenant", "holder", "identity", "team", "site"} {
		x.NotNil(at(name), "%s is not a command", name)
	}

	verbs := []string{}
	for _, v := range at("tenant").Commands {
		verbs = append(verbs, v.Name)
	}
	x.Contains(verbs, "get")
	x.Contains(verbs, "ls")

	// And the ones roster wrote are still there.
	for _, name := range []string{"init", "key", "serve", "config", "version"} {
		x.NotNil(at(name), "%s went missing", name)
	}
}

// TestHelpOpensNoDatabase, which is what makes these safe to have on the root.
//
// The tree is built while the command set is assembled, before a flag has been
// parsed -- so a connection opened then would be one opened by `roster --help`
// and by a shell completion. The configuration here names a driver that does not
// exist, so anything that tried would fail loudly.
func TestHelpOpensNoDatabase(t *testing.T) {
	x := require.New(t)

	c := &cmd.Config{Db: config.DbConfig{Driver: "nothing-of-the-sort", Dsn: "no"}}

	out, err := entities(t, c, "tenant", "ls", "--help")
	x.NoError(err)
	x.Contains(out, "ls")
}

// TestAConfigurationThatCannotBeOpenedIsTheCommandsAnswer, and not the app's
// startup -- which is the other half of opening late.
func TestAConfigurationThatCannotBeOpenedIsTheCommandsAnswer(t *testing.T) {
	x := require.New(t)

	c := &cmd.Config{Db: config.DbConfig{Driver: "nothing-of-the-sort", Dsn: "no"}}

	// Building the root touches nothing.
	root := cmd.Cmd(c)
	x.NotNil(root)

	_, err := entities(t, c, "tenant", "ls")
	x.Error(err)
}

// TestClientAddrChoosesTheWire is the switch, and it is the only thing that
// decides which of the two a command runs against.
//
// Empty is this process, which is the default and what every test above used.
// Set, it is a caller like any other -- so the wall runs, and what comes back
// is what that credential may see.
//
// The assertion is the difference between the two views and not that a call
// succeeded: a tenant key belongs to a holder in one tenant, so over the wire
// it sees one, and locally there is no wall and it sees both. A test that only
// checked the remote call worked would pass against a connector that quietly
// went local.
func TestClientAddrChoosesTheWire(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	b := keyFor(t, listTenants)

	// A second customer, so that "sees one" is a claim with something to be
	// wrong about.
	add(t, ctx, b.Server, "hooli")

	// What the key's holder may do. A key cannot reach past what its holder
	// holds, so the binding is what makes the remote call answer at all.
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{listTenants},
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	token := mintFor(t, ctx, b, b.Who, "a-script", []string{listTenants}, time.Time{})

	c := b.Config
	g, err := b.Grpc(ctx, c)
	x.NoError(err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go g.Serve(lis)
	t.Cleanup(g.Stop)

	// Locally: every tenant, because the server a command builds for itself has
	// no wall on it.
	out, err := entities(t, &c, "tenant", "ls")
	x.NoError(err)
	x.Contains(out, "acme")
	x.Contains(out, "hooli")

	// Over the wire as a key belonging to somebody in acme: acme, and not
	// hooli.
	c.Client = cmd.ClientConfig{
		Addr:     lis.Addr().String(),
		Insecure: true,
		Auth:     cmd.ClientAuthConfig{Scheme: "bearer", Credential: token},
	}

	out, err = entities(t, &c, "tenant", "ls")
	x.NoError(err)
	x.Contains(out, "acme")
	x.NotContains(out, "hooli", "the wall did not run, so this did not go over the wire")
}

const listTenants = "/roster.TenantService/List"

// TestPlainReachesADeploymentWithNoControlPlane is why the scheme is a setting
// and not a constant.
//
// With no `control` block this app serves `auth.Plain`: a caller says who it is
// and is believed. That is a sandbox and not something to serve where anyone
// can reach it -- and it is also what every `roster.yaml` that has not set a
// control plane up is running, which is most of them on a first day. A command
// that could only send `Bearer` would be a command that does not work there.
func TestPlainReachesADeploymentWithNoControlPlane(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seeded(t)

	s, err := cmd.Build(ctx, *c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	g, err := s.Grpc(ctx, *c)
	x.NoError(err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go g.Serve(lis)
	t.Cleanup(g.Stop)

	c.Client = cmd.ClientConfig{
		Addr:     lis.Addr().String(),
		Insecure: true,
		Auth:     cmd.ClientAuthConfig{Scheme: "plain", Credential: "@acme/admin"},
	}

	out, err := entities(t, c, "holder", "ls")
	x.NoError(err)
	x.Contains(out, "admin")

	// And `bearer` against the same server does not, which is the half of the
	// claim that says the setting is doing something.
	c.Client.Auth = cmd.ClientAuthConfig{Scheme: "bearer", Credential: "@acme/admin"}

	_, err = entities(t, c, "holder", "ls")
	x.Error(err)
}

// TestACredentialFileThatIsNotThereIsRefused.
//
// A deployment that mounted a secret and got the path wrong must not fall
// through to calling as nobody: that arrives as a permission problem three
// layers away, about a credential nobody meant to omit.
func TestACredentialFileThatIsNotThereIsRefused(t *testing.T) {
	x := require.New(t)

	c := seeded(t)
	c.Client = cmd.ClientConfig{
		Addr:     "127.0.0.1:1",
		Insecure: true,
		Auth:     cmd.ClientAuthConfig{Scheme: "bearer", CredentialFile: "/nowhere/at/all"},
	}

	_, err := entities(t, c, "tenant", "ls")
	x.ErrorContains(err, "client.auth.credential_file")
}

// TestTheCredentialFileWinsOverTheLiteral, so that a development default in a
// checked-in file is overridden by the mount rather than competing with it.
func TestTheCredentialFileWinsOverTheLiteral(t *testing.T) {
	x := require.New(t)

	at := filepath.Join(t.TempDir(), "credential")
	x.NoError(os.WriteFile(at, []byte("  a-key\n"), 0o600))

	c := cmd.ClientAuthConfig{Scheme: "bearer", Credential: "another-key", CredentialFile: at}

	p, err := c.Provider()
	x.NoError(err)

	// What it actually puts on the wire, since that is the only thing that
	// settles which of the two won -- and it is trimmed, because a file ends in
	// a newline.
	md, ok := metadata.FromOutgoingContext(p.Provide(t.Context()))
	x.True(ok)
	x.Equal([]string{"Bearer a-key"}, md.Get("authorization"))
}

// TestTheSchemeSaysHowTheCredentialIsSent.
//
// roster serves more than one and which it serves depends on the rest of the
// configuration: with a control plane the data plane reads `Bearer` and checks
// an API key, without one it reads `Plain` and believes what the caller writes.
// A command that could only send one would work against half the deployments
// this app supports.
func TestTheSchemeSaysHowTheCredentialIsSent(t *testing.T) {
	sent := func(t *testing.T, c cmd.ClientAuthConfig) []string {
		t.Helper()

		p, err := c.Provider()
		require.NoError(t, err)
		if p == nil {
			return nil
		}

		md, ok := metadata.FromOutgoingContext(p.Provide(t.Context()))
		require.True(t, ok)

		return md.Get("authorization")
	}

	t.Run("bearer is an api key", func(t *testing.T) {
		x := require.New(t)
		x.Equal([]string{"Bearer a-key"},
			sent(t, cmd.ClientAuthConfig{Scheme: "bearer", Credential: "a-key"}))
	})

	t.Run("plain is a slug, and the server believes it", func(t *testing.T) {
		x := require.New(t)
		x.Equal([]string{"Plain @acme/admin"},
			sent(t, cmd.ClientAuthConfig{Scheme: "plain", Credential: "@acme/admin"}))
	})

	t.Run("none sends nothing", func(t *testing.T) {
		x := require.New(t)
		x.Nil(sent(t, cmd.ClientAuthConfig{Scheme: "none"}))
	})

	t.Run("a credential with no scheme is bearer", func(t *testing.T) {
		x := require.New(t)
		x.Equal([]string{"Bearer a-key"},
			sent(t, cmd.ClientAuthConfig{Credential: "a-key"}))
	})

	t.Run("nothing at all sends nothing", func(t *testing.T) {
		x := require.New(t)
		x.Nil(sent(t, cmd.ClientAuthConfig{}))
	})
}

// TestASchemeThatIsNotOneIsRefused, and refused **here** rather than by the far
// end: a typo answers `Unauthenticated` from a server, which reads as a
// credential that is wrong rather than as a word this app does not know.
func TestASchemeThatIsNotOneIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := (cmd.ClientAuthConfig{Scheme: "basic", Credential: "x"}).Provider()
	x.ErrorContains(err, "not one of bearer, plain, none")

	// And the two halves of one that says nothing to send, or nothing to send
	// it as.
	_, err = (cmd.ClientAuthConfig{Scheme: "bearer"}).Provider()
	x.ErrorContains(err, "no credential to send")

	_, err = (cmd.ClientAuthConfig{Scheme: "none", Credential: "x"}).Provider()
	x.ErrorContains(err, "sends none")
}

// TestACredentialWithNowhereToSendItIsRefused.
//
// A `client.auth` block is somebody saying the wire was the intent. Without an
// address there is nowhere to send it, and what would otherwise happen is the
// database read directly while the deployment believes it is calling a server
// -- a mounted secret going unused, with one line on stderr to say so.
func TestACredentialWithNowhereToSendItIsRefused(t *testing.T) {
	x := require.New(t)

	c := seeded(t)
	c.Client = cmd.ClientConfig{
		Auth: cmd.ClientAuthConfig{Scheme: "bearer", Credential: "a-key"},
	}

	_, err := entities(t, c, "tenant", "ls")
	x.ErrorContains(err, "nowhere to send it")

	// And a file that says nothing about a client is left alone: that is the
	// default and it is what every deployment starts as.
	c.Client = cmd.ClientConfig{}

	out, err := entities(t, c, "tenant", "ls")
	x.NoError(err)
	x.Contains(out, "acme")
}

// TestHalReadsTheDatabaseWhateverTheFileSays.
//
// The flag exists for somebody with a shell on the box who wants to look at the
// rows under a deployment configured for the wire. Editing the configuration to
// do that is a change that outlives the look.
//
// It is asserted against an address that answers nothing, so a run that reached
// the wire would fail rather than quietly agree.
func TestHalReadsTheDatabaseWhateverTheFileSays(t *testing.T) {
	x := require.New(t)

	c := seeded(t)
	c.Client = cmd.ClientConfig{
		Addr:     "127.0.0.1:1",
		Insecure: true,
		Auth:     cmd.ClientAuthConfig{Scheme: "bearer", Credential: "a-key"},
	}

	// Without it: the wire, and nothing is listening there.
	_, err := entities(t, c, "tenant", "ls")
	x.Error(err)

	// With it: the database, and the credential is deliberately unused.
	out, err := entities(t, c, "--HAL", "tenant", "ls")
	x.NoError(err)
	x.Contains(out, "acme")
	x.Contains(out, "hooli", "the local one has no wall, which is what it is for")
}
