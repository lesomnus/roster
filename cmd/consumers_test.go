package cmd_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestServeOpensTheConsumersItIsToldTo is the whole of what `account:` and
// `ldap:` are for: one process, and the two front doors on it.
//
// What it asserts is that the listener is **the consumer's** and not roster's.
// A front door answering `no operator here serves this name` to a hostname
// nobody registered is the account app, in this process, having resolved its
// key and come up -- which is every part of the wiring except a `Host` row.
func TestServeOpensTheConsumersItIsToldTo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	// A customer, and the key the front door fronts them with. Minted the way
	// an operator mints one, because that is the only way there is.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	// The app is a holder in that tenant, like any other caller: `key add`
	// mints for somebody who is there and does not make them.
	front, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "front",
	}.Build())
	x.NoError(err)

	// And a role, because a key cannot hand out what its holder does not hold.
	// `scripts/e2e.sh` grants the front door the same way.
	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: front.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	token := stdoutOf(t, cli.NewCmdKey(&c), "add",
		"--tenant", "newco", "--holder", "front", "--name", "e2e",
		"--allow", "/roster.*/*")

	// The block's values are references and not tokens, which is the rule the
	// file states; the reference is resolved here the way it would be in a
	// deployment.
	t.Setenv("ROSTER_TEST_FRONT_KEY", token)

	grpcAddr := free(t)
	httpAddr := free(t)
	accountAddr := free(t)

	c.Server = config.ServerConfig{Addr: grpcAddr}
	c.Server.Http.Addr = httpAddr
	c.Account = cmd.AccountConfig{
		Addr:     accountAddr,
		Insecure: true,
		Keys:     map[string]string{"newco": "env:ROSTER_TEST_FRONT_KEY"},

		// `roster` and `connect` deliberately left out: in one process they
		// default to this deployment's own listeners, which is the convenience
		// the block exists for.
	}

	done := make(chan error, 1)
	go func() { done <- cli.NewCmdServe(&c).Run(ctx, nil) }()

	res := until(t, "http://"+accountAddr+"/providers", done)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)

	x.Equal(http.StatusNotFound, res.StatusCode,
		"the front door is up but answered something else: %s", b)
	x.Contains(string(b), "no operator here serves this name",
		"the port answered, but not as the account app")

	select {
	case err := <-done:
		x.NoError(err, "serve stopped on its own")
	default:
	}
}

// TestServeOpensNoConsumerItWasNotToldAbout is the other half of "empty is
// nowhere", and it is the half that would fail silently: a deployment that
// never wrote the block should have nothing extra listening.
func TestServeOpensNoConsumerItWasNotToldAbout(t *testing.T) {
	x := require.New(t)

	x.False(cmd.AccountConfig{}.Serves())
	x.False(cmd.LdapConfig{}.Serves())
	x.True(cmd.AccountConfig{Addr: ":8090"}.Serves())
	x.True(cmd.LdapConfig{Addr: ":389"}.Serves())

	// LDAPS alone is a directory too. It was `addr` only for a while, which is
	// a deployment that names `addr_tls` and gets nothing.
	x.True(cmd.LdapConfig{AddrTls: ":636"}.Serves())
}

// free is an address nothing is listening on, by listening on it and stopping.
//
// A port and not `:0`, because what this asks is whether a **named** address is
// answered, and `:0` is a name only the listener learns.
func free(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	return addr
}

// until is a GET that waits for the server to come up, and fails saying so.
//
// It watches `serve` as well as the port, because the interesting failure is
// the one where serve refused the configuration and exited: waiting the whole
// timeout and then saying "connection refused" hides the sentence that says
// why.
func until(t *testing.T, url string, done <-chan error) *http.Response {
	t.Helper()

	var last error
	for range 100 {
		select {
		case err := <-done:
			require.FailNow(t, fmt.Sprintf("serve stopped before %s answered: %v", url, err))
		default:
		}

		res, err := http.Get(url) //nolint:noctx // a test, and the deadline is the loop
		if err == nil {
			return res
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}

	require.FailNow(t, fmt.Sprintf("%s never answered: %v", url, last))

	return nil
}
