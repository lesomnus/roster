package core

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/prove"
)

type coreHostProof struct {
	Core
	app.HostProofServiceServer
}

func (s Core) HostProof() app.HostProofServiceServer {
	return coreHostProof{s, s.Next().HostProof()}
}

// Add writes a claim, and the two things on it that are roster's.
//
// The token and the window are generated here rather than taken from the
// request, and the request is **refused** for carrying either rather than having
// them overwritten. That is `normalised`'s direction and for its reason: a
// caller whose value is silently replaced compares the two afterwards and
// disagrees with itself.
//
// # What a caller-chosen token would be
//
// A name proved by a record the caller did not write. Somebody who can guess
// what will be asked for can claim a name whose zone already happens to say it
// -- a stale `_roster-challenge` from an earlier claim, or one they read out of
// a public zone -- and the whole of what this measures is whether they could put
// it there.
//
// # And a caller-chosen window
//
// A claim that never expires, which is a token lying in DNS forever and a name
// provable by whoever finds the record. `prove.For` is the number, and there is
// no field for a caller to say otherwise -- the same place `Continuation` landed
// for the same argument.
func (s coreHostProof) Add(ctx context.Context, req *app.HostProofAddRequest) (*app.HostProof, error) {
	if err := normalised("name", req.GetName(), front.Hostname); err != nil {
		return nil, err
	}
	if req.GetToken() != "" {
		return nil, status.Error(codes.InvalidArgument,
			"token: roster generates what you are to publish, so this is not yours to write")
	}
	if req.GetDateExpires() != nil {
		return nil, status.Error(codes.InvalidArgument,
			"date_expires: how long a claim is good for is roster's, so this is not yours to write")
	}

	token, err := prove.Token()
	if err != nil {
		return nil, err
	}

	// A copy, because the request is the caller's: a layer that writes into what
	// it was handed is a layer whose effects outlive its own call.
	v := proto.Clone(req).(*app.HostProofAddRequest)
	v.SetToken(token)
	v.SetDateExpires(timestamppb.New(time.Now().Add(prove.For)))

	return s.HostProofServiceServer.Add(ctx, v)
}

// Patch is the normalisation and nothing else, which the name being immutable
// makes almost empty: what is left that a patch may write is `desc`.
//
// `token` and `date_expires` are immutable and nullable-with-a-flag
// respectively, so a patch could clear the window a claim is bounded by. It is
// closed at the transport with every other general write (`grpcx.GeneralWrite`,
// and roster sets no `AllowGeneralWrites`), which is what the equivalent gap in
// `coreEmail.Patch` rests on as well -- said here too, because a deployment that
// opened general writes would need this to refuse a cleared expiry.
func (s coreHostProof) Patch(ctx context.Context, req *app.HostProofPatchRequest) (*app.HostProof, error) {
	if req.GetDateExpiresNull() {
		return nil, status.Error(codes.InvalidArgument,
			"date_expires: a claim that never expires is a name provable by whoever finds the record")
	}

	return s.HostProofServiceServer.Patch(ctx, req)
}
