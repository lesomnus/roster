package cmd_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// TestTheQuickStartRunsAsWritten types what `README.md` says to type from a
// checkout, as it is written there, and makes the call it ends on.
//
// # Why the page is read, and not followed by hand
//
// `TestTheTutorialRunsAsWritten` follows its page: the commands are written in
// the test, beside the page, and a reader keeps the two together. The quick
// start had nobody. It went on saying `holder add`, `role add` and `binding
// add` after `tenant add` had begun writing those three rows itself, so the
// first thing anybody typed from this repository answered `AlreadyExists`
// twice and bound a role a second time -- and every gate was green, because
// what holds the pages holds the names in them (`cli/docs_test.go`), and each
// of those commands exists.
//
// So the block is read from the page and run a line at a time. A line this
// does not know how to run fails it: the page does not grow a command nothing
// ran.
//
// # What stands in for what
//
// `go run ./cmd/roster` is the command, run in this process. `serve &` is a
// server built on the same databases and its transcoded listener, which is
// what the curl is pointed at. `KEY=$(…)` is the key, kept.
func TestTheQuickStartRunsAsWritten(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	page, err := os.ReadFile(filepath.Join("..", "README.md"))
	x.NoError(err)

	_, after, found := strings.Cut(string(page), "### Or from a checkout")
	x.True(found, "README.md has no quick start from a checkout")
	blocks := regexp.MustCompile("(?s)```sh\n(.*?)```").FindAllStringSubmatch(after, 2)
	x.Len(blocks, 2, "the commands, and then the call")

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	var (
		srv  *httptest.Server
		key  string
		runs int
	)
	comment := regexp.MustCompile(`\s+#\s.*$`)
	for _, line := range strings.Split(blocks[0][1], "\n") {
		line = strings.TrimSpace(comment.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}
		runs++

		switch {
		case line == "go run ./cmd/roster init":
			out, err := initRun(t, c)
			x.NoError(err, "%s: %s", line, out)

		case line == "go run ./cmd/roster serve &":
			s, err := cmd.Build(ctx, c)
			x.NoError(err, line)
			t.Cleanup(func() { s.Close() })

			g, err := s.Grpc(ctx, cmd.Config{})
			x.NoError(err)
			h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
			x.NoError(err)

			srv = httptest.NewServer(h)
			t.Cleanup(srv.Close)

		case strings.HasPrefix(line, "KEY=$(roster ") && strings.HasSuffix(line, ")"):
			args := words(t, strings.TrimSuffix(strings.TrimPrefix(line, "KEY=$(roster "), ")"))
			key = strings.TrimSpace(stdoutOf(t, cli.Cmd(&c), args...))
			x.NotEmpty(key, "%s printed no key", line)

		case strings.HasPrefix(line, "roster "):
			out, err := entities(t, &c, words(t, strings.TrimPrefix(line, "roster "))...)
			x.NoError(err, "%s\n%s", line, out)

		default:
			t.Fatalf("README.md's quick start has a line this does not know how to run: %q", line)
		}
	}
	x.Equal(4, runs, "the heading says five commands, and the fifth is the call")
	x.NotNil(srv, "nothing was served")
	x.NotEmpty(key, "no key was minted")

	// The call, as the page writes it: the path it names, with the key.
	call := blocks[1][1]
	x.Contains(call, `"authorization: Bearer ${KEY}"`)
	path := regexp.MustCompile(`http://[^/\s]+(/roster\.\w+/\w+)`).FindStringSubmatch(call)
	x.Len(path, 2, "the call names no RPC:\n%s", call)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path[1], strings.NewReader(`{}`))
	x.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+key)

	res, err := http.DefaultClient.Do(req)
	x.NoError(err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	x.NoError(err)

	// "The key is printed once and resolves to that person."
	x.Equal(http.StatusOK, res.StatusCode, "%s", body)
	x.Contains(string(body), `"admin"`, "the key does not resolve to the person the tenant arrived with: %s", body)
}

// words is a line as a shell would hand it to a command: split at spaces, a
// run in single quotes kept whole. It is as much shell as the quick start
// uses, and a line that wants more is one this refuses to guess at.
func words(t *testing.T, line string) []string {
	t.Helper()

	if strings.ContainsAny(line, "\"\\$`|;&<>") {
		t.Fatalf("more shell than this reads: %q", line)
	}

	var (
		out    []string
		word   strings.Builder
		quoted bool
		any    bool
	)
	for _, r := range line {
		switch {
		case r == '\'':
			quoted = !quoted
			any = true
		case r == ' ' && !quoted:
			if any {
				out = append(out, word.String())
				word.Reset()
				any = false
			}
		default:
			word.WriteRune(r)
			any = true
		}
	}
	require.False(t, quoted, "a quote that does not close: %q", line)
	if any {
		out = append(out, word.String())
	}

	return out
}
