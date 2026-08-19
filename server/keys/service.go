package keys

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
)

// Service is `payday.TokenService` over this deployment's keys: what a product
// app asks when somebody presents it a token roster issued.
//
// # Why it is not [Store]
//
// They read the same rows and answer different questions, because they are
// asked by different processes about different populations of key.
//
// [Store] is roster authenticating **its own** callers, in process, against the
// control plane. The actor it answers with is the key, which is what makes
// roster's trail say which key asked. Those keys are the deployment operator's
// and nobody introspects them -- they are presented *to* roster, never received
// by anybody else.
//
// This is a product app asking about a token one of **its** users holds. Its
// rows are about people, its resolver looks up a holder, and a key identifier
// handed to it resolves to nobody -- or worse, to a holder it creates on the
// spot for a row that is not a person. So what crosses here is the holder the
// key hangs off, which is the same pair `VouchService.Verify` answers with and
// the same pair that app already knows how to resolve.
//
// # Which database
//
// Whichever server it is built on, and the separation is the two databases
// rather than a check written here. Built on the data plane, this answers about
// the data plane's keys and cannot see a control-plane key at all -- there is
// no query from the one instance to the other. That is the same property the
// control plane was split out for; see PLAN.md, D15.
//
// # It reads through no wall
//
// `s` has no wall on it, for the reason `cmd.Resolver` and `vouch.Verify` do:
// this is asked before anybody has been resolved to a person, so there is no
// frame to narrow by. It is also the only way to read the row at all --
// `ApiKeyService` is unregistered and closed precisely because its generated
// `Get` answers with the verifier.
func Service(s app.Server) pdpb.TokenServiceServer { return service{s: s} }

type service struct {
	pdpb.UnimplementedTokenServiceServer

	s app.Server
}

// Introspect answers who a token stands for.
//
// # Two tables, and the prefix says which
//
// A key and a delegation are both this plane's, so the switch is about the
// table rather than the database. It is written out rather than left to the
// hash: `Sum` covers the prefix, so a token looked up in the wrong table finds
// nothing anyway -- but that is a second line of defence, and letting it be the
// first one means the day somebody changes how a token is hashed is the day
// this quietly stops distinguishing anything.
//
// Anything that is not a delegation goes to the key table, which is what this
// did before there was a second table and is what the control listener needs:
// `Service` is built on whichever plane it is serving, so there the keys it
// answers about are `rk_`.
//
// # And the delegation is bound to whoever was given it
//
// This is where that is checked, and it is the only place it can be. D21 and
// D23 both require it -- one product app must not be able to use what another
// was issued -- and `auth.TokenStore.Lookup` is handed the token and nothing
// else, so the in-process path has no caller to compare against. This one runs
// behind roster's own authentication, so `frame.From` names the app asking.
//
// What it compares against is the **frame's actor**, which for a deployment key
// is the key row rather than the service holding it (`cmd.Resolver`). So
// rotating an app's key invalidates the delegations it issued. That is a
// deliberate reading of "the caller": a delegation lives for minutes, and a
// caller whose credential has been replaced is not obviously the same caller.
func (v service) Introspect(ctx context.Context, req *pdpb.TokenIntrospectRequest) (*pdpb.TokenIntrospectResponse, error) {
	token := req.GetToken()

	find := findKey
	if strings.HasPrefix(token, PrefixDelegation) {
		find = findDelegation
	}

	k, err := find(ctx, v.s, token)
	if err != nil {
		// Whatever the lookup refused with is already the shape the contract
		// asks for: `NotFound` for anything about the token, and everything
		// else -- a database that would not answer -- passed on as itself so
		// that the app in front tells its caller to come back rather than
		// telling them their token is bad.
		return nil, err
	}
	if len(k.Issuer) > 0 {
		f, ok := frame.From(ctx)
		if !ok {
			// Nothing said who is asking, so nothing can be bound. Refused the
			// way a token that was never here is refused.
			return nil, status.Error(codes.NotFound, "no such token")
		}
		if err := issued(k, f.Actor); err != nil {
			return nil, err
		}
	}

	h := k.Holder
	if h == nil || len(h.GetId()) == 0 {
		// A key with no holder is a row that should not exist: the edge is
		// required and immutable. If one is ever read, answering with a token
		// that names nobody is worse than answering with nothing, since
		// `auth.Bearer` would have to work out that an identity with no name
		// is not a caller.
		return nil, status.Error(codes.Internal, "this key names nobody")
	}

	res := pdpb.TokenIntrospectResponse_builder{
		// The holder, not the key. See the note on the package's two answers.
		Id: h.GetId(),

		// The tenant as roster knows it, which the app in front is expected to
		// disagree with if its own row says otherwise; see
		// `auth.Identity.TenantId`.
		TenantId: h.GetTenant().GetId(),

		Grant: grantOf(k.Methods),
	}
	if !k.Expires.IsZero() {
		res.Expires = timestamppb.New(k.Expires)
	}

	return res.Build(), nil
}

// grantOf is what the key was narrowed to, encoded the way payday reads one.
//
// Built through [frame.Grant] and `auth.Introspection`'s encoder rather than by
// filling the message out here, because the message has a rule -- every axis
// carries a flag beside its list, and an empty list means **nothing** -- and
// the encoder is where that rule is applied for everybody. Written out here it
// would be applied twice, and the copy that gets it wrong hands out a token
// that allows everything.
func grantOf(methods []string) *pdpb.Grant {
	// Only the method axis. A key does not narrow tenants or sets today: what
	// it may reach is decided by the app in front, meeting this against its own
	// policy, and an empty `methods` is Grant's zero for actions, which allows
	// nothing.
	id, err := auth.Introspection(auth.Identity{
		Grant: frame.Whole().To(methods...),
	})
	if err != nil {
		// Introspection only fails on an identifier it cannot parse, and no
		// identifier was given.
		return nil
	}

	return id.GetGrant()
}
