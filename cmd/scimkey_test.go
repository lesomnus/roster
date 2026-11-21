package cmd_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// TestTheEndpointsKeyIsTheManualsList: the table in `docs/scim.md` of what the
// directory's key may do names exactly what the endpoint calls
// (`scim.Methods`), and the rows the role does not bind are the ones the
// role leaves out (`scim.Bound()`).
//
// The table was written by hand, twenty-six methods counted, and nothing held
// it to the code, where LDAP's list has `TestTheDirectorysKeyIsTheManualsList`.
// A method the endpoint comes to call is a key minted again in every tenant,
// and the table is where an operator reads why.
//
// A cell names a service once and its next methods by name alone
// (`HolderService/Deactivate`, `Activate`), as the table is written to be
// read.
func TestTheEndpointsKeyIsTheManualsList(t *testing.T) {
	x := require.New(t)

	page, err := os.ReadFile("../docs/scim.md")
	x.NoError(err)
	_, after, ok := strings.Cut(string(page), "\n## What its key may do, and why not more\n")
	x.True(ok, "docs/scim.md has no section called What its key may do, and why not more")
	table, _, _ := strings.Cut(strings.TrimLeft(after, "\n"), "\n\n")

	ticked := regexp.MustCompile("`([^`]+)`")
	var all, bound []string
	for _, line := range strings.Split(table, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 3 || !strings.Contains(cells[1], "`") {
			continue
		}

		service := ""
		var row []string
		for _, m := range ticked.FindAllStringSubmatch(cells[1], -1) {
			name := m[1]
			if s, method, ok := strings.Cut(name, "/"); ok {
				service, name = s, method
			}
			x.NotEmpty(service, "%q names a method before any service", line)
			row = append(row, "/roster."+service+"/"+name)
		}
		all = append(all, row...)
		if !strings.Contains(cells[2], "**not bound**") {
			bound = append(bound, row...)
		}
	}

	x.ElementsMatch(scim.Methods, all, "the manual's table is not what the endpoint's key lists")
	x.ElementsMatch(scim.Bound(), bound, "the rows the manual binds are not what the directory's role holds")
}
