package cmd_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// graceSeen is one thing a listener's stop said: that it began, with the
// deadline it was given, or that it had stopped.
type graceSeen struct {
	listener string
	until    time.Time
	stopped  bool
}

// graceStops is what every listener of a server says as it stops, in the
// order said.
type graceStops struct {
	mu   sync.Mutex
	said []graceSeen
}

func (v graceSeen) String() string {
	if v.stopped {
		return v.listener + " stopped"
	}

	return v.listener + " began, until " + v.until.Format(time.RFC3339Nano)
}

func graceStopsOf(s *cmd.Server) *graceStops {
	v := &graceStops{}
	cmd.SeeStops(s, func(listener string, until time.Time, stopped bool) {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.said = append(v.said, graceSeen{listener, until, stopped})
	})

	return v
}

func (v *graceStops) all() []graceSeen {
	v.mu.Lock()
	defer v.mu.Unlock()

	return append([]graceSeen(nil), v.said...)
}

// graceAddrOf is the address the listener `name` says it answers on, once it
// has said it.
func graceAddrOf(t *testing.T, said *heard, name string) string {
	t.Helper()

	var addr string
	require.Eventually(t, func() bool {
		for _, l := range said.lines(t, `"msg":"`+name+`"`) {
			if v, ok := l["addr"].(string); ok && l["msg"] == name {
				addr = v
				return true
			}
		}
		return false
	}, 30*time.Second, 10*time.Millisecond, "%s never said where it answers", name)

	return addr
}

// graceDeployment is a deployment with a control plane, an admin port and
// whatever `with` adds, initialised, with a key on the data plane that may do
// anything in a tenant of its own.
type graceDeployment struct {
	*cmd.Server
	c      cmd.Config
	said   *heard
	out    string
	token  string
	tenant pdid.Id
	who    pdid.Id
	l      net.Listener
}

func graceDeploymentOf(t *testing.T, with func(*cmd.Config)) *graceDeployment {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, c, out := adminDeployment(t, func(c *cmd.Config) {
		c.Admin = cmd.AdminConfig{ServerConfig: config.ServerConfig{Addr: "127.0.0.1:0"}}
		c.Control.Addr = "127.0.0.1:0"
		if with != nil {
			with(c)
		}
	})

	b := &built{Server: s}
	b.Contoso = b.tenant(t, ctx, "contoso")
	who := b.holder(t, ctx, b.Contoso, "watched")
	b.mayAnything(who, b.Contoso)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	_, err = s.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   "stopping",
		Secret:  sum,
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)

	return &graceDeployment{Server: s, c: c, said: &heard{}, out: out, token: token, tenant: b.Contoso, who: who, l: l}
}

// serve answers until the answer's `stop` is called, and the channel is what
// `Serve` returned.
func (d *graceDeployment) serve(t *testing.T) (func(), <-chan error) {
	ctx, stop := context.WithCancel(log.Into(context.Background(), slog.New(slog.NewJSONHandler(d.said, nil))))
	t.Cleanup(stop)

	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, d.c, d.l) }()

	return stop, served
}

// graceWatch opens a watch of `who` on `conn` and answers once the server has
// said its first word on it, with a channel that is closed when the stream
// ends.
func graceWatch(t *testing.T, ctx context.Context, conn *grpc.ClientConn, who []byte) <-chan struct{} {
	t.Helper()
	x := require.New(t)

	out, err := app.NewHolderServiceClient(conn).Watch(ctx, app.HolderWatchRequest_builder{
		Filters: []*app.HolderFilter{
			app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: who}.Build()}.Build(),
		},
	}.Build())
	x.NoError(err)

	_, err = out.Recv()
	x.NoError(err, "the watch never started, so this proves nothing about stopping")

	ended := make(chan struct{})
	go func() {
		defer close(ended)
		for {
			if _, err := out.Recv(); err != nil {
				return
			}
		}
	}()

	return ended
}

func graceDial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return conn
}

// TestStoppingIsOneGraceForEveryListener: the process is given
// [cmd.ShutdownGrace] from the signal for every listener it has, whatever a
// deployment opened. It was given more: the data plane's gRPC server was
// stopped and waited for, and only then did the control and admin listeners
// begin their stops, each with a grace of its own -- with a stream on the data
// plane and one on the admin listener the process was gone after two graces,
// which is when `docker stop` kills it, and the admin listener went on taking
// calls through the first of them.
//
// Read from the deadline each listener is given and not from a stopwatch:
// every listener begins its stop before any has finished one, and all of them
// are given the one deadline.
func TestStoppingIsOneGraceForEveryListener(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := graceDeploymentOf(t, nil)
	stops := graceStopsOf(d.Server)
	stop, served := d.serve(t)

	// A stream on the data plane, by key.
	data := graceWatch(t, bearing(ctx, d.token), graceDial(t, d.l.Addr().String()), d.who.Bytes())

	// And one on the admin listener, by the operator's cookie.
	k := signIn(t, d.Server, "admin", passwordFrom(t, d.out))
	x.NotNil(k, "the operator init printed cannot sign in")
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", k.Name+"="+k.Value))
	adminConn := graceDial(t, graceAddrOf(t, d.said, "admin"))
	admin := graceWatch(t, as, adminConn, adminCustomer(t, adminConn, as, "stopping"))

	stop()

	select {
	case <-served:
	case <-time.After(60 * time.Second):
		t.Fatal("the process never stopped")
	}
	for name, ended := range map[string]<-chan struct{}{"the data plane's": data, "the admin listener's": admin} {
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s stream outlived the process", name)
		}
	}

	said := stops.all()
	began := map[string]time.Time{}
	for i, v := range said {
		if v.stopped {
			for _, w := range said[i+1:] {
				x.True(w.stopped, "%s began its stop after %s had finished its own: %v", w.listener, v.listener, said)
			}
			break
		}
		began[v.listener] = v.until
	}
	for _, name := range []string{"grpc", "control", "admin"} {
		x.Contains(began, name, "%s was not stopped with the others: %v", name, said)
	}

	var until time.Time
	for name, v := range began {
		if until.IsZero() {
			until = v
		}
		x.True(until.Equal(v), "%s was given a deadline of its own: %v", name, said)
	}
}

// TestACallThroughAPageIsAnsweredWhileTheProcessStops: a call that arrives
// through a listener a page talks to is given the grace a gRPC call is given.
// It was cut at the signal: the HTTP servers were stopped with `Close`, and the
// gRPC server they call into was stopped under them, so a write in flight
// through `server.http` was answered at once with 500 and gRPC's words about a
// codec, and was not made, where the same write over gRPC was answered and
// made.
//
// The call is in a handler before the signal -- it asks to be told to go on
// before it sends its body, and a handler is what tells it -- and the write
// waits on a lock the test holds, which is let go once the listener has begun
// its stop: the write is in flight across the signal.
func TestACallThroughAPageIsAnsweredWhileTheProcessStops(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := graceDeploymentOf(t, func(c *cmd.Config) {
		c.Server.Http = config.HttpConfig{Addr: "127.0.0.1:0", AllowWeb: true}
	})
	b := &built{Server: d.Server}
	held := b.holder(t, ctx, d.tenant, "held")

	// The lock: a write transaction on SQLite, which every other write waits
	// behind for the busy timeout, and the row itself on PostgreSQL.
	tx, err := d.Db.BeginTx(ctx, nil)
	x.NoError(err)
	defer tx.Rollback()
	if d.Dialect == config.DialectPostgres {
		_, err = tx.ExecContext(ctx, "SELECT id FROM holder WHERE id = $1 FOR UPDATE", held.Uuid())
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE holder SET alias = alias WHERE alias = 'held'")
	}
	x.NoError(err)

	letGo := sync.OnceFunc(func() { _ = tx.Rollback() })
	cmd.SeeStops(d.Server, func(listener string, _ time.Time, stopped bool) {
		if listener == "http" && !stopped {
			go letGo()
		}
	})

	stop, served := d.serve(t)
	addr := graceAddrOf(t, d.said, "http")

	type answer struct {
		code int
		body string
		err  error
	}
	sent := make(chan struct{})
	answered := make(chan answer, 1)
	go func() {
		body := fmt.Sprintf(`{"ref":{"id":%q}}`, base64.StdEncoding.EncodeToString(held.Bytes()))
		req, _ := http.NewRequestWithContext(
			httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
				Got100Continue: func() { close(sent) },
			}),
			http.MethodPost, "http://"+addr+"/roster.HolderService/Disable", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Expect", "100-continue")
		req.Header.Set("Authorization", "Bearer "+d.token)

		r, err := (&http.Client{Transport: &http.Transport{ExpectContinueTimeout: time.Minute}}).Do(req)
		if err != nil {
			answered <- answer{err: err}
			return
		}
		defer r.Body.Close()
		v, _ := io.ReadAll(r.Body)
		answered <- answer{code: r.StatusCode, body: string(v)}
	}()

	<-sent
	stop()

	var v answer
	select {
	case v = <-answered:
	case <-time.After(60 * time.Second):
		t.Fatal("the call through the page was never answered")
	}
	<-served
	letGo()

	x.NoError(v.err, "the call was cut")
	x.Equal(http.StatusOK, v.code, "answered %d: %s", v.code, v.body)

	got, err := d.Ent.Holder.Get(ctx, held.Uuid())
	x.NoError(err)
	x.NotNil(got.DateDisabled, "the write was answered and not made")
}
