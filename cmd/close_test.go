package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
)

// TestClosingADeploymentClosesBothOfIts.
//
// `Build` is recursive: a deployment that names a `control:` plane is two
// servers, each with a database and a pool of its own. `Close` was one line
// closing the outer one, so the control plane's connections were never given
// back -- and nothing said so, because a process that is shutting down does not
// care and a test that builds one deployment has room for the leak.
//
// A suite does not. Every test here that needs keys, a console or an operator
// builds both planes, and each of them left a pool behind: against PostgreSql
// with the hundred connections its image allows -- which is what this app's own
// CI gives it -- the package ran out part way through and the failures came
// back as `too many clients already` against whichever tests happened to be
// running when the last one was taken. A different set every time, which reads
// exactly like a flaky suite and is not one.
//
// Asserted by asking the database rather than by counting anything here: what
// matters is that the server let go, and only the server knows.
func TestClosingADeploymentClosesBothOfIts(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	})
	x.NoError(err)
	x.NotNil(s.Control, "no control plane was built, so this proves nothing")

	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	x.NoError(s.Close())

	// A closed pool refuses. Both of them, which is the whole claim.
	x.Error(s.Db.PingContext(ctx), "the data plane is still open")
	x.Error(s.Control.Db.PingContext(ctx),
		"the control plane's connections were never given back")
}
