package cli_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryCommandBuildsWhatItWritesQueued holds the commands to `built`
// (#214): a command at a shell is a process of its own that ends once it has
// answered, so the deployment it stands up writes to the outbox, and the
// process that serves publishes what it wrote. One that called `cmd.Build`
// itself would write rows an app following the stream hears of only when it
// next asks -- which is what `roster holder disable` did, and nothing failed.
//
// `serve` is the one command that serves, and so publishes for itself; `cli.go`
// is where `built` is.
func TestEveryCommandBuildsWhatItWritesQueued(t *testing.T) {
	x := require.New(t)

	fs := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	x.NoError(err)

	builds := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		v, err := parser.ParseFile(fs, f, nil, 0)
		x.NoError(err)

		ast.Inspect(v, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Build" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "cmd" {
				builds[f]++
			}

			return true
		})
	}

	x.Equal(map[string]int{"cli.go": 1, "serve.go": 1}, builds,
		"a command builds the deployment through `built`, so that what it writes is queued for the serving process to publish")
}
