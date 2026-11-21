package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestControlKeyAddAsksBeforeItAltersTables: `control key add` makes the
// control plane's tables only where `control.db.migrate` says this process
// may, as `login provision` asks; where it may not, a database with no tables
// is refused and left as it was.
//
// It migrated the control database whatever the switch said, so a deployment
// that runs its migration as a step of its own had its tables made by the
// first key. Answered: a key, and the tables made; right is the refusal
// `login provision` gives on the same file.
func TestControlKeyAddAsksBeforeItAltersTables(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	c.Db.Migrate = true
	c.Control.Db.Migrate = false

	out, said, err := boxRun(t, &c, "", "control", "key", "add", "--allow", "/roster.MeService/Get", "svc")
	x.Error(err, said)
	x.Empty(out, "a key was printed")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	_, err = s.Control.Ent.Tenant.Query().Count(ctx)
	s.Close()
	x.Error(err, "the control plane's tables were made where control.db.migrate says they may not be")

	t.Run("and where it may, they are made", func(t *testing.T) {
		x := require.New(t)

		c.Control.Db.Migrate = true
		out, said, err := boxRun(t, &c, "", "control", "key", "add", "--allow", "/roster.MeService/Get", "svc")
		x.NoError(err, said)
		x.NotEmpty(out)
	})
}

// TestKeyAddRefusesWhereNothingReadsKeys: `key add` on a deployment with no
// control plane is refused, in the sentence `init` refuses one with, and no
// key is made.
//
// It minted a key nothing reads, with the usual *this is the only time it is
// shown*, and `key list` and `key revoke` then both refused, so the key could
// be listed and ended only through `api-key`. And the day a control plane is
// named every such key works at once, which is why `init` refuses. Answered: a
// key; right is that refusal.
func TestKeyAddRefusesWhereNothingReadsKeys(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seeded(t)

	out, said, err := boxRun(t, c, "", "key", "add", "--tenant", "contoso", "--holder", "admin", "--allow", "/roster.MeService/Get")
	x.ErrorContains(err, "control.db.driver: key add needs a control plane")
	x.ErrorContains(err, "sits inert")
	x.Empty(out, "a key was printed: %s", said)

	s, err := cmd.Build(ctx, *c)
	x.NoError(err)
	defer s.Close()
	n, err := s.Ent.ApiKey.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "a key was written that nothing reads")
}
