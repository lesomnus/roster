package cli_test

import (
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
)

// statusChecksOnThePage is the list `docs/usage/cli.md` § *Exit statuses*
// gives of the commands that answer 3, as `trail verify`.
func statusChecksOnThePage(t *testing.T) []string {
	t.Helper()
	x := require.New(t)

	doc, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "usage", "cli.md"))
	x.NoError(err)

	_, section, ok := strings.Cut(string(doc), "\n## Exit statuses\n")
	x.True(ok, "docs/usage/cli.md has no section `Exit statuses`")
	section, _, _ = strings.Cut(section, "\n## ")

	item := regexp.MustCompile("(?m)^- `roster ([a-z -]+)`")
	out := []string{}
	for _, m := range item.FindAllStringSubmatch(section, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)

	return out
}

// statusChecksNamed is the command a function is that its name does not say.
var statusChecksNamed = map[string]string{
	"newCmdOperatorProve": "control vouch prove",
}

// statusChecksInTheCode is every command whose handler answers a finding:
// the functions of this package that call `finding`, `newCmdTrailVerify` read
// as `trail verify`.
func statusChecksInTheCode(t *testing.T) []string {
	t.Helper()

	out := []string{}
	for _, f := range files(t, ".") {
		for _, d := range f.Decls {
			v, ok := d.(*ast.FuncDecl)
			if !ok || v.Body == nil || v.Name.Name == "finding" {
				continue
			}
			if !calls(t, v.Name.Name, "finding") {
				continue
			}

			// The control plane's `vouch` is built by functions named for
			// the operator it is about.
			if named, ok := statusChecksNamed[v.Name.Name]; ok {
				out = append(out, named)
				continue
			}

			name, ok := strings.CutPrefix(v.Name.Name, "newCmd")
			require.True(t, ok, "%s answers a finding and is no command's handler; say which command it is", v.Name.Name)

			words := []string{}
			for i, r := range name {
				if i > 0 && unicode.IsUpper(r) {
					words = append(words, " ")
				}
				words = append(words, string(unicode.ToLower(r)))
			}
			out = append(out, strings.Join(words, ""))
		}
	}
	sort.Strings(out)

	return out
}

// TestTheManualsListOfChecksIsTheCommandsThatExitThree: the commands the
// manual says answer status 3 are the ones whose handler answers a finding,
// and no others.
//
// A script reads the status by that list: a cron job tells a trail with a
// finding from a database that was not there by it. A command that came to
// answer 3 without the page saying so would be read as one that failed, and
// one the page named that no longer did would be a check whose finding
// passed for a failure to run (#284).
func TestTheManualsListOfChecksIsTheCommandsThatExitThree(t *testing.T) {
	x := require.New(t)

	page := statusChecksOnThePage(t)
	x.NotEmpty(page, "the section lists no command, which is this test reading nothing")
	x.Equal(statusChecksInTheCode(t), page)
}
