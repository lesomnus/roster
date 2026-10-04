package core

import (
	"context"
	"crypto/subtle"

	"github.com/lesomnus/payday/pdid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// coreDelegation is the layer over the generated `DelegationService`.
//
// The service is served: `Revoke`, the delete a sign-out makes with a token in
// hand, and `Get`, `List` and `Erase` beside it, so a person
// sees where they are signed in and ends one by reference. The token in
// `secret` never travels: the sink strips it on the way out like every other
// secret, and this layer holds the rows to `mayReach`. `Add` stays closed,
// because it would take a verifier the caller chose. See
// `delegation_svc.ext.proto`.
type coreDelegation struct {
	Core
	app.DelegationServiceServer
}

func (s Core) Delegation() app.DelegationServiceServer {
	return coreDelegation{s, s.Next().Delegation()}
}

// Revoke ends a delegation the caller was issued, before its expiry.
//
// The caller is the frame's actor -- the issuer a delegation is tied to -- and
// `keys.Undelegate` finds the token, refuses to touch one this caller did not
// issue, and erases it. Everything answers the same: a token that was never
// here, expired, or somebody else's succeeds and removes nothing, so the answer
// tells whoever holds a found string nothing about it. What a caller may rely
// on is that a delegation it was issued is gone afterwards.
//
// Through `Next()`, the generated server behind this layer, which is where the
// row is and where `Erase`'s reach already narrows to what the caller can see.
func (s coreDelegation) Revoke(ctx context.Context, req *app.DelegationRevokeRequest) (*app.DelegationRevokeResponse, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return nil, status.Error(codes.Unauthenticated,
			"a delegation is minted for whoever asked, and nothing here said who that is")
	}

	if err := keys.Undelegate(ctx, s.Next(), req.GetToken(), f.Actor); err != nil {
		return nil, err
	}

	return &app.DelegationRevokeResponse{}, nil
}

// Get, List and Erase are served now, which the file comment above said they
// were not: what closed them was the token in the `secret` column, and the
// answer to that is the layer roster already has for every secret -- the sink
// strips `(payday.field).secret` on the way out -- rather than a verb of
// `MeService`'s that curates the same rows. So a person lists their own
// delegations to see where they are signed in, and erases one to end it; an
// operator does the same for somebody they reach. `Revoke` stays for the caller
// that holds a token and no reference.
//
// The reach rule is the one every write about somebody's ways in meets, applied
// here to a read as well: a delegation is a credential, and listing them is
// listing where somebody is signed in.
func (s coreDelegation) Get(ctx context.Context, req *app.DelegationGetRequest) (*app.Delegation, error) {
	if err := s.reachesOrWasIssued(ctx, req.GetRef()); err != nil {
		return nil, err
	}

	return s.DelegationServiceServer.Get(ctx, req)
}

func (s coreDelegation) List(ctx context.Context, req *app.DelegationListRequest) (*app.DelegationListResponse, error) {
	for _, f := range req.GetFilters() {
		if f.GetHolder() == nil {
			continue
		}
		holder, err := s.holderOf(ctx, f.GetHolder())
		if err != nil {
			return nil, err
		}
		if err := s.mayReach(ctx, "holder", holder); err != nil {
			return nil, err
		}
	}

	return s.DelegationServiceServer.List(ctx, req)
}

func (s coreDelegation) Erase(ctx context.Context, req *app.DelegationRef) (*app.DelegationEraseResponse, error) {
	if err := s.reaches(ctx, req); err != nil {
		if status.Code(err) == codes.NotFound {
			return app.DelegationEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	return s.DelegationServiceServer.Erase(ctx, req)
}

// reachesOrWasIssued is [coreDelegation.reaches], or the caller being the one
// the delegation was issued to.
//
// # A delegation issued to you is yours to read
//
// Whoever it is about. `TokenService/Introspect` reads a delegation with the
// caller's frame on the context, and an app is routinely narrower than the
// person or app a delegation it was handed is about: khala is told who kamino
// is by a token kamino exchanged for it (`Exchange`), and a product introspects
// the delegation of somebody who holds more than the product does. The reach
// rule would refuse both, and it is the wrong rule here -- it guards *seeing
// where somebody is signed in*, and a holder of the token already has the one
// row it names. The rule for presenting a delegation is the issuer binding
// (`keys.issued`), and this is the same rule for reading one: `Revoke` already
// rests on it.
func (s coreDelegation) reachesOrWasIssued(ctx context.Context, ref *app.DelegationRef) error {
	if f, ok := frame.From(ctx); ok && !f.Actor.IsZero() {
		v, err := s.DelegationServiceServer.Get(ctx, app.DelegationGetRequest_builder{
			Ref:    ref,
			Select: app.DelegationSelect_builder{Issuer: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(v.GetIssuer(), f.Actor.Bytes()) == 1 {
			return nil
		}
	}

	return s.reaches(ctx, ref)
}

// reaches is `mayReach` on the holder of the delegation a reference names.
func (s coreDelegation) reaches(ctx context.Context, ref *app.DelegationRef) error {
	v, err := s.DelegationServiceServer.Get(ctx, app.DelegationGetRequest_builder{
		Ref:    ref,
		Select: app.DelegationSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return err
	}

	return s.mayReach(ctx, "ref", holder)
}

// Exchange mints a delegation about the caller, issued to the audience: a token
// one app hands another so the second can ask roster who the first is.
// `delegation_svc.ext.proto` has the argument.
//
// # What it reuses, and what it turns round
//
// [keys.Delegate] mints and stores it, and `TokenService/Introspect` already
// answers a delegation only to the caller it was issued to. What is new is
// which way round the two fields go: an ordinary delegation is *about somebody
// else, for the caller*; this is *about the caller, for somebody else*. So the
// receiver introspects and is told the caller, and the caller -- who would be
// the obvious one to leak it -- is told nothing about it at all.
//
// # The rules
//
// The caller is a holder, because the token names a holder: a deployment key as
// itself is no one in any tenant. The audience is the caller's tenant's own,
// the agreement every row naming two holders is held to (`agree.go`), and
// somebody **else**.
//
// # And what it may carry
//
// This said once that no grant check was needed, because `methods` would name
// the receiver's RPCs and not roster's. Nothing made them: the row is an
// ordinary delegation, the receiver may present it beside its own key in
// `roster-as`, and roster answers that as the caller narrowed to `methods` --
// whatever they name. So a key attenuated to this one method could mint a
// token for itself allowing `/*.*/*`, present it beside the same key, and be
// answered with its holder's whole role: an attenuation undone by the
// credential it attenuates.
//
// So three refusals, each closing one leg of that:
//
//   - `methods` are held to what the caller's own credential allows, the rule
//     `Vouch.Delegate` holds its tokens to (`server/vouch`, `mayDelegate`):
//     nobody hands on what they may not call.
//   - The audience is not the caller. A token for yourself proves nothing to
//     anybody, and it was the whole of the trick above.
//   - A caller acting through a delegation (`roster-as`) is refused. They are
//     the person it names only within what that person handed over, and a
//     token minted about them from it would be the same delegation renewed --
//     a fresh expiry, out of reach of anything that revokes the first.
func (s coreDelegation) Exchange(ctx context.Context, req *app.DelegationExchangeRequest) (*app.DelegationExchangeResponse, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() || f.Tenant == pdid.Nil {
		return nil, status.Error(codes.FailedPrecondition,
			"a token naming the caller needs a caller in a tenant: a deployment key names one with roster-at")
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok && len(md.Get(keys.HeaderActing)) > 0 {
		return nil, status.Error(codes.PermissionDenied,
			"a token naming the caller is the caller's own; one acting through a delegation may not mint another from it")
	}
	if req.GetAudience() == nil {
		return nil, status.Error(codes.InvalidArgument, "audience: which app in this tenant the token is for")
	}

	to, err := s.holderOf(ctx, req.GetAudience())
	if err != nil {
		return nil, err
	}
	if to == f.Actor {
		return nil, pderr.Invalidf("audience", "a token for yourself proves who you are to nobody; name the app it is for")
	}
	whose, err := s.tenantOfHolder(ctx, app.HolderRef_builder{Id: to.Bytes()}.Build())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("audience", whose, f.Tenant); err != nil {
		return nil, err
	}

	for _, m := range req.GetMethods() {
		if !f.Grant.Allows(m) {
			return nil, status.Errorf(codes.PermissionDenied,
				"methods: you may not call %s, so you may not hand it on", m)
		}
	}

	token, v, err := keys.Delegate(ctx, s.Next(), keys.Delegated{
		Holder:  f.Actor,
		Issuer:  to.Bytes(),
		Methods: req.GetMethods(),
	})
	if err != nil {
		return nil, err
	}

	return app.DelegationExchangeResponse_builder{
		Token:       token,
		DateExpires: v.GetDateExpires(),
	}.Build(), nil
}
