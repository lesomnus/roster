package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// What bounds the access record, and what a deployment is told when nothing
// does (#280).
//
// The record takes a row for every call that shows somebody, and nothing
// destroys one unless `audit:` names a window for it. That default stays:
// roster destroys nothing nobody asked it to. What it lacked was a word about
// it. The trail's default is said beside the block that changes it, with its
// reason -- every write is a row -- and the files a deployment starts from
// spoke of the trail alone.

// heard is a log a test reads while a server writes it.
type heard struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (h *heard) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.b.Write(p)
}

// lines is every line said so far that holds `what`, as it was written.
func (h *heard) lines(t *testing.T, what string) []map[string]any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()

	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(h.b.String()), "\n") {
		if !strings.Contains(l, what) {
			continue
		}
		v := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(l), &v), l)
		out = append(out, v)
	}

	return out
}

// servedSaying is what `serve` says in its log from when it begins to answer
// until it has stopped, for a deployment configured as `with` says.
func servedSaying(t *testing.T, with func(*cmd.Config)) *heard {
	t.Helper()
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	c := cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}
	if with != nil {
		with(&c)
	}

	said := &heard{}
	ctx := log.Into(t.Context(), slog.New(slog.NewJSONHandler(said, nil)))

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)

	sctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Serve(sctx, c, l) }()

	// Answering, which is after whatever it says as it starts.
	x.Eventually(func() bool {
		conn, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
		if err != nil {
			return false
		}
		conn.Close()

		return true
	}, 10*time.Second, 10*time.Millisecond)

	stop()
	<-done

	return said
}

// TestServeSaysWhenTheAccessRecordHasNoWindow: a deployment whose access
// record nothing bounds is told so as `serve` starts -- once, as a warning,
// with the key that gives it a window -- and one that named a window, or said
// that for good is meant, is told nothing.
//
// # What it was
//
// Nothing was said. `roster.yaml` and `deploy/config.yaml` name no window and
// spoke only of the trail, so a deployment made from either kept a row for
// every read of anybody, for good, and learnt of it from the size of a table.
//
// It is said by `serve` and not as a server is built, which every command at
// the box does too.
func TestServeSaysWhenTheAccessRecordHasNoWindow(t *testing.T) {
	const what = "the access record has no window"

	t.Run("a deployment that names none", func(t *testing.T) {
		x := require.New(t)

		said := servedSaying(t, nil).lines(t, what)
		x.Len(said, 1, "said once, as serve starts")
		x.Equal("WARN", said[0]["level"])
		x.Contains(said[0]["msg"], "audit.by.access", "with the key that gives it one")
		x.Contains(said[0]["msg"], "retain")
		x.Contains(said[0]["msg"], "discard: true")
	})

	t.Run("one that names a window for it, and no archive", func(t *testing.T) {
		x := require.New(t)

		said := servedSaying(t, func(c *cmd.Config) {
			c.Audit.By = map[string]config.AuditKeepConfig{"access": {Retain: 90 * 24 * time.Hour, Discard: true}}
		})
		x.Empty(said.lines(t, what))
	})

	t.Run("and one whose every kind has a window", func(t *testing.T) {
		x := require.New(t)

		said := servedSaying(t, func(c *cmd.Config) {
			c.Audit = config.AuditConfig{Retain: 90 * 24 * time.Hour, Discard: true}
		})
		x.Empty(said.lines(t, what))
	})

	t.Run("and one that says for good is meant", func(t *testing.T) {
		x := require.New(t)

		said := servedSaying(t, func(c *cmd.Config) {
			c.Audit.By = map[string]config.AuditKeepConfig{"access": {Profile: "forever"}}
		})
		x.Empty(said.lines(t, what), "a deployment that said so is not told again at every start")
	})

	t.Run("and a command that builds a server and serves nothing says nothing", func(t *testing.T) {
		x := require.New(t)

		drv, dsn := pdtest.DB(t)
		said := &heard{}
		s, err := cmd.Build(log.Into(t.Context(), slog.New(slog.NewJSONHandler(said, nil))), cmd.Config{
			Db:    config.DbConfig{Driver: drv, Dsn: dsn},
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
		})
		x.NoError(err)
		x.NoError(s.Close())
		x.Empty(said.lines(t, what))
	})
}

// TestAWindowOfItsOwnBoundsTheAccessRecordWithNoArchive: `audit.by.access`
// with `retain` and `discard: true` is a deployment that starts, sweeps its
// access record, and keeps its trail for good -- with no archive named.
//
// It is the key `serve` names to a deployment that has no window
// (`TestServeSaysWhenTheAccessRecordHasNoWindow`), so it is held that the key
// does what the sentence says: a pass destroys the access record's rows older
// than the window and no row of the trail.
func TestAWindowOfItsOwnBoundsTheAccessRecordWithNoArchive(t *testing.T) {
	x := require.New(t)

	window := config.AuditConfig{By: map[string]config.AuditKeepConfig{
		"access": {Retain: time.Nanosecond, Discard: true},
	}}
	b, ctx := build(t, func(c *cmd.Config) { c.Audit = window })
	b.mayAnything(b.ContosoUser, b.Contoso)

	// The pass is one of the loops the deployment runs.
	named := []string{}
	for _, v := range b.Spin {
		if n, ok := v.(interface{ SpinName() string }); ok {
			named = append(named, n.SpinName())
		}
	}
	x.Contains(named, "roster.trail")

	_, err := app.NewHolderServiceClient(served(t, b.Server)).List(asOverTheWire(ctx, b.ContosoUser), app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	x.NoError(b.Access.Flush(ctx))
	x.NotZero(b.Ent.Access.Query().CountX(ctx))
	written := b.Ent.Audit.Query().CountX(ctx)
	x.NotZero(written)

	p, err := cmd.TrailPolicy(ctx, cmd.Config{Audit: window})
	x.NoError(err)
	p.Pass(ctx, pd.TrailStore(b.Ent))

	x.Zero(b.Ent.Access.Query().CountX(ctx), "the access record's rows are past their window, and are still there")
	x.Equal(written, b.Ent.Audit.Query().CountX(ctx), "the trail has no window, and a pass destroyed some of it")
}

// TestAFlushThatFailsPartWayWritesNothing: what the access record's writer
// writes in one go is written whole or not at all, so that rows it tries again
// are written once.
//
// # What it was
//
// A flush writes its rows five hundred a statement, each statement by itself,
// and a flush that fails puts every row back to be tried again -- under an
// identifier minted at each try. So with 501 rows waiting and the last of
// them refused, the first five hundred were written, and written again at
// every second the refusal lasted: two failures and a success made 1,501 rows
// of 501 reads. It takes more than five hundred reads waiting and a failure
// after the first statement, which is what the end of a database's outage
// looks like.
//
// The refusal here is a row whose identifier is taken, which every database
// refuses the same way.
func TestAFlushThatFailsPartWayWritesNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	row := func(id pdid.Id) access.Record {
		return access.Record{
			Id:     id,
			Tenant: b.Contoso, Actor: b.ContosoUser, ActorTenant: b.Contoso,
			Action: app.HolderService_List_FullMethodName, Kind: access.Read, Result: "ok", At: time.Now(),
			Subjects: []pdid.Id{b.ContosoUser}, Count: 1,
		}
	}
	count := func() int { return b.Ent.Access.Query().CountX(ctx) }

	// A row that is there already, and 501 waiting whose last has its
	// identifier.
	taken := pdid.New(pd.AccessDomain)
	x.NoError(b.Access.Write(ctx, row(taken)))

	const waiting = 501
	rs := make([]access.Record, 0, waiting)
	for range waiting - 1 {
		rs = append(rs, row(pdid.Nil))
	}
	b.Access.Queue(append(rs, row(taken))...)

	for range 2 {
		x.Error(b.Access.Flush(ctx))
		x.Equal(waiting, b.Access.Pending(), "what could not be written waits")
		x.Equal(1, count(), "a flush that failed left part of itself behind")
	}

	// The refusal lifted: the row whose identifier was taken is gone.
	x.NoError(b.Ent.Access.DeleteOneId(taken.Uuid()).Exec(ctx))
	x.NoError(b.Access.Flush(ctx))
	x.Zero(b.Access.Pending())
	x.Equal(waiting, count(), "each read that waited is one row")
}
