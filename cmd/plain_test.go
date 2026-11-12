package cmd_test

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
)

// plainCase is the way of serving a run of this test binary builds, when it
// is run again by [TestPlainIsSaidOnlyWhereItServes] for one.
const plainCase = "ROSTER_TEST_PLAIN_CASE"

// TestPlainIsSaidOnlyWhereItServes: payday's `auth: serving with Plain` --
// every caller believed to be whoever it says -- is in the log of a deployment
// that does believe its callers, and of no other. With a control plane nobody
// is, sign-in or not, and the log says what does serve instead: a warning
// that every caller is believed, of a deployment that believes nobody, is the
// one line an operator reads to tell the two apart.
//
// The warning is said once a process, so each way of serving is built in a
// process of its own: this test binary, run again for one.
func TestPlainIsSaidOnlyWhereItServes(t *testing.T) {
	if c := os.Getenv(plainCase); c != "" {
		servePlainCase(t, c)
		return
	}

	for _, c := range []struct {
		name  string
		plain bool
		says  string
	}{
		{name: "nothing", plain: true},
		{name: "sign-in", plain: true},
		{name: "control", says: "callers are who a key from the control plane says; nobody is believed"},
		{name: "control+sign-in", says: "or the person a session cookie was minted for; nobody is believed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			run := exec.Command(os.Args[0], "-test.run=^TestPlainIsSaidOnlyWhereItServes$", "-test.count=1")
			run.Env = append(os.Environ(), plainCase+"="+c.name)
			out, err := run.CombinedOutput()
			x.NoError(err, "%s", out)
			x.Equal(c.plain, strings.Contains(string(out), "serving with Plain"), "%s", out)
			if c.says != "" {
				x.Contains(string(out), c.says)
			}
		})
	}
}

// servePlainCase builds the server one way and the gRPC server in front of
// it, which is where a deployment with nothing to read a credential with
// falls back to Plain, with the log going where the test that ran this reads.
func servePlainCase(t *testing.T, name string) {
	x := require.New(t)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx := log.Into(t.Context(), slog.Default())

	drv, dsn := pdtest.DB(t)
	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}
	if strings.HasPrefix(name, "control") {
		cdrv, cdsn := pdtest.DB(t)
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	}
	c.SignIn.Enabled = strings.HasSuffix(name, "sign-in")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	g.Stop()
}
