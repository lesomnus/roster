package standing_test

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/standing"
)

// app is the alias of the holder the app acts as in its directory.
const app = "relying"

// methods is the role the app is installed with: what this package asks, and
// the one read of its own the tests' [asking] makes before every call, as an
// app that looks its directory up does.
var methods = append(slices.Clone(standing.Methods), rstr.TenantService_Get_FullMethodName)

// external is a roster deployment of its own, served on a TCP listener the way
// an operator stands one up: `init`, two tenants with somebody in acme, and the
// app installed into acme -- with a deployment key, and with acme's own key
// for the app's holder there.
type external struct {
	c    cmd.Config
	s    *cmd.Server
	addr string

	// key is a deployment key acme nominated, and tenantKey acme's own key
	// for the app's holder. operator is this roster's own operator, who
	// suspends and erases people over the wire -- which is the only way the
	// stream hears of it: what publishes is the interceptor on the served
	// stack, and a write through the server in this process tells nobody.
	key, tenantKey, operator string
	acme, beta               pdid.Id
	kim                      pdid.Id
}

func newExternal(t *testing.T) *external {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}
	k := cli.NewCmdInit(&c)
	k.Writer = io.Discard
	x.NoError(k.Run(ctx, nil))

	e := &external{c: c}
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	for alias, id := range map[string]*pdid.Id{"acme": &e.acme, "beta": &e.beta} {
		v, err := s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: alias, Name: strings.ToUpper(alias)}.Build())
		x.NoError(err)
		*id, err = pdid.From(v.GetId())
		x.NoError(err)
	}
	e.kim = addPerson(t, s, e.acme, "kim")
	x.NoError(s.Close())

	// What `docs/apps.md` says an app beside roster is given: a deployment
	// key that holds nothing as itself, installed into the tenant whose people
	// are its accounts -- acme, and not beta.
	e.key = stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--narrowed", app)
	x.NoError(cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme", "--role", strings.Join(methods, ","), app}))
	e.tenantKey = stdoutOf(t, cli.NewCmdKey(&c), "add", "--tenant", "acme", "--holder", app, "--name", app, "--allow", strings.Join(methods, ","))
	e.operator = stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--allow",
		"/roster.HolderService/Invalidate,/roster.HolderService/Disable,/roster.HolderService/Enable,/roster.HolderService/Erase,"+
			"/roster.TenantService/Get,/roster.TenantService/Update,/roster.TenantService/Close,/roster.TenantService/Restore", "operator")

	e.s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { e.s.Close() })
	g, err := e.s.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)
	e.addr = l.Addr().String()

	return e
}

// dial is a connection to the data plane, the way an app dials it.
func (e *external) dial(t *testing.T, opts ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(e.addr, append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, opts...)...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return conn
}

// at is [standing.Config.As] for a deployment key: the key, and `roster-at`
// naming the directory, which roster answers as the holder that tenant
// nominated for it.
func at(key, directory string) func(ctx context.Context) (context.Context, error) {
	return func(ctx context.Context) (context.Context, error) {
		return metadata.AppendToOutgoingContext(auth.BearerProvider(key).Provide(ctx), front.HeaderAt, front.AtTenant(directory)), nil
	}
}

// bearer is [standing.Config.As] for a tenant key, which names its tenant by
// being its, and for a deployment key asked as itself.
func bearer(key string) func(ctx context.Context) (context.Context, error) {
	return func(ctx context.Context) (context.Context, error) {
		return auth.BearerProvider(key).Provide(ctx), nil
	}
}

// asOperator is roster's operator over the wire.
func (e *external) asOperator(t *testing.T) (rstr.HolderServiceClient, context.Context) {
	t.Helper()
	return rstr.NewHolderServiceClient(e.dial(t)), auth.BearerProvider(e.operator).Provide(t.Context())
}

// role patches the role acme installed the app with to `methods`, the way an
// operator widens an older installation: the whole list, with `roster role
// patch`.
func (e *external) role(t *testing.T, methods []string) {
	t.Helper()
	x := require.New(t)
	var entity *xli.Command
	for _, k := range cli.NewCmdEntities(&e.c) {
		if k.Name == "role" {
			entity = k
		}
	}
	x.NotNil(entity)
	quoted := make([]string, len(methods))
	for i, m := range methods {
		quoted[i] = `"` + m + `"`
	}
	x.NoError(entity.Run(t.Context(), []string{"patch", "@acme/" + app + "-app",
		`{"methods": [` + strings.Join(quoted, ",") + `], "date_updated_force": true}`}))
}

// embedded is roster in the app's own process, as Loft runs it: its own
// database, no broker, served on an in-memory listener nothing outside can
// dial -- which is why it may believe what a caller says it is -- with the
// app's holder written into the directory by the deployment's own door.
type embedded struct {
	s   *cmd.Server
	lis *bufconn.Listener
	dir pdid.Id
	kim pdid.Id
}

const directory = "home"

func newEmbedded(t *testing.T) *embedded {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	c := cmd.Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}}
	c.Watch.Broker = config.BrokerNone
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))
	s.Auth = auth.PlainInProcess()
	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	e := &embedded{s: s, lis: bufconn.Listen(1 << 20)}
	go func() { _ = g.Serve(e.lis) }()
	t.Cleanup(g.Stop)

	d, err := s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: directory, Name: "Home"}.Build())
	x.NoError(err)
	e.dir, err = pdid.From(d.GetId())
	x.NoError(err)
	e.kim = addPerson(t, s, e.dir, "kim")

	tref := rstr.TenantRef_builder{Id: d.GetId()}.Build()
	h, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: tref, Alias: app, Name: "the app"}.Build())
	x.NoError(err)
	r, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: tref, Alias: app, Methods: methods}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	return e
}

func (e *embedded) dial(t *testing.T, opts ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///roster", append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return e.lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, opts...)...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return conn
}

// as is [standing.Config.As] on the roster in this process: payday's Plain,
// naming the app's holder in the directory.
func (e *embedded) as(ctx context.Context) (context.Context, error) {
	return auth.PlainProvider(front.AtTenant(directory) + "/" + app).Provide(ctx), nil
}

func addPerson(t *testing.T, s *cmd.Server, tenant pdid.Id, alias string) pdid.Id {
	t.Helper()
	v, err := s.Ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: tenant.Bytes()}.Build(), Alias: alias, Name: strings.ToUpper(alias[:1]) + alias[1:],
	}.Build())
	require.NoError(t, err)
	id, err := pdid.From(v.GetId())
	require.NoError(t, err)

	return id
}

// rosterUnder is one of the two places an app's accounts can be -- roster in
// its own process, or one of its own over TCP with the key it gave -- with an
// account in it, and roster's own door to that account: what its operator
// does, which the app is not told of until it asks again or the stream says.
type rosterUnder struct {
	name    string
	dial    func(t *testing.T, opts ...grpc.DialOption) *grpc.ClientConn
	as      func(ctx context.Context) (context.Context, error)
	account pdid.Id
	tenant  pdid.Id

	invalidate, suspend func() error
}

func bothRosters(t *testing.T) []rosterUnder {
	t.Helper()
	ctx := t.Context()

	em := newEmbedded(t)
	mine := func() *rstr.HolderRef { return rstr.HolderRef_builder{Id: em.kim.Bytes()}.Build() }

	e := newExternal(t)
	op, as := e.asOperator(t)
	theirs := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	return []rosterUnder{
		{
			name: "in this process", dial: em.dial, as: em.as, account: em.kim, tenant: em.dir,
			invalidate: func() error {
				_, err := em.s.Ungated.Holder().Invalidate(ctx, rstr.HolderInvalidateRequest_builder{Ref: mine()}.Build())
				return err
			},
			suspend: func() error {
				_, err := em.s.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: mine()}.Build())
				return err
			},
		},
		{
			name: "of its own", dial: e.dial, as: at(e.key, "acme"), account: e.kim, tenant: e.acme,
			invalidate: func() error {
				_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: theirs}.Build())
				return err
			},
			suspend: func() error {
				_, err := op.Disable(as, rstr.HolderDisableRequest_builder{Ref: theirs}.Build())
				return err
			},
		},
	}
}

// asking is `as` with a read of the directory before it, as an app that looks
// its directory up before each call does: something an account's call can be
// cut short at before it asks roster about the account.
func asking(conn grpc.ClientConnInterface, as func(ctx context.Context) (context.Context, error), tenant pdid.Id) func(ctx context.Context) (context.Context, error) {
	return func(ctx context.Context) (context.Context, error) {
		ctx, err := as(ctx)
		if err != nil {
			return nil, err
		}
		_, err = rstr.NewTenantServiceClient(conn).Get(ctx, rstr.TenantGetRequest_builder{
			Ref:    rstr.TenantRef_builder{Id: tenant.Bytes()}.Build(),
			Select: rstr.TenantSelect_builder{Alias: proto.Bool(true)}.Build(),
		}.Build())

		return ctx, err
	}
}

func accounts(t *testing.T, c standing.Config) *standing.Accounts {
	t.Helper()
	a, err := standing.New(c)
	require.NoError(t, err)

	return a
}

// stdoutOf runs a roster command and answers what it printed, which is how its
// keys are handed out.
func stdoutOf(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	x.NoError(w.Close())

	b, e := io.ReadAll(r)
	x.NoError(e)
	x.NoError(err, "%s: %s", strings.Join(args, " "), b)

	return strings.TrimSpace(string(b))
}

// logs is a log a test reads while it is written.
type logs struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// clock is a time a test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
