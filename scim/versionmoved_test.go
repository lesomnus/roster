package scim_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// TestTheScimEndpointAnswersAVersionThatMovedAsItDid: a profile the endpoint
// writes under the version it read, and somebody else's write lands in
// between each time, is asked again three times over and then answered `409`
// with no `scimType`, as it was when roster answered the version that moved
// `FailedPrecondition`. roster answers it `Aborted` now, which is the code the
// endpoint asks again on: what its own client is told does not change with
// roster's code, and SCIM has no word of its own for it (RFC 7644 §3.12 keeps
// `409` for a version, by `ETag`, which this endpoint does not serve).
func TestTheScimEndpointAnswersAVersionThatMovedAsItDid(t *testing.T) {
	x := require.New(t)

	// Every `Update` of somebody is met by another write of them first, made
	// in process as an operator's would be: the version the endpoint read is
	// never the row's by the time it is written under.
	var s *cmd.Server
	var tries atomic.Int32
	moving := grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if r, ok := req.(*rstr.HolderUpdateRequest); ok && info.FullMethod == rstr.HolderService_Update_FullMethodName {
			tries.Add(1)
			if _, err := s.Ungated.Holder().Invalidate(ctx, rstr.HolderInvalidateRequest_builder{Ref: r.GetRef()}.Build()); err != nil {
				return nil, err
			}
		}

		return next(ctx, req)
	})
	d := stand(t, moving)
	s = d.s

	id := d.someone(t, "versioned", erinOid)
	code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "replace", "path": "displayName", "value": "Renamed"}},
	})
	x.Equal(http.StatusConflict, code, "%v", got)
	x.Nil(got["scimType"], "%v", got)
	x.EqualValues(4, tries.Load(), "a version that moved is asked again three times, and then answered")
}
