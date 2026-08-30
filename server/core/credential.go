package core

import (
	"context"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/types/known/timestamppb"

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

// Unlock opens an account too many wrong answers closed, without touching the
// secret. It is the operator write `Vouch.Unlock` was, now on the entity it is
// about and named by reference rather than by a sign-in form.
//
// Held to `mayReach`: a lockout you may clear is one you could have caused, so
// you may clear it for nobody whose permissions are not a subset of yours. That
// rule is `server/core`'s own -- the same `mayReach` every credential write
// meets -- so it is a line here rather than a service reaching back for it.
func (s coreCredential) Unlock(ctx context.Context, req *app.CredentialUnlockRequest) (*app.CredentialUnlockResponse, error) {
	kind := req.GetKind()
	if kind == "" {
		kind = vouch.KindPassword
	}

	v, err := s.Next().Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{
				Holder: req.GetRef(),
				Kind:   z.Ptr(kind),
			}.Build(),
		}.Build(),
		Select: app.CredentialSelect_builder{
			DateLocked:  z.Ptr(true),
			DateUpdated: z.Ptr(true),
			Holder:      app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return nil, err
	}

	was := v.GetDateLocked()
	if _, err := s.Next().Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateUpdated:    v.GetDateUpdated(),
	}.Build()); err != nil {
		return nil, err
	}

	res := app.CredentialUnlockResponse_builder{}
	if was != nil {
		res.WasLockedUntil = was
	}

	return res.Build(), nil
}

// Set writes somebody's secret to the one given, hashing it -- the operator
// write `Vouch.Set` was, now on the entity and named by a reference. The
// email/sign-in-form addressing stays with the recovery flow (option 2), so
// this takes a HolderRef and does no address lookup.
//
// A first password and a rotation are one call: absent, it is added; present,
// it is replaced and the lockout cleared. The three rules `Vouch.Set` carried
// travel with it -- the settable-kind check, the leaked-corpus refusal, and
// `mayReach` -- the last two now `server/core`'s own.
func (s coreCredential) Set(ctx context.Context, req *app.CredentialSetRequest) (*app.CredentialSetResponse, error) {
	if len(req.GetSecret()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "secret: must not be empty")
	}

	kind := req.GetKind()
	if kind == "" {
		kind = vouch.KindPassword
	}
	// Only a kind something can later check, which is `vouch.Settable`'s whole
	// subject: a second factor is `Enrol`'s and a kind nothing checks is a row
	// no call on any plane can take back. See its comment.
	if err := vouch.Settable(kind); err != nil {
		return nil, err
	}

	// A leaked secret is refused before anything is read or hashed -- a fact
	// about the secret, not about the person, so the refusal cannot depend on
	// whether they exist.
	if s.breached != nil {
		bad, err := s.breached(ctx, req.GetSecret())
		if err != nil {
			return nil, status.Error(codes.Internal, "whether this secret is known cannot be answered just now")
		}
		if bad {
			return nil, status.Error(codes.FailedPrecondition,
				"this one is in a corpus of leaked passwords; pick another")
		}
	}

	sum, err := vouch.Hash(req.GetSecret())
	if err != nil {
		return nil, status.Error(codes.Internal, "the secret cannot be stored just now")
	}

	// Read the person before writing, so the escalation rule has an id to
	// compare and an Add cannot point its edge at somebody who is gone.
	who, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	holder, err := pdid.From(who.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return nil, err
	}

	ref := app.HolderRef_builder{Id: who.GetId()}.Build()
	byKind := app.CredentialRef_builder{
		Kind: app.CredentialRefByKind_builder{Holder: ref, Kind: z.Ptr(kind)}.Build(),
	}.Build()

	v, err := s.Next().Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref:    byKind,
		Select: app.CredentialSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		// None yet: add it.
		if _, err := s.Next().Credential().Add(ctx, app.CredentialAddRequest_builder{
			Holder: ref,
			Kind:   kind,
			Secret: sum,
		}.Build()); err != nil {
			return nil, err
		}

		return &app.CredentialSetResponse{}, nil
	}

	// Replace it, clearing the lockout -- somebody who set it is not who the
	// lockout was protecting against -- under the version read, so a concurrent
	// write is reported rather than lost.
	if _, err := s.Next().Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Secret:         sum,
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateRotated:    timestamppb.Now(),
		DateUpdated:    v.GetDateUpdated(),
	}.Build()); err != nil {
		return nil, err
	}

	return &app.CredentialSetResponse{}, nil
}
