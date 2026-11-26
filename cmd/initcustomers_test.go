package cmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestInitSaysTheCustomersThatAreThere: `roster init` ended on *there are no
// customers yet, which is the right state to start in*, and that the admin
// console makes the first one, whatever the data plane held. On a deployment
// whose file declares a tenant -- every one made from `deploy/` -- `serve` has
// made it before anybody runs `init`, so the sentence was false where it was
// read and its advice was to make a customer that was there.
//
// So `init` looks: with no tenant it says what it said, and with tenants how
// many there are, and which a file declared and which were made before it,
// without the advice. The case operating.md shows under the declared rows is
// the case it shows, whole.
func TestInitSaysTheCustomersThatAreThere(t *testing.T) {
	// What `serve` does before it serves, and then a tenant made at the box
	// where `made` asks for one, as `roster tenant add` makes it.
	before := func(t *testing.T, made bool) cmd.Config {
		t.Helper()
		x := require.New(t)
		ctx := t.Context()

		c := seedbed(t)
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		x.NoError(cli.Migrate(ctx, s))

		rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Tenant
    alias: contoso
    name: Contoso
`))
		x.NoError(err)
		_, err = cmd.ApplyResources(ctx, s, rs, false)
		x.NoError(err)

		if made {
			_, err = s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "fabrikam"}.Build())
			x.NoError(err)
		}

		return c
	}

	t.Run("one a file declared", func(t *testing.T) {
		x := require.New(t)

		out, err := initRun(t, before(t, false))
		x.NoError(err, "init: %s", out)

		x.Contains(out, "\nthere is 1 customer already, declared by the resources serve applies.\n")
		x.NotContains(out, "no customers yet")
		x.NotContains(out, "makes the first one")
		x.Equal(initPrintedAsShown(out), initShownUnder(t, "operating.md", "## Declaring the rows that are configuration"),
			"operating.md shows `roster init` on a deployment that declares a tenant printing something else")
	})

	t.Run("and one made before init", func(t *testing.T) {
		x := require.New(t)

		out, err := initRun(t, before(t, true))
		x.NoError(err, "init: %s", out)

		x.Contains(out, "\nthere are 2 customers already: 1 declared by the resources serve applies, and 1 made before init.\n")
		x.NotContains(out, "makes the first one")
	})
}

// initShownUnder is the block of what `roster init` printed that a page
// shows under `heading`, up to the next heading of its level: a page that
// shows the command twice shows two cases of it.
func initShownUnder(t *testing.T, page, heading string) []string {
	t.Helper()

	doc, err := os.ReadFile(filepath.Join("..", "docs", page))
	require.NoError(t, err)

	_, under, ok := strings.Cut(string(doc), "\n"+heading+"\n")
	require.True(t, ok, "%s has no %q", page, heading)
	level := heading[:strings.Index(heading, " ")+1]
	under, _, _ = strings.Cut(under, "\n"+level)

	m := regexp.MustCompile("(?s)```sh\n(?:[^`]*\n)?roster init\n```\n+```\n(.*?)```").FindStringSubmatch(under)
	require.NotNil(t, m, "%s shows no block of what `roster init` printed under %q", page, heading)

	return initPrintedAsShown(m[1])
}
