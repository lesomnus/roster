package core

import (
	"context"
	"encoding/base32"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
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
//
// Your own row is the one case with a rule of its own, and it is a rule and
// not a verb: `current` is required and verified first, a wrong one is counted
// like a wrong sign-in (`vouch.MaxFailures`, `vouch.LockFor`), and a locked row
// is not compared at all. That is what `ChangeMine` was for, folded back in
// here so that a person and an operator call one method about one row and
// only the layer knows the difference. Naming somebody else with `current` set
// is refused: an operator does not know it and must not be asked for it.
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

	// Whose row this is decides one thing: your own asks for the password you
	// hold, anybody else's must not be asked for it.
	f, framed := frame.From(ctx)
	own := framed && !f.Actor.IsZero() && f.Actor == holder
	switch {
	case own && len(req.GetCurrent()) == 0:
		return nil, status.Error(codes.PermissionDenied,
			"current: your own password is changed by proving the one you hold; a reset is somebody else's to make")
	case !own && len(req.GetCurrent()) != 0:
		return nil, status.Error(codes.InvalidArgument,
			"current: is for your own password; naming somebody else, leave it out")
	}

	ref := app.HolderRef_builder{Id: who.GetId()}.Build()
	byKind := app.CredentialRef_builder{
		Kind: app.CredentialRefByKind_builder{Holder: ref, Kind: z.Ptr(kind)}.Build(),
	}.Build()

	// The stored verifier is read here, in process, and only for the caller's
	// own row -- the wire method that would answer it (`Get`) is closed.
	v, err := s.Next().Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: byKind,
		Select: app.CredentialSelect_builder{
			DateUpdated: z.Ptr(true),
			Secret:      z.Ptr(own),
			Failures:    z.Ptr(own),
			DateLocked:  z.Ptr(own),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}
		if own {
			// Nothing to reauth against. A first password is set for somebody
			// by an operator or the recovery flow, not by them here: a bearer
			// that could set a first password with no current one to prove is
			// the takeover the reauth exists to close.
			return nil, status.Error(codes.FailedPrecondition,
				"you have no password to change; a first one is set for you, not changed by you")
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

	if own {
		if err := s.reauth(ctx, v, req.GetCurrent()); err != nil {
			return nil, err
		}
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

// reauth is the proof a caller gives before their own password is replaced:
// the one they hold, compared timing-safe against the stored verifier.
//
// A wrong answer is a wrong sign-in and is counted as one -- the same
// `MaxFailures` and `LockFor` as `Verify`, on the same columns -- so a stolen
// delegation cannot guess its way past this any faster than past the sign-in
// form. `ChangeMine` compared without counting, which was a hole this closes.
// A locked row is refused before it is compared, for the reason the sign-in
// refuses one: the lock is what the comparison's cost was buying.
func (s coreCredential) reauth(ctx context.Context, v *app.Credential, current []byte) error {
	if until := v.GetDateLocked(); until != nil && until.AsTime().After(time.Now()) {
		return status.Errorf(codes.PermissionDenied,
			"locked until %s after too many wrong answers", until.AsTime().Format(time.RFC3339))
	}

	same, err := vouch.Compare(v.GetSecret(), current)
	if err != nil {
		return status.Error(codes.Internal, "the stored password cannot be read")
	}
	if same {
		return nil
	}

	n := v.GetFailures() + 1
	patch := app.CredentialPatchRequest_builder{
		Ref:         app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Failures:    z.Ptr(n),
		DateUpdated: v.GetDateUpdated(),
	}
	if n >= vouch.MaxFailures {
		patch.DateLocked = timestamppb.New(time.Now().Add(vouch.LockFor))
		patch.Failures = z.Ptr(int32(0))
	}
	// Best effort, like `Verify`'s: a count lost to a concurrent write is a
	// worse thing to fail the call over than to under-count.
	_, _ = s.Next().Credential().Patch(ctx, patch.Build())

	return status.Error(codes.PermissionDenied, "the current password is not the one held")
}

// Enrol makes a second factor and answers with it once -- the write `Vouch.Enrol`
// was, on the entity it writes a row to. A `totp` seed is generated, wrapped
// with the deployment's key and answered once (the row itself is the secret, so
// it cannot go through `Set`, which hashes); a `webauthn` public key is checked
// and kept, with nothing to answer.
//
// The seed is wrapped with `server/core`'s keyring, which is the same one
// `server/vouch` reads a code back with -- handed to the layer by
// `core.WithKeyring` so the crypto stays in `server/vouch` and only the
// orchestration is here. A deployment that holds no key refuses a `totp`, since
// a seed it cannot wrap is one nothing can ever read back.
//
// Held to `mayReach` like every credential write: adding a way in for somebody
// is one of the two things they sign in with, so you may enrol a factor for
// nobody whose permissions are not a subset of yours. The row goes in
// unconfirmed (`Verify` moves its step), which is `server/vouch`'s to enforce.
// enrol is `Enrol`'s work: a factor made for `ref`, answered as a seed and URI
// (both empty for webauthn). One path whoever `ref` is -- `mayReach` passes for
// a caller writing their own and refuses one wider than the caller -- which is
// why there is no self-only twin of it; see the service comment in
// `credential_svc.ext.proto`.
func (s coreCredential) enrol(ctx context.Context, ref *app.HolderRef, kind, name, issuer string, attestation []byte) (string, string, error) {
	switch kind {
	case vouch.KindTotp:
		if len(attestation) > 0 {
			return "", "", pderr.Invalidf("attestation",
				"a seed is made here; a request carrying one has not decided which ceremony it is doing")
		}
		if s.keyring.Current == "" {
			return "", "", status.Error(codes.Unimplemented,
				"this deployment holds no key to wrap a seed with, so it cannot hold a second factor")
		}

	case vouch.KindWebAuthn:
		if len(attestation) == 0 {
			return "", "", pderr.Invalidf("attestation",
				"an authenticator makes this one; roster is handed the public half")
		}

	default:
		// A password is `Set` or `Reset`, and neither is a thing a phone or a
		// key holds. Refused rather than routed: a caller asking for one here
		// has misunderstood which act they are doing.
		return "", "", status.Errorf(codes.InvalidArgument,
			"kind: %q is not something to enrol; a password is Set or Reset", kind)
	}

	// Read the person before writing, for the alias the URI carries and the id
	// the escalation rule compares -- and through `Next()`, so a caller who
	// cannot see them cannot enrol a way into their account either.
	who, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return "", "", err
	}

	holder, err := pdid.From(who.GetId())
	if err != nil {
		return "", "", err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return "", "", err
	}

	on := app.HolderRef_builder{Id: who.GetId()}.Build()

	if kind == vouch.KindWebAuthn {
		// Checked before it is written: an attestation nobody verified is a row
		// that answers to whoever sent it.
		v, err := vouch.Register(attestation)
		if err != nil {
			return "", "", pderr.Invalidf("attestation", "%s", err)
		}

		if _, err := s.Next().Credential().Add(ctx, app.CredentialAddRequest_builder{
			Holder: on,
			Kind:   vouch.KindWebAuthn,
			Name:   name,
			Secret: v.Stored,

			// The counter the authenticator reported, so the first assertion has
			// something to exceed. Registering **is** the proof here, so unlike a
			// seed there is no unconfirmed state to be in.
			LastStep: v.Count,
		}.Build()); err != nil {
			return "", "", err
		}

		// The private half never left the authenticator, so there is nothing to
		// answer with.
		return "", "", nil
	}

	seed, err := vouch.TotpSeed()
	if err != nil {
		return "", "", status.Error(codes.Internal, "a seed cannot be made just now")
	}

	stored, err := s.keyring.Wrap(seed)
	if err != nil {
		return "", "", status.Error(codes.Internal, "a seed cannot be stored just now")
	}

	if _, err := s.Next().Credential().Add(ctx, app.CredentialAddRequest_builder{
		Holder: on,
		Kind:   vouch.KindTotp,
		Name:   name,
		Secret: stored,
	}.Build()); err != nil {
		return "", "", err
	}

	if issuer == "" {
		issuer = "roster"
	}

	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed),
		vouch.TotpUri(issuer, who.GetAlias(), seed), nil
}

// Enrol makes a second factor and answers with it once -- the write `Vouch.Enrol`
// was, on the entity it writes a row to, named by a reference. See [coreCredential.enrol].
func (s coreCredential) Enrol(ctx context.Context, req *app.CredentialEnrolRequest) (*app.CredentialEnrolResponse, error) {
	seed, uri, err := s.enrol(ctx, req.GetRef(), req.GetKind(), req.GetName(), req.GetIssuer(), req.GetAttestation())
	if err != nil {
		return nil, err
	}

	return app.CredentialEnrolResponse_builder{Seed: seed, Uri: uri}.Build(), nil
}
