package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAFlagIsTheSettingItSays is a flag bound to a setting, through the tree a
// deployment runs: the load applies it over the file and the environment, and
// only when it was given.
//
// The flags were copied over the block by hand after the load -- `if v != "" {
// lc.Addr = v }`, `if v { lc.Insecure = true }` -- and that copy is what the
// middle cases here fail under: an empty value was no value, and a switch could
// only turn a setting on. Both are the loader's rules now, for a flag as for the
// file and the environment.
//
// `roster ldap serve` stands for every command with such flags because it
// checks most of what it is told before it dials anything. Each run is told one
// setting it refuses, by the file or the environment, and the flag under test
// is what takes it away -- or, absent, does not.
func TestAFlagIsTheSettingItSays(t *testing.T) {
	// As much of `ldap:` as the command needs to get past its own checks, and
	// `key` and `keys` both, which the directory refuses before it dials
	// anything (`ldap.New`). So that refusal is how a run says it got through
	// every check a flag here takes part in.
	const through = "one way to front tenants, not two"
	block := "ldap:\n" +
		"  roster: 127.0.0.1:1\n" +
		"  key: rk_named\n" +
		"  keys:\n" +
		"    newco: env:NOTHING_READS_THIS\n"

	serve := func(t *testing.T, more string, args ...string) error {
		t.Helper()

		path := filepath.Join(t.TempDir(), "roster.yaml")
		require.NoError(t, os.WriteFile(path, []byte(block+more), 0o600))

		return Cmd(&cmd.Config{}).Run(t.Context(), append([]string{"--config", path, "ldap", "serve"}, args...))
	}

	t.Run("absent, the file's value stands", func(t *testing.T) {
		require.ErrorContains(t, serve(t, "  bind: maybe\n"), "ldap.bind")
	})

	t.Run("empty clears the file's value rather than leaving it", func(t *testing.T) {
		require.ErrorContains(t, serve(t, "  bind: maybe\n", "--bind="), through)
	})

	t.Run("a switch given false turns off the file's true", func(t *testing.T) {
		x := require.New(t)

		x.ErrorContains(serve(t, "  require_tls: true\n"), "ldap.require_tls")
		x.ErrorContains(serve(t, "  require_tls: true\n", "--require-tls=false"), through)
	})

	t.Run("and a flag still beats the environment", func(t *testing.T) {
		x := require.New(t)

		t.Setenv("ROSTER_LDAP_BIND", "maybe")
		t.Setenv("ROSTER_LDAP_REQUIRE_TLS", "true")
		x.ErrorContains(serve(t, ""), "ldap.bind")
		x.ErrorContains(serve(t, "", "--bind", "key", "--require-tls=false"), through)
	})

	t.Run("a certificate is a block the flag sets whole", func(t *testing.T) {
		x := require.New(t)

		named := "  tls:\n" +
			"    cert: /nowhere/cert.pem\n" +
			"    key: /nowhere/key.pem\n"
		x.ErrorContains(serve(t, named), "/nowhere/cert.pem")
		x.ErrorContains(serve(t, named, "--tls="), through)
		x.ErrorContains(serve(t, "", "--tls", "cert.pem"), "cert.pem,key.pem",
			"a value that is not the pair is refused with the form it takes")
	})

	t.Run("and --help says which variable it is", func(t *testing.T) {
		x := require.New(t)

		out := &bytes.Buffer{}
		root := Cmd(&cmd.Config{})
		root.Writer = out
		x.NoError(root.Run(t.Context(), []string{"ldap", "serve", "--help"}))
		x.Contains(out.String(), "[$ROSTER_LDAP_ADDR]")
	})
}
