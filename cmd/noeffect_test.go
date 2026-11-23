package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"

	"github.com/lesomnus/roster/cmd"
)

// TestWhatCannotTakeEffectIsRefused: `build` refused `control:` with no
// database and `server.console.dir` with no sign-in because an address that is
// written down and not served is worse than one that is refused. These are
// the same shape, and each started and said nothing: a console's page no
// listener serves, a switch of a listener that is not opened, a refresh of no
// file, a broker's address no broker reads, a burst of no limit. Each is
// refused, naming both keys.
//
// The consumer blocks said with no address still start: `roster ldap serve`
// and the others read the same block.
func TestWhatCannotTakeEffectIsRefused(t *testing.T) {
	page := t.TempDir()
	alone := func(c *cmd.Config) { c.Control = cmd.ControlConfig{} }

	for _, v := range []struct {
		name string
		keys []string
		with func(*cmd.Config)
	}{
		{"the admin console with no control plane", []string{"admin.console.dir", "control.db.driver"}, func(c *cmd.Config) {
			alone(c)
			c.Admin.Console.Dir = page
		}},
		{"the admin console with no listener", []string{"admin.console.dir", "admin.http.addr"}, func(c *cmd.Config) {
			c.Admin.Addr = "127.0.0.1:0"
			c.Admin.Console.Dir = page
		}},
		{"the user console with no listener", []string{"server.console.dir", "server.http.addr"}, func(c *cmd.Config) {
			c.SignIn.Enabled = true
			c.Server.Console.Dir = page
		}},
		{"the web with no listener", []string{"server.http.allow_web", "server.http.addr"}, func(c *cmd.Config) {
			c.Server.Http.AllowWeb = true
		}},
		{"origins with no listener", []string{"control.http.origins", "control.http.addr"}, func(c *cmd.Config) {
			c.Control.Http.Origins = []string{"http://localhost:5173"}
		}},
		{"the admin listener's web with no listener", []string{"admin.http.allow_web", "admin.http.addr"}, func(c *cmd.Config) {
			c.Admin.Addr = "127.0.0.1:0"
			c.Admin.Http.AllowWeb = true
		}},
		{"a refresh of no file", []string{"policy.refresh", "policy.file"}, func(c *cmd.Config) {
			c.Policy.Refresh = 5e9
		}},
		{"a broker's address under memory", []string{"watch.dsn", "watch.broker"}, func(c *cmd.Config) {
			c.Watch.Dsn = "postgres://elsewhere/db"
		}},
		{"a broker's address under none, on the control plane", []string{"control.watch.dsn", "control.watch.broker"}, func(c *cmd.Config) {
			c.Control.Watch = config.WatchConfig{Broker: config.BrokerNone, Dsn: "postgres://elsewhere/db"}
		}},
		{"a burst of no limit", []string{"server.limit.burst", "server.limit.rate"}, func(c *cmd.Config) {
			c.Server.Limit.Burst = 10
		}},
	} {
		t.Run(v.name, func(t *testing.T) {
			err := startedWith(t, v.with)
			require.Error(t, err, "written down, and takes no effect")
			for _, k := range v.keys {
				require.Contains(t, err.Error(), k)
			}
		})
	}

	t.Run("a consumer's block with no address still starts", func(t *testing.T) {
		require.NoError(t, startedWith(t, func(c *cmd.Config) {
			c.Ldap.Bind = "key"
			c.Account.Enrol = "invited"
			c.Login.Consent = "skip"
		}))
	})
}
