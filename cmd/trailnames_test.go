package cmd_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// trailNamesWritten is every name of roster's own the code gives a line of the
// trail, read out of the source: a constant, in any package, whose value is
// `roster.` and two or more words in small letters. That is how each of them is
// declared (`keys.Used`, `forget.DestroyedTenant`, `cli`'s `holdPlaced`), and
// four of them are unexported in `cli`, which a test here cannot name. Tests,
// `ts/` and `node_modules` are not read.
func trailNamesWritten(t *testing.T) map[string][]string {
	t.Helper()

	declared := regexp.MustCompile(`(?m)^\s*(?:const\s+)?\w+\s*=\s*"(roster(?:\.[a-z]+){2,})"`)
	root := filepath.Join("..")

	out := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "ts", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range declared.FindAllStringSubmatch(string(src), -1) {
			rel, _ := filepath.Rel(root, path)
			out[m[1]] = append(out[m[1]], rel)
		}

		return nil
	})
	require.NoError(t, err)

	return out
}

// TestTheTrailsOwnNamesAreTheManualsTable holds the table of
// `docs/operating.md` that names each act with no RPC on the trail to the names
// the code writes, both ways.
//
// A line of the trail is named by the RPC that wrote it or, for an act with no
// RPC, by a name of roster's own. There are seventeen. Five were in no page of
// `docs/` but the roadmap -- `roster.holder.forget`, `roster.tenant.destroy`
// and the three sweeps' that are named for a package -- and the others were in
// three places and nowhere whole, so a reader of a trail who met
// `roster.keys.expire` found that it is a delegation by reading
// `server/keys`. Each is a row of one table, with the act that writes it and
// the row the line is about; a name the code adds is a row the page has to
// gain, and a row the page keeps after the code drops the name fails here.
func TestTheTrailsOwnNamesAreTheManualsTable(t *testing.T) {
	x := require.New(t)

	written := trailNamesWritten(t)
	x.NotEmpty(written, "no name of the trail was found in the source, which is this test reading nothing")

	doc, err := os.ReadFile(filepath.Join("..", "docs", "operating.md"))
	x.NoError(err)

	_, section, ok := strings.Cut(string(doc), "\n| the name | what writes it | what the line is about (`object_id`) |\n")
	x.True(ok, "docs/operating.md has no table of the trail's own names")

	row := regexp.MustCompile("^\\| `(roster(?:\\.[a-z]+){2,})` \\|")
	var said []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| ---") {
			continue
		}
		if !strings.HasPrefix(line, "| ") {
			break
		}
		m := row.FindStringSubmatch(line)
		x.NotNil(m, "a row of the table of the trail's own names names no act: %s", line)
		x.NotContains(said, m[1], "the table names %s twice", m[1])
		said = append(said, m[1])
	}

	for name, where := range written {
		x.Contains(said, name, "%s writes %s on the trail, and docs/operating.md's table does not say it", strings.Join(where, " and "), name)
	}
	for _, name := range said {
		_, ok := written[name]
		x.True(ok, "docs/operating.md's table says %s, and nothing writes it", name)
	}
	x.True(slices.IsSorted(said), "the table is in the order of the names, so that one is found by reading down it")
}
