package cmd

import (
	"context"
	"crypto/rand"
	"strings"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"uuid"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/vouch"
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
	//
	// `s.sink` rather than a second `pd.NewSink` of the same shape, and the
	// difference was not cosmetic: this re-typed the recorder list without the
	// `if watch.outbox` branch beside it, so writes made **here** were the only
	// ones in the deployment with nothing durable behind them. `pd.Sink` is a
	// value whose every method answers with a copy, so sharing one costs
	// nothing and makes the drift structurally impossible rather than
	// remembered.
	//
	// `core` reading the **control** plane. Its judgements are about the
	// caller -- what they hold, what they may pass on -- and the caller is
	// there.
	// `core.WithBreached`, because `Credential.Set` reads the corpus through
	// this layer now and the admin port is the one door such a password could
	// otherwise still come through -- the reasoning the vouch registration below
	// gives at length, arriving here because the write moved onto the entity.
	return app.Build(s.sink.WithWatch(s.Watch),
		core.Build(Rules(s.Control.Ent),
			core.WithBreached(core.Breached(s.Breached)),
			core.WithKeyring(s.Keyring),

			// The data plane's kind: the admin port mints `rt_` for a customer's
			// person, the same key that plane serves through `MeService`.
			core.WithPrefix(keys.PrefixTenant)),
		pd.AuditBuild())
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
				SetId(pdid.New(pd.AuditDomain).Uuid()).
				// Filed under the operator's own tenant, which is the one this
				// database has, so the wall lets them read their own trail.
				SetTenantId(f.Tenant.Uuid()).
				SetActorTenantId(f.Tenant.Uuid()).
				SetActorId(f.Actor.Uuid()).
				SetTraceId(traceOf(ctx)).
				SetAction(info.FullMethod).

				// The zero identifier, because there is no object yet -- that
				// is what "before the attempt" means. An `Add` has not chosen
				// one and an `Erase` may find nothing.
				//
				// So a row here is read by actor and by trace and never by
				// object, which is what it is for: this trail answers *who
				// decided*, and *what changed* is the other one, joined by the
				// trace.
				SetObjectId(uuid.Nil()).

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

// writes reports whether a method changes anything, and it says so by naming
// the **reads**, because the other direction was wrong here.
//
// By the name at all, because nothing else says so: payday's recorder is
// invoked by the sink rather than named per method, and there is no descriptor
// option for it. What this used to name was the four verbs generation emits --
// Add, Patch, Apply, Erase -- with a note that a hand-written service writing
// under another name would be missed. It was missed, on this port, and the
// note is where it was written down.
//
// Every credential write is such a name. `Set`, `Reset`, `Unlock`, `Revoke`,
// `Link` and `Enrol` are `VouchService`'s, hand-written and registered by
// `GrpcAdmin` below; `Update`, `Disable`, `Enable` and `Invalidate` are the
// overlay a holder carries. So an operator resetting a password left **one**
// row rather than two -- and since the trace is made past this line, in a
// deployment that configured no `otel:` that one row carried no trace either
// and there was nothing to join it to. The audit came apart on exactly the
// writes this port exists to make, which are also the ones a reader of the
// trail would go looking for first.
//
// Naming the reads cannot fail in that direction. A read this does not know is
// recorded as a decision it was not, which costs a row somebody reads past; a
// write it does not know is a write nothing accounts for, and nothing says so.
// `register` in `serve.go` chose the same way round for the same reason: the
// arrangement that fails by doing too much fails where somebody can see it.
func writes(method string) bool {
	i := strings.LastIndex(method, "/")
	if i < 0 {
		// Not something gRPC dispatched, which cannot arrive at this end of an
		// interceptor. Recorded rather than skipped, for the reason above.
		return true
	}

	switch method[i+1:] {
	// The three generation emits, and `SignsIn` -- the one hand-written read
	// this port serves, which answers how somebody signs in without the
	// verifier. `Watch` is a stream and never reaches a unary interceptor; it
	// is named anyway, because a list of reads that is missing a read is the
	// half of this that is worth being careful about.
	case "Get", "List", "Watch", "SignsIn":
		return false
	}

	return true
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

	shut := s.closed(Config{Server: c.Admin})

	// One limiter, handed to both halves -- see the same line in
	// [Server.Grpc]. `Limiter()` builds a bucket, so calling it twice is two
	// limits with the same numbers on them and neither counting what the other
	// let through.
	rate := c.Admin.Limiter()

	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Admin.CallTimeout())).
		WithUnary(auth.InterceptorUnary(s.Sessions.Handler(), Resolver(s.Control.Ungated, nil), public)).
		WithStream(auth.InterceptorStream(s.Sessions.Handler(), Resolver(s.Control.Ungated, nil), public)).

		// `admin:` is a whole `config.ServerConfig`, and this was the one knob
		// of it nothing here read: the deadline is taken from it on the line
		// above, the certificate and what is closed below, and the rate was
		// not. So a deployment that wrote one on this port was answered by a
		// server that had never been told about it -- which is worse than no
		// limit, because it is the limit somebody believes they have.
		//
		// `gate.ByTenant`, as on the data plane, and here that is one bucket
		// for the whole console: every caller on this port is in the operator's
		// own tenant, by construction, since a session names a control plane
		// holder. That is what a rate means here -- the deployment slowing
		// itself down on the listener that has no wall -- and it is the reading
		// that makes a limit written on `admin:` do something rather than
		// nothing.
		//
		// Both halves, which is what `grpcx.Limit` is: a stream is one call
		// counted when it opens, and leaving it out meant `Watch` was the way
		// past a rate on either port. Written as `LimitUnary` alone here and on
		// the data plane, where it was the same omission -- one line each,
		// because there is one answer.
		WithUnary(grpcx.LimitUnary(rate, gate.ByTenant())).
		WithStream(grpcx.LimitStream(rate, gate.ByTenant())).
		With(gate.Interceptor(Policy(s.Control.Ent))).
		WithUnary(Intent(s.Control.Ent)).
		With(s.Watch.Interceptor()).
		WithUnary(grpcx.ClosedUnary(shut)).
		WithStream(grpcx.ClosedStream(shut))

	vs, err := c.Admin.GrpcOptions()
	if err != nil {
		return nil, err
	}

	os := append(opts, chain.ServerOptions()...)
	os = append(os, vs...)

	g := grpc.NewServer(os...)
	register(g, admin)

	// And the credential writes, which is what an operator with no mail has
	// instead of one: reset a password, open an account ten wrong answers
	// closed. Roadmap.md's item 10, and D28 is the shape.
	//
	// # Without `WithReach`, and that is the decision rather than an omission
	//
	// D28 refuses somebody writing the credential of a person who holds more
	// than they do. It is a rule about escalation **inside a tenant**, and it
	// reads what the caller holds through their bindings -- which a session on
	// this port does not have any of, because it names a control plane holder
	// and the bindings are in the other database.
	//
	// So the rule would refuse every reset of anybody with a role, which is not
	// conservatism but an accident of which database the actor is in. This port
	// already says the same thing about permissions generally: *an operator has
	// no standing in a customer's tenant. They administer the deployment; a
	// customer's people are the customer's.* Administering is what this is.
	//
	// What bounds it instead is the port: no wall, bound where only a console
	// reaches, behind a control-plane session, and every write recorded twice
	// and joined by the trace.
	//
	// # And the same is true of the rule about a way in, less visibly
	//
	// D41 added a second rule of that family: nobody writes a way to sign in --
	// an account at a provider, a mailbox, a key that acts as them -- onto
	// somebody who holds more than they do. `IdentityService` and
	// `EmailService` are both registered on this port, and they do carry it,
	// because the layer is built here like any other.
	//
	// It refuses nothing, for the same reason `WithReach` was left out: the
	// rules are built from `s.Control.Ent`, so asking what a **customer's**
	// person holds is asking the wrong database, which answers nothing -- and
	// nothing is what the rule reads as *there is nothing to escalate to*.
	//
	// Which is the same waiver and is worth naming separately, because this one
	// is silent: a reader of `escalate.go` sees the rule applied to
	// `Identity.Add` and has no way to know it is inert here. Granting
	// `EmailService/Add` on this port is granting the account, exactly as
	// granting `Vouch.Reset` is, and it does not look like it.
	//
	// # The corpus reaches this port through `core`, not the vouch server
	//
	// `WithReach` is about the caller and does not survive the crossing between
	// the two databases, which is the paragraph above. A corpus of leaked
	// passwords is about none of that: it answers *has this secret already been
	// lost*, before anybody is read, so it is a fact about the value and not
	// about who is writing it or where they reached the server. A deployment
	// that named one has said it will not hold that password, full stop -- and
	// this port is the one door it could still come through, since setting a
	// password for somebody who has just phoned support is the whole reason
	// `VouchService` is registered here at all.
	//
	// It applies here because `Admin` builds its `core` stack with
	// `core.WithBreached`, and the writes served on this port run through it:
	// `Reset` hands its write to `Credential.Set`, where the check lives now. So
	// the vouch server below carries only the keyring, and the corpus is
	// `core`'s on both planes -- one wire rather than three.
	app.RegisterVouchServiceServer(g, vouch.New(admin, admin,
		vouch.WithKeys(s.Keyring)))

	// Minting a key for one of a customer's people is `ApiKey.Issue` now, and
	// `register` above already served it on this port -- the same `rt_`, on the
	// same rows, on the data-plane stack this port answers through, which
	// `Admin` built with `core.WithPrefix(keys.PrefixTenant)`.

	return g, nil
}
