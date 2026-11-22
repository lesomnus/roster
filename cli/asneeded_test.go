package cli_test

import (
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// asNeededBy is every function of this package that refuses without `--as`
// (`saidWho`), and the commands each is: what the code says needs a name.
// The entity commands' connectors are said on the page in words, and are
// not in its list.
var asNeededBy = map[string][]string{
	"Connect":             nil,
	"newCmdKeyList":       {"key list"},
	"newCmdKeyAdd":        {"key add"},
	"newCmdKeyRevoke":     {"key revoke"},
	"newCmdControlKeyAdd": {"control key add"},
	"vouching": {
		"vouch reset", "vouch set", "vouch unlock",
		"control vouch reset", "control vouch set", "control vouch unlock",
		"control vouch enrol", "control vouch prove",
	},
	"NewCmdRestore": {"restore"},
}

// asNeededOnThePage is the commands `docs/usage/cli.md` § *Locally, say who
// you are* says refuse to run without `--as`, each alternative of `a b|c`
// written out.
func asNeededOnThePage(t *testing.T) []string {
	t.Helper()
	x := require.New(t)

	doc, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "usage", "cli.md"))
	x.NoError(err)

	_, section, ok := strings.Cut(string(doc), "\n### Locally, say who you are\n")
	x.True(ok, "docs/usage/cli.md has no section `Locally, say who you are`")
	section, _, _ = strings.Cut(section, "\n#")
	section = strings.Join(strings.Fields(section), " ")

	_, list, ok := strings.Cut(section, "These refuse to run without it on a deployment with a control plane:")
	x.True(ok, "the section does not say which commands refuse to run without --as")
	list, _, _ = strings.Cut(list, ". ")

	out := []string{}
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(list, -1) {
		words := strings.Fields(strings.ReplaceAll(m[1], `\|`, "|"))
		last := words[len(words)-1]
		for _, v := range strings.Split(last, "|") {
			out = append(out, strings.Join(append(append([]string{}, words[:len(words)-1]...), v), " "))
		}
	}
	sort.Strings(out)

	return out
}

// asNeededCalls is, for every function of this package, the names of the
// functions it calls, read once.
func asNeededCalls(t *testing.T) map[string]map[string]bool {
	t.Helper()

	out := map[string]map[string]bool{}
	for _, f := range files(t, ".") {
		for _, d := range f.Decls {
			v, ok := d.(*ast.FuncDecl)
			if !ok || v.Body == nil {
				continue
			}
			if out[v.Name.Name] == nil {
				out[v.Name.Name] = map[string]bool{}
			}
			ast.Inspect(v.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					switch f := call.Fun.(type) {
					case *ast.Ident:
						out[v.Name.Name][f.Name] = true
					case *ast.SelectorExpr:
						out[v.Name.Name][f.Sel.Name] = true
					}
				}

				return true
			})
		}
	}

	return out
}

// TestTheManualsListOfCommandsThatNeedAsIsTheCode: the commands the manual
// says refuse to run without `--as` are the ones whose code refuses, and no
// others.
//
// The commands that write somebody's way in ran with nobody named, and the
// page said so; they refuse now, as the entity commands do (#284). A script
// that types one is written by that list, so a command that came to refuse
// without the page saying so is a script that stops, and one the page named
// that no longer refused is a write by nobody.
func TestTheManualsListOfCommandsThatNeedAsIsTheCode(t *testing.T) {
	x := require.New(t)

	called := asNeededCalls(t)
	for name := range asNeededBy {
		x.True(called[name]["saidWho"], "%s is said to refuse without --as and does not ask", name)
	}
	for name, vs := range called {
		if vs["saidWho"] {
			_, ok := asNeededBy[name]
			x.True(ok, "%s refuses without --as, and no command of the manual's list is it", name)
		}
		if vs["vouching"] {
			x.Contains([]string{"newCmdVouchReset", "newCmdVouchSet", "newCmdVouchUnlock", "newCmdOperatorEnrol", "newCmdOperatorProve"}, name,
				"%s opens the deployment through vouching, and so refuses without --as", name)
		}
	}

	code := []string{}
	for _, vs := range asNeededBy {
		code = append(code, vs...)
	}
	sort.Strings(code)

	x.Equal(code, asNeededOnThePage(t))
}
