package cmd_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/keys"
)

// TestTheTutorialRunsAsWritten is docs/usage/tutorial.md, step for step.
//
// The suite already proves most of what the tutorial shows, each claim in its
// own test against its own harness -- and that is exactly how the control
// plane shipped refusing every key it held: every part was tested and the
// documented journey was not, so the one path an integrator actually walks was
// the one path nothing walked. A journey test is not redundant with the tests
// of its parts; it is the assertion that the parts are wired into the walk the
// doc sells.
//
// So this follows the page: the CLI commands as written, and the wire calls
// over the **transcoded data plane** -- `server.http`, which the tutorial's
// curl talks to and which no other test stands up. Where the page shows an
// answer, the shape of the answer is asserted; where it promises a refusal,
// the refusal is. If this test disagrees with the page, one of the two is
// wrong and both are load-bearing.
func TestTheTutorialRunsAsWritten(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// §1 -- two databases and a server.
	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	x.Contains(out, "no customers yet", "the state the tutorial starts from")

	// The server the curl half talks to. Built once; the commands below build
	// their own on the same files, which is the tutorial's arrangement too --
	// `serve` in one terminal and the CLI in another.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// §2 -- a customer, and it is four writes.
	_, err = entities(t, &c, "tenant", "add", "@newco", `{"name":"Newco Ltd"}`)
	x.NoError(err)
	_, err = entities(t, &c, "holder", "add", "@newco/admin", `{"name":"Ada Admin"}`)
	x.NoError(err)
	_, err = entities(t, &c, "role", "add", "@newco/everything", `{"methods":["/roster.*/*"]}`)
	x.NoError(err)
	bind(t, &c, `{"role":  {"slug":{"alias":"everything","tenant":{"alias":"newco"}}},
	              "holder":{"slug":{"alias":"admin",      "tenant":{"alias":"newco"}}}}`)

	table, err := entities(t, &c, "tenant", "ls", "-o", "table")
	x.NoError(err)
	x.Contains(table, "newco")
	x.Contains(table, "Newco Ltd")

	// §3 -- somebody who works there.
	_, err = entities(t, &c, "holder", "add", "@newco/alice", `{"name":"Alice Nguyen"}`)
	x.NoError(err)
	_, err = entities(t, &c, "role", "add", "@newco/support",
		`{"methods":["/roster.HolderService/Get","/roster.HolderService/List","/roster.MeService/Get"]}`)
	x.NoError(err)
	bind(t, &c, `{"role":  {"slug":{"alias":"support","tenant":{"alias":"newco"}}},
	              "holder":{"slug":{"alias":"alice",  "tenant":{"alias":"newco"}}}}`)

	// §4 -- ways in. The secret is on stdout and the sentence on stderr, so
	// `$(roster vouch reset …)` is the password and nothing else -- which is
	// what `stdoutOf` captures and this test then signs in with.
	pw := stdoutOf(t, cmd.Cmd(&c), "vouch", "reset", "@newco/alice")

	app := stdoutOf(t, cmd.NewCmdKey(&c), "add", "--service", "portal",
		"--allow", "/roster.VouchService/Verify,/roster.MeService/Get")
	key := stdoutOf(t, cmd.NewCmdKey(&c), "add", "--tenant", "newco", "--holder", "alice",
		"--name", "laptop", "--allow", "/roster.MeService/Get")

	// "the prefix follows from which it is rather than from anything you
	// typed."
	x.True(strings.HasPrefix(app, keys.PrefixDeployment), "a service's key: %.3q", app)
	x.True(strings.HasPrefix(key, keys.PrefixTenant), "a person's key: %.3q", key)

	// "Two planes in one listing."
	listing := stdoutOf(t, cmd.NewCmdKey(&c), "list")
	x.Contains(listing, "/portal/")
	x.Contains(listing, "@newco/alice/laptop")

	var laptop string
	for _, line := range strings.Split(listing, "\n") {
		if strings.Contains(line, "@newco/alice/laptop") {
			laptop = strings.Fields(line)[0]
		}
	}
	x.NotEmpty(laptop, "the listing names the key `revoke --id` takes:\n%s", listing)

	// The port the tutorial's curl talks to: the **walled data plane**,
	// transcoded -- `server.http` in the shipped configuration. The same `g`,
	// so the same interceptors and the same wall; there is no second stack
	// here for a rule to be missing from.
	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)

	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	post := func(path, bearer, body string) (int, string) {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(body))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+bearer)

		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		defer res.Body.Close()

		b, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(b)
	}

	// §5 -- the login app checks Alice's password.
	sec := base64.StdEncoding.EncodeToString([]byte(pw))

	var herId string
	t.Run("the portal's key verifies her password", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.VouchService/Verify", app,
			`{"who":{"tenant":"newco","alias":"alice"},"secret":"`+sec+`"}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"ok":true`)

		// Who she is, kept to compare the refusal against: a wrong password
		// must name nobody, and "does not contain this" needs a this.
		herId = fieldOf(t, body, "holder")
		x.NotEmpty(herId)
	})

	t.Run("and a wrong one is an answer, not an error", func(t *testing.T) {
		x := require.New(t)

		wrong := base64.StdEncoding.EncodeToString([]byte("not-her-password"))
		code, body := post("/roster.VouchService/Verify", app,
			`{"who":{"tenant":"newco","alias":"alice"},"secret":"`+wrong+`"}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"ok":false`)
		x.NotContains(body, herId, "a refusal named who it refused")
	})

	// §6 -- Alice's own key.
	t.Run("her key answers as her, narrowed to itself", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.MeService/Get", key, `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"alice"`, "an rt_ resolves to its holder")
		x.Contains(body, "/roster.MeService/Get")
		x.NotContains(body, "/roster.HolderService/Get",
			"`methods` is the key's, not Alice's -- her role has three")
	})

	t.Run("Alice may call it; her laptop may not", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.HolderService/List", key, `{}`)
		x.Equal(http.StatusForbidden, code, body)
		x.Contains(body, "permission_denied")
	})

	// §7 -- which customer is this request for?
	_, err = entities(t, &c, "host", "add", `{"tenant":{"alias":"newco"},"name":"newco.example.com"}`)
	x.NoError(err)

	fd := stdoutOf(t, cmd.NewCmdKey(&c), "add", "--service", "frontdoor",
		"--allow", "/roster.FrontService/WhoseHost,/roster.VouchService/Verify")

	t.Run("a hostname resolves to a tenant and nothing else", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.FrontService/WhoseHost", fd, `{"host":"newco.example.com"}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"tenant"`)
		x.NotContains(body, "newco", "more than an identifier before anybody has authenticated")
	})

	t.Run("a name nobody claimed is not found", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.FrontService/WhoseHost", fd, `{"host":"nobody.example.com"}`)
		x.Equal(http.StatusNotFound, code, body)
	})

	t.Run("the portal's key could not make that call", func(t *testing.T) {
		x := require.New(t)

		// "That refusal is the system working, and it is what a role is for."
		code, body := post("/roster.FrontService/WhoseHost", app, `{"host":"newco.example.com"}`)
		x.Equal(http.StatusForbidden, code, body)
	})

	// §8 -- stopping something.
	t.Run("a revoked key finds nothing on the very next call", func(t *testing.T) {
		x := require.New(t)

		k := cmd.NewCmdKey(&c)
		k.Writer = io.Discard
		x.NoError(k.Run(ctx, []string{"revoke", "--id", laptop}))

		code, body := post("/roster.MeService/Get", key, `{}`)
		x.Equal(http.StatusUnauthorized, code, body)
	})

	t.Run("and unlock answers for an account that was not locked", func(t *testing.T) {
		x := require.New(t)

		r := cmd.Cmd(&c)
		r.Writer = io.Discard
		x.NoError(r.Run(ctx, []string{"vouch", "unlock", "@newco/alice"}))
	})
}

// bind is `echo '{…}' | roster binding add -`, which is how the tutorial
// writes the one request whose two references do not fit on a flag.
func bind(t *testing.T, c *cmd.Config, req string) {
	t.Helper()

	root := cmd.Cmd(c)
	root.ReadCloser = io.NopCloser(strings.NewReader(req))
	root.Writer = io.Discard

	require.NoError(t, root.Run(t.Context(), []string{"binding", "add", "-"}))
}

// fieldOf is the string value of a field in a one-level protojson answer,
// asserted present. Enough Json for a test that compares two responses.
func fieldOf(t *testing.T, body, name string) string {
	t.Helper()

	_, rest, ok := strings.Cut(body, `"`+name+`":"`)
	require.True(t, ok, "%s: no %q", body, name)
	v, _, ok := strings.Cut(rest, `"`)
	require.True(t, ok)

	return v
}
