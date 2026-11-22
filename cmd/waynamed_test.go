package cmd_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/vouch/vouchtest"
)

// TestALocalWriteOfSomebodysWayInNamesWhoRanIt: each command at the box that
// writes somebody's way in is refused without `--as` where the deployment has
// a control plane, as the entity commands are, before anything is opened; and
// given it, runs, and each row its write leaves on the access record names
// them, as an entity command's does.
//
// `key add`, `key revoke`, `control key add`, `vouch reset|set|unlock` on
// either plane, `control vouch enrol|prove` and `restore` ran with
// nobody named, and nothing said who did it: a password reset by nobody
// is the gap `--as` exists for. Answered: the write, by nobody; right is the
// refusal the entity commands give, `--as` named as typed, status 2 (#284).
// `restore` asks it whatever it names, a tenant as a person, before it opens
// anything: one rule for one command.
func TestALocalWriteOfSomebodysWayInNamesWhoRanIt(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	// A key to seal an authenticator's seed with, for `control vouch enrol`.
	b.c.Vouch.Keys = []string{"one:" + strings.Repeat("QUFB", 10) + "QUE="}

	for _, alias := range []string{"alice", "bob"} {
		_, _, err := boxRun(t, at(b.c, "admin"), "", "holder", "add", "@newco/"+alias)
		x.NoError(err)
	}
	_, said, err := boxRun(t, at(b.c, "admin"), "", "key", "add", "--tenant", "newco", "--holder", "bob", "--name", "spare", "--allow", "/roster.MeService/Get")
	x.NoError(err)
	key := keyIdSaid(t, said)
	_, _, err = boxRun(t, at(b.c, "admin"), "", "holder", "erase", "@newco/bob")
	x.NoError(err)
	for _, args := range [][]string{{"tenant", "add", "@gone"}, {"tenant", "close", "@gone"}} {
		_, said, err := boxRun(t, at(b.c, "admin"), "", args...)
		x.NoError(err, "%v: %s", args, said)
	}

	const pw = "a-long-enough-password-of-theirs\n"
	var seed []byte
	for _, v := range []struct {
		control bool
		stdin   func() string
		args    []string
	}{
		{false, nil, []string{"key", "add", "--tenant", "newco", "--holder", "alice", "--allow", "/roster.MeService/Get"}},
		{false, nil, []string{"key", "revoke", key}},
		{true, nil, []string{"control", "key", "add", "--allow", "/roster.MeService/Get", "loft"}},
		{false, nil, []string{"vouch", "reset", "@newco/alice"}},
		{false, func() string { return pw }, []string{"vouch", "set", "--password-stdin", "@newco/alice"}},
		{false, nil, []string{"vouch", "unlock", "@newco/alice"}},
		{true, nil, []string{"control", "vouch", "reset", "@admin"}},
		{true, func() string { return pw }, []string{"control", "vouch", "set", "--password-stdin", "@admin"}},
		{true, nil, []string{"control", "vouch", "unlock", "@admin"}},
		{true, nil, []string{"control", "vouch", "enrol", "@admin"}},
		{true, func() string { return vouchtest.CodeBefore(seed) + "\n" }, []string{"control", "vouch", "prove", "--code-stdin", "@admin"}},
		{false, nil, []string{"restore", "@newco/bob"}},
		{false, nil, []string{"restore", "@gone"}},
	} {
		stdin := ""
		if v.stdin != nil {
			stdin = v.stdin()
		}

		// Refused, before anything is opened: nothing is written on either
		// plane.
		before := boxWritten(t, b.c)
		out, _, err := boxRun(t, at(b.c, ""), stdin, v.args...)
		x.ErrorIs(err, cmd.ErrUnnamed, "%v ran with nobody named", v.args)
		x.Equal(2, cli.Status(err), "%v", v.args)
		x.Empty(out, "%v printed something for nobody", v.args)
		x.Equal(before, boxWritten(t, b.c), "%v wrote something for nobody", v.args)

		// Named, it runs, and the record says who.
		seen := boxSeen(t, b.c, v.control)
		out, said, err := boxRun(t, at(b.c, "admin"), stdin, v.args...)
		x.NoError(err, "%v: %s", v.args, said)
		rows := boxRows(t, b.c, v.control, seen)
		// A password, a second factor and a tenant have no field declared
		// personal, so setting, unlocking, enrolling or proving one, or
		// opening a tenant again, may leave no row: whatever row there is
		// names them.
		if !slices.ContainsFunc(v.args, func(a string) bool {
			return a == "set" || a == "unlock" || a == "enrol" || a == "prove" || a == "@gone"
		}) {
			x.NotEmpty(rows, "%v left no row on the access record", v.args)
		}
		for _, r := range rows {
			x.Equal(b.operator.Uuid(), r.ActorId, "%v: the row %s does not name who ran it", v.args, r.Action)
		}

		if v.args[len(v.args)-2] == "enrol" {
			u, err := url.Parse(strings.TrimSpace(out))
			x.NoError(err)
			seed = seedOf(t, u.Query().Get("secret"))
		}
	}
}
