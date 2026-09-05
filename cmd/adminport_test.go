package cmd_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
)

// The admin port is the data plane with no wall, reached with a session
// cookie. Everything the data plane's own listener is told to do, this one is
// told to do as well or the deployment has said one thing and got two -- on the
// port with the most authority of the three.
//
// These are the differences from `Server.Grpc` that were not decisions.

// adminDeployment is `roster init` with the deployment's own configuration
// in hand.
//
// `inited` writes the configuration itself, which is right for the tests that
// are about what init leaves behind. These are about what a deployment turns
// **on** -- a corpus of leaked passwords, a rate -- and whether this port obeys
// it, so the configuration has to be sayable.
func adminDeployment(t *testing.T, with func(c *cmd.Config)) (*cmd.Server, cmd.Config, string) {
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
	if with != nil {
		with(&c)
	}

	out := &strings.Builder{}
	k := cmd.NewCmdInit(&c)
	k.Writer = out

	x.NoError(k.Run(ctx, nil), "init: %s", out)

	// A second server on the same databases, since the command closed its own.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	return s, c, out.String()
}

// leakedCorpus is a file of leaked hashes in the shape the well-known one is
// published in: SHA-1, uppercase hex, sorted, one per line.
func leakedCorpus(t *testing.T, secrets ...string) string {
	t.Helper()

	lines := make([]string, 0, len(secrets))
	for _, v := range secrets {
		sum := sha1.Sum([]byte(v))
		lines = append(lines, strings.ToUpper(hex.EncodeToString(sum[:]))+":12")
	}

	// Sorted, because the shape is not decoration: `vouch.Sorted` binary
	// searches the file, so a corpus in argument order finds the first hash and
	// misses whichever ones happen to sort before it. A helper that produced
	// one would make a test about a refusal pass by refusing nothing.
	slices.Sort(lines)

	at := filepath.Join(t.TempDir(), "leaked.txt")
	require.NoError(t, os.WriteFile(at, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	return at
}

// adminPort is the admin port on a listener that is a channel, dialed, and a
// context carrying the operator's cookie -- which is the only thing that
// reaches it.
func adminPort(t *testing.T, s *cmd.Server, c cmd.Config, out string) (*grpc.ClientConn, context.Context) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	k := signIn(t, s, "admin", passwordFrom(t, out))
	x.NotNil(k, "the operator init printed cannot sign in")

	g, err := s.GrpcAdmin(ctx, c)
	x.NoError(err)
	x.NotNil(g, "a deployment with a control plane has an admin port")

	conn := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", k.Name+"="+k.Value))

	return conn, as
}

// adminCustomer is a tenant with somebody in it, made the way an operator
// makes one.
func adminCustomer(t *testing.T, conn *grpc.ClientConn, as context.Context, alias string) []byte {
	t.Helper()
	x := require.New(t)

	tn, err := app.NewTenantServiceClient(conn).Add(as,
		app.TenantAddRequest_builder{Alias: alias}.Build())
	x.NoError(err)

	h, err := app.NewHolderServiceClient(conn).Add(as, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "someone",
	}.Build())
	x.NoError(err)

	return h.GetId()
}

// TestTheCorpusIsTheDeploymentsAndNotThePorts is item 5 where it is actually
// reached from.
//
// A deployment that names `vouch.breached` has said it will not hold a password
// somebody has already lost. That is a fact about the secret rather than about
// the port it arrived on, and the operator's port is where passwords are set
// **for** people -- a reset by somebody who called support is exactly the write
// this refuses, and it is the one write that does not happen on the data plane.
func TestTheCorpusIsTheDeploymentsAndNotThePorts(t *testing.T) {
	x := require.New(t)

	at := leakedCorpus(t, "hunter2hunter2")
	s, c, out := adminDeployment(t, func(c *cmd.Config) { c.Vouch.Breached = at })

	conn, as := adminPort(t, s, c, out)
	who := adminCustomer(t, conn, as, "newco")

	v := app.NewVouchServiceClient(conn)
	set := func(secret string) error {
		_, err := app.NewCredentialServiceClient(conn).Set(as, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who}.Build(),
			Secret: []byte(secret),
		}.Build())

		return err
	}

	// `FailedPrecondition` rather than `InvalidArgument`, as everywhere else:
	// there is nothing wrong with the request, the world changed under the
	// value in it.
	x.Equal(codes.FailedPrecondition, status.Code(set("hunter2hunter2")),
		"the admin port stored a password this deployment refuses on the data plane")

	// And the check is a check and not a refusal of everything.
	x.NoError(set("correct horse battery staple"))

	// The generated one goes through the same door, which is what makes a
	// reset safe to hand somebody over the phone.
	t.Run("and a reset is checked by the same corpus", func(t *testing.T) {
		x := require.New(t)

		_, err := v.Reset(as, app.VouchResetRequest_builder{
			Who: app.VouchWho_builder{Id: who}.Build(),
		}.Build())
		x.NoError(err, "thirty-two random bytes were in a corpus of one")
	})
}

// TestEveryOperatorWriteLeavesBothTrails is the compensating control the port
// says it runs on, asked about the writes it exists for.
//
// `GrpcAdmin` waives `vouch.WithReach` on the grounds that what bounds an
// operator instead is the port: no wall, behind a console session, and *every
// write recorded twice and joined by the trace*. Credential writes are the
// reason this port has a `VouchService` at all -- and they were the writes the
// second trail did not cover, because the intent record was written by method
// name and the only names it knew were the four an entity is generated with.
//
// So a password reset left one row, in the data plane, naming an actor that
// resolves in neither database; and with no `otel:` configured that row carried
// no trace either, so there was nothing to join it to. That is the audit coming
// apart on exactly the write it was built for.
func TestEveryOperatorWriteLeavesBothTrails(t *testing.T) {
	ctx := t.Context()

	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)
	who := adminCustomer(t, conn, as, "newco")

	// Every write this port serves that is not one of the four generated
	// verbs: the hand-written service, and the overlay a holder carries.
	for _, w := range []struct {
		name string
		call func(context.Context) error
	}{
		{"/roster.CredentialService/Set", func(as context.Context) error {
			_, err := app.NewCredentialServiceClient(conn).Set(as, app.CredentialSetRequest_builder{
				Ref:    app.HolderRef_builder{Id: who}.Build(),
				Secret: []byte("correct horse battery staple"),
			}.Build())

			return err
		}},
		{"/roster.VouchService/Reset", func(as context.Context) error {
			_, err := app.NewVouchServiceClient(conn).Reset(as, app.VouchResetRequest_builder{
				Who: app.VouchWho_builder{Id: who}.Build(),
			}.Build())

			return err
		}},
		{"/roster.HolderService/Disable", func(as context.Context) error {
			_, err := app.NewHolderServiceClient(conn).Disable(as, app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: who}.Build(),
			}.Build())

			return err
		}},
	} {
		t.Run(w.name, func(t *testing.T) {
			x := require.New(t)

			was := adminTrail(t, s.Ent)

			x.NoError(w.call(as))

			// What changed, in the plane the customer is in.
			data := adminTrailSince(t, s.Ent, was)
			x.NotEmpty(data, "the data plane recorded nothing about the write")
			for _, v := range data {
				x.NotEmpty(v.TraceId,
					"%s: no trace on %s, so nothing to join it to", w.name, v.Action)
			}

			// Who decided, in the plane the operator is in -- asked for by
			// the action rather than taken as the last row of an unordered
			// read. `All` promises no order, so `cs[len(cs)-1]` was insertion
			// order holding on a fresh table and nothing more; the same is
			// true of `data[0]`, and both are joined on a trace that has to be
			// the right pair to mean anything.
			cs, err := s.Control.Ent.Audit.Query().
				Where(entaudit.ActionEQ(w.name)).
				All(ctx)
			x.NoError(err)
			x.Len(cs, 1, "%s: the decision was not recorded", w.name)

			intent := cs[0]
			x.Equal(w.name, intent.Action)

			traces := map[string]bool{}
			for _, v := range data {
				traces[string(v.TraceId)] = true
			}
			x.True(traces[string(intent.TraceId)],
				"the two trails carry different traces and cannot be joined")

			who, err := s.Control.Ent.Holder.Get(ctx, intent.ActorId)
			x.NoError(err, "the operator does not resolve in the plane that recorded them")
			x.Equal("admin", who.Alias)
		})
	}

	// And a read is still not a decision. The trail that answers *who decided*
	// is the whole reason the control plane writes a row at all, and a row per
	// `List` would be a differently-shaped second audit nobody asked for.
	t.Run("and a read records nothing", func(t *testing.T) {
		x := require.New(t)

		before, err := s.Control.Ent.Audit.Query().Count(ctx)
		x.NoError(err)

		_, err = app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
		x.NoError(err)

		after, err := s.Control.Ent.Audit.Query().Count(ctx)
		x.NoError(err)
		x.Equal(before, after, "a read was recorded as a decision")
	})
}

// adminTrail is what the plane's trail holds now, by identifier.
func adminTrail(t *testing.T, c *ent.Client) map[string]bool {
	t.Helper()

	vs, err := c.Audit.Query().All(t.Context())
	require.NoError(t, err)

	was := map[string]bool{}
	for _, v := range vs {
		was[v.Id.String()] = true
	}

	return was
}

// adminTrailSince is what it holds that it did not hold then.
func adminTrailSince(t *testing.T, c *ent.Client, was map[string]bool) []*ent.Audit {
	t.Helper()

	vs, err := c.Audit.Query().All(t.Context())
	require.NoError(t, err)

	news := []*ent.Audit{}
	for _, v := range vs {
		if !was[v.Id.String()] {
			news = append(news, v)
		}
	}

	return news
}

// TestTheAdminPortLimitsWhatItWasToldTo is the last of the differences from
// `Server.Grpc`, and the only one nobody had noticed.
//
// `admin:` is a whole `config.ServerConfig`, and every other knob of it is read
// -- the deadline, the TLS, what is closed. The rate was not, so a deployment
// that wrote one on this port was answered by a server that had never been told
// about it. A limit nobody enforces is worse than no limit, because somebody
// went and configured it.
func TestTheAdminPortLimitsWhatItWasToldTo(t *testing.T) {
	x := require.New(t)

	s, c, out := adminDeployment(t, func(c *cmd.Config) {
		c.Admin.Limit = config.LimitConfig{Rate: 1, Burst: 1}
	})

	conn, as := adminPort(t, s, c, out)

	_, err := app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
	x.NoError(err, "the first call in a second was refused")

	_, err = app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
	x.Equal(codes.ResourceExhausted, status.Code(err),
		"the rate this deployment configured for its admin port is not enforced")

	// And a stream counts, which is what `grpcx.Limit` is two interceptors for.
	// Written as `LimitUnary` alone -- here and on the data plane, the same
	// omission twice -- `Watch` was the way past a rate on either port: one
	// call to open, and nothing counted however long it ran or however many
	// were opened.
	t.Run("and so does a stream", func(t *testing.T) {
		x := require.New(t)

		s, c, out := adminDeployment(t, func(c *cmd.Config) {
			c.Admin.Limit = config.LimitConfig{Rate: 1, Burst: 1}
		})
		conn, as := adminPort(t, s, c, out)

		// Somebody to watch, written in process rather than over the port.
		// `init` leaves no customers -- making one is an operator's act -- and
		// making one here would spend the budget this test is measuring.
		tn, err := s.Ungated.Tenant().Add(t.Context(),
			app.TenantAddRequest_builder{Alias: "newco"}.Build())
		x.NoError(err)

		h, err := s.Ungated.Holder().Add(t.Context(), app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Alias:  "someone",
		}.Build())
		x.NoError(err)

		_, err = app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
		x.NoError(err, "the first call in a second was refused")

		// Filtered, because a watch has to say which rows it is about and an
		// unfiltered one is refused for that instead -- which would be this
		// test passing on the wrong refusal.
		req := app.HolderWatchRequest_builder{
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{
					Ref: app.HolderRef_builder{Id: h.GetId()}.Build(),
				}.Build(),
			},
		}.Build()

		// The refusal arrives on the first receive rather than at the call: a
		// client stream is created without a round trip, so the interceptor has
		// not run when `Watch` answers.
		w, err := app.NewHolderServiceClient(conn).Watch(as, req)
		x.NoError(err, "opening a stream is not the call")

		_, err = w.Recv()
		x.Equal(codes.ResourceExhausted, status.Code(err),
			"a stream was not counted, so Watch is the way past any rate")
	})
}
