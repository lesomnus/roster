package cmd_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// whichKeyRun is the whole tree run with `args` over `file`, and what it said
// on standard error.
func whichKeyRun(t *testing.T, file string, args ...string) (string, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "roster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(file), 0o600))
	var said bytes.Buffer
	k := cli.Cmd(&cmd.Config{})
	k.Writer, k.ErrWriter = io.Discard, &said

	err := k.Run(t.Context(), append([]string{"--config", path}, args...))

	return said.String(), err
}

// TestAnAppsKeysSayWhichKeyEachIs: an app's block named its deployment key
// `key` and its tenant keys `keys`, beside `ldap.tls.key`, which is a third
// key in one block; the flag that takes a tenant's was `--key`, beside
// `--deployment-key`, and the variable per tenant `ROSTER_ACCOUNT_KEY_<ALIAS>`.
// Answered: those names. Right is `deployment_key` and `tenant_keys`,
// `--tenant-key` and `ROSTER_…_TENANT_KEY_<ALIAS>`, and the loader's own
// variables after the keys; the old ones are unknown, as any key, flag or
// variable nothing reads is, with nothing kept for them (#284).
func TestAnAppsKeysSayWhichKeyEachIs(t *testing.T) {
	t.Run("the old keys are keys nothing reads", func(t *testing.T) {
		for key, file := range map[string]string{
			"account.key":  "account:\n  key: rk_x\n",
			"account.keys": "account:\n  keys: { acme: rt_x }\n",
			"ldap.key":     "ldap:\n  key: rk_x\n",
			"ldap.keys":    "ldap:\n  keys: { acme: rt_x }\n",
			"login.key":    "login:\n  key: rk_x\n",
		} {
			_, err := whichKeyRun(t, file, "config")
			require.ErrorContains(t, err, key+": nothing reads this key", key)
		}
	})

	t.Run("and the new ones are what the apps read", func(t *testing.T) {
		x := require.New(t)

		for _, file := range []string{
			"account:\n  deployment_key: rk_x\n  tenant_keys: { acme: rt_x }\n",
			"ldap:\n  deployment_key: rk_x\n  tenant_keys: { acme: rt_x }\n",
			"login:\n  deployment_key: rk_x\n",
		} {
			_, err := whichKeyRun(t, file, "config")
			x.NoError(err, file)
		}

		// Read, and refused for what it holds, which only a key the command
		// reads can be.
		_, err := whichKeyRun(t, "login:\n  roster: 127.0.0.1:1\n  deployment_key: rt_x\n", "login", "serve")
		x.ErrorContains(err, "login.deployment_key (--deployment-key): a deployment key")
	})

	t.Run("the old flag is unknown", func(t *testing.T) {
		for _, app := range []string{"account", "ldap"} {
			_, err := whichKeyRun(t, "", app, "serve", "--key", "acme=rt_x")
			require.ErrorIs(t, err, xli.ErrUnknownFlag, app)
			require.Equal(t, 2, cli.Status(err), app)
		}
	})

	t.Run("and the new one is what the commands read", func(t *testing.T) {
		for _, app := range []string{"account", "ldap"} {
			_, err := whichKeyRun(t, "", app, "serve", "--roster", "127.0.0.1:1", "--tenant-key", "acme")
			require.ErrorContains(t, err, "--tenant-key: a key is given as alias=token", app)
		}
	})

	t.Run("the variables follow the flag and the keys", func(t *testing.T) {
		x := require.New(t)

		// Read: past the refusal of a directory with no key, to the roster
		// it then asks about the key, which is not there.
		t.Setenv("ROSTER_LDAP_TENANT_KEY_ACME", "rt_x")
		said, err := whichKeyRun(t, "", "ldap", "serve", "--roster", "127.0.0.1:1", "--insecure")
		x.Error(err)
		x.NotContains(err.Error(), "no tenant key")
		x.NotContains(said, "ROSTER_LDAP_TENANT_KEY_ACME")

		t.Setenv("ROSTER_LOGIN_DEPLOYMENT_KEY", "rt_x")
		_, err = whichKeyRun(t, "", "login", "serve", "--roster", "127.0.0.1:1")
		x.ErrorContains(err, "a deployment key")
	})

	t.Run("and the old ones are said to be read by nothing", func(t *testing.T) {
		x := require.New(t)

		t.Setenv("ROSTER_LDAP_KEY_ACME", "rt_x")
		t.Setenv("ROSTER_ACCOUNT_KEY_ACME", "rt_x")
		t.Setenv("ROSTER_LOGIN_KEY", "rk_x")
		said, err := whichKeyRun(t, "", "ldap", "serve", "--roster", "127.0.0.1:1", "--insecure")
		x.ErrorContains(err, "no tenant key")
		for _, v := range []string{"ROSTER_LDAP_KEY_ACME", "ROSTER_ACCOUNT_KEY_ACME", "ROSTER_LOGIN_KEY"} {
			x.Contains(said, v+" is set and nothing reads it", v)
		}
	})
}

// TestBothKindsOfKeyAreRefusedInTheFilesWords: an app given a deployment key
// and tenant keys both was refused by the app's library in its Go names,
// `Keys and Key: one way to front tenants, not two`, which resemble nothing
// the file or the command line writes; and a `--tenant-key` beside
// `--deployment-key` was not refused at all, but dropped. Right is the
// refusal where the block and the flags are read, before the app is built,
// naming the keys as the file writes them and the flags as they are typed.
func TestBothKindsOfKeyAreRefusedInTheFilesWords(t *testing.T) {
	for app, says := range map[string]string{
		"account": "account.deployment_key (--deployment-key) and account.tenant_keys (--tenant-key): one way to front tenants, not two",
		"ldap":    "ldap.deployment_key (--deployment-key) and ldap.tenant_keys (--tenant-key): one way to front tenants, not two",
	} {
		t.Run(app, func(t *testing.T) {
			x := require.New(t)
			where := []string{"--roster", "127.0.0.1:1", "--insecure", "--listen", "127.0.0.1:0"}
			if app == "account" {
				where = append(where, "--connect", "http://127.0.0.1:1")
			}

			// Bounded, since one that is not refused serves.
			run := func(file string, args ...string) error {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()

				path := filepath.Join(t.TempDir(), "roster.yaml")
				x.NoError(os.WriteFile(path, []byte(file), 0o600))
				k := cli.Cmd(&cmd.Config{})
				k.Writer, k.ErrWriter = io.Discard, io.Discard

				return k.Run(ctx, append(append([]string{"--config", path, app, "serve"}, where...), args...))
			}

			x.ErrorContains(run(app+":\n  deployment_key: rk_x\n  tenant_keys: { acme: rt_x }\n"), says, "in the file")
			x.ErrorContains(run("", "--deployment-key", "rk_x", "--tenant-key", "acme=rt_x"), says, "by the flags")
		})
	}
}
