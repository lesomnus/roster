package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAnAddressCarriesNoCredential: `login.hydra.admin` written
// `http://user:pass@…` was printed by `roster config` and logged by the Login
// App as it started, password and all. An address is printed and logged as it
// is written, so it is no place for a credential, and there is nothing in one
// to redact: one with anything between `://` and `@` is refused at start, by
// its key, and the address is not said back. A credential for Hydra's admin
// API goes in `login.hydra.header`. `login.base` and `account.base` are held
// to the same, since nothing reads a credential out of them either.
func TestAnAddressCarriesNoCredential(t *testing.T) {
	for _, v := range []struct {
		key  string
		with func(*cmd.Config, string)
	}{
		{"login.hydra.admin", func(c *cmd.Config, v string) { c.Login.Hydra.Admin = v }},
		{"login.base", func(c *cmd.Config, v string) { c.Login.Base = v }},
		{"account.base", func(c *cmd.Config, v string) { c.Account.Base = v }},
	} {
		for _, address := range []string{"http://admin:ADDRESSPASSWORD@hydra:4445", "https://ADDRESSTOKEN@login.example", "admin:ADDRESSPASSWORD@hydra:4445"} {
			t.Run(v.key+" "+address, func(t *testing.T) {
				x := require.New(t)
				err := startedWith(t, func(c *cmd.Config) {
					c.Login.Addr = "127.0.0.1:0"
					c.Login.Hydra.Admin = "http://hydra:4445"
					c.Account.Addr = "127.0.0.1:0"
					v.with(c, address)
				})
				x.Error(err)
				x.True(strings.HasPrefix(err.Error(), v.key), "named: %s", err)
				x.Contains(err.Error(), "login.hydra.header", "where a credential goes")
				x.NotContains(err.Error(), "ADDRESS", "the address is not said back")
			})
		}
	}
}
