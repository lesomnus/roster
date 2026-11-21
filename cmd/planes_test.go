package cmd_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/watch"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
)

// planesBroker is a broker for a test that refuses the databases named in
// [planesRefused] and publishes inside the process on any other, as
// `postgres` refuses SQLite: a test of what is said of the control plane's
// broker needs no PostgreSQL.
const planesBroker = "refuses-some-for-a-test"

var planesRefused sync.Map

func init() {
	config.RegisterBroker(planesBroker, func(_ context.Context, _ config.WatchConfig, db config.DbConfig) (watch.Broker, error) {
		if _, ok := planesRefused.Load(db.Dsn); ok {
			return nil, errors.New("this database cannot carry it")
		}

		return watch.Memory(), nil
	})
}

// servedLeasedSaying is [servedSaying] for a file that says `lease` too: the
// server is built without it, since a lease of several is refused over the
// SQLite these tests run on, and served with it, which is where `serve` says
// what it says of it.
func servedLeasedSaying(t *testing.T, lease string, with func(*cmd.Config)) *heard {
	t.Helper()
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}
	if with != nil {
		with(&c)
	}

	said := &heard{}
	ctx := log.Into(t.Context(), slog.New(slog.NewJSONHandler(said, nil)))

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)

	c.Spin.Lease = lease
	sctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Serve(sctx, c, l) }()

	x.Eventually(func() bool {
		conn, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
		if err != nil {
			return false
		}
		conn.Close()

		return true
	}, 10*time.Second, 10*time.Millisecond)

	stop()
	<-done

	return said
}

// TestTheLineAboutABrokerOfOneProcessAsksBothPlanes: `serve` said at start
// that a broker of one process stood beside a lease of several, and asked the
// data plane's broker alone. `control.watch.broker: memory` beside a lease of
// several left a watch on the control plane hearing one process with nothing
// said. Both planes are asked, in one line, and `control.watch.broker` is
// named where it is the one.
func TestTheLineAboutABrokerOfOneProcessAsksBothPlanes(t *testing.T) {
	const what = "hears only the process it is connected to"
	controlOf := func(t *testing.T, broker string) cmd.ControlConfig {
		cdrv, cdsn := pdtest.DB(t)

		return cmd.ControlConfig{
			Db:    config.DbConfig{Driver: cdrv, Dsn: cdsn},
			Watch: config.WatchConfig{Broker: broker},
		}
	}

	t.Run("the control plane's alone", func(t *testing.T) {
		x := require.New(t)
		said := servedLeasedSaying(t, severalLease, func(c *cmd.Config) {
			c.Watch.Broker = config.BrokerNone
			c.Control = controlOf(t, config.BrokerMemory)
		}).lines(t, what)
		x.Len(said, 1, "said once, as serve starts")
		x.Equal("WARN", said[0]["level"])
		x.Contains(said[0]["msg"], "control.watch.broker")
		x.Contains(said[0]["msg"], "spin.lease")
	})

	t.Run("the data plane's, which the control plane took", func(t *testing.T) {
		x := require.New(t)
		said := servedLeasedSaying(t, severalLease, func(c *cmd.Config) {
			c.Control = controlOf(t, "")
		}).lines(t, what)
		x.Len(said, 1)
		x.Contains(said[0]["msg"], "watch.broker")
		x.NotContains(said[0]["msg"], "control.watch.broker", "a key the file does not say")
	})

	t.Run("neither", func(t *testing.T) {
		require.Empty(t, servedLeasedSaying(t, severalLease, func(c *cmd.Config) {
			c.Watch.Broker = config.BrokerNone
			c.Control = controlOf(t, config.BrokerNone)
		}).lines(t, what))
	})
}

// TestALeaseOfSeveralOverSqliteIsRefused: payday asks the data plane's
// database of a lease of several only where `spin.dsn` names none, and never
// asks the control plane's. So a lease of several stood over a SQLite control
// plane always, and over a SQLite data plane where `spin.dsn` named another
// database: each process then had operators, keys and sessions of its own. It
// is refused before anything is opened, naming the plane's driver and the
// lease.
func TestALeaseOfSeveralOverSqliteIsRefused(t *testing.T) {
	refused := func(t *testing.T, c cmd.Config, key string) {
		t.Helper()

		err := c.Meant()
		require.Error(t, err, "a lease of several over a database of one machine")
		require.True(t, strings.HasPrefix(err.Error(), key+":"), "named: %s", err)
		require.Contains(t, err.Error(), "spin.lease")
	}

	t.Run("the data plane's, with the lease elsewhere", func(t *testing.T) {
		refused(t, cmd.Config{
			Db:   config.DbConfig{Driver: "sqlite3", Dsn: "file:roster.db"},
			Spin: config.SpinConfig{Lease: severalLease, Dsn: "postgres://leases/db"},
		}, "db.driver")
	})

	t.Run("the control plane's", func(t *testing.T) {
		refused(t, cmd.Config{
			Db:      config.DbConfig{Driver: "pgx", Dsn: "postgres://roster/db"},
			Spin:    config.SpinConfig{Lease: severalLease},
			Control: cmd.ControlConfig{Db: config.DbConfig{Driver: "sqlite3", Dsn: "file:control.db"}},
		}, "control.db.driver")
	})

	t.Run("a lease of one over SQLite starts", func(t *testing.T) {
		for _, lease := range []string{config.LeaseLocal, config.LeaseNone} {
			require.NoError(t, cmd.Config{
				Db:      config.DbConfig{Driver: "sqlite3", Dsn: "file:roster.db"},
				Spin:    config.SpinConfig{Lease: lease},
				Control: cmd.ControlConfig{Db: config.DbConfig{Driver: "sqlite3", Dsn: "file:control.db"}},
			}.Meant())
		}
	})
}

// TestTheControlPlanesTakenBrokerIsSaidWhereItIsRefused: a control plane that
// left `control.watch.broker` unsaid takes `watch.broker`, and where that
// broker refused the control plane's database the refusal was about a line
// that is not in the file. It says where the broker came from, as the
// outbox's refusal does.
func TestTheControlPlanesTakenBrokerIsSaidWhereItIsRefused(t *testing.T) {
	x := require.New(t)

	cdrv, cdsn := pdtest.DB(t)
	planesRefused.Store(cdsn, true)
	t.Cleanup(func() { planesRefused.Delete(cdsn) })

	err := startedWith(t, func(c *cmd.Config) {
		c.Watch.Broker = planesBroker
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	x.Error(err)
	x.Contains(err.Error(), "this database cannot carry it")
	x.Contains(err.Error(), "control.watch.broker is empty, so it took watch.broker")
}
