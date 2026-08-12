package cmd

import (
	"context"
	"crypto/rand"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/pd"
)

// The port an operator administers customers through.
//
// # Why it is a third listener
//
// The data plane's port is walled, and an operator has no tenant there: their
// row is in the control plane, so the wall narrows them to a tenant that does
// not exist in this database and they see nothing. That is not a rule anybody
// chose; it is what two databases means.
//
// And it cannot share the control plane's port either, because both would
// register `roster.HolderService` -- one over the control plane's rows and one
// over the data plane's, under one name.
//
// # The rule this port runs on
//
//	Who is calling and what they hold are **control plane** questions.
//	What they are operating on is the **data plane**.
//
// Every difference from `Server.Grpc` is that sentence. The resolver reads the
// control plane, so a session resolves; the gate's policy reads it, so `May`
// finds their bindings; `core`'s rules read it, so `mayGrant` knows what they
// may hand on. The sink is the data plane's, with no wall and no gate layer --
// which is the only way to reach a customer at all from outside every tenant.
//
// Found by running it: with `core` reading the data plane, an operator creates
// a tenant and a holder and is then refused the role, because `Granted` looks
// for their bindings in the wrong database.
//
// # What is not registered
//
// `CredentialService` and `ApiKeyService`, for the reason they are not
// registered anywhere: their generated `Get` answers with the verifier column.
// A port being private is not a reason to serve a password hash over it.

// Admin is the stack this port answers through.
func Admin(s *Server) (app.Server, error) {
	if s.Control == nil {
		return nil, nil
	}

	// The data plane, with no wall and no gate layer. `pd.Wall` narrows by a
	// tenant the caller belongs to, and an operator belongs to none here.
	sink, err := pd.NewSink(s.Ent,
		bare.WithMinter(pd.Minter()),
		bare.WithRecorder(bare.Recorders{pd.Recorder(), pd.WatchRecorder(s.Watch)}),
	)
	if err != nil {
		return nil, err
	}

	// `core` reading the **control** plane. Its judgements are about the
	// caller -- what they hold, what they may pass on -- and the caller is
	// there.
	return app.Build(sink.WithWatch(s.Watch), core.Build(Rules(s.Control.Ent)), pd.AuditBuild())
}

// Intent records that an operator decided to do something, in the control
// plane, **before** it is attempted in the data plane.
//
// # Why before, and why not one transaction
//
// Because they are two events and only one of them is about the operator. "This
// operator called this RPC" is true whether or not the write then succeeded --
// and a failed attempt is a thing an audit wants to keep, not one to roll back.
// A record written afterwards would be missing exactly the attempts worth
// looking at.
//
// It cannot be atomic in any case: two databases, deliberately, with no query
// from one to the other. Rather than pretend, the two rows are separate events
// and the trace is what joins them.
//
// # Which is why the trace is made here if there is none
//
// `Audit.trace_id` comes from an OpenTelemetry span, and a deployment that has
// not configured `otel:` produces none -- confirmed by running it, where every
// row came back with an empty trace.
//
// That is fine for observability, which is a thing you may turn off. It is not
// fine here: without a trace the data plane's row names an actor that resolves
// in neither database and there is nothing to join it to. So this makes one
// when there is none, and the correlation does not depend on a setting.
//
// # Writes only
//
// A read leaves no row in the data plane, so an intent record for one would
// have nothing to correlate with and would be a second, differently-shaped
// audit nobody asked for. What an operator read is a real question and this is
// not the mechanism for it.
func Intent(control *ent.Client) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if !writes(info.FullMethod) {
			return next(ctx, req)
		}

		ctx = traced(ctx)

		f, ok := frame.From(ctx)
		if ok {
			// A failure to record is a failure of the call. The whole point is
			// that the decision is on record before the attempt, so proceeding
			// without it would serve the request this exists to account for.
			// Through ent and not through a server, because every server
			// refuses this: `pd.Audit` answers `Unimplemented` to everybody,
			// including the deployment, on the grounds that a trail somebody
			// can edit is evidence of nothing. That is right, and it makes this
			// the same kind of write `pd.Recorder` does -- something the
			// deployment performs rather than something anybody asks for.
			if err := control.Audit.Create().
				// Minted here, because `pd.Minter` is a hook on the sink and
				// this write does not go through one. The domain is what makes
				// an identifier say what it names; see `pdid`.
				SetID(pdid.New(pd.AuditDomain).Uuid()).
				// Filed under the operator's own tenant, which is the one this
				// database has, so the wall lets them read their own trail.
				SetTenantID(f.Tenant.Uuid()).
				SetActorTenantID(f.Tenant.Uuid()).
				SetActorID(f.Actor.Uuid()).
				SetTraceID(traceOf(ctx)).
				SetAction(info.FullMethod).

				// The zero identifier, because there is no object yet -- that
				// is what "before the attempt" means. An `Add` has not chosen
				// one and an `Erase` may find nothing.
				//
				// So a row here is read by actor and by trace and never by
				// object, which is what it is for: this trail answers *who
				// decided*, and *what changed* is the other one, joined by the
				// trace.
				SetObjectID(uuid.Nil).

				// Empty, and required to be said. `patch` is the document a
				// write was compiled from and `value` is the row afterwards;
				// this record is neither, because it is made before there is
				// either one.
				//
				// An empty slice and not nil. Nil is SQL NULL, which Postgres
				// refuses on a non-null column and SQLite accepts -- so nil
				// here is green on a checkout and broken on a deployment.
				SetPatch([]byte{}).
				SetValue([]byte{}).
				Exec(ctx); err != nil {
				return nil, err
			}
		}

		return next(ctx, req)
	}
}

// writes reports whether a method changes anything.
//
// By the name, because nothing else says so: payday's recorder is invoked by
// the sink rather than named per method, and there is no descriptor option for
// it. The four are what generation emits for every entity; a hand-written
// service that writes under another name would be missed, which is why this
// list is here and not somewhere it reads as exhaustive.
func writes(method string) bool {
	i := strings.LastIndex(method, "/")
	if i < 0 {
		return false
	}

	switch method[i+1:] {
	case "Add", "Patch", "Apply", "Erase":
		return true
	}

	return false
}

// traced puts a trace on the context when nothing else has.
//
// Sixteen bytes from `crypto/rand`, which is what a trace identifier is. It is
// not a span -- nothing is exported and no tracer is involved -- it is the
// value both audit rows will carry, and making one here is what keeps the two
// trails joined in a deployment that never configured `otel:`.
func traced(ctx context.Context) context.Context {
	if trace.SpanContextFromContext(ctx).HasTraceID() {
		return ctx
	}

	var t trace.TraceID
	if _, err := rand.Read(t[:]); err != nil {
		return ctx
	}

	var s trace.SpanID
	if _, err := rand.Read(s[:]); err != nil {
		return ctx
	}

	return trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: t,
		SpanID:  s,
	}))
}

// traceOf is the identifier both rows carry, and empty when there is none.
func traceOf(ctx context.Context) []byte {
	v := trace.SpanContextFromContext(ctx)
	if !v.HasTraceID() {
		return nil
	}

	id := v.TraceID()

	return id[:]
}

// GrpcAdmin is the server that port answers with.
func (s *Server) GrpcAdmin(ctx context.Context, c Config, opts ...grpc.ServerOption) (*grpc.Server, error) {
	admin, err := Admin(s)
	if err != nil {
		return nil, err
	}
	if admin == nil {
		return nil, nil
	}

	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Admin.CallTimeout())).
		WithUnary(auth.InterceptorUnary(s.Sessions.Handler(), Resolver(s.Control.Ungated, nil), public)).
		WithStream(auth.InterceptorStream(s.Sessions.Handler(), Resolver(s.Control.Ungated, nil), public)).
		With(gate.Interceptor(Policy(s.Control.Ent))).
		WithUnary(Intent(s.Control.Ent)).
		With(s.Watch.Interceptor()).
		WithUnary(grpcx.ClosedUnary(s.closed(Config{Server: c.Admin})))

	os := append(opts, chain.ServerOptions()...)
	os = append(os, c.Admin.GrpcOptions()...)

	g := grpc.NewServer(os...)
	register(g, admin)

	return g, nil
}
