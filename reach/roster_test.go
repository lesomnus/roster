package reach_test

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/reach"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// app is the alias of the holder the app acts as in its tenant, and its
// methods are what `docs/apps.md` says to install it with for this package:
// [reach.Methods], and the app's own package, which the tenant's
// administrators are given to hand on (`--administer`).
const app = "relying"

const (
	ownMethods = "/loft.*/*"
	get        = "/loft.AssetService/Get"
	list       = "/loft.AssetService/List"
	forOthers  = "/loft.Permission/BookForOthers"
	runWork    = "/loft.Permission/RunWork"
)

// external is a roster deployment of its own, served on a TCP listener the way
// an operator stands one up: `init`, two tenants with somebody in acme, and the
// app installed into acme with a deployment key.
type external struct {
	c    cmd.Config
	s    *cmd.Server
	addr string

	// key is a deployment key acme nominated, and admin acme's own
	// administrator's key -- who writes acme's roles and bindings over the
	// wire, which is the only way the stream hears of them.
	key, admin string
	acme, beta pdid.Id
	kim        pdid.Id
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

	e.key = stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--narrowed", app)
	x.NoError(cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme",
		"--role", strings.Join(reach.Methods, ","), "--administer", ownMethods, app}))
	e.admin = stdoutOf(t, cli.NewCmdKey(&c), "add", "--tenant", "acme", "--holder", "admin", "--name", "admin", "--allow", "/roster.*/*")

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

// as is [reach.Config.As] for the app's deployment key: the key, and
// `roster-at` naming acme, which roster answers as the holder acme nominated.
func (e *external) as(ctx context.Context) (context.Context, error) {
	return metadata.AppendToOutgoingContext(auth.BearerProvider(e.key).Provide(ctx), front.HeaderAt, front.AtTenant("acme")), nil
}

// door is acme's administrator over the wire.
func (e *external) door(t *testing.T) door {
	t.Helper()
	conn := e.dial(t)
	roles, bindings := rstr.NewRoleServiceClient(conn), rstr.NewBindingServiceClient(conn)
	groups, members := rstr.NewGroupServiceClient(conn), rstr.NewGroupMembershipServiceClient(conn)
	people := rstr.NewHolderServiceClient(conn)

	return door{
		ctx:          auth.BearerProvider(e.admin).Provide(t.Context()),
		tenant:       e.acme,
		addRole:      wire(roles.Add),
		widen:        wire(roles.Widen),
		narrow:       wire(roles.Narrow),
		addBinding:   wire(bindings.Add),
		eraseBinding: wire(bindings.Erase),
		addGroup:     wire(groups.Add),
		addMember:    wire(members.Add),
		disable:      wire(people.Disable),
	}
}

// wire is a client's call as a server's, which is what [door] holds.
func wire[Req, Res any](f func(context.Context, Req, ...grpc.CallOption) (Res, error)) func(context.Context, Req) (Res, error) {
	return func(ctx context.Context, req Req) (Res, error) { return f(ctx, req) }
}

// embedded is roster in the app's own process, as Loft runs it for its tests:
// its own database, no broker, served on an in-memory listener nothing outside
// can dial, with the app's holder written into the tenant by the deployment's
// own door -- which is also what writes the tenant's roles here.
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
	r, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: tref, Alias: app, Methods: reach.Methods}.Build())
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

// as is [reach.Config.As] on the roster in this process: payday's Plain,
// naming the app's holder in its tenant.
func (e *embedded) as(ctx context.Context) (context.Context, error) {
	return auth.PlainProvider(front.AtTenant(directory) + "/" + app).Provide(ctx), nil
}

// door is the deployment's own: the server in this process, which publishes
// nothing -- an app with roster in its process has no stream, and calls
// `Recheck` after a change of its own.
func (e *embedded) door(t *testing.T) door {
	t.Helper()
	u := e.s.Ungated

	return door{
		ctx: t.Context(), tenant: e.dir,
		addRole:      u.Role().Add,
		widen:        u.Role().Widen,
		narrow:       u.Role().Narrow,
		addBinding:   u.Binding().Add,
		eraseBinding: u.Binding().Erase,
		addGroup:     u.Group().Add,
		addMember:    u.GroupMembership().Add,
		disable:      u.Holder().Disable,
	}
}

// door is whoever changes a tenant's roles and people, on either roster.
type door struct {
	ctx    context.Context
	tenant pdid.Id

	addRole      func(context.Context, *rstr.RoleAddRequest) (*rstr.Role, error)
	widen        func(context.Context, *rstr.RoleWidenRequest) (*rstr.Role, error)
	narrow       func(context.Context, *rstr.RoleNarrowRequest) (*rstr.Role, error)
	addBinding   func(context.Context, *rstr.BindingAddRequest) (*rstr.Binding, error)
	eraseBinding func(context.Context, *rstr.BindingRef) (*rstr.BindingEraseResponse, error)
	addGroup     func(context.Context, *rstr.GroupAddRequest) (*rstr.Group, error)
	addMember    func(context.Context, *rstr.GroupMembershipAddRequest) (*rstr.GroupMembership, error)
	disable      func(context.Context, *rstr.HolderDisableRequest) (*rstr.Holder, error)
}

// role is a role of the tenant's holding `methods`.
func (d door) role(t *testing.T, alias string, methods ...string) *rstr.RoleRef {
	t.Helper()
	v, err := d.addRole(d.ctx, rstr.RoleAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(), Alias: alias, Methods: methods,
	}.Build())
	require.NoError(t, err)

	return rstr.RoleRef_builder{Id: v.GetId()}.Build()
}

// bind binds a role to somebody, at a site or across the tenant, and answers
// the binding.
func (d door) bind(t *testing.T, role *rstr.RoleRef, who pdid.Id, site *rstr.SiteRef) *rstr.BindingRef {
	t.Helper()
	v, err := d.addBinding(d.ctx, rstr.BindingAddRequest_builder{
		Role: role, Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(), Site: site,
	}.Build())
	require.NoError(t, err)

	return rstr.BindingRef_builder{Id: v.GetId()}.Build()
}

// rosterUnder is one of the two places an app's people can be -- roster in its
// own process, or one of its own over TCP with the key it gave -- with
// somebody in it, and the door to its roles.
type rosterUnder struct {
	name    string
	dial    func(t *testing.T, opts ...grpc.DialOption) *grpc.ClientConn
	as      func(ctx context.Context) (context.Context, error)
	door    func(t *testing.T) door
	holder  pdid.Id
	tenant  pdid.Id
	ungated rstr.Server
}

func bothRosters(t *testing.T) []rosterUnder {
	t.Helper()
	em := newEmbedded(t)
	e := newExternal(t)

	return []rosterUnder{
		{name: "in this process", dial: em.dial, as: em.as, door: em.door, holder: em.kim, tenant: em.dir, ungated: em.s.Ungated},
		{name: "of its own", dial: e.dial, as: e.as, door: e.door, holder: e.kim, tenant: e.acme, ungated: e.s.Ungated},
	}
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

func holders(t *testing.T, c reach.Config) *reach.Holders {
	t.Helper()
	h, err := reach.New(c)
	require.NoError(t, err)

	return h
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

// asking is roster as the app asks it, over a connection of its own: one that
// counts every asking of `Reaches`, answers none while roster is `down`, and
// ends the call that asks when `cut` says to -- as a page left half loaded
// ends it.
type asking struct {
	asked atomic.Int32
	down  atomic.Bool
	cut   atomic.Pointer[context.CancelFunc]
}

func (a *asking) option() grpc.DialOption {
	return grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, res any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if method == rstr.HolderService_Reaches_FullMethodName {
			a.asked.Add(1)
			if v := a.cut.Swap(nil); v != nil {
				(*v)()
			}
		}
		if a.down.Load() {
			return status.Error(codes.Unavailable, "connection refused")
		}
		return invoke(ctx, method, req, res, cc, opts...)
	})
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
