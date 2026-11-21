package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWhatResourcesApplyPrints: `roster resources apply` prints one line a
// row, `<sign> <Kind> <name>`, the name with its tenant in front where the
// kind has one, and after a `~` the keys that change, by the names the file
// writes them; and what was done before an error is printed before it.
//
// # What it was
//
// A change printed `~ name` and not which field, where the command exists so
// that somebody learns that a `Connection`'s issuer is about to change. A
// `Host` and a `MailDomain` of one name printed alike, and neither said whose
// it was. And on an error the command returned before it printed, so a
// `Tenant` that was made before a row that failed was made with no line
// saying so.
func TestWhatResourcesApplyPrints(t *testing.T) {
	const file = `
resources:
  - kind: Tenant
    alias: newco
    name: Newco
  - kind: Connection
    tenant: newco
    name: entra
    issuer: https://login.microsoftonline.com/newco/v2.0
    client_id: the-app
    scopes: [openid]
  - kind: Host
    tenant: newco
    name: newco.example
  - kind: MailDomain
    tenant: newco
    name: newco.example
    routes: entra
`
	x := require.New(t)
	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, out)

	lines := func(out string) []string {
		ls := []string{}
		for _, l := range strings.Split(out, "\n") {
			if l != "" && !strings.HasPrefix(l, "(") {
				ls = append(ls, l)
			}
		}

		return ls
	}

	out, err = entities(t, &c, "resources", "apply", "--file", declare(t, file)[0])
	x.NoError(err)
	x.Equal([]string{
		"+ Tenant @newco",
		"+ Connection @newco/entra",
		"+ Host @newco/newco.example",
		"+ MailDomain @newco/newco.example",
	}, lines(out))

	t.Run("a change says which keys", func(t *testing.T) {
		x := require.New(t)
		edited := strings.NewReplacer(
			"newco/v2.0", "other/v2.0",
			"scopes: [openid]", "scopes: [openid, email]",
			"name: Newco", "name: Newco Ltd",
		).Replace(file)

		out, err := entities(t, &c, "resources", "apply", "--dry-run", "--file", declare(t, edited)[0])
		x.NoError(err)
		x.Equal([]string{
			"~ Tenant @newco: name",
			"~ Connection @newco/entra: issuer, scopes",
			"  Host @newco/newco.example",
			"  MailDomain @newco/newco.example",
		}, lines(out))
	})

	t.Run("what was done before an error is printed", func(t *testing.T) {
		x := require.New(t)
		out, err := entities(t, &c, "resources", "apply", "--file", declare(t, `
resources:
  - kind: Tenant
    alias: acme
  - kind: Connection
    tenant: acme
    name: entra
    client_id: the-app
`)[0])
		x.Error(err, "a connection with no issuer")
		x.Equal([]string{"+ Tenant @acme"}, lines(out))
	})
}
