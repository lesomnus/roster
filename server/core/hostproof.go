package core

import (
	"context"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
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
//
// # And a claim that ran out is replaced
//
// A tenant has one claim of a name, and `HostService/Add` on one that has run
// out says *make another*. The one that ran out was still that claim -- the
// sweep that collects them is daily -- so making another answered
// `AlreadyExists` for up to a day, unless somebody erased the old one first,
// which nothing said to do (#279). A claim past its window proves nothing and
// is spent by nothing, so a new one takes its place: the tenant's own, found
// the way `HostService/Add` finds the claim it refuses, and erased as a claim
// that is spent is.
//
// One still good is not replaced. Its token is what the tenant was told to
// publish, and a second `Add` handing out another would make the first a
// record that never matches.
func (s coreHostProof) Add(ctx context.Context, req *app.HostProofAddRequest) (*app.HostProof, error) {
	if err := s.acrossTheTenant(ctx, app.HostProofService_Add_FullMethodName); err != nil {
		return nil, err
	}
	if err := hostnamed("name", req.GetName()); err != nil {
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

	if err := s.lapsed(ctx, req); err != nil {
		return nil, err
	}

	return s.HostProofServiceServer.Add(ctx, v)
}

// lapsed lets go of the claim a new one would collide with, where that claim
// has run out.
//
// Read before the write rather than after its refusal: a write the database
// refuses ends the transaction it was in, and this may be inside one
// ([coreEmail.Attest] says what that cost). Through the wall, so the claim is
// only ever the caller's tenant's own, by the reference `coreHost.proved`
// reads the same claim by. A claim that is not there, or not one this caller
// may see, is nothing to let go of: the `Add` that follows answers for
// itself.
//
// Two writes and no transaction around them. Between them the tenant has no
// claim of the name, which is where it stood the moment the old one ran out.
func (s coreHostProof) lapsed(ctx context.Context, req *app.HostProofAddRequest) error {
	was, err := s.HostProofServiceServer.Get(ctx, app.HostProofGetRequest_builder{
		Ref: app.HostProofRef_builder{
			At: app.HostProofRefByAt_builder{Tenant: req.GetTenant(), Name: z.Ptr(req.GetName())}.Build(),
		}.Build(),
		Select: app.HostProofSelect_builder{DateExpires: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil
	}
	if e := was.GetDateExpires(); e == nil || !e.AsTime().Before(time.Now()) {
		return nil
	}

	_, err = s.HostProofServiceServer.Erase(ctx, app.HostProofRef_builder{Id: was.GetId()}.Build())

	return err
}

// Patch is the normalisation and nothing else, which the name being immutable
// makes almost empty: what is left that a patch may write is `desc`.
//
// `token` and `date_expires` are immutable and nullable-with-a-flag
// respectively, so a patch could clear the window a claim is bounded by. It is
// closed at the transport with every other general write (`closed`,
// `cmd/serve.go`: no port serves one, whatever a configuration says), which is
// what the equivalent gap in `coreEmail.Patch` rests on as well.
func (s coreHostProof) Patch(ctx context.Context, req *app.HostProofPatchRequest) (*app.HostProof, error) {
	if err := s.acrossTheTenant(ctx, app.HostProofService_Patch_FullMethodName); err != nil {
		return nil, err
	}
	if req.GetDateExpiresNull() {
		return nil, status.Error(codes.InvalidArgument,
			"date_expires: a claim that never expires is a name provable by whoever finds the record")
	}

	return s.HostProofServiceServer.Patch(ctx, req)
}
