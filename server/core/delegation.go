package core

import (
	"context"

	"github.com/lesomnus/payday/pdid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// coreDelegation is the layer over the generated `DelegationService`.
//
// The service is served: `Revoke`, the delete a sign-out makes with a token in
// hand, and -- since `ts/plan.md` § C -- `Get`, `List` and `Erase`, so a person
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
	if err := s.reaches(ctx, req.GetRef()); err != nil {
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
