package core

import (
	"context"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// A connection's secret is the deployment's, and goes to the connection's
// issuer.
//
// `secret_ref` names one of the deployment's secrets, and the front door sends
// what it names to the issuer, as the client secret of the code exchange. Both
// halves were a tenant's to write, so a tenant's administrator could name any
// secret the front door holds -- another tenant's client secret, the front
// door's own key -- beside an issuer of their own, start a sign-in through it,
// and be handed the secret by the front door.
//
// So the two writes that decide where a secret goes are the deployment's: a
// reference that is new, and a new issuer for a connection that has one. The
// deployment is the file it declares its tenants in, the CLI on its database,
// a key of its own, and its operators ([Core.deployment]). A tenant's
// administrator may keep the
// reference they were given or take it away, which hands nothing to anybody,
// may change everything else about the connection, and a connection with no
// secret -- a public client, which PKCE is for -- is entirely theirs.
//
// The same answer `TenantProfile.slack_secret_ref` gives, for the same reason:
// a reference is the deployment naming its own secret.
func (s Core) maySend(ctx context.Context, was *app.Connection, ref, issuer string) error {
	if ref == "" || s.deployment(ctx) {
		return nil
	}
	if ref != was.GetSecretRef() {
		return status.Error(codes.PermissionDenied,
			"secret_ref: names one of the deployment's secrets, which the deployment writes; a tenant may keep it or take it away")
	}
	if issuer != was.GetIssuer() {
		return status.Error(codes.PermissionDenied,
			"issuer: a connection with a secret sends it to its issuer, so moving it is the deployment's; take the secret away first, or ask the deployment")
	}

	return nil
}

// Add refuses a secret from a tenant, for [maySend]'s reason: a new connection
// is a new reference and a new issuer at once.
func (s coreConnection) Add(ctx context.Context, req *app.ConnectionAddRequest) (*app.Connection, error) {
	if err := s.maySend(ctx, nil, req.GetSecretRef(), req.GetIssuer()); err != nil {
		return nil, err
	}

	return s.ConnectionServiceServer.Add(ctx, req)
}

// Update is `Patch` with the name held back -- and refused outright on a row a
// file declared, which `declared.go` argues for -- and where a secret goes held
// to [maySend].
func (s coreConnection) Update(ctx context.Context, req *app.ConnectionUpdateRequest) (*app.Connection, error) {
	got, err := s.ConnectionServiceServer.Get(ctx, app.ConnectionGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.ConnectionSelect_builder{Labels: z.Ptr(true), Issuer: z.Ptr(true), SecretRef: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	ref, issuer := got.GetSecretRef(), got.GetIssuer()
	if req.HasSecretRef() {
		ref = req.GetSecretRef()
	}
	if req.HasIssuer() {
		issuer = req.GetIssuer()
	}
	if err := s.maySend(ctx, got, ref, issuer); err != nil {
		return nil, err
	}

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
