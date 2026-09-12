package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// The documentation is checked rather than re-read.
//
// Four things a page can name that this repository can answer for itself: a file,
// a command, a test, and an environment variable. Every one of them rots the same
// way -- the tree moves and the sentence does not -- and none of them is caught by
// a compiler, a reviewer or a reader who already knows what was meant.
//
// What made it a test rather than an afternoon: an audit on 2026-09-12 found
// `ts/plan.md` cited eleven times from code and twice from a doc, as the authority
// for four *invariants* and five lettered sections, months after the file was
// retired; `ts/src` cited six times after it became `ts/console` and `ts/lib`;
// four citations of `cl/…`, which is a mangled `cmd/…`; and two commands written
// `roster groupmembership add`, which fail as typed. Not one of those was visible
// to anything.
//
// # What it deliberately does not check
//
// **`docs/roadmap.md`**, which is a record of the tree as each piece of work
// landed: it names files and services that have since gone, each because a later
// row says what replaced them. Checking it would ask history to be current, and
// the file says so in its own second paragraph.
//
// **Configuration keys** written `login.page.dir`, because a proto field path
// (`holder.tenant`) and a key in somebody else's configuration (`urls.login`) are
// spelled exactly the same way, and a check that cannot tell them apart teaches
// people to work around it. `ROSTER_…` is checked instead: it is unambiguous, and
// it is the form an operator copies.
//
// **RPC names**, for the same reason: `Holder.Invalidate` is the doc's shorthand
// for a generated method, a Go method on a layer, and a field on a request
// message, and the three are not distinguishable from the page.

// elsewhere is the paths a comment or a page may name that are not in this tree,
// each with the reason it is not a mistake.
var elsewhere = map[string]string{
	"docs/guide/server.md":        "payday's guide, cited as payday's",
	"docs/guide/signing-in.md":    "payday's guide",
	"consent/strategy_default.go": "Hydra's source, cited with its version",
	"auth/requests/":              "Hydra's admin API path, not a file",
	"ts/plan.md":                  "retired, and named in docs/roadmap.md as retired",
	"PLAN.md":                     "retired; `git log -p -- PLAN.md` is where it is",
	".vite/deps/":                 "vite's own cache directory, at run time",
	"esm/vs/":                     "monaco's layout inside its package",
	"esm/vs/esm/vs/":              "the same, wrong on purpose in a comment about it",
	"./esm/vs/*.js":               "the same",
	"public/":                     "vite's convention, named in a comment about it",
	"lib/route.ts":                "relative to ts/console, where the comment is",
	"clients/*.json":              "relative to deploy/, and a glob",
	"migrations/":                 "named to say there is not one",
}

// TestTheDocumentationNamesFilesThatExist reads every backticked path in the
// documentation and in the comments of the code, and opens it.
func TestTheDocumentationNamesFilesThatExist(t *testing.T) {
	x := require.New(t)
	root := repoRoot(t)

	known := []string{
		".go", ".md", ".proto", ".yaml", ".yml", ".json", ".sh", ".ts", ".tsx",
		".js", ".sql", ".html", ".txt", ".mod", ".lock", ".css",
	}
	looksLikeAPath := func(s string) bool {
		if !strings.Contains(s, "/") || strings.ContainsAny(s, " *…()\"'") {
			return false
		}
		if strings.Contains(s, "://") || strings.Contains(s, "${") ||
			strings.HasPrefix(s, "/") || strings.HasPrefix(s, "@") {
			return false
		}

		// A path that is **produced** is not a pointer into the tree, and
		// checking one makes this gate answer differently depending on whether
		// somebody has built: `ts/console/devtools.tsx` names `ts/dist/console/`,
		// which is here after `npm run build` and not in a fresh checkout. It was
		// green on a desk and red in CI, which is the worst thing a gate can be.
		for _, made := range []string{"dist/", "node_modules/", ".vite/", "target/"} {
			if strings.HasPrefix(s, made) || strings.Contains(s, "/"+made) {
				return false
			}
		}
		if strings.HasSuffix(s, "/") {
			return true
		}
		for _, ext := range known {
			if strings.HasSuffix(s, ext) {
				return true
			}
		}

		return false
	}

	for _, f := range sources(t, root) {
		src, err := os.ReadFile(filepath.Join(root, f))
		x.NoError(err)

		page := strings.HasSuffix(f, ".md")
		for i, line := range strings.Split(string(src), "\n") {
			// In a page every line counts; in source only a comment does. A
			// backtick is a raw string in Go and a template literal in
			// TypeScript, and `${base}/` is neither a path nor a mistake.
			if !page && !comment(line) {
				continue
			}

			for _, tok := range regexp.MustCompile("`([^`\n]+)`").FindAllStringSubmatch(line, -1) {
				p := strings.TrimRight(strings.SplitN(tok[1], "#", 2)[0], ",.;:")
				if !looksLikeAPath(p) {
					continue
				}
				if _, ok := elsewhere[p]; ok {
					continue
				}

				// Relative to the tree, or to whatever names it -- a comment in
				// `ts/console` may say `lib/route.ts` and mean its neighbour.
				_, here := os.Stat(filepath.Join(root, p))
				_, beside := os.Stat(filepath.Join(root, filepath.Dir(f), p))
				x.True(here == nil || beside == nil,
					"%s:%d names %s, and there is no such file; if it is somebody else's, say so in `elsewhere`", f, i+1, p)
			}
		}
	}
}

// TestTheDocumentationNamesCommandsThatExist walks the command tree this binary
// actually builds and holds every documented invocation against it.
//
// Backticks and fenced blocks only. Prose says *roster answers* and *roster runs
// twice*, which are sentences rather than commands, and a check that read those
// would be a check somebody turns off.
func TestTheDocumentationNamesCommandsThatExist(t *testing.T) {
	x := require.New(t)
	root := repoRoot(t)

	have := map[string]bool{}
	var walk func(path []string, c *xli.Command)
	walk = func(path []string, c *xli.Command) {
		if len(path) > 0 {
			have[strings.Join(path, " ")] = true
		}
		for _, sub := range c.Commands {
			walk(append(append([]string{}, path...), sub.Name), sub)
		}
	}
	walk(nil, cli.Cmd(&cmd.Config{}))
	x.Greater(len(have), 100, "the command tree did not build")

	word := regexp.MustCompile(`^[a-z][\w-]*$`)
	named := regexp.MustCompile(`(?:^|[^-\w])roster\s+([a-z][\w-]*(?:\s+[a-z][\w-]*)*)`)

	for _, f := range docs(t, root) {
		src, err := os.ReadFile(filepath.Join(root, f))
		x.NoError(err)

		// Only a shell fence holds commands. A `mermaid` note says *roster stores
		// it and never reads it*, which is a sentence, and an unlabelled fence is
		// usually sample output.
		shell := map[string]bool{"sh": true, "bash": true, "shell": true, "console": true}
		fenced := false
		for i, line := range strings.Split(string(src), "\n") {
			if tag, ok := strings.CutPrefix(strings.TrimSpace(line), "```"); ok {
				fenced = !fenced && shell[strings.TrimSpace(tag)]

				continue
			}

			var inside []string
			if fenced {
				inside = []string{line}
			} else {
				for _, m := range regexp.MustCompile("`([^`\n]+)`").FindAllStringSubmatch(line, -1) {
					inside = append(inside, m[1])
				}
			}

			for _, s := range inside {
				for _, m := range named.FindAllStringSubmatch(s, -1) {
					var path []string
					for _, w := range strings.Fields(m[1]) {
						if !word.MatchString(w) {
							break
						}
						path = append(path, w)
					}

					// Every lowercase word has to be part of the command,
					// which is stricter than *the longest prefix resolves* on
					// purpose: that version accepts `roster holder frobnicate`,
					// since `holder` is a command and the rest reads as an
					// argument. An argument in this documentation is `@tenant`,
					// a flag, JSON or `<PLACEHOLDER>`, and every one of those
					// ends the run before this sees it.
					x.True(have[strings.Join(path, " ")],
						"%s:%d says `roster %s`, and no such command exists", f, i+1, m[1])
				}
			}
		}
	}
}

// TestTheDocumentationNamesTestsThatExist keeps a page from claiming a promise is
// pinned by a test that has been renamed out from under it.
//
// `docs/baseline.md` and `docs/login.md` both hold their own version of this over
// the tests they name; this one is the same check over every other page, so a
// name in a paragraph is worth as much as a name in a table.
func TestTheDocumentationNamesTestsThatExist(t *testing.T) {
	x := require.New(t)
	root := repoRoot(t)

	have := map[string]bool{}
	x.NoError(filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return err
		}
		if strings.Contains(p, "node_modules") {
			return nil
		}

		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)`).FindAllStringSubmatch(string(src), -1) {
			have[m[1]] = true
		}

		return nil
	}))

	// Somebody else's, cited as theirs.
	have["TestOverlappingWritersDoNotFail"] = true // payday's pdtest, cited with its sha

	for _, f := range docs(t, root) {
		src, err := os.ReadFile(filepath.Join(root, f))
		x.NoError(err)

		for i, line := range strings.Split(string(src), "\n") {
			for _, m := range regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(line, -1) {
				x.True(have[m[1]], "%s:%d names %s, and no such test exists", f, i+1, m[1])
			}
		}
	}
}

// TestTheDocumentationNamesVariablesThatAreRead holds every `ROSTER_…` in the
// documentation against what the loader reads.
//
// The four prefixes are the ones the consumers read for themselves rather than
// through the loader (`cli.Cmd`'s `pdcmd.Reads`), so they are names with an alias
// after them and are documented as such.
func TestTheDocumentationNamesVariablesThatAreRead(t *testing.T) {
	x := require.New(t)
	root := repoRoot(t)

	have := map[string]bool{}
	for _, n := range cmd.Loader.EnvNames(&cmd.Config{}) {
		have[n] = true
	}
	x.Greater(len(have), 50, "the loader named nothing")

	perAlias := []string{"ROSTER_ACCOUNT_KEY_", "ROSTER_LDAP_KEY_", "ROSTER_LOGIN_KEY_", "ROSTER_LOGIN_CLIENT_"}

	// And what something in this tree reads for itself. `ROSTER_ROOT_PASSWORD` is
	// the image's entrypoint, not a field, and a deployment copies it out of the
	// documentation exactly as it copies a field -- so it is documented, and what
	// makes it not a typo is that a file reads it.
	read := map[string]bool{}
	for _, f := range sources(t, root) {
		if strings.HasSuffix(f, ".md") {
			continue
		}

		src, err := os.ReadFile(filepath.Join(root, f))
		x.NoError(err)

		for _, g := range regexp.MustCompile(`(?:^|[^A-Z0-9_])(ROSTER_[A-Z0-9_]+)`).FindAllStringSubmatch(string(src), -1) {
			read[g[1]] = true
		}
	}

	for _, f := range docs(t, root) {
		src, err := os.ReadFile(filepath.Join(root, f))
		x.NoError(err)

		for i, line := range strings.Split(string(src), "\n") {
			for _, g := range regexp.MustCompile(`(?:^|[^A-Z0-9_])(ROSTER_[A-Z0-9_]+)`).FindAllStringSubmatch(line, -1) {
				m := g[1]
				if have[m] {
					continue
				}

				per := read[m]
				for _, p := range perAlias {
					if strings.HasPrefix(m+"_", p) || strings.HasPrefix(m, p) {
						per = true
					}
				}
				x.True(per, "%s:%d names %s, and no field answers to it and nothing reads it", f, i+1, m)
			}
		}
	}
}

// TestTheDocumentationLinksResolve opens every relative markdown link.
func TestTheDocumentationLinksResolve(t *testing.T) {
	x := require.New(t)
	root := repoRoot(t)

	for _, f := range docs(t, root) {
		src, err := os.ReadFile(filepath.Join(root, f))
		x.NoError(err)

		for i, line := range strings.Split(string(src), "\n") {
			for _, m := range regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`).FindAllStringSubmatch(line, -1) {
				to := strings.SplitN(m[1], "#", 2)[0]
				if to == "" || strings.HasPrefix(to, "http") || strings.HasPrefix(to, "mailto") {
					continue
				}

				_, err := os.Stat(filepath.Join(root, filepath.Dir(f), to))
				x.NoError(err, "%s:%d links to %s, and there is nothing there", f, i+1, to)
			}
		}
	}
}

// comment reports whether a line of source is one.
func comment(line string) bool {
	s := strings.TrimSpace(line)

	return strings.HasPrefix(s, "//") || strings.HasPrefix(s, "*") ||
		strings.HasPrefix(s, "/*") || strings.HasPrefix(s, "#")
}

// docs is every page a reader is pointed at, which is every page but the record.
func docs(t *testing.T, root string) []string {
	t.Helper()

	out := []string{"README.md", "CLAUDE.md", "deploy/README.md"}
	for _, dir := range []string{"docs", filepath.Join("docs", "usage")} {
		vs, err := filepath.Glob(filepath.Join(root, dir, "*.md"))
		require.NoError(t, err)

		for _, v := range vs {
			rel, err := filepath.Rel(root, v)
			require.NoError(t, err)
			if rel == filepath.Join("docs", "roadmap.md") {
				continue // the record; see this file's comment
			}
			out = append(out, rel)
		}
	}

	return out
}

// sources is the documentation plus everything with comments in it, because a
// dangling pointer in a comment is the one nobody is looking at.
func sources(t *testing.T, root string) []string {
	t.Helper()

	out := docs(t, root)
	skip := []string{
		"node_modules", string(filepath.Separator) + "dist", "internal/ent/",
		"server/bare/", "server/pd/", "ts/gen/", "proto/roster/payday/", "ts/vendor/",
		".git", "rstr/",
	}

	require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		for _, s := range skip {
			if strings.Contains(rel, s) {
				if info.IsDir() {
					return filepath.SkipDir
				}

				return nil
			}
		}
		if info.IsDir() {
			return nil
		}

		switch {
		case strings.HasSuffix(p, ".pb.go"), strings.HasSuffix(p, ".g.go"):
			return nil
		case strings.HasSuffix(p, ".go"), strings.HasSuffix(p, ".proto"),
			strings.HasSuffix(p, ".sh"), strings.HasSuffix(p, ".ts"),
			strings.HasSuffix(p, ".tsx"), strings.HasSuffix(p, ".yaml"):
			out = append(out, rel)
		}

		return nil
	}))

	return out
}

// repoRoot is the tree this test is in, found by looking for `go.mod`.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("no go.mod above the working directory")

	return ""
}
