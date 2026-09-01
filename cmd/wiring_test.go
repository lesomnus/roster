package cmd_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// How the process is assembled, and how it stops.
//
// Everything here is about wiring rather than about a rule: which database gets
// looked at before anything is served on it, which signals end the process, and
// what a shutdown waits for. None of it is reachable from a request, which is
// exactly why none of it had a test -- and why each of these failures is the
// quiet kind, found by an operator on a Friday rather than by a caller.

// TestBothPlanesAreChecked is the control plane's database, before anything is
// served on it.
//
// `serve` looked at one of the two. The data plane was migrated or checked and
// refused if it did not match; the control plane -- a second roster, on its own
// database, holding the keys every request is authenticated against -- was
// opened and served, whatever shape it was in. `ROSTER_CONTROL_DB_MIGRATE` is
// listed by `roster config env` and set by `compose.yaml`, and it was read by
// nothing at all.
//
// What that costs is a release that adds a control-plane table -- `session` was
// one -- landing on a deployment whose entrypoint skips `init` because the
// marker beside its databases says it is already seeded. The data plane
// migrates, the control plane does not, and the process starts and says
// nothing. The first sign is an operator who cannot sign in, reported as a
// missing table three layers from here.
func TestBothPlanesAreChecked(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	// The data plane migrates and the control plane does not, which is the
	// combination that hides the failure: a deployment that says `migrate` for
	// both is fine either way, and one that says it for neither is caught by
	// the check that already existed.
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	err = s.Ready(ctx, c)
	x.Error(err, "an empty control plane was served without a word")
	x.ErrorContains(err, "control", "the error does not say which of the two databases is wrong")
}

// TestTheDataPlaneIsCheckedToo is the same refusal the other way round, and it
// is the **default**: `db.migrate` unset is a deployment whose data plane is
// migrated by a step and not by `serve`, which is every deployment
// `docs/upgrading.md` describes. A new binary over an old data plane must
// refuse to start naming the right database, rather than serve wrong answers
// about the rows every caller resolves through.
func TestTheDataPlaneIsCheckedToo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true},
		},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	err = s.Ready(ctx, c)
	x.Error(err, "an empty data plane was served without a word")
	x.NotContains(err.Error(), "control",
		"the mismatch is the data plane's, and the error points at the other one")
}

// TestTheControlPlaneMigratesWhenItSaysSo is the other half: what
// `control.db.migrate` was always documented to do.
func TestTheControlPlaneMigratesWhenItSaysSo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn, Migrate: true},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: cdrv, Dsn: cdsn, Migrate: true},
		},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NoError(s.Ready(ctx, c))

	// The table the console signs in through, which is the one a deployment
	// upgraded past P9 would have been missing. Queried rather than listed,
	// because what matters is that the control plane's own client can read it.
	_, err = s.Control.Ent.Session.Query().Count(ctx)
	x.NoError(err, "the control plane was migrated and the session table is not there")

	// And having migrated, the same configuration with `migrate` off now
	// agrees -- which is the whole of what the check is for.
	c.Db.Migrate = false
	c.Control.Db.Migrate = false
	x.NoError(s.Ready(ctx, c), "a plane that was just migrated does not match itself")
}

// TestAControlPlaneThatIsNotOneIsRefused is a block written down that does
// nothing.
//
// `ControlConfig.Serves` reads `control.db.driver` and nothing else, which is
// the right field: a control plane is a database, and an address is only how it
// is reached. What it leaves is a block with every field but that one -- and
// that built a **working server with no control plane at all**. Every caller is
// `auth.Plain`, which is to say whoever they type; no `rk_` or `rt_` is read,
// because `auth.Bearer` is never in the chain; no `rd_` is honoured, because
// `keys.Acting` is not either; no console session is minted; and the port on
// the line above opens nothing.
//
// Nothing could have said so. payday warns once that `Plain` is being served,
// and a deployment that wrote no `control:` at all prints the same line -- so
// the one line that would have told somebody is the line that cannot tell the
// two apart.
func TestAControlPlaneThatIsNotOneIsRefused(t *testing.T) {
	drv, dsn := pdtest.DB(t)

	// A database that opens, so that what is refused is the control block and
	// not a side effect of a configuration that was broken anyway.
	base := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}

	// Whatever the block said, and not its address alone: every field under it
	// is as inert without a database as `control.addr` is, and the next one
	// added will be too.
	for _, tc := range []struct {
		desc string
		v    cmd.ControlConfig
	}{
		{"an address", cmd.ControlConfig{
			ServerConfig: config.ServerConfig{Addr: "127.0.0.1:0"},
		}},
		{"the console's port", cmd.ControlConfig{
			ServerConfig: config.ServerConfig{
				Http: config.HttpConfig{Addr: "127.0.0.1:0", AllowWeb: true},
			},
		}},
		{"a broker for a plane that is not there", cmd.ControlConfig{
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
		}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			c := base
			c.Control = tc.v

			_, err := cmd.Build(t.Context(), c)
			x.Error(err, "a control plane was written down and the deployment served Plain")
			x.ErrorContains(err, "control.db.driver",
				"the refusal does not name the field that is missing")
		})
	}

	t.Run("and a plane that is only a database still builds", func(t *testing.T) {
		x := require.New(t)

		// The arrangement this must not break: a control plane reachable by a
		// Go call and on no port at all. `Serves()` is true and `Answers()` is
		// false, which is what `control.addr` being empty means -- see
		// `ControlConfig`.
		cdrv, cdsn := pdtest.DB(t)

		c := base
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}

		s, err := cmd.Build(t.Context(), c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		x.NotNil(s.Control, "a control plane with no address was not built")
	})
}

// TestAnAdminPortWithNobodyToBeIsRefused is the same shape one step further
// out.
//
// The port authenticates a session cookie and resolves it against the **control
// plane's** holders, so `cmd.Admin` answers with nothing where there is no
// control plane and `serveAdmin` opens no listener. Written down and not
// served, which is worse than refused: it is a port an operator believes is
// there.
func TestAnAdminPortWithNobodyToBeIsRefused(t *testing.T) {
	x := require.New(t)

	drv, dsn := pdtest.DB(t)

	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Admin: config.ServerConfig{Addr: "127.0.0.1:0"},
	}

	_, err := cmd.Build(t.Context(), c)
	x.Error(err, "an admin port was written down and nothing opened it")
	x.ErrorContains(err, "admin")
	x.ErrorContains(err, "control.db.driver",
		"the refusal does not say which block has to be filled in")
}

// TestServeStopsOnSigterm is how this process is actually asked to stop.
//
// `docker stop` and every orchestrator send SIGTERM. Only `os.Interrupt` was
// registered, so the graceful path -- GracefulStop, the spin teardown, the
// deferred stops for the other listeners -- ran on an interactive Ctrl-C and
// nowhere else. Under Docker roster is PId 1, where SIGTERM has no default
// handler at all, so every `docker stop` waited out the grace period and ended
// in SIGKILL; anywhere else the default disposition kills the process on the
// spot, which is what this test observes.
//
// Either way a routine restart is a crash, and with `watch.outbox` off -- the
// default -- a committed-but-unpublished event is lost on every one of them.
//
// It is a subprocess because there is nothing else to test: the signal set is
// installed in `main`, which is the one function a test cannot call.
func TestServeStopsOnSigterm(t *testing.T) {
	x := require.New(t)

	if testing.Short() {
		t.Skip("builds the binary")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "roster")

	// The whole binary, because `main` is the subject. `..` is the module root
	// from this package's directory, and the build cache makes this the cost of
	// linking rather than of compiling.
	build := exec.Command("go", "build", "-o", bin, "./cmd/roster")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// A port of its own, taken and released, so two tests running at once do
	// not collide. `:0` on the server itself would be tidier and the address it
	// picked is only in a log line this deployment does not print.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	addr := l.Addr().String()
	x.NoError(l.Close())

	// A run directory of its own: the loader reads `roster.yaml` from the
	// working directory, and the repository has one.
	run := filepath.Join(dir, "run")
	x.NoError(os.Mkdir(run, 0o755))

	proc := exec.Command(bin, "serve")
	proc.Dir = run
	proc.Stdout = os.Stderr
	proc.Stderr = os.Stderr
	proc.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"ROSTER_DB_DRIVER=sqlite3",
		"ROSTER_DB_DSN=file:roster.db?_pragma=foreign_keys(1)",
		"ROSTER_DB_MIGRATE=true",
		"ROSTER_WATCH_BROKER=memory",
		"ROSTER_SERVER_ADDR=" + addr,
	}
	x.NoError(proc.Start())

	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()

	t.Cleanup(func() {
		_ = proc.Process.Kill()
	})

	// Answering is the only readiness this process reports, so it is what is
	// waited for: a SIGTERM that arrives before the handler is installed would
	// pass this test for the wrong reason.
	x.Eventually(func() bool {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return false
		}
		c.Close()

		return true
	}, 30*time.Second, 100*time.Millisecond, "the server never answered")

	x.NoError(proc.Process.Signal(syscall.SIGTERM))

	select {
	case err := <-done:
		// Exited because it decided to. A process killed by the signal answers
		// with an `*exec.ExitError` whose state is `Signaled`, which is the
		// failure this is about -- and on the way it skips every deferred stop
		// in `Serve`.
		x.NoError(err, "SIGTERM did not reach the graceful path")

	case <-time.After(30 * time.Second):
		t.Fatal("SIGTERM was ignored: the process is still running")
	}
}

// TestServeRefusesAControlPlaneItWasNotAllowedToMigrate is the wiring, rather
// than the method the wiring calls.
//
// [Server.Ready] can be right and unreached: what was broken was that `serve`
// looked at one plane, and a test that calls `Ready` itself would pass with the
// call deleted from `serve` -- which is the state this is about.
//
// So it runs the binary. The data plane is allowed to migrate and the control
// plane is not, on an empty database, which is the upgrade this was found in:
// an entrypoint that skips `init` because the marker beside its databases says
// it is seeded, past a release that adds a control-plane table. Before, the
// process started, said nothing, and the first report was an operator who could
// not sign in.
func TestServeRefusesAControlPlaneItWasNotAllowedToMigrate(t *testing.T) {
	x := require.New(t)

	if testing.Short() {
		t.Skip("builds the binary")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "roster")

	build := exec.Command("go", "build", "-o", bin, "./cmd/roster")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	run := filepath.Join(dir, "run")
	x.NoError(os.Mkdir(run, 0o755))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	addr := l.Addr().String()
	x.NoError(l.Close())

	proc := exec.Command(bin, "serve")
	proc.Dir = run

	out := &bytes.Buffer{}
	proc.Stdout = out
	proc.Stderr = out
	proc.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"ROSTER_DB_DRIVER=sqlite3",
		"ROSTER_DB_DSN=file:roster.db?_pragma=foreign_keys(1)",
		"ROSTER_DB_MIGRATE=true",
		"ROSTER_WATCH_BROKER=memory",
		"ROSTER_SERVER_ADDR=" + addr,

		// A control plane on its own empty database, told not to migrate.
		"ROSTER_CONTROL_DB_DRIVER=sqlite3",
		"ROSTER_CONTROL_DB_DSN=file:control.db?_pragma=foreign_keys(1)",
		"ROSTER_CONTROL_DB_MIGRATE=false",
		"ROSTER_CONTROL_ADDR=127.0.0.1:0",
	}

	t.Cleanup(func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
		}
	})

	x.NoError(proc.Start())

	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()

	select {
	case err := <-done:
		x.Error(err, "a control plane in the wrong shape was served anyway")
		x.Contains(out.String(), "control:",
			"the refusal did not say which of the two databases to go and look at")

	case <-time.After(30 * time.Second):
		// Which is what it did before: started, said nothing, and served. A
		// deadline rather than a `Run` that waits, because the failure this is
		// about is a process that does not stop.
		t.Fatal("the process is still serving a control plane it never looked at")
	}
}

// TestShutdownDoesNotWaitOnAStream is the deadline a graceful stop had none of.
//
// `GracefulStop` waits for every Rpc in flight, and a Watch is an Rpc that
// never ends on its own: the stream loops until the **client** hangs up, the
// deadline the chain installs is unary-only, and nothing ages a connection out
// unless a deployment configured keepalive. So one client holding a watch --
// which is exactly what the sync channel tells a product app to do -- meant
// `g.Serve` never returned, the deferred stops for the other listeners never
// ran, and further signals were swallowed by the handler that had already
// fired. The process had to be killed.
//
// What is wanted is not a hard stop: an ordinary unary call in flight should
// finish. What is wanted is that waiting for it ends.
func TestShutdownDoesNotWaitOnAStream(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.mayAnything(b.ContosoUser, b.Contoso)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)

	// Its own context, so that cancelling it is the shutdown and the client's
	// stream is not cancelled with it. A test that cancelled both would be
	// watching the client hang up, which is the case that always worked.
	sctx, stop := context.WithCancel(context.Background())
	defer stop()

	served := make(chan error, 1)
	go func() { served <- b.Serve(sctx, cmd.Config{}, l) }()

	conn, err := grpc.NewClient(l.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	defer conn.Close()

	// A watch that is open and staying open. The snapshot it opens with is what
	// says the handler is running on the server rather than still being dialed.
	//
	// Filtered because a watch has to say which rows it is about; which rows
	// they are decides nothing here.
	who := b.holder(t, ctx, b.Contoso, "watched-while-stopping")
	out, err := app.NewHolderServiceClient(conn).Watch(
		asOverTheWire(ctx, b.ContosoUser),
		app.HolderWatchRequest_builder{
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{
					Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
				}.Build(),
			},
		}.Build())
	x.NoError(err)

	_, err = out.Recv()
	x.NoError(err, "the watch never started, so this proves nothing about shutdown")

	at := time.Now()
	stop()

	select {
	case err := <-served:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			x.NoError(err)
		}

	case <-time.After(30 * time.Second):
		t.Fatal("shutdown waited on a stream that has no reason to end")
	}

	// And it is the grace that ended it rather than the stream ending on its
	// own, which is the difference between a bound and a coincidence. Twice
	// the grace, because what is being asserted is that there is one at all
	// and not how precisely a timer fires under a loaded test binary.
	took := time.Since(at)
	x.Less(took, 2*cmd.ShutdownGrace,
		"the stop was not bounded by anything this app chose")
	t.Logf("shutdown with an open stream took %s", took)
}

// TestTheControlPlaneKeepsItsOwnOutbox is the rest of `control.watch`.
//
// The block was documented to inherit one thing -- the **broker name** -- and
// the code inherited by replacing the whole block: a control plane that said
// `outbox: true` and left `broker` empty got a fresh `WatchConfig` carrying the
// data plane's broker and nothing else. So the setting was accepted by the
// loader, listed by `roster config env`, and silently dropped: no recorder, no
// drain, and a crash between the commit and the publish still loses the key
// change an operator paid a row per write to keep.
//
// The failure needs both halves of the configuration to be written, which is
// why nothing caught it: every test either named a broker for the control plane
// or said nothing about it at all.
func TestTheControlPlaneKeepsItsOwnOutbox(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: cdrv, Dsn: cdsn},

			// The broker is the one thing left out, which is what the field's
			// own comment says a deployment may do.
			Watch: config.WatchConfig{Outbox: true},
		},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	// Inherited, still: the half that always worked.
	x.NotNil(s.Control.Watch.Broker())

	// A write on the control plane -- its own tenant, which is what `init`
	// makes there.
	_, err = s.Control.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "deployment"}.Build())
	x.NoError(err)

	n, err := s.Control.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(n, "the control plane's outbox was configured and nothing recorded to it")

	// And something to publish and delete them, which is the other half of the
	// same setting: a recorder with no drain is a table that only grows.
	//
	// Compared against the same deployment without the setting, rather than
	// counted. A bare length was what was written, and it says the right thing
	// only until somebody adds a background loop -- then it fails with a
	// message about an outbox that has nothing to do with what changed, and it
	// would equally have passed with three sweeps and no drain. `pd.Drain`
	// answers an unexported type, so what can be asked is what the setting
	// added.
	bare := withoutOutbox(t)
	x.Equal(len(bare.Control.Spin)+1, len(s.Control.Spin),
		"the control plane's outbox has no drain behind it")

	// The data plane said nothing about an outbox and still has none -- the
	// inheritance goes one way.
	n, err = s.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "the control plane's outbox was inherited by the data plane")
}

// withoutOutbox is the same two-plane deployment with the setting off, which
// is what makes the count above about the setting rather than about how many
// loops this app happens to run.
func withoutOutbox(t *testing.T) *cmd.Server {
	t.Helper()
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	s, err := cmd.Build(t.Context(), cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db:    config.DbConfig{Driver: cdrv, Dsn: cdsn},
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
		},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	return s
}

// TestAnOutboxWithNowhereToPublishIsRefused.
//
// The recorder was installed on `watch.outbox` alone and the drain spun only
// where there was a broker to publish into, so `broker: none` with
// `outbox: true` -- two plain environment variables, accepted by the loader
// without a word -- wrote a queue row inside every transaction and had nothing
// anywhere that would ever delete one. `OutboxService` answers no Rpc, so not
// even an operator could drain it by hand. The table grows until the database
// is full, which is the failure `outbox.proto` names in as many words.
//
// Refused rather than logged: the two settings contradict each other, and the
// deployment that wrote them meant one of the two.
func TestAnOutboxWithNowhereToPublishIsRefused(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerNone, Outbox: true},
	})
	if s != nil {
		t.Cleanup(func() { s.Close() })
	}

	x.Error(err, "a queue nothing drains was built without a word")
	x.ErrorContains(err, "watch.outbox", "the error does not name the setting to change")
}

// TestTheDataPlaneCountsAStreamToo, which is the same omission as the admin
// port's and was found by fixing that one.
//
// A rate was `LimitUnary` alone on both chains, so `Watch` was the way past
// whatever a deployment configured: one call to open, nothing counted however
// long it ran, and nothing counted for the next one either.
//
// And the two halves share **one** limiter. `ServerConfig.Limiter()` builds a
// bucket, so handing each interceptor its own call would be a rate of n per
// second answering 2n, with nothing to see in the configuration.
func TestTheDataPlaneCountsAStreamToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.mayAnything(b.ContosoUser, b.Contoso)
	who := b.holder(t, ctx, b.Contoso, "watched-while-limited")

	g, err := b.Grpc(ctx, cmd.Config{
		Server: config.ServerConfig{Limit: config.LimitConfig{Rate: 1, Burst: 1}},
	})
	x.NoError(err)

	conn := pdtest.Serve(t, g)
	as := asOverTheWire(ctx, b.ContosoUser)

	// The one token this deployment allows per second.
	_, err = app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
	x.NoError(err)

	out, err := app.NewHolderServiceClient(conn).Watch(as, app.HolderWatchRequest_builder{
		Filters: []*app.HolderFilter{
			app.HolderFilter_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build(),
		},
	}.Build())
	x.NoError(err, "opening a stream is not the call")

	_, err = out.Recv()
	x.Equal(codes.ResourceExhausted, status.Code(err),
		"a stream was not counted, or it was counted against a bucket of its own")
}

// TestAFailureThatIsNotAboutBrokersDoesNotMentionThem.
//
// The note the control plane adds -- *control.watch.broker is empty, so it took
// watch.broker* -- is true of exactly one refusal and was said of every error
// the nested `Build` could answer with. A control database that would not
// answer was reported as a database that would not answer, plus a sentence
// about brokers, which sends whoever reads it to the wrong file.
//
// It is the shape of thing that reads as harmless and is not: an error message
// is the only thing between an operator and the setting they have to change,
// and one that names a second setting confidently is worse than one that names
// none.
func TestAFailureThatIsNotAboutBrokersDoesNotMentionThem(t *testing.T) {
	x := require.New(t)

	drv, dsn := pdtest.DB(t)

	// A control plane pointed at a database that is not there, and no broker
	// named for it -- which is the arrangement that used to attract the note.
	_, err := cmd.Build(t.Context(), cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db: config.DbConfig{Driver: "postgres", Dsn: "postgres://nobody@127.0.0.1:1/nothing?sslmode=disable"},
		},
	})
	x.Error(err)
	x.Contains(err.Error(), "control:", "the refusal did not say which plane")
	x.NotContains(err.Error(), "watch.broker",
		"a database that would not answer was reported as a broker question")
}

// TestADeploymentWithEveryPortOpenStops.
//
// [cmd.ShutdownGrace] is chosen against a number somebody else owns: `docker
// stop` waits ten seconds and then sends SIGKILL, so a longer grace is one
// nothing lives to use. That argument holds for **one** listener. A deployment
// with a control plane and an admin port opens five, each stopped by a `defer`
// of its own -- and defers run one after another, so five graces end to end is
// twenty-five seconds against a ten-second budget, with four of the five never
// having been asked before the process was killed.
//
// They have nothing to say to each other: one listener draining is not a reason
// for the next to still be accepting. So they run together and the budget is
// the grace, whatever a deployment opened.
//
// Measured with a stream held open on every port that serves one, because a
// stream is the thing that makes a stop take its whole grace -- a watch never
// ends on its own, which is what `TestShutdownDoesNotWaitOnAStream` is about
// one listener at a time.
func TestADeploymentWithEveryPortOpenStops(t *testing.T) {
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	ctx := t.Context()

	// Every port this app opens, on addresses the kernel picks.
	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{
			Db:           config.DbConfig{Driver: cdrv, Dsn: cdsn},
			ServerConfig: config.ServerConfig{Addr: "127.0.0.1:0"},
		},
		Admin: config.ServerConfig{Addr: "127.0.0.1:0"},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)

	sctx, stop := context.WithCancel(context.Background())
	defer stop()

	served := make(chan error, 1)
	go func() { served <- s.Serve(sctx, c, l) }()

	// A stream on the data plane, which is the one this test can reach without
	// a session: the other two take a cookie, and what is being measured is
	// whether the stops wait on each other rather than what each one waits for.
	conn, err := grpc.NewClient(l.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	defer conn.Close()

	b := &built{Server: s}
	b.Contoso = b.tenant(t, ctx, "contoso")
	who := b.holder(t, ctx, b.Contoso, "watched")
	b.mayAnything(who, b.Contoso)

	// A real credential, because naming a control plane is what switches this
	// deployment off `auth.Plain` -- and naming one is the whole point here,
	// since it is the deployment that opens all five listeners.
	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	_, err = s.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   "watcher",
		Secret:  sum,
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	out, err := app.NewHolderServiceClient(conn).Watch(bearing(ctx, token),
		app.HolderWatchRequest_builder{
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{
					Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
				}.Build(),
			},
		}.Build())
	x.NoError(err)

	_, err = out.Recv()
	x.NoError(err, "the watch never started, so this proves nothing about shutdown")

	at := time.Now()
	stop()

	select {
	case <-served:
	case <-time.After(60 * time.Second):
		t.Fatal("the process never stopped")
	}

	took := time.Since(at)
	t.Logf("five listeners stopped in %s", took)

	// Within one grace, with every listener a deployment opens open and a
	// stream held on the one this test can reach without a session cookie.
	//
	// It does **not** discriminate serial from together, and saying so is the
	// point of the note: only the listener with the stream waits its grace
	// here, so five in series would answer in one grace too. What that costs
	// is why `TestTheListenersStopTogether` is written against the loop
	// instead. What this pins is the wiring around it -- that every listener
	// is stopped at all, and that opening four more of them does not leave the
	// process running.
	x.Less(took, 2*cmd.ShutdownGrace,
		"a deployment with every port open did not stop within its own grace")
}
