package cmd_test

import (
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestTheUserConsoleAndItsDoorAreOneListener is #34, and it is one test because
// the whole claim is that they are one thing.
//
// The page a roster user opens is served by `server.http`; the `AuthService`
// they sign in at is registered on the same `g`; and the session that comes
// back is a `__Host-` cookie, which is host-only, so a page served anywhere
// else could not send it. That is the same sentence the admin console's #27
// made about `admin.http`, one plane over.
//
// What is different here, and what the last subtest is about: the caller is the
// tenant's **own** holder, so the wall narrows what the page can draw. The
// admin console's caller is a roster operator on a port that waives two rules,
// which is why this is not that page with a filter.
func TestTheUserConsoleAndItsDoorAreOneListener(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	x.NoError(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>roster</title>"), 0o644))

	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })

	// Somebody in contoso, with a password and a role. Their password is what
	// they type into the page; the role is what decides which screens it draws.
	b.mayAnything(b.ContosoUser, b.Contoso)
	res, err := b.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
		Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	secret := res.GetSecret()

	h, err := web.New(config.HttpConfig{AllowWeb: true}, b.grpc(t))
	x.NoError(err)
	cmd.ConsoleMount(cmd.ConsoleConfig{Dir: dir})(h)

	// `cmd.Arrived` and not a header written by hand: a browser that reaches
	// roster through no proxy says the name in `Host` alone, and Go keeps that
	// one out of `Request.Header` -- so without this the sign-in below has no
	// name to resolve a tenant from. It is the whole of what makes the address
	// bar the thing that decides which tenant.
	srv := httptest.NewServer(cmd.Arrived(h))
	t.Cleanup(srv.Close)

	// And the name this deployment is reached at is one contoso claims, which
	// is what a tenant's `Host` row is for. Without the port: a `Host` row is
	// stored as it is compared, and `front.Hostname` is what takes the port off
	// the name a request arrived with.
	at, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	x.NoError(err)
	_, err = b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   at,
	}.Build())
	x.NoError(err)

	jar, err := cookiejar.New(nil)
	x.NoError(err)
	c := &http.Client{Jar: jar}

	get := func(path string) (int, string) {
		t.Helper()

		res, err := c.Get(srv.URL + path)
		x.NoError(err)
		defer res.Body.Close()
		v, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(v)
	}
	post := func(path, body string) (int, string) {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(body))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")

		res, err := c.Do(req)
		x.NoError(err)
		defer res.Body.Close()
		v, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(v)
	}

	t.Run("the page is at the root, and a route it owns survives a reload", func(t *testing.T) {
		x := require.New(t)

		code, body := get("/")
		x.Equal(http.StatusOK, code)
		x.Contains(body, "<title>roster</title>")

		// The page routes in the browser, so a path it owns is the index rather
		// than a 404 -- which is what a reload on `/people/erin` does.
		code, body = get("/people/erin")
		x.Equal(http.StatusOK, code)
		x.Contains(body, "<title>roster</title>")
	})

	t.Run("and the sign-in is on the same origin, so the cookie reaches it", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.AuthService/SignIn",
			`{"alias":"someone","password":"`+secret+`"}`)
		x.Equal(http.StatusOK, code, body)

		cs := jar.Cookies(mustUrl(t, srv.URL))
		x.Len(cs, 1)
		x.NotEmpty(cs[0].Value)

		// A `__Host-` cookie carries no Domain and is sent to one host and no
		// other. A page served anywhere but here would never send this.
		x.True(strings.HasPrefix(cs[0].Name, "__Host-"), "the session is not host-only: %s", cs[0].Name)
	})

	t.Run("and what they are is read from that cookie", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"alias":"someone"`)
	})

	// The line #34 draws against the admin console: this page shows one tenant
	// because the caller is narrowed to one, not because the page chose to ask
	// for one. `fabrikam` exists and has people in it, and the same request on
	// `admin.addr` would answer with them.
	t.Run("and the wall is what narrows the page, not the page", func(t *testing.T) {
		x := require.New(t)

		b.holder(t, ctx, b.Fabrikam, "somebody-else")

		code, body := post("/roster.HolderService/List", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"alias":"someone"`)
		x.NotContains(body, "somebody-else")
	})
}

// TestAUserConsoleWithNoDoorIsRefused is the pair of settings that builds a
// deployment nobody wrote down.
//
// `sign_in.enabled` is what puts `AuthService` on this listener. Without it the
// page is served, loads and draws a form that is answered `Unimplemented` by a
// method that is not on the wire -- and there is nothing in that for anybody to
// read, because the page looks served and the deployment looks up.
//
// The same shape as the two refusals above it in `build`: two settings that
// cannot both be meant, and picking one silently is how each of them got
// written.
func TestAUserConsoleWithNoDoorIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := cmd.Build(t.Context(), cmd.Config{
		Db:          config.DbConfig{Driver: "sqlite3", Dsn: "file:refused?mode=memory&cache=shared"},
		Watch:       config.WatchConfig{Broker: config.BrokerMemory},
		UserConsole: cmd.ConsoleConfig{Dir: t.TempDir()},
	})
	x.Error(err)
	x.ErrorContains(err, "sign_in.enabled")
}
