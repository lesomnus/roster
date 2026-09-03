package core

import (
	"context"

	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
)

// coreConnection is the layer over the generated `ConnectionService`, for its
// one overlay: `Update`, which `connection_svc.ext.proto` argues for.
type coreConnection struct {
	Core
	app.ConnectionServiceServer
}

func (s Core) Connection() app.ConnectionServiceServer {
	return coreConnection{s, s.Next().Connection()}
}

// Update is `Patch` with the name held back.
func (s coreConnection) Update(ctx context.Context, req *app.ConnectionUpdateRequest) (*app.Connection, error) {
	patch := app.ConnectionPatchRequest_builder{
		Ref:         req.GetRef(),
		DateUpdated: req.GetDateUpdated(),
	}
	if req.HasIssuer() {
		patch.Issuer = z.Ptr(req.GetIssuer())
	}
	if req.HasClientId() {
		patch.ClientId = z.Ptr(req.GetClientId())
	}
	// A list has no presence: given, it replaces; empty, it is left as it is.
	if len(req.GetScopes()) > 0 {
		patch.Scopes = req.GetScopes()
	}
	if req.HasSecretRef() {
		patch.SecretRef = z.Ptr(req.GetSecretRef())
	}
	if req.HasDesc() {
		patch.Desc = z.Ptr(req.GetDesc())
	}

	return s.ConnectionServiceServer.Patch(ctx, patch.Build())
}
