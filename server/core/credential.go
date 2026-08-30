package core

import (
	"context"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// coreCredential is the layer over the generated `CredentialService`.
//
// The service is served now -- see `cmd/serve`'s register/closed -- but its
// generated reads and raw writes stay closed by method, so what reaches the
// wire is these overlays and nothing that answers with a stored verifier. The
// hashing that made `Vouch.Set` a service rather than a `Credential.Add` lives
// here as a layer; this file is the beginning of moving that surface onto the
// entity it was always about. See `CLAUDE.md`, *Overlay before service, layer
// before overlay*.
type coreCredential struct {
	Core
	app.CredentialServiceServer
}

func (s Core) Credential() app.CredentialServiceServer {
	return coreCredential{s, s.Next().Credential()}
}

// ChangeMine changes the caller's own password, verifying the current one.
//
// It takes no subject -- the row is the frame's actor -- so a role naming it
// grants exactly *change your own password* and nothing wider. The `current`
// secret is the reauth: the new one is written only after it is verified, which
// is what keeps a credential that merely acts as somebody from changing their
// password without knowing it. See `credential_svc.ext.proto` for the whole of
// the argument.
//
// The stored verifier is read here, in process, and never travels -- the wire
// method that would answer it (`Get`) is closed. Hashing and the timing-safe
// compare are `server/vouch`'s (`Hash`/`Compare`), because roster is the one
// that compares and so is the one that hashes (D14).
func (s coreCredential) ChangeMine(ctx context.Context, req *app.CredentialChangeMineRequest) (*app.CredentialChangeMineResponse, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return nil, status.Error(codes.Unauthenticated, "changing your own password is a thing only a caller can do, and nothing here says who that is")
	}
	if len(req.GetSecret()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "secret: must not be empty")
	}

	ref := app.HolderRef_builder{Id: f.Actor.Bytes()}.Build()
	named := func() *app.CredentialRef {
		return app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{
				Holder: ref,
				Kind:   z.Ptr(vouch.KindPassword),
				Name:   z.Ptr(""),
			}.Build(),
		}.Build()
	}

	// The current password, read through the layer's own stack. `Next()` is the
	// generated server, and its `Get` answers with the secret column -- which
	// is exactly why it is closed on the wire and read only here.
	v, err := s.Next().Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: named(),
		Select: app.CredentialSelect_builder{
			Secret:      z.Ptr(true),
			DateUpdated: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Nothing to reauth against. A first password is set for somebody
			// (the operator/recovery path), not by them here -- a bearer that
			// could set a first password with no current one to prove is the
			// takeover the reauth exists to close.
			return nil, status.Error(codes.FailedPrecondition,
				"you have no password to change; a first one is set for you, not changed by you")
		}

		return nil, err
	}

	same, err := vouch.Compare(v.GetSecret(), req.GetCurrent())
	if err != nil {
		return nil, status.Error(codes.Internal, "the stored password cannot be read")
	}
	if !same {
		return nil, status.Error(codes.PermissionDenied, "the current password is not the one held")
	}

	sum, err := vouch.Hash(req.GetSecret())
	if err != nil {
		return nil, status.Error(codes.Internal, "the new password cannot be stored just now")
	}

	// The new one, with the lockout cleared -- a fresh secret starts fresh --
	// under the version just read, so a concurrent write is refused rather than
	// lost.
	if _, err := s.Next().Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            named(),
		Secret:         sum,
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateUpdated:    v.GetDateUpdated(),
	}.Build()); err != nil {
		return nil, err
	}

	return &app.CredentialChangeMineResponse{}, nil
}
