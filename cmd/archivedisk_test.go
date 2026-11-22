package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
)

// TestAnArchiveOnADiskIsSaidWhereThereMayBeSeveralProcesses: `audit.archive`,
// `audit.checkpoints.dir` and a `store:` witness are directories on the disk
// of the process that writes them, and the pass that moves lines of the trail
// out of the database into them runs in whichever process holds its lease.
// With two processes on a disk each, the second found the archive was not what
// its manifest said and went on: the database then held no line, and `roster
// trail verify` failed at both disks. Nothing said so, and the manual said a
// second replica needs no shared filesystem.
//
// `serve` says so once as it starts, at `WARN`, naming the keys, wherever
// `spin.lease` is of several processes. Nothing is refused: roster cannot see
// whether a path is one every process mounts.
func TestAnArchiveOnADiskIsSaidWhereThereMayBeSeveralProcesses(t *testing.T) {
	const what = "one directory every process mounts"

	onDisks := func(t *testing.T) func(c *cmd.Config) {
		return func(c *cmd.Config) {
			c.Audit = config.AuditConfig{
				Retain:  90 * 24 * time.Hour,
				Archive: t.TempDir(),
				Checkpoints: config.AuditCheckpointsConfig{
					Key:       pdtest.Key(t),
					Dir:       t.TempDir(),
					Witnesses: []string{"log:", "store:" + t.TempDir()},
				},
			}
		}
	}

	t.Run("each on a path, beside a lease of several", func(t *testing.T) {
		x := require.New(t)
		said := servedLeasedSaying(t, severalLease, onDisks(t)).lines(t, what)
		x.Len(said, 1, "said once, as serve starts")
		x.Equal("WARN", said[0]["level"])
		for _, key := range []string{"audit.archive", "audit.checkpoints.dir", "audit.checkpoints.witnesses", "spin.lease"} {
			x.Contains(said[0]["msg"], key)
		}
	})

	t.Run("the archive alone, beside a lease of several", func(t *testing.T) {
		x := require.New(t)
		said := servedLeasedSaying(t, severalLease, func(c *cmd.Config) {
			c.Audit = config.AuditConfig{
				Retain:      90 * 24 * time.Hour,
				Archive:     t.TempDir(),
				Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t), Witnesses: []string{"log:"}},
			}
		}).lines(t, what)
		x.Len(said, 1)
		x.Contains(said[0]["msg"], "audit.archive")
		x.NotContains(said[0]["msg"], "audit.checkpoints.dir")
		x.NotContains(said[0]["msg"], "audit.checkpoints.witnesses")
	})

	t.Run("each on a path, beside a lease of one", func(t *testing.T) {
		require.Empty(t, servedLeasedSaying(t, config.LeaseLocal, onDisks(t)).lines(t, what))
	})

	t.Run("nothing on a path, beside a lease of several", func(t *testing.T) {
		require.Empty(t, servedLeasedSaying(t, severalLease, nil).lines(t, what))
	})
}
