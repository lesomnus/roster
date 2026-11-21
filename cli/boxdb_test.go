package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestACommandOfTheBoxSaysSoWhereNoDatabaseIsNamed: a command that reads the
// deployment's database, run on a configuration that names none -- the file a
// customer's person is shown, with only `client:` in it -- answered payday's
// sentence about a key nobody wrote, `unknown driver ""`. It says what is so:
// this command reads the deployment's database, the configuration names none,
// and which commands ask a running roster instead.
func TestACommandOfTheBoxSaysSoWhereNoDatabaseIsNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roster.yaml")
	require.NoError(t, os.WriteFile(path, []byte("client:\n  addr: roster.example:443\n"), 0o600))

	for _, args := range [][]string{
		{"key", "list"},
		{"hold", "ls"},
		{"control", "holder", "list"},
	} {
		t.Run(args[0], func(t *testing.T) {
			x := require.New(t)

			err := Cmd(&cmd.Config{}).Run(t.Context(), append([]string{"--config", path}, args...))
			x.Error(err)
			x.NotContains(err.Error(), `unknown driver ""`, "payday's sentence about a key the file never wrote")
			x.Contains(err.Error(), "db.driver")
			x.Contains(err.Error(), "client.addr", "and which commands ask a running roster")
		})
	}
}
