package cmd_test

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/config/leasepg"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// replica is one of two processes of a deployment, as `roster serve` runs
// one: the loops under the deployment's lease and the server, together, with
// a log of its own that the test reads.
type replica struct {
	data, admin string
	said        *heard
	stop        func()
}

// replicaServing starts a process of the deployment `c` describes, as
// `cli/serve.go` does: the lease built from the file, the loops run under it,
// and the server answering on listeners of its own.
func replicaServing(t *testing.T, c cmd.Config) *replica {
	t.Helper()
	x := require.New(t)

	said := &heard{}
	ctx := log.Into(t.Context(), slog.New(slog.NewJSONHandler(said, &slog.HandlerOptions{Level: slog.LevelDebug})))

	dataL, data := listening(t)
	adminL, admin := listening(t)
	c.Server.ServerConfig = config.ServerConfig{Addr: data}
	c.Admin.Addr = admin

	lease, err := c.Spin.Build(ctx, c.Db)
	x.NoError(err)
	s, err := cmd.Build(ctx, c)
	x.NoError(err)

	sctx, cancel := context.WithCancel(cmd.Held(ctx, adminL))
	g, gctx := errgroup.WithContext(sctx)
	g.Go(func() error { return spin.Run(gctx, slices.Values(s.Spin), spin.WithLease(lease)) })
	g.Go(func() error { return s.Serve(gctx, c, dataL) })

	stopped := false
	r := &replica{data: data, admin: admin, said: said}
	r.stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		require.NoError(t, g.Wait())
		require.NoError(t, s.Close())
	}
	t.Cleanup(r.stop)

	return r
}

// leased is which loops this process has waited for under the lease, and
// how many times it has said it took each.
func (r *replica) leased(t *testing.T) (asked []string, up map[string]int) {
	t.Helper()

	up = map[string]int{}
	for _, l := range r.said.lines(t, `"msg":"spin`) {
		name, _ := l["spin"].(string)
		switch {
		case l["msg"] == "spin waits for its lease" && !slices.Contains(asked, name):
			asked = append(asked, name)
		case l["msg"] == "spin up" && l["spin.lease"] == true:
			up[name]++
		}
	}

	return asked, up
}

// dial is a connection to one of a replica's listeners.
func (r *replica) dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return conn
}

// TestTwoProcessesOnOneDatabase: a deployment of two replicas is two
// processes and one database, with `watch.broker: postgres` and `spin.lease:
// postgres`, and nothing of roster's held it. The lease ran under no test of
// roster's (a made-up one stands in for it), the broker under one test of an
// entity's `Watch`, and the sync stream was never followed across two
// servers; what a deployment of two replicas relies on was seen to hold by
// hand, and nothing would have said so had it stopped.
//
// So, two servers on one database, as `serve` runs each: a follower of the
// sync stream at one hears a suspension written at the other; an operator's
// session made at one admin listener is answered at the other; and every loop
// under the lease is held by one of the two, and by the other once the first
// is closed.
//
// Nothing here waits on a clock before it asserts. The stream says when it
// listens (its header, #459), and the broker has listened before the server
// is built. The lease is asked again every few seconds by a process that does
// not hold it, so the handover is waited for, with a bound.
func TestTwoProcessesOnOneDatabase(t *testing.T) {
	x := require.New(t)
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skipf("%s is not set; two processes share a broker and a lease through the database that carries them", pdtest.Postgres)
	}
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: brokerpg.Name},
		Spin:    config.SpinConfig{Lease: leasepg.Name},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	c.Client.As = "admin" // who `init` made, named at the box (#284)
	password := passwordFrom(t, out)

	// A customer, its administrator's key, and somebody to suspend: made at
	// the box, as a terminal makes them.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)
	who, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "suspended",
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	token := stdoutOf(t, cli.NewCmdKey(&c), "add",
		"--tenant", "newco", "--holder", "admin", "--name", "replicas",
		"--allow", "/roster.*/*")
	keyed := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))

	one := replicaServing(t, c)
	two := replicaServing(t, c)

	t.Run("a follower at one hears a suspension written at the other", func(t *testing.T) {
		x := require.New(t)

		// The header is the stream saying it listens; a write after it is
		// heard, wherever it was made.
		c := syncing(t, keyed, one.dial(t, one.data), pdid.Nil)

		ref := app.HolderRef_builder{Id: who.GetId()}.Build()
		_, err := app.NewHolderServiceClient(two.dial(t, two.data)).Disable(keyed, app.HolderDisableRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		v := about(t, c, mustId(t, who.GetId()))
		// The facts, and not the movement: this stream had not heard of them
		// before, and its first event about somebody says what is so.
		x.NotNil(v.GetDateDisabled(), "a suspension written at the other replica was not heard here")
	})

	t.Run("a session made at one admin listener is answered at the other", func(t *testing.T) {
		x := require.New(t)

		var h metadata.MD
		_, err := app.NewAuthServiceClient(one.dial(t, one.admin)).SignIn(ctx,
			app.AuthSignInRequest_builder{Alias: "admin", Secret: []byte(password)}.Build(),
			grpc.Header(&h))
		x.NoError(err, "the operator init printed could not sign in")
		cookie := ""
		for _, v := range (&http.Response{Header: http.Header{"Set-Cookie": h.Get("set-cookie")}}).Cookies() {
			if v.Value != "" {
				cookie = v.Name + "=" + v.Value
			}
		}
		x.NotEmpty(cookie, "signing in set no cookie")

		as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", cookie))
		vs, err := app.NewTenantServiceClient(two.dial(t, two.admin)).List(as, app.TenantListRequest_builder{}.Build())
		x.NoError(err, "a session made at the other replica was not answered here")
		x.NotEmpty(vs.GetItems())
	})

	t.Run("every loop under the lease is held by one, and by the other once it closes", func(t *testing.T) {
		x := require.New(t)

		// Each loop held by exactly one of the two. A loop the other has not
		// taken is one it is waiting for, which is the lease working.
		var asked []string
		x.Eventually(func() bool {
			asked, _ = one.leased(t)
			if len(asked) == 0 {
				return false
			}
			_, ups := one.leased(t)
			_, upt := two.leased(t)
			for _, n := range asked {
				if ups[n]+upt[n] == 0 {
					return false
				}
			}

			return true
		}, 30*time.Second, 50*time.Millisecond, "a loop under the lease was held by neither")

		_, ups := one.leased(t)
		_, upt := two.leased(t)
		for _, n := range asked {
			x.Equal(1, ups[n]+upt[n], "%s: held by both, or taken twice", n)
		}

		// The one that holds a loop is closed, and the other takes it.
		first, other, held := one, two, ups
		if len(upt) > len(ups) {
			first, other, held = two, one, upt
		}
		x.NotEmpty(held)
		t.Logf("loops under the lease: %v; the replica closed held %v", asked, held)
		first.stop()

		x.Eventually(func() bool {
			_, up := other.leased(t)
			for n := range held {
				if up[n] == 0 {
					return false
				}
			}

			return true
		}, 30*time.Second, 50*time.Millisecond, "a loop the closed replica held was taken by nobody: %v", held)
	})
}
