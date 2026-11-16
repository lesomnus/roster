package keys

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Authenticated is when the person a call is made as last proved who they are,
// by the credential the call arrived with: a delegation a sign-in minted for
// them (`roster-as`), or a key of their own (`rt_`) that carries the sign-in it
// was issued from. Zero for anything else -- a deployment's key, a session at a
// console, a key somebody else minted for them -- because none of those says
// the person proved anything at a moment (`Delegation.date_authenticated`).
//
// It reads the headers the call came with, because that is the only place the
// credential is: what resolved it answers with a frame, and a frame says who
// and not since when. The row is found by its verifier, as this package finds
// it, and has to be `who`'s own. Nothing is decided about the credential here
// -- the call was already authenticated with it -- so the verifier is not read
// back, and `s` may be a server that clears it.
//
// # Which is only true while the headers are what authenticated the call
//
// So they are read here exactly as the chain reads them, and the chain reads
// them **before** a cookie (`cmd`, `Build`): `roster-as` is [Acting]'s, which
// refuses it without the key it was given to; one key in `authorization`, which
// is `auth.Bearer`'s ([bearerOf]). A cookie used to answer first, and beside
// one neither header had been checked by anything -- a session, which says
// nothing of when, was fresh by any delegation string of the person's that was
// minutes old, whoever it had been given to (#276).
//
// # And a sign-in that was ended is not one
//
// `Holder.date_invalidated` voids what was issued before a moment. A key is
// not voided by it, on purpose; the sign-in it carries is, since that is what
// *sign out everywhere* and a changed password end. So a time not after the
// epoch is no time.
func Authenticated(ctx context.Context, s app.Server, who pdid.Id) (time.Time, error) {
	md, _ := metadata.FromIncomingContext(ctx)

	holder := app.HolderSelect_builder{DateInvalidated: z.Ptr(true)}.Build()

	if acting := md.Get(HeaderActing); len(acting) > 0 {
		as, ok := one(acting)
		if !ok {
			return time.Time{}, nil
		}

		v, err := s.Delegation().Get(ctx, app.DelegationGetRequest_builder{
			Ref: app.DelegationRef_builder{Secret: Sum(as)}.Build(),
			Select: app.DelegationSelect_builder{
				DateAuthenticated: z.Ptr(true),
				Holder:            holder,
			}.Build(),
		}.Build())

		return authenticatedOf(v.GetHolder(), v.GetDateAuthenticated().AsTime(), v.HasDateAuthenticated(), who, err)
	}

	token, ok := bearerOf(md)
	if !ok || !strings.HasPrefix(token, PrefixTenant) {
		return time.Time{}, nil
	}

	v, err := s.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref: app.ApiKeyRef_builder{Secret: Sum(token)}.Build(),
		Select: app.ApiKeySelect_builder{
			DateAuthenticated: z.Ptr(true),
			Holder:            holder,
		}.Build(),
	}.Build())

	return authenticatedOf(v.GetHolder(), v.GetDateAuthenticated().AsTime(), v.HasDateAuthenticated(), who, err)
}

// authenticatedOf is [Authenticated]'s answer from the row it found, which is
// nothing for a row that is not there, not `who`'s, or of a sign-in since
// ended.
func authenticatedOf(holder *app.Holder, at time.Time, has bool, who pdid.Id, err error) (time.Time, error) {
	switch {
	case status.Code(err) == codes.NotFound:
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, err
	case !has || !bytes.Equal(holder.GetId(), who.Bytes()):
		return time.Time{}, nil
	}
	if w := holder.GetDateInvalidated(); w != nil && !at.After(w.AsTime()) {
		// Not before or equal, as [findDelegation] reads a tie: too old.
		return time.Time{}, nil
	}

	return at, nil
}
