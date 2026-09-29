package cmd_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cmd"
)

// TestTheConsoleIsServedByRosterItself is `control.console`: the built page
// at `/` on the control listener, so a deployment needs no
// `origins:` for its own page. A path that is not a file is the index -- the
// page routes in the browser and must survive a reload -- and every answer
// says what a browser may keep of it, which is `TestABuiltPageSaysWhatMayBeKept`
// as the mount arranges it.
func TestTheConsoleIsServedByRosterItself(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>roster</title>"), 0o644))
	x.NoError(os.WriteFile(filepath.Join(dir, "assets", "index.js"), []byte("console.log('roster')"), 0o644))

	h, err := web.New(config.HttpConfig{}, grpc.NewServer())
	x.NoError(err)
	cmd.ConsoleMount(cmd.ConsoleConfig{Dir: dir})(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	get := func(path string) (int, string, string) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		x.NoError(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(b), res.Header.Get("Cache-Control")
	}

	code, body, kept := get("/")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "<title>roster</title>")
	x.Equal("no-cache", kept, "the document is what names the bundle, and must be asked about")

	code, body, kept = get("/assets/index.js")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "console.log")
	x.Equal(immutable, kept, "a bundle is named by its content and may be kept")

	// A route the page owns, reloaded: the index, not a 404.
	code, body, kept = get("/tenants/@contoso/people")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "<title>roster</title>")
	x.Equal("no-cache", kept)

	// And no `config.json`: it existed to tell the page another origin to call,
	// and the page is served by the listener it calls now (#27, #32). A route
	// the page does not own is the index, which is what this asserts about
	// every other unknown path above.
	code, body, kept = get("/config.json")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "<title>roster</title>")
	x.Equal("no-cache", kept)

	// The trap the fallback sets: a bundle that is gone -- the previous one,
	// asked for by a tab that was open across an upgrade -- is answered with
	// the index, and the index must not be kept for a year under its name.
	code, body, kept = get("/assets/index-gone.js")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "<title>roster</title>")
	x.Equal("no-cache", kept, "the index was answered for a bundle and kept as one")
}

const immutable = "public, max-age=31536000, immutable"

// TestABuiltPageSaysWhatMayBeKept is #69, at the file server the four pages
// share. Three of the four shipped no `Cache-Control` at all, so a browser kept
// the previous `index.html` -- which names the previous bundle -- for as long
// as a heuristic off the image's build time allowed, and an upgrade was
// invisible until it felt like asking.
func TestABuiltPageSaysWhatMayBeKept(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>roster</title>"), 0o644))
	x.NoError(os.WriteFile(filepath.Join(dir, "assets", "index-DTU4Ooe7.js"), []byte("console.log('roster')"), 0o644))
	// What the user console's build carries beside the index that is not
	// hashed: the sandbox's two files, copied from `ts/public/` by name.
	x.NoError(os.WriteFile(filepath.Join(dir, "wasm_exec.js"), []byte("// go"), 0o644))

	page := cmd.Page(dir)
	serve := func(path string, said string) (int, string) {
		t.Helper()

		w := httptest.NewRecorder()
		if said != "" {
			w.Header().Set("Cache-Control", said)
		}
		page.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

		return w.Code, w.Header().Get("Cache-Control")
	}

	code, kept := serve("/", "")
	x.Equal(http.StatusOK, code)
	x.Equal("no-cache", kept)

	code, kept = serve("/assets/index-DTU4Ooe7.js", "")
	x.Equal(http.StatusOK, code)
	x.Equal(immutable, kept)

	// A name that does not change is one that must be asked about, however
	// far from the document it lives.
	code, kept = serve("/wasm_exec.js", "")
	x.Equal(http.StatusOK, code)
	x.Equal("no-cache", kept, "an unhashed file was kept as if it were a bundle")

	// Decided by what is served: a bundle that is not there is a 404 here and,
	// behind a fallback, the index -- and neither is a thing to keep. The 404
	// says nothing at all, because `ServeContent` strips what was said on an
	// error; what matters is that it does not say a year.
	code, kept = serve("/assets/nope.js", "")
	x.Equal(http.StatusNotFound, code)
	x.NotEqual(immutable, kept, "a bundle that is not there was kept as one")

	code, kept = serve("/assets/", "")
	x.Equal("no-cache", kept, "a listing was kept as a bundle")

	// The file server cleans the path, and so does the rule: the document by
	// a bundle's road is still the document.
	_, kept = serve("/assets/../index.html", "")
	x.NotEqual(immutable, kept, "the document was kept for a year by way of `..`")

	// And an app that has already said what to do with its document is not
	// overruled: the Login App's `no-store` stays.
	code, kept = serve("/", "no-store")
	x.Equal(http.StatusOK, code)
	x.Equal("no-store", kept)
}
