package login_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheContractIsTheRoutes keeps `docs/login.md` § "A page of your own" honest
// the mechanical way, which is `cmd/baseline_test.go`'s move one surface along.
//
// # Why this table and not another
//
// That section is the only document in this repository that a **third party**
// writes code against: a deployment serving its own sign-in screens builds them
// against those endpoints, and `login.page.dir` is how they arrive. Four
// implementations of it already exist here -- this app and the made-up server in
// `ts/vite.login.ts` on the server side, `ts/lib/signin.tsx` and
// `frontdoor/web/frontdoor.js` on the browser's -- and until this test there was
// nothing saying they were implementing the same thing.
//
// So the two directions both cost somebody:
//
//   - **a documented endpoint that is not mounted** is a page written against a
//     404, and the author's first guess will be that they got the path wrong
//   - **a mounted endpoint the table does not claim** is the contract quietly
//     growing a surface nobody promised to keep, which is how an internal shape
//     becomes an accidental API
//
// # Why it reads source rather than standing the app up
//
// `App.Handler()` needs a roster connection, a Hydra, a session store and a
// page, and none of that is what is being asked about: the question is which
// patterns are registered, which is a fact about two functions. Reading them is
// exact where standing the app up would be a fixture to maintain -- and the
// routes themselves are exercised by `login_test.go`, `frontdoor`'s tests and
// the walks, which is what the table's last column names.
func TestTheContractIsTheRoutes(t *testing.T) {
	x := require.New(t)

	// The table: the first cell of every row in that one section.
	doc, err := os.ReadFile(filepath.Join("..", "docs", "login.md"))
	x.NoError(err)

	section := between(string(doc), "### A page of your own, and the contract it writes against")
	x.NotEmpty(section, "docs/login.md no longer has the contract section this test is about")

	pattern := regexp.MustCompile("`((?:GET|POST|PUT|DELETE) )?(/[\\w/-]*)`")

	documented := map[string]bool{}
	var rows int
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| ---") {
			continue
		}

		cell := strings.TrimSpace(strings.Split(strings.TrimPrefix(line, "|"), "|")[0])
		if cell == "mounted as" {
			continue
		}

		rows++
		for _, m := range pattern.FindAllStringSubmatch(cell, -1) {
			documented[strings.TrimSpace(m[1])+" "+m[2]] = true
		}
	}
	x.NotZero(rows, "the contract section has no table in it")
	x.NotEmpty(documented, "the contract table's first column names no endpoint")

	// What is mounted: this app's own routes, and `frontdoor`'s three, which it
	// mounts whole.
	mounted := map[string]string{}
	for _, f := range []string{
		filepath.Join("login.go"),
		filepath.Join("..", "frontdoor", "frontdoor.go"),
	} {
		src, err := os.ReadFile(f)
		x.NoError(err)

		found := regexp.MustCompile(`m\.Handle(?:Func)?\("([^"]+)"`).FindAllStringSubmatch(string(src), -1)
		x.NotEmpty(found, "%s registers no routes, so this test is reading the wrong file", f)

		for _, m := range found {
			mounted[normal(m[1])] = f
		}
	}

	for p, f := range mounted {
		x.True(documented[p], "%s mounts %q and the contract table in docs/login.md does not claim it", f, p)
	}
	for p := range documented {
		_, ok := mounted[p]
		x.True(ok, "docs/login.md promises a page %q and nothing mounts it", p)
	}

	// And the last column, which is the half that rots silently: a renamed test
	// orphans the row that said the promise was pinned.
	named := regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(section, -1)
	x.NotEmpty(named, "no row says what pins it")

	have := map[string]bool{}
	for _, dir := range []string{".", filepath.Join("..", "frontdoor")} {
		vs, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		x.NoError(err)

		for _, f := range vs {
			src, err := os.ReadFile(f)
			x.NoError(err)

			for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)`).FindAllStringSubmatch(string(src), -1) {
				have[m[1]] = true
			}
		}
	}
	for _, m := range named {
		x.True(have[m[1]], "the contract table names %s, and no test in login/ or frontdoor/ is called that", m[1])
	}

	// A walk is a gate too, and a path is checkable the same way a name is.
	for _, m := range regexp.MustCompile("`((?:docker|ts|scripts)/[\\w./-]+)`").FindAllStringSubmatch(section, -1) {
		_, err := os.Stat(filepath.Join("..", m[1]))
		x.NoError(err, "the contract table names %s, and there is no such file", m[1])
	}
}

// normal is a pattern as `http.ServeMux` would resolve it, so that the table may
// write `POST /session` where the mount is `/session` -- which is the one place
// the two lists are legitimately spelled differently, since this app mounts
// `frontdoor`'s whole handler and the methods are inside it.
func normal(p string) string {
	if !strings.Contains(p, " ") {
		return " " + p
	}

	return p
}

// TestTheMountedRoutesAreTheOnesThatAnswer is the assumption the test above
// rests on: that a pattern written in the source is a route a browser reaches.
//
// It is not obvious. This app mounts `frontdoor`'s handler under `/session` and
// `/session/` **without** stripping the prefix, so `POST /session/continue`
// matches inside the inner mux only because the outer one hands the whole path
// over. Flattening the two lists is right exactly while that stays true, and
// this is what says so.
func TestTheMountedRoutesAreTheOnesThatAnswer(t *testing.T) {
	x := require.New(t)

	inner := http.NewServeMux()
	inner.HandleFunc("POST /session", ok)
	inner.HandleFunc("POST /session/continue", ok)
	inner.HandleFunc("DELETE /session", ok)

	outer := http.NewServeMux()
	outer.Handle("/session", inner)
	outer.Handle("/session/", inner)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/session"},
		{http.MethodPost, "/session/continue"},
		{http.MethodDelete, "/session"},
	} {
		res := httptest.NewRecorder()
		outer.ServeHTTP(res, httptest.NewRequest(tc.method, tc.path, nil))
		x.Equal(http.StatusNoContent, res.Code, "%s %s did not reach the handler it is mounted for", tc.method, tc.path)
	}
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

// between is one section of a markdown document: from its heading to the next
// one at the same depth or above.
func between(doc, heading string) string {
	i := strings.Index(doc, heading)
	if i < 0 {
		return ""
	}

	rest := doc[i+len(heading):]
	for _, next := range []string{"\n### ", "\n## ", "\n# "} {
		if j := strings.Index(rest, next); j >= 0 {
			rest = rest[:j]
		}
	}

	return rest
}
