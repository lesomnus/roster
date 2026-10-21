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

	"github.com/lesomnus/payday/auth"
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
func Authenticated(ctx context.Context, s app.Server, who pdid.Id) (time.Time, error) {
	md, _ := metadata.FromIncomingContext(ctx)

	if as := lastOf(md.Get(HeaderActing)); as != "" {
		v, err := s.Delegation().Get(ctx, app.DelegationGetRequest_builder{
			Ref: app.DelegationRef_builder{Secret: Sum(as)}.Build(),
			Select: app.DelegationSelect_builder{
				DateAuthenticated: z.Ptr(true),
				Holder:            app.HolderSelect_builder{}.Build(),
			}.Build(),
		}.Build())

		return authenticatedOf(v.GetHolder().GetId(), v.GetDateAuthenticated().AsTime(), v.HasDateAuthenticated(), who, err)
	}

	for _, h := range md.Get(auth.Header) {
		token, ok := strings.CutPrefix(h, auth.BearerScheme+" ")
		if !ok || !strings.HasPrefix(token, PrefixTenant) {
			continue
		}

		v, err := s.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
			Ref: app.ApiKeyRef_builder{Secret: Sum(token)}.Build(),
			Select: app.ApiKeySelect_builder{
				DateAuthenticated: z.Ptr(true),
				Holder:            app.HolderSelect_builder{}.Build(),
			}.Build(),
		}.Build())

		return authenticatedOf(v.GetHolder().GetId(), v.GetDateAuthenticated().AsTime(), v.HasDateAuthenticated(), who, err)
	}

	return time.Time{}, nil
}

// authenticatedOf is [Authenticated]'s answer from the row it found, which is
// nothing for a row that is not there or not `who`'s.
func authenticatedOf(holder []byte, at time.Time, has bool, who pdid.Id, err error) (time.Time, error) {
	switch {
	case status.Code(err) == codes.NotFound:
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, err
	case !has || !bytes.Equal(holder, who.Bytes()):
		return time.Time{}, nil
	}

	return at, nil
}

func lastOf(vs []string) string {
	out := ""
	for _, v := range vs {
		if v != "" {
			out = v
		}
	}

	return out
}
