package cmd_test

import (
	"path/filepath"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
)

// TestFourFlagsSayWhatTheOthersSay: four flags take the names the other
// commands give the same thing, and the names they had are unknown.
//
// `login serve --key` was the deployment's key, where `account serve` and
// `ldap serve` call that `--deployment-key` and a tenant's `alias=rt_…`
// `--key` (`--tenant-key` now); `app install --role` took methods, which eight other commands take
// as `--allow`; `--in` was the archive's directory on four `trail` commands
// and `prune --to` on the fifth, where `--in` is how a request is read on
// every entity command; and `key revoke --id` took the key that `me
// revoke-key KEY` and `api-key erase` take as an argument. Answered: the old
// names; right is the others', and the old ones refused as any unknown flag
// is, status 2, with nothing kept for them (#284).
func TestFourFlagsSayWhatTheOthersSay(t *testing.T) {
	b := atTheBox(t)
	archive := t.TempDir()
	b.c.Audit.Archive = archive
	b.c.Audit.Checkpoints.Key = pdtest.Key(t)
	other := filepath.Join(t.TempDir(), "other")

	_, said, err := boxRun(t, at(b.c, "admin"), "", "key", "add", "--tenant", "newco", "--holder", "admin", "--allow", "/roster.MeService/Get")
	require.NoError(t, err, said)
	key := keyIdSaid(t, said)

	t.Run("the old names are unknown", func(t *testing.T) {
		for _, args := range [][]string{
			{"login", "serve", "--key", "rk_x"},
			{"app", "install", "--tenant", "newco", "--role", "/loft.AssetService/*", "loft"},
			{"trail", "read", "--in", archive},
			{"trail", "verify", "--in", archive},
			{"trail", "purge", "--older-than", "1h", "--in", archive},
			{"trail", "accept", "--in", archive, "--of", nothingLookedAt, "--why", "looked"},
			{"trail", "prune", "--older-than", "1h", "--to", archive},
			{"key", "revoke", "--id", key},
		} {
			_, _, err := boxRun(t, at(b.c, "admin"), "", args...)
			require.ErrorIs(t, err, xli.ErrUnknownFlag, "%v", args)
			require.Equal(t, 2, cli.Status(err), "%v", args)
		}
	})

	t.Run("and the new ones are what the commands read", func(t *testing.T) {
		for _, v := range []struct {
			args []string
			says string
		}{
			// Each refused for what its value is, which only a flag the
			// command reads can be.
			{[]string{"login", "serve", "--roster", "roster:50051", "--deployment-key", "rt_x"}, "login.deployment_key (--deployment-key): a deployment key"},
			{[]string{"app", "install", "--tenant", "newco", "--allow", "not-a-method", "loft"}, "--allow not-a-method: not a method"},
			{[]string{"app", "install", "--tenant", "newco", "loft"}, "--allow: what the app may do"},
			{[]string{"trail", "purge", "--older-than", "1h", "--archive", other}, "--archive: " + other + " is not audit.archive"},
			{[]string{"trail", "accept", "--archive", other, "--of", nothingLookedAt, "--why", "looked"}, "--archive: " + other + " is not audit.archive"},
			{[]string{"trail", "prune", "--older-than", "1h", "--archive", other}, "--archive: " + other + " is not audit.archive"},
			{[]string{"trail", "prune", "--older-than", "1h", "--discard", "--archive", other}, "--discard and --archive"},
			{[]string{"key", "revoke"}, `"KEY": required argument not given`},
		} {
			_, _, err := boxRun(t, at(b.c, "admin"), "", v.args...)
			require.ErrorContains(t, err, v.says, "%v", v.args)
		}

		// Read where it is bound, as `audit.archive`.
		for _, args := range [][]string{
			{"trail", "read", "--archive", archive},
			{"trail", "verify", "--archive", archive},
		} {
			_, said, err := boxRun(t, at(b.c, "admin"), "", args...)
			require.NoError(t, err, "%v: %s", args, said)
		}

		_, said, err := boxRun(t, at(b.c, "admin"), "", "key", "revoke", key)
		require.NoError(t, err, said)
	})
}
