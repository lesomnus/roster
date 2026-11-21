package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cmd"
)

// severalLease is a lease that shares loops among processes, as `postgres`
// does, built on any database: a test of the rule needs no PostgreSQL.
const severalLease = "several-for-a-test"

func init() {
	config.RegisterLease(severalLease, func(context.Context, config.SpinConfig, config.DbConfig) (spin.Lease, error) {
		return spin.Local(), nil
	})
}

// TestABrokerOfOneProcessBesideALeaseOfSeveralIsSaidAtStart: `watch.broker:
// memory` beside a lease that shares the loops among processes is a file that
// says one process and several, and nothing compared them: a follower of one
// replica heard nothing written at another, on a stream that looked open.
// `serve` says so once as it starts, as a warning; nothing is refused, since
// one process may hold a lease of several.
func TestABrokerOfOneProcessBesideALeaseOfSeveralIsSaidAtStart(t *testing.T) {
	const what = "hears only the process it is connected to"

	t.Run("memory beside a lease of several", func(t *testing.T) {
		x := require.New(t)
		said := servedLeasedSaying(t, severalLease, nil).lines(t, what)
		x.Len(said, 1, "said once, as serve starts")
		x.Equal("WARN", said[0]["level"])
		x.Contains(said[0]["msg"], "watch.broker")
		x.Contains(said[0]["msg"], "spin.lease")
	})

	t.Run("memory beside a lease of one", func(t *testing.T) {
		require.Empty(t, servedSaying(t, func(c *cmd.Config) {
			c.Spin.Lease = config.LeaseLocal
		}).lines(t, what))
	})

	t.Run("memory and no lease", func(t *testing.T) {
		require.Empty(t, servedSaying(t, nil).lines(t, what))
	})
}
