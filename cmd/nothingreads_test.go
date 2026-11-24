package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAKeyNothingReadsIsRefusedByName: the manual says a key the file names
// that nothing reads is refused at start, by name, and five were taken and
// read by nothing -- payday's, in blocks roster holds. `allow_reflection`
// serves nothing, since roster registers no reflection service on any port;
// `dev_dsn` names the database `migrate plan` works with, and roster has no
// such command. Each started and said nothing, the shape `allow_general_writes`
// had before it was refused; each is refused now, with what roster does
// instead. payday's `watch.namespace` came with its NATS broker, which roster
// does not link, and was refused as a key payday did not have before it came:
// it is refused still, on either plane.
func TestAKeyNothingReadsIsRefusedByName(t *testing.T) {
	for key, with := range map[string]func(*cmd.Config){
		"server.allow_reflection":  func(c *cmd.Config) { c.Server.AllowReflection = true },
		"control.allow_reflection": func(c *cmd.Config) { c.Control.AllowReflection = true },
		"admin.allow_reflection":   func(c *cmd.Config) { c.Admin.AllowReflection = true },
		"db.dev_dsn":               func(c *cmd.Config) { c.Db.DevDsn = "file:dev.db" },
		"control.db.dev_dsn":       func(c *cmd.Config) { c.Control.Db.DevDsn = "file:dev.db" },
		"watch.namespace":          func(c *cmd.Config) { c.Watch.Namespace = "roster" },
		"control.watch.namespace":  func(c *cmd.Config) { c.Control.Watch.Namespace = "roster" },
	} {
		t.Run(key, func(t *testing.T) {
			err := startedWith(t, with)
			require.Error(t, err, "taken and read by nothing")
			require.True(t, strings.HasPrefix(err.Error(), key+":"), "named: %s", err)
		})
	}
}
