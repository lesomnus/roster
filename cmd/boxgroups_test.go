package cmd_test

import (
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// TestAGroupWithNothingAfterItFails: a group of commands typed with nothing
// after it says a subcommand is required, and fails, as `roster hold` does.
//
// `roster key`, `issue`, `vouch`, `trail`, `account`, `ldap`, `scim`, `login`
// and `resources` printed nothing on either stream and ended with status 0
// -- which is what a script whose variable holding the verb is empty runs.
// Answered: nothing, and success; right is the refusal `hold`, `app`,
// `control` and every entity give.
func TestAGroupWithNothingAfterItFails(t *testing.T) {
	x := require.New(t)

	_, held, err := boxRun(t, &cmd.Config{}, "", "hold")
	x.Error(err, "what the others are held to")

	for _, group := range []string{"key", "issue", "vouch", "trail", "account", "ldap", "scim", "login", "resources"} {
		out, said, err := boxRun(t, &cmd.Config{}, "", group)
		x.Error(err, "roster %s, with nothing after it, succeeded", group)
		x.ErrorContains(err, "subcommand", group)
		x.Empty(out, group)
		x.Equal(held, said, group)
	}
}

// boxCommand is the command at `path` of the whole tree, or nil.
func boxCommand(root *xli.Command, path ...string) *xli.Command {
	at := root
	for _, name := range path {
		var next *xli.Command
		for _, v := range at.Commands {
			if v.Name == name {
				next = v
			}
		}
		if next == nil {
			return nil
		}
		at = next
	}

	return at
}

// TestNoCommandIsOfferedThatWorksNowhere: the help offers no command and no
// flag that cannot work in either mode.
//
// The tree is built from the schema, and offered `audit add` on both planes,
// which every server refuses (*the trail is written by what happened, not by
// anybody asking*); `control tenant close` and
// `control tenant restore`, of the one tenant the control plane has, which is
// the deployment; and `vouch enrol --tenant` and `--address`, refused whenever
// they were given. Answered: each offered, and each a refusal when run; right
// is not offered, as `control tenant add` is not.
//
// `ConsentService` `Agree`, `Withdraw`, `Settled` and `EmailService`
// `Claim`, `Confirm` ask who is calling and refuse a call made as nobody,
// which every call at the box is: they work over the wire alone. Under
// `control`, which is local and nothing else, the five were offered and
// refused when run (*this call is made as nobody*; *who is asking?*). Right
// is offered on the data plane's tree, where the wire reaches them, and not
// under `control`.
func TestNoCommandIsOfferedThatWorksNowhere(t *testing.T) {
	x := require.New(t)
	root := cli.Cmd(&cmd.Config{})

	x.NotNil(boxCommand(root, "audit", "list"), "the tree is not where this looks")
	x.NotNil(boxCommand(root, "control", "audit", "list"))
	x.Nil(boxCommand(root, "audit", "add"), "audit add is offered")
	x.Nil(boxCommand(root, "control", "audit", "add"), "control audit add is offered")

	x.NotNil(boxCommand(root, "tenant", "close"), "a customer is closed")
	x.NotNil(boxCommand(root, "tenant", "restore"))
	x.Nil(boxCommand(root, "control", "tenant", "close"), "the deployment's own tenant is offered a close")
	x.Nil(boxCommand(root, "control", "tenant", "restore"))

	for _, path := range [][]string{
		{"consent", "agree"}, {"consent", "withdraw"}, {"consent", "settled"},
		{"email", "claim"}, {"email", "confirm"},
	} {
		x.NotNil(boxCommand(root, path...), "%v is a caller's, and offered where the wire reaches it", path)
		x.Nil(boxCommand(root, append([]string{"control"}, path...)...), "control %v is offered, and is made as nobody", path)
	}
	x.NotNil(boxCommand(root, "control", "consent", "list"), "the rest of `control consent` stays")
	x.NotNil(boxCommand(root, "control", "email", "list"), "the rest of `control email` stays")

	enrol := boxCommand(root, "vouch", "enrol")
	x.NotNil(enrol)
	for _, f := range enrol.Flags {
		x.NotContains([]string{"tenant", "address"}, f.Info().Name, "vouch enrol offers --%s, and refuses it", f.Info().Name)
	}

	_, _, err := boxRun(t, &cmd.Config{}, "", "audit", "add")
	x.ErrorContains(err, "unknown subcommand")
}
