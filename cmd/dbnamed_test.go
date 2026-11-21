package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"

	"github.com/lesomnus/roster/cmd"
)

// TestADatabaseNamedByNothingIsRefused: SQLite reads an empty name as a
// private temporary database, a new one for each connection of the pool. So a
// `db.dsn` left empty -- or given through a variable spelt wrong, which is one
// line on standard error -- under `driver: sqlite3` and `migrate: true` started
// and served: the migration made the tables on one connection, the loops found
// none on the others, and everything written was thrown away. Each plane's
// empty name is refused by its own key, before anything is opened.
func TestADatabaseNamedByNothingIsRefused(t *testing.T) {
	refused := func(t *testing.T, key string, c cmd.Config) {
		t.Helper()

		s, err := cmd.Build(t.Context(), c)
		if s != nil {
			s.Close()
		}
		require.Error(t, err, "a database named by nothing")
		require.True(t, strings.HasPrefix(err.Error(), key+":"), "named: %s", err)
	}

	t.Run("the data plane's", func(t *testing.T) {
		refused(t, "db.dsn", cmd.Config{
			Db:    config.DbConfig{Driver: "sqlite3", Migrate: true},
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
		})
	})

	t.Run("the control plane's", func(t *testing.T) {
		refused(t, "control.db.dsn", cmd.Config{
			Db:      config.DbConfig{Driver: "sqlite3", Dsn: "file:" + t.TempDir() + "/roster.db", Migrate: true},
			Watch:   config.WatchConfig{Broker: config.BrokerMemory},
			Control: cmd.ControlConfig{Db: config.DbConfig{Driver: "sqlite3", Migrate: true}},
		})
	})
}
