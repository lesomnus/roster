package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAPortThatWouldAnswerNothingIsRefused: an `http` listener transcodes
// only where `http.allow_web` says so. `control.http` has no page and no other
// route, so its address with neither `allow_web` nor `allow_pprof` answered
// `404` to everything -- `deploy/config.yaml` shipped one, under a comment
// that said a shell asks it -- and an `admin.http` or `server.http` that
// serves a console without it served the page and none of the calls the page
// makes, while the log said the listener was up. Each is refused, naming the
// address's key and the switch.
func TestAPortThatWouldAnswerNothingIsRefused(t *testing.T) {
	page := t.TempDir()

	for key, with := range map[string]func(*cmd.Config){
		"control.http.addr": func(c *cmd.Config) {
			c.Control.Addr = "127.0.0.1:0"
			c.Control.Http.Addr = "127.0.0.1:0"
		},
		"admin.http.addr": func(c *cmd.Config) {
			c.Admin.Addr = "127.0.0.1:0"
			c.Admin.Http.Addr = "127.0.0.1:0"
			c.Admin.Console.Dir = page
		},
		"server.http.addr": func(c *cmd.Config) {
			c.SignIn.Enabled = true
			c.Server.Http.Addr = "127.0.0.1:0"
			c.UserConsole.Dir = page
		},
	} {
		t.Run(key, func(t *testing.T) {
			err := startedWith(t, with)
			require.Error(t, err, "a listener that answers nothing it is there for")
			require.True(t, strings.HasPrefix(err.Error(), key+":"), "named: %s", err)
			require.Contains(t, err.Error(), "allow_web")
		})
	}

	t.Run("what answers something still starts", func(t *testing.T) {
		x := require.New(t)
		x.NoError(startedWith(t, func(c *cmd.Config) {
			c.Control.Http.Addr = "127.0.0.1:0"
			c.Control.Http.AllowPprof = true
		}), "the profiles are what it serves")
		x.NoError(startedWith(t, func(c *cmd.Config) {
			c.Control.Http.Addr = "127.0.0.1:0"
			c.Control.Http.AllowWeb = true
		}))
		x.NoError(startedWith(t, func(c *cmd.Config) {
			c.Admin.Http.Addr = "127.0.0.1:0"
			c.Admin.Http.AllowWeb = true
			c.Admin.Console.Dir = page
		}))
	})
}
