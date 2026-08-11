package keys

import (
	"context"

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
func (v service) Introspect(ctx context.Context, req *pdpb.TokenIntrospectRequest) (*pdpb.TokenIntrospectResponse, error) {
	k, err := lookup(ctx, v.s, req.GetToken())
	if err != nil {
		// Whatever `lookup` refused with is already the shape the contract
		// asks for: `NotFound` for anything about the token, and everything
		// else -- a database that would not answer -- passed on as itself so
		// that the app in front tells its caller to come back rather than
		// telling them their token is bad.
		return nil, err
	}

	h := k.GetHolder()
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

		Grant: grantOf(k),
	}
	if u := k.GetDateExpires(); u != nil {
		res.Expires = timestamppb.New(u.AsTime())
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
func grantOf(k *app.ApiKey) *pdpb.Grant {
	// Only the method axis. A key does not narrow tenants or sets today: what
	// it may reach is decided by the app in front, meeting this against its own
	// policy, and an empty `methods` is Grant's zero for actions, which allows
	// nothing.
	id, err := auth.Introspection(auth.Identity{
		Grant: frame.Whole().To(k.GetMethods()...),
	})
	if err != nil {
		// Introspection only fails on an identifier it cannot parse, and no
		// identifier was given.
		return nil
	}

	return id.GetGrant()
}
