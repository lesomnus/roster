package cmd_test

import (
	"bytes"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
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
	b.holder(t, ctx, b.Acme, "admin")

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
