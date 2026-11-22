package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// TestIssueKeyCallsAKeyWithNoNameDefault: `issue key` with no `--name` calls
// the key `default`, as `key add` and `me issue-key` call theirs.
//
// It sent no name, and the server answered `InvalidArgument: a name for the
// key`, where its two siblings took the same line. Answered: the refusal;
// right is a key called `default`.
func TestIssueKeyCallsAKeyWithNoNameDefault(t *testing.T) {
	const holderGet = "/roster.HolderService/Get"

	x := require.New(t)
	b := cliUp(t, holderGet, "/roster.ApiKeyService/Issue")

	v := stdoutOf(t, cli.Cmd(&b.Hers), "issue", "key", "--allow", holderGet, "@newco/bob")
	x.NotEmpty(v)

	ks, err := b.Server.Ungated.ApiKey().List(t.Context(), app.ApiKeyListRequest_builder{
		Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{
			Holder: app.HolderRef_builder{Id: b.Bob.GetId()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(ks.GetItems(), 1)
	x.Equal("default", ks.GetItems()[0].GetAlias())
}

// TestATenantIsNotAddedUnderAnAliasNobodyChose: `tenant add` with no NAME
// and no alias in REQ is refused, and nothing is made; one with either is
// made, a REQ on stdin included.
//
// It made a customer under an alias roster chose, with its administrator and
// its roles, and status 0. A tenant is not erased, so the slip cost a close
// and a forget. Answered: a tenant; right is a refusal.
func TestATenantIsNotAddedUnderAnAliasNobodyChose(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	b := atTheBox(t)

	count := func() int {
		s, err := cmd.Build(ctx, b.c)
		x.NoError(err)
		defer s.Close()
		n, err := s.Ent.Tenant.Query().Count(ctx)
		x.NoError(err)
		return n
	}

	before := count()
	for _, args := range [][]string{
		{"tenant", "add"},
		{"tenant", "add", `{"name":"Nobody Chose"}`},
		{"tenant", "add", "-"},
	} {
		out, _, err := boxRun(t, at(b.c, "admin"), `{"name":"Nobody Chose"}`, args...)
		x.ErrorContains(err, "NAME: which tenant", "%v", args)
		x.Empty(out)
	}
	x.Equal(before, count(), "a tenant was made under an alias nobody chose")

	for _, v := range []struct {
		stdin string
		args  []string
	}{
		{"", []string{"tenant", "add", "@named"}},
		{"", []string{"tenant", "add", `{"alias":"inreq"}`}},
		{`{"alias":"piped","name":"Piped"}`, []string{"tenant", "add", "-"}},
	} {
		_, said, err := boxRun(t, at(b.c, "admin"), v.stdin, v.args...)
		x.NoError(err, "%v: %s", v.args, said)
	}
	x.Equal(before+3, count())

	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	defer s.Close()
	piped, err := s.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: cmd.TenantRef("piped")}.Build())
	x.NoError(err, "what was piped reached the command whole")
	x.Equal("Piped", piped.GetName())
}

// TestAnIdentifierNobodyHasIsSaidToBeNobodys: `forget`, `forget --dry-run`,
// `restore` and `hold` given an identifier the customers' database does not
// have say so, in a sentence; one it has is taken as before.
//
// `forget --dry-run <identifier>` printed the identifier and ended with status
// 0 whether or not anybody had it, and `forget` and `restore` said `ent:
// holder not found` -- which is all they said of an operator's identifier.
// Answered: the identifier, or the database's words; right is that nobody of
// the customers' database has it.
func TestAnIdentifierNobodyHasIsSaidToBeNobodys(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	for _, args := range [][]string{
		{"forget", "--dry-run", b.operator.String()},
		{"forget", b.operator.String()},
		{"restore", b.operator.String()},
	} {
		out, _, err := boxRun(t, at(b.c, "admin"), "", args...)
		x.ErrorContains(err, b.operator.String()+": nobody of the customers' database has that identifier", "%v", args)
		x.NotContains(err.Error(), "ent:", "%v", args)
		x.Empty(out, "%v", args)
	}
	for _, args := range [][]string{
		{"forget", "--dry-run", b.owner.String()},
		{"hold", "place", "--why", "a claim", b.owner.String()},
	} {
		_, _, err := boxRun(t, at(b.c, "admin"), "", args...)
		x.ErrorContains(err, b.owner.String()+": no tenant of the customers' database has that identifier", "%v", args)
	}

	out, _, err := boxRun(t, at(b.c, "admin"), "", "forget", "--dry-run", b.admin.String())
	x.NoError(err)
	x.Equal(b.admin.String()+"\n", out)
}

// TestAListIsListOrLs: `key` and `hold` list under either name, as an entity
// does.
//
// `roster key ls` and `roster hold list` were *unknown subcommand*, where
// every entity takes both.
func TestAListIsListOrLs(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	for _, args := range [][]string{{"key", "ls"}, {"key", "list"}, {"hold", "ls"}, {"hold", "list"}} {
		_, said, err := boxRun(t, at(b.c, "admin"), "", args...)
		x.NoError(err, "%v: %s", args, said)
	}
}

// TestInitSaysWhoItFound: `init` refused because the first operator is there
// names them as a command names somebody, and says they were erased where
// they were; and `init` given the name of a holder already on the control
// plane says that holder was there before it.
//
// The refusal named an erased operator by thirty-two hexadecimal digits, in no
// form any other command prints; and `init --operator` naming a service a key
// was minted for bound it the role and a password, saying `holder … is` as of
// somebody made now. Answered: the digits, and nothing; right is who, and
// that they were there.
func TestInitSaysWhoItFound(t *testing.T) {
	t.Run("an operator erased", func(t *testing.T) {
		x := require.New(t)
		b := atTheBox(t)

		_, _, err := boxRun(t, at(b.c, "admin"), "", "control", "holder", "erase", "@owner/admin")
		x.NoError(err)

		_, err = initRun(t, b.c)
		x.ErrorIs(err, cmd.ErrOperated)
		x.ErrorContains(err, `"admin" (`+b.operator.String()+") signed in to its admin console, and was erased ")
	})

	t.Run("a holder that was there", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)
		c.Control.Db.Migrate = true

		// Made in process, as `login provision` makes the Login App's before
		// anybody runs `init`: `control key add` names who runs it, and
		// nobody is there to name yet (#284).
		s, err := cmd.Build(t.Context(), c)
		x.NoError(err)
		x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(t.Context()))
		_, err = cmd.HolderNamed(t.Context(), s.Control, "svc")
		x.NoError(err)
		x.NoError(s.Close())

		out, err := initRun(t, c, "--operator", "svc")
		x.NoError(err, out)
		x.Contains(out, "holder svc is ")
		x.Contains(out, "was there before init")

		out, err = initRun(t, seedbed(t))
		x.NoError(err, out)
		x.NotContains(out, "was there before init", "a holder made now is said to have been there")
	})
}
