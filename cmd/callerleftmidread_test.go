package cmd_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// heldSQLite is SQLite, as roster opens it, with one read that can be held:
// the next statement whose text holds [readHold.match] waits, as SQLite's
// driver is asked to prepare it, until the call it is read for has ended, and
// is then handed to the driver with that call's context. So the caller hangs
// up while its read is with the driver, every time, and SQLite's driver says
// what it said on a deployment: `sqlite3: interrupted`, which is not the
// context's error.
//
// Held at the driver and not inside SQLite's statement, and that is the one
// place that says it for certain. Held inside a step, the statement comes
// back with its row once the call has ended, and `database/sql`, which closes
// the rows of an ended context as soon as the step lets go of them, answers
// the context's own error -- the case that was already `Canceled`.
const heldSQLite = "sqlite3-held"

// readHold is a statement waiting to be held, and how a test hears it was.
type readHold struct {
	match   string
	reading chan struct{}
	said    chan error // what the driver said of the read it was handed
}

var (
	readHolding    atomic.Pointer[readHold]
	readHoldDriver sync.Once
)

type heldDriver struct{ *sqlitedriver.SQLite }

func (d heldDriver) OpenConnector(name string) (driver.Connector, error) {
	c, err := d.SQLite.OpenConnector(name)
	if err != nil {
		return nil, err
	}

	return heldConnector{c}, nil
}

type heldConnector struct{ driver.Connector }

func (c heldConnector) Driver() driver.Driver { return heldDriver{&sqlitedriver.SQLite{}} }

func (c heldConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	v, ok := conn.(sqliteConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("%T is not the connection this holds", conn)
	}

	return heldConn{v}, nil
}

// sqliteConn is what SQLite's driver's connection is to `database/sql`, all
// of which [heldConn] hands on.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.NamedValueChecker
	Raw() *sqlite3.Conn
}

type heldConn struct{ sqliteConn }

func (c heldConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	h := readHolding.Load()
	if h == nil || !strings.Contains(query, h.match) || !readHolding.CompareAndSwap(h, nil) {
		return c.sqliteConn.PrepareContext(ctx, query)
	}

	close(h.reading)
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}

	v, err := c.sqliteConn.PrepareContext(ctx, query)
	h.said <- err

	return v, err
}

// TestACallerWhoLeftMidReadIsCanceled: `HolderService/Get` whose caller hangs
// up while SQLite is reading the row is answered `Canceled`, and the call log's
// record of it is the one line it leaves, a warning.
//
// # What it was
//
// `Internal`, twice at ERROR for one call: SQLite said `interrupted`, which is
// not the context's error, and the step that answers a handler's error that
// carries no status asked the error rather than the context, then wrote a
// record of its own beside the call log's. On PostgreSQL the driver wraps the
// context's error and the same call was `Canceled`. Answered by the context
// now (lesomnus/payday#322), whatever the database.
//
// SQLite only, since the hold is SQLite's: on PostgreSQL it is skipped.
func TestACallerWhoLeftMidReadIsCanceled(t *testing.T) {
	if os.Getenv(pdtest.Postgres) != "" {
		t.Skipf("the read is held inside SQLite, and %s is set", pdtest.Postgres)
	}
	readHoldDriver.Do(func() {
		sql.Register(heldSQLite, heldDriver{&sqlitedriver.SQLite{}})
		config.RegisterDriver(heldSQLite, config.DialectSQLite)
	})

	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.Db.Driver = heldSQLite })
	b.mayAnything(b.ContosoUser, b.Contoso)

	said := &heard{}
	l := slog.New(slog.NewJSONHandler(said, nil))
	conn := served(t, b.Server, grpcx.Seed(func(ctx context.Context) context.Context { return log.Into(ctx, l) }))

	// The handler's read, which asks for the column this call alone selects;
	// the reads that resolve who is calling go by.
	h := &readHold{match: "SELECT `holder`.`id`, `holder`.`desc` FROM `holder`", reading: make(chan struct{}), said: make(chan error, 1)}
	readHolding.Store(h)
	t.Cleanup(func() { readHolding.Store(nil) })

	call, cancel := context.WithCancel(asOverTheWire(ctx, b.ContosoUser))
	go func() {
		<-h.reading
		cancel()
	}()

	_, err := app.NewHolderServiceClient(conn).Get(call, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Select: app.HolderSelect_builder{Desc: z.Ptr(true)}.Build(),
	}.Build())
	x.Equal(codes.Canceled, status.Code(err), "%v", err)
	select {
	case <-h.reading:
	default:
		x.FailNow("the read was never held, so this did not cancel it mid-read")
	}

	// What the store said is SQLite's, and not the context's error: the case a
	// test of the context's own error does not reach.
	read := <-h.said
	x.Error(read)
	x.NotErrorIs(read, context.Canceled)
	x.Contains(read.Error(), "interrupted")

	// The server's answer is on its log after the caller has gone.
	x.Eventually(func() bool { return len(said.lines(t, `"msg":"gRPC out"`)) > 0 }, 10*time.Second, 10*time.Millisecond)

	out := said.lines(t, `"msg":"gRPC out"`)
	x.Len(out, 1, "%s", said.b.String())
	x.Equal("Canceled", out[0]["rpc.grpc.status"])
	x.Equal("WARN", out[0]["level"])
	x.Empty(said.lines(t, `"level":"ERROR"`), "a caller who left is no fault of roster's: %s", said.b.String())
}
