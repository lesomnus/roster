package cmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBaselineNamesRealTests keeps docs/baseline.md honest the mechanical way.
//
// The file maps promises to the tests that pin them, and a mapping is only
// worth reading while every name in it resolves: a renamed test orphans its
// row silently, and an orphaned row is a promise that reads as pinned and is
// not -- the exact state the baseline exists to make impossible. So the doc is
// data, and this is the check, the same move as `pd gen --check`: what cannot
// be kept in step by discipline is kept in step by a diff.
//
// Only tests at the wire may be named, deliberately. The baseline is about
// what a caller experiences, and `cmd` is where the served stack is stood up
// whole -- and `account/` and `frontdoor/` are where it is stood up whole and
// then reached from outside, as the front door a customer's people arrive at,
// which is a caller's experience too. A promise that seems to need a deeper
// test's name is a promise that should be re-stated at the wire.
func TestBaselineNamesRealTests(t *testing.T) {
	x := require.New(t)

	doc, err := os.ReadFile(filepath.Join("..", "docs", "baseline.md"))
	x.NoError(err)

	have := map[string]bool{}
	var files []string
	for _, dir := range []string{".", filepath.Join("..", "account"), filepath.Join("..", "frontdoor"), filepath.Join("..", "wasm", "sandbox")} {
		vs, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		x.NoError(err)
		files = append(files, vs...)
	}

	decl := regexp.MustCompile(`(?m)^func (Test\w+)`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		x.NoError(err)

		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			have[m[1]] = true
		}
	}

	named := regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(string(doc), -1)
	x.NotEmpty(named, "the baseline names no tests at all, which cannot be right")

	for _, m := range named {
		x.True(have[m[1]], "docs/baseline.md names %s, and no such test exists", m[1])
	}
}
