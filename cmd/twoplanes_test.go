package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// TestTwoPlanesAreTwoDatabases: a deployment whose control plane is given the
// data plane's database is refused, when it is built where the file says so
// outright and when it is served where it says so by another spelling.
//
// # What it was
//
// Nothing compared them. A file that named one database twice -- `control.db`
// pasted from `db`, which is what two blocks of the same shape invite -- made
// a deployment that started, served, and looked like any other. In it the
// sessions of both planes are one table and so are the holders: a customer's
// administrator signed in on the data plane, sent that cookie to `admin.addr`,
// and was an operator there. Their binding in their own tenant, read where an
// operator's is read, was `/roster.*/*` on the stack with no wall, and
// `TenantService/List` answered every customer (#275).
//
// What separates somebody who runs a deployment from somebody who is a
// customer of it is that they are rows of two databases. It is the one thing
// `docs/operating.md` assumes about the file and the one thing the file was
// not held to.
func TestTwoPlanesAreTwoDatabases(t *testing.T) {
	t.Run("one database named twice is refused before it is opened", func(t *testing.T) {
		x := require.New(t)
		drv, dsn := pdtest.DB(t)

		_, err := cmd.Build(t.Context(), cmd.Config{
			Db:      config.DbConfig{Driver: drv, Dsn: dsn},
			Watch:   config.WatchConfig{Broker: config.BrokerMemory},
			Control: cmd.ControlConfig{Db: config.DbConfig{Driver: drv, Dsn: dsn}},
		})
		x.ErrorContains(err, "control.db")
		x.ErrorContains(err, "the same database")
	})

	t.Run("and one named by another spelling, when it is served", func(t *testing.T) {
		x := require.New(t)
		drv, dsn := pdtest.DB(t)

		// The same database, written another way: a parameter that changes
		// nothing about which one it is.
		again := dsn + "&application_name=again"
		if drv == "sqlite3" {
			again = dsn + "&_pragma=busy_timeout(5001)"
		}

		c := cmd.Config{
			Db:      config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
			Watch:   config.WatchConfig{Broker: config.BrokerMemory},
			Control: cmd.ControlConfig{Db: config.DbConfig{Driver: drv, Dsn: again, Migrate: true}},
		}

		// `init` is what writes the row the two are told apart by: who runs
		// this deployment.
		out, err := initRun(t, c)
		x.NoError(err, "init: %s", out)

		s, err := cmd.Build(t.Context(), c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		err = cli.Ready(t.Context(), s, c)
		x.ErrorContains(err, "control.db")
		x.ErrorContains(err, "the same database")
	})

	t.Run("two databases are served", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)
		c.Db.Migrate, c.Control.Db.Migrate = true, true

		out, err := initRun(t, c)
		x.NoError(err, "init: %s", out)

		s, err := cmd.Build(t.Context(), c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		x.NoError(cli.Ready(t.Context(), s, c))
	})
}
