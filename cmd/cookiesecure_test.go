package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestACookieWithoutSecureBesideAnHttpsBaseIsRefused: `insecure_cookie` is for
// a page served over plain http, and a front door whose base is `https` beside
// it handed every browser its cookies without `Secure`. `deploy/config.yaml`
// ships the switch on beside an `http` base, and an overlay that says its own
// `https` base through the environment inherits the switch; it was told once
// at start, by a line that names no key.
//
// Refused at start, naming both keys, for each front door that has the two:
// the account app's and the Login App's. An `http` base with the switch on
// starts as before.
func TestACookieWithoutSecureBesideAnHttpsBaseIsRefused(t *testing.T) {
	for name, meant := range map[string]func(base string, insecure bool) error{
		"account": func(base string, insecure bool) error {
			return cmd.AccountConfig{Base: base, InsecureCookie: insecure}.Meant()
		},
		"login": func(base string, insecure bool) error {
			return cmd.LoginConfig{Base: base, InsecureCookie: insecure}.Meant()
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			for _, base := range []string{"https://" + name + ".example.com", "HTTPS://" + name + ".example.com/"} {
				err := meant(base, true)
				x.Error(err, "%s with %s.insecure_cookie started", base, name)
				x.Contains(err.Error(), name+".base")
				x.Contains(err.Error(), name+".insecure_cookie")
			}

			x.NoError(meant("http://localhost:8080", true), "an http base with the switch on is what the switch is for")
			x.NoError(meant("https://"+name+".example.com", false))
			x.NoError(meant("", true), "no base is said nothing of")
		})
	}
}
