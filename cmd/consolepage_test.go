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
// under `/console/` on the control listener, so a deployment needs no
// `origins:` for its own page. A path that is not a file is the index -- the
// page routes in the browser and must survive a reload -- and `config.json`
// tells it the one thing its own origin does not say: where the admin listener
// is.
func TestTheConsoleIsServedByRosterItself(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>roster</title>"), 0o644))
	x.NoError(os.WriteFile(filepath.Join(dir, "assets", "index.js"), []byte("console.log('roster')"), 0o644))

	h, err := web.New(config.HttpConfig{}, grpc.NewServer())
	x.NoError(err)
	cmd.ConsoleMount(cmd.ConsoleConfig{Dir: dir, Admin: "https://roster-admin.internal"})(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	get := func(path string) (int, string) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		x.NoError(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(b)
	}

	code, body := get("/console/")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "<title>roster</title>")

	code, body = get("/console/assets/index.js")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "console.log")

	// A route the page owns, reloaded: the index, not a 404.
	code, body = get("/console/customers/contoso")
	x.Equal(http.StatusOK, code)
	x.Contains(body, "<title>roster</title>")

	code, body = get("/console/config.json")
	x.Equal(http.StatusOK, code)
	x.Contains(body, `"admin":"https://roster-admin.internal"`)
}
