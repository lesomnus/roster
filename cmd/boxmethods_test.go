package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAWordThatIsNoMethodIsRefused: where a command reads a list of methods
// -- `--allow`, `--administer` -- each is a method or a pattern in
// the form the gate matches a caller's methods by, and a word that could match
// nothing is refused before anything is opened, naming the flag, the word and
// the form.
//
// `SplitMethods` trimmed and split and nothing looked further: `roster key add
// … --allow HolderService/Get` printed a key *allowing 1 method(s)* that calls
// nothing, and `app install --allow not-a-method` a role that may call one.
// Answered: a key; right is a refusal, since a credential that silently does
// not work is worse to debug than being told now.
func TestAWordThatIsNoMethodIsRefused(t *testing.T) {
	x := require.New(t)

	for _, v := range []string{
		"/roster.HolderService/Get",
		"/roster.HolderService/*",
		"/roster.*/*",
		"/roster.*/Get",
		"/*.*/*",
		"/grpc.health.v1.Health/Check",
		"/Unpackaged/Method",
	} {
		got, err := cmd.MethodsOf("--allow", []string{v})
		x.NoError(err, v)
		x.Equal([]string{v}, got)
	}
	got, err := cmd.MethodsOf("--allow", []string{" /roster.HolderService/Get , /roster.MeService/Get", "/roster.TeamService/*"})
	x.NoError(err, "a list wrapped for a runbook")
	x.Equal([]string{"/roster.HolderService/Get", "/roster.MeService/Get", "/roster.TeamService/*"}, got)

	for _, v := range []string{
		"HolderService/Get",
		"roster.HolderService/Get",
		"/roster.HolderService",
		"/roster.HolderService/",
		"//Get",
		"/.HolderService/Get",
		"/roster./Get",
		"/roster.Holder*/Get",
		"/roster.HolderService/Get*",
		"/roster.HolderService/Get/More",
		"/*/Get",
		"/roster.Holder Service/Get",
		"*",
	} {
		_, err := cmd.MethodsOf("--allow", []string{"/roster.MeService/Get", v})
		x.ErrorContains(err, "--allow "+v+": not a method", v)
		x.ErrorContains(err, "/<package>.<Service>/<Method>")
	}

	t.Run("and the commands go through it before anything is opened", func(t *testing.T) {
		x := require.New(t)
		b := atTheBox(t)

		before := boxWritten(t, b.c)
		for _, args := range [][]string{
			{"key", "add", "--tenant", "newco", "--holder", "admin", "--allow", "HolderService/Get"},
			{"control", "key", "add", "--allow", "/roster.MeService/Get,roster.HolderService/Get", "loft"},
			{"app", "install", "--tenant", "newco", "--allow", "not-a-method", "loft"},
			{"app", "install", "--tenant", "newco", "--allow", "/roster.MeService/Get", "--serves", "/roster.Holder*/Get", "loft"},
			{"sign-in", "--at", "http://127.0.0.1:1", "--allow", "MeService/Get"},
		} {
			out, _, err := boxRun(t, at(b.c, "admin"), "", args...)
			x.ErrorContains(err, ": not a method", "%v", args)
			x.Empty(out, "%v", args)
		}
		x.Equal(before, boxWritten(t, b.c), "a word that is no method wrote something")

		for _, args := range [][]string{
			{"me", "issue-key", "--allow", "HolderService/Get"},
			{"vouch", "redeem", "--token-stdin", "--allow", "HolderService/Get"},
		} {
			c := at(b.c, "")
			c.Client.Addr = "127.0.0.1:1"
			c.Client.Insecure = true
			_, _, err := boxRun(t, c, "rl_not-a-link\n", args...)
			x.ErrorContains(err, ": not a method", "%v", args)
		}
	})
}
