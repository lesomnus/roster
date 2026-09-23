package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestTheControlPlaneHasCommandsOfItsOwn is `roster control`: the rows on
// `control.db` reached by a path that says so, rather than by a flag on a
// command about customers.
//
// Before it, nothing in a shell could list the operators: the entity commands
// were built on the data plane alone, and the console was the only view.
func TestTheControlPlaneHasCommandsOfItsOwn(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	t.Run("its holders are the operators, and the data plane's are not", func(t *testing.T) {
		x := require.New(t)

		ours, err := entities(t, &c, "control", "holder", "ls")
		x.NoError(err)
		x.Contains(ours, "admin")

		theirs, err := entities(t, &c, "holder", "ls")
		x.NoError(err)
		x.NotContains(theirs, "admin", "the data plane listed the operator")
	})

	t.Run("and it cannot be given a second tenant", func(t *testing.T) {
		x := require.New(t)

		// `ServiceOf` takes the first tenant it lists and an operator is named
		// by alias alone, so a second one would make both answer about
		// whichever row came back first.
		_, err := entities(t, &c, "control", "tenant", "add", `{"alias":"second"}`)
		x.Error(err)

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		n, err := s.Control.Ent.Tenant.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n)
	})

	t.Run("and a deployment without one is told so", func(t *testing.T) {
		x := require.New(t)

		c := seedbed(t)
		c.Control = cmd.ControlConfig{}

		_, err := entities(t, &c, "control", "holder", "ls")
		x.ErrorContains(err, "no control plane")
	})
}
