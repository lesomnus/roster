package core

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// coreDelegation is the layer over the generated `DelegationService`.
//
// Like `coreCredential`, the service is served now for its overlay while its
// generated reads and raw writes stay closed by method -- so what reaches the
// wire is `Revoke` and nothing that answers with a token. `Revoke` is the
// delete a sign-out has to be able to make; it was `Vouch.Revoke`, a verb on
// delegation rows that the sign-in flow had no reason to keep. See
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
