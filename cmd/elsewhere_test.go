package cmd_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// elsewhereFile is a file holding `v` and a newline, as a tool writes a key,
// and the path it is at.
func elsewhereFile(t *testing.T, v string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(path, []byte(v+"\n"), 0o600))

	return path
}

// elsewhereRun is the whole tree run with `args`, reading `file` as its
// configuration, as a deployment runs it.
func elsewhereRun(t *testing.T, file string, args ...string) error {
	t.Helper()

	path := filepath.Join(t.TempDir(), "roster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(file), 0o600))
	k := cli.Cmd(&cmd.Config{})
	k.Writer, k.ErrWriter = io.Discard, io.Discard

	return k.Run(t.Context(), append([]string{"--config", path}, args...))
}

// TestAValueKeptSomewhereElseIsWrittenAsTheLoaderWritesIt: an app's keys, its
// seals and its mail's credentials were read by roster's own words for a
// value kept somewhere else, `env:NAME` and `file:PATH`, where every other
// value of the file is written `${env:NAME}` or `${file:/path}` and read by
// the loader. A `${file:}` in one of them was refused at load ("a file is only
// read into a ref.String or a cfg.Secret field"), and the old words were
// taken. Right is the loader's way for each, a tenant key in the map and a
// seal in the list included, and the old words refused by the loader, saying
// how to write one, rather than read as a reference or as the key itself.
func TestAValueKeptSomewhereElseIsWrittenAsTheLoaderWritesIt(t *testing.T) {
	const old = "ROSTER_TEST_ELSEWHERE"

	t.Run("the old words are refused, saying how to write one", func(t *testing.T) {
		for key, file := range map[string]string{
			"account.key":           "account:\n  key: env:" + old + "\n",
			"account.keys.acme":     "account:\n  keys:\n    acme: env:" + old + "\n",
			"account.seal[0]":       "account:\n  seal: [env:" + old + "]\n",
			"account.mail.username": "account:\n  mail:\n    username: env:" + old + "\n",
			"account.mail.password": "account:\n  mail:\n    password: env:" + old + "\n",
			"ldap.key":              "ldap:\n  key: env:" + old + "\n",
			"ldap.keys.acme":        "ldap:\n  keys:\n    acme: env:" + old + "\n",
			"login.key":             "login:\n  key: env:" + old + "\n",
			"login.seal[0]":         "login:\n  seal: [env:" + old + "]\n",
		} {
			err := elsewhereRun(t, file, "config")
			require.ErrorContains(t, err, key+`: "env:`+old+`" would be taken as it is; a reference is written ${env:`+old+`}`, key)
		}
	})

	t.Run("a deployment key in a file is the key", func(t *testing.T) {
		x := require.New(t)
		at := elsewhereFile(t, "rk_ELSEWHERE")

		// Past the key and the prefix it is held to, to the next thing the
		// command was not told: the key read is the file's, a deployment key.
		x.ErrorContains(elsewhereRun(t, "login:\n  roster: 127.0.0.1:1\n  key: ${file:"+at+"}\n", "login", "serve"),
			"login.hydra.admin (--hydra)")

		// And from the flag, one reference as the whole value.
		x.ErrorContains(elsewhereRun(t, "", "login", "serve", "--roster", "127.0.0.1:1", "--deployment-key", "${file:"+at+"}"),
			"login.hydra.admin (--hydra)")

		// What it names, read: an `rt_` is still not this app's.
		at = elsewhereFile(t, "rt_ELSEWHERE")
		x.ErrorContains(elsewhereRun(t, "login:\n  roster: 127.0.0.1:1\n  key: ${file:"+at+"}\n", "login", "serve"),
			"a deployment key")
	})

	t.Run("a seal from the flag is a reference the loader reads", func(t *testing.T) {
		err := elsewhereRun(t, "", "login", "serve", "--roster", "127.0.0.1:1", "--deployment-key", "rk_x",
			"--hydra", "http://127.0.0.1:1", "--seal", "${env:"+old+"_UNSET}")
		require.ErrorContains(t, err, "--seal: ")
		require.ErrorContains(t, err, old+"_UNSET")
	})

	t.Run("a tenant key in the map is the key the reference names", func(t *testing.T) {
		b := cliUp(t, directoryReads...)
		at := elsewhereFile(t, b.Hers.Client.Auth.Credential)

		// newco's key named for another tenant: refused by the directory as a
		// key that cannot see it, which only the token itself can be.
		err := elsewhereRun(t, "ldap:\n  roster: "+b.Hers.Client.Addr+"\n  insecure: true\n  keys:\n    other: ${file:"+at+"}\n",
			"ldap", "serve")
		require.ErrorContains(t, err, "cannot see")
	})
}
