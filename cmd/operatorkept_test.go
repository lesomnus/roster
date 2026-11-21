package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorErasedIsSaidToBeKeptAndNotBroughtBack: `roster restore`,
// `forget` and `hold` open the customers' database and no other, and the
// control plane is built with no forget window. So an operator erased with
// `roster control holder erase` cannot be brought back -- `restore` answers
// that nobody is called that -- and their alias, name, verifier and second
// factor stay for good, with no command that destroys them. Nothing said so:
// the erase answered as a customer's does, where an erase is undone by a
// restore and ended by a forget.
//
// Commands for the control plane add to the command line and are not made
// here. What is made is the sentence, where it is of use: on standard error,
// before the erase is made, with what to do instead for somebody who may come
// back.
func TestAnOperatorErasedIsSaidToBeKeptAndNotBroughtBack(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	b := atTheBox(t)

	// A second operator: somebody who ran the deployment, and leaves.
	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	alice, err := s.Control.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.owner.Bytes()}.Build(), Alias: "ops-alice", Name: "Alice Operator",
	}.Build())
	x.NoError(err)
	who := mustId(t, alice.GetId())
	x.NoError(s.Close())

	out, err := stderrOf(t, cli.Cmd(at(b.c, "admin")), "control", "holder", "erase", who.String())
	x.NoError(err, "%s", out)

	x.Contains(out, "not brought back", "that an operator erased is not brought back is not said: %s", out)
	x.Contains(out, "stays", "that what was theirs stays is not said: %s", out)
	x.Contains(out, "control holder disable", "what to do for somebody who may come back is not said: %s", out)

	// And it is so. The erase was made, and what undoes one for a customer
	// finds nobody.
	s, err = cmd.Build(ctx, b.c)
	x.NoError(err)
	row, err := s.Control.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	x.NoError(s.Close())
	x.NotNil(row.DateErased, "the erase was not made, so this proves nothing")
	x.Equal("Alice Operator", row.Name)

	_, err = stderrOf(t, cli.Cmd(&b.c), "restore", who.String())
	x.Error(err, "an operator was brought back, so the sentence is wrong and this test with it")
}
