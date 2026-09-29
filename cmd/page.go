package cmd

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Page serves a built page out of a directory: the document, the hashed
// bundles under `assets/`, and whatever else the build put beside them.
//
// It is `http.FileServer` and the one header a file server cannot know, and
// all four pages roster serves go through it -- the two consoles by way of
// [ConsoleMount], the account page and the Login App's by way of `cli/`. It
// was three file servers, and the three that set nothing were the ones it took
// a person to find.
//
// # What a file server does not say
//
// It sends no `Cache-Control`, so a browser falls back to a heuristic: a share,
// commonly a tenth, of how long it has been since `Last-Modified`. And
// `Last-Modified` here is the mtime of the file in the image, which is when the
// page was **built** -- the same for every file, and further in the past the
// longer a deployment stays on one pin. So the older an image, the longer a
// browser keeps the `index.html` it has, which names the previous bundle. An
// upgrade shipped, the server answered right, and the person it was for got
// the old page for hours; a reload did not clear it and a private window did
// (#69). Nothing was red anywhere, which is the expensive shape.
//
// # The two answers
//
//	the document, and anything else beside it     no-cache
//	assets/<name>-<hash>.*                        public, max-age=31536000, immutable
//
// The first is "ask every time", and asking is cheap: a conditional request is
// answered 304 off `Last-Modified`. The second is "never ask": vite names every
// bundle by its content, so a changed file is a different URL and the one a
// browser holds is right for as long as it holds it. The second is what makes
// the first cost nothing -- the document is small and revalidates, and the
// bundles behind it never have to.
//
// Decided by **what is served** and not by the URL. [ConsoleMount] answers a
// path that is not a file with the document, so a request for a bundle that is
// gone -- the previous one, from a tab open across an upgrade -- would otherwise
// be answered with the document under a bundle's name and kept for a year. And
// `assets/` by name rather than "everything but the document", because a build
// puts unhashed files beside it too: the sandbox's `app.wasm` and `wasm_exec.js`
// land in the admin and user console builds from `ts/public/`, and a name that
// does not change is one that must be asked about.
//
// Said only where nothing has said otherwise. An app that knows its document
// must not be kept at all sets `no-store` before handing over and keeps it --
// the Login App, for a form bound to a challenge that is spent when it is
// posted (`login/page.go`).
func Page(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", cacheOf(dir, r.URL.Path))
		}

		files.ServeHTTP(w, r)
	})
}

// cacheOf is what a browser may do with the file at `p`, by the rule above.
//
// Cleaned first, as the file server cleans it: `/assets/../index.html` is the
// document, and the document is never immutable.
func cacheOf(dir, p string) string {
	rel, ok := strings.CutPrefix(path.Clean("/"+p), "/assets/")
	if !ok || rel == "" {
		return "no-cache"
	}

	st, err := os.Stat(filepath.Join(dir, "assets", filepath.FromSlash(rel)))
	if err != nil || !st.Mode().IsRegular() {
		return "no-cache"
	}

	return "public, max-age=31536000, immutable"
}
