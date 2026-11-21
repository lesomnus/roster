package cmd_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestWhatHasAFormIsHeldToItAtStart: each of these started, and was found at
// the first request or never. `host.resolver: None` was dialled as an address,
// so every proof failed as a resolver that could not be reached -- the case
// `none` exists to tell apart. `login.hydra.admin: hydra:4445` was taken and
// logged. A page's directory that is not there served nothing. An origin with
// a path matched no browser's. Two `vouch.keys` of one name started, the
// second sealing new seeds under the first's name. And a directory's suffix
// given in `ROSTER_LDAP_BASES` was cut at its commas into a second tenant.
// Each is refused before anything is opened, by its key, saying what would be
// right; a refusal says no secret back.
func TestWhatHasAFormIsHeldToItAtStart(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	key := func(name string) string { return name + ":XfYoVszmos0DoMNUOSy1vJfaOLVOrq7x6QiNlqRr0G8=" }

	for _, v := range []struct {
		name string
		key  string
		with func(*cmd.Config)
	}{
		{"a resolver that is no address", "host.resolver", func(c *cmd.Config) { c.Host.Resolver = "None" }},
		{"a resolver with no port", "host.resolver", func(c *cmd.Config) { c.Host.Resolver = "1.1.1.1" }},
		{"hydra with no scheme", "login.hydra.admin", func(c *cmd.Config) {
			c.Login.Addr = "127.0.0.1:0"
			c.Login.Hydra.Admin = "hydra:4445"
		}},
		{"the login app's origin with another scheme", "login.base", func(c *cmd.Config) {
			c.Login.Addr = "127.0.0.1:0"
			c.Login.Hydra.Admin = "http://hydra:4445"
			c.Login.Base = "ftp://login.example"
		}},
		{"the account app's origin with no host", "account.base", func(c *cmd.Config) {
			c.Account.Addr = "127.0.0.1:0"
			c.Account.Base = "account.example"
		}},
		{"the user console's page that is not there", "user_console.dir", func(c *cmd.Config) {
			c.SignIn.Enabled = true
			c.Server.Http.Addr = "127.0.0.1:0"
			c.Server.Http.AllowWeb = true
			c.UserConsole.Dir = gone
		}},
		{"the admin console's page that is not there", "admin.console.dir", func(c *cmd.Config) {
			c.Admin.Http.Addr = "127.0.0.1:0"
			c.Admin.Http.AllowWeb = true
			c.Admin.Console.Dir = gone
		}},
		{"the account app's page that is not there", "account.page.dir", func(c *cmd.Config) {
			c.Account.Addr = "127.0.0.1:0"
			c.Account.Page.Dir = gone
		}},
		{"the login app's page that is not there", "login.page.dir", func(c *cmd.Config) {
			c.Login.Addr = "127.0.0.1:0"
			c.Login.Hydra.Admin = "http://hydra:4445"
			c.Login.Page.Dir = gone
		}},
		{"an origin with a path", "server.http.origins", func(c *cmd.Config) {
			c.Server.Http.Addr = "127.0.0.1:0"
			c.Server.Http.AllowWeb = true
			c.Server.Http.Origins = []string{"http://x.example/"}
		}},
		{"an origin with no scheme", "admin.http.origins", func(c *cmd.Config) {
			c.Admin.Http.Addr = "127.0.0.1:0"
			c.Admin.Http.AllowWeb = true
			c.Admin.Http.Origins = []string{"x.example"}
		}},
		{"two keys of one name", "vouch.keys", func(c *cmd.Config) {
			c.Vouch.Keys = []string{key("one"), key("two"), key("one")}
		}},
		{"a suffix cut at its commas", "ldap.bases", func(c *cmd.Config) {
			c.Ldap.Addr = "127.0.0.1:0"
			c.Ldap.Bases = map[string]string{"acme": "dc=acme", "dc": "example"}
		}},
	} {
		t.Run(v.name, func(t *testing.T) {
			err := startedWith(t, v.with)
			require.Error(t, err)
			require.True(t, strings.HasPrefix(err.Error(), v.key), "named: %s", err)
			require.NotContains(t, err.Error(), "XfYoVszmos0DoMNUOSy1vJfaOLVOrq7x6QiNlqRr0G8=", "a secret is not said back")
		})
	}

	t.Run("what has its form starts", func(t *testing.T) {
		x := require.New(t)
		page := t.TempDir()
		for _, r := range []string{"", "none", "1.1.1.1:53", "[::1]:53"} {
			x.NoError(startedWith(t, func(c *cmd.Config) { c.Host.Resolver = r }), "host.resolver %q", r)
		}
		x.NoError(startedWith(t, func(c *cmd.Config) {
			c.Login.Addr = "127.0.0.1:0"
			c.Login.Hydra.Admin = "http://hydra:4445/"
			c.Login.Base = "https://login.example"
			c.Login.Page.Dir = page
			c.Account.Addr = "127.0.0.1:0"
			c.Account.Base = "https://account.example:8443"
			c.Account.Page.Dir = page
			c.Server.Http.Addr = "127.0.0.1:0"
			c.Server.Http.AllowWeb = true
			c.Server.Http.Origins = []string{"http://localhost:5173", "*"}
			c.Vouch.Keys = []string{key("one"), key("two")}
			c.Ldap.Addr = "127.0.0.1:0"
			c.Ldap.Bases = map[string]string{"acme": "dc=acme,dc=example"}
		}))
	})
}
