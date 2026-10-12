package cmd_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cmd"
)

// TestEveryLoopIsOneTheProcessesAgreeOn: under a lease (`spin.lease`,
// payday#54) the processes of a deployment agree on a loop by its name and one
// of them runs it, so every loop either plane arranges has a name, and no two
// share one -- `spin.Run` refuses both before anything starts.
//
// The deployment with the most loops this app can arrange: both planes, an
// outbox on each, the door people sign in at, retention, and the window after
// which somebody who left is forgotten. Most of the sweeps are in both planes,
// on two databases, which is the case a name per sweep would get wrong.
func TestEveryLoopIsOneTheProcessesAgreeOn(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory, Outbox: true},
		Control: cmd.ControlConfig{
			Db:    config.DbConfig{Driver: cdrv, Dsn: cdsn},
			Watch: config.WatchConfig{Outbox: true},
		},
		SignIn: cmd.SignInConfig{Enabled: true},
		Audit:  config.AuditConfig{Retain: 90 * 24 * time.Hour, Discard: true},
		Holder: cmd.HolderConfig{ForgetAfter: 30 * 24 * time.Hour},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NotEmpty(s.Control.Spin)
	for i, v := range s.Spin {
		_, ok := v.(spin.Spinner)
		x.True(ok, "s.Spin[%d] is a %T, which spin.Run skips", i, v)
	}

	// Under a lease nothing is held by, so that no loop runs here and what is
	// asked is only whether the processes could agree on them.
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	x.NoError(spin.Run(ctx, slices.Values(s.Spin), spin.WithLease(spin.Nowhere())))
}
