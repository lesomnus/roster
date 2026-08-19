package vouch

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
)

// What a local operator does, in a deployment that has no mail.
//
// # Why these are here at all
//
// D13 closed `CredentialService` entirely -- unregistered, and closed to the
// batch -- because its generated `Get` answers with the verifier. That is right
// for the read and it took the write with it: nothing on the wire could set a
// password, and `init` plus a shell was the only way in.
//
// An air-gapped deployment cannot live with that. There is no mail, so the
// "somebody else" who delivers a recovery code is a **person**, which makes
// recovery and an operator-initiated reset the same mechanism reached two ways
// -- PLAN.md's list, items 3 and 10. What that person needs is a way to open an
// account and a way to hand somebody a new password, both from a console rather
// than from a shell on the box.
//
// The shape is the one D13 named when it shut the door: not reopening
// `CredentialService`, but a narrow service that takes secrets in and never
// answers with one it was holding. This is that service, and these are two more
// of its methods.
//
// # And why the rule went in first
//
// Resetting a password is a way to **become** somebody. An operator who may
// reset anybody in their tenant effectively holds every permission in it, which
// is `server/core/escalate.go`'s shape arriving through a door that did not
// exist when that file was written. It went in before this did, and PLAN.md's
// list said so: it is the only pair in that list where the order is a
// correctness question rather than a convenience.

// Reset gives somebody a new password and answers with it once.
func (s *Server) Reset(ctx context.Context, req *app.VouchResetRequest) (*app.VouchResetResponse, error) {
	if k := kindOf(req.GetKind()); k != KindPassword {
		// A second factor is [Server.Enrol], which generates a seed and answers
		// with it once -- the same shape as this and a different act, because
		// what somebody does with a seed is scan it rather than type it. This
		// comment used to say there was nothing sensible to generate for one,
		// which was wrong and was the reason `Enrol` did not exist.
		return nil, status.Errorf(codes.InvalidArgument,
			"kind: %q is not a password; a second factor is Enrol", k)
	}

	secret, err := passphrase()
	if err != nil {
		return nil, status.Error(codes.Internal, "a secret cannot be made just now")
	}

	// Through `Set`, which is where the hashing, the wall and the escalation
	// rule already are. Reimplementing any of the three here would be a second
	// copy of each, and the copy that gets it wrong is the one nobody reads.
	if _, err := s.Set(ctx, app.VouchSetRequest_builder{
		Who:    req.GetWho(),
		Kind:   req.GetKind(),
		Secret: []byte(secret),
	}.Build()); err != nil {
		return nil, err
	}

	return app.VouchResetResponse_builder{Secret: secret}.Build(), nil
}

// Unlock opens an account too many wrong answers closed.
//
// It clears the lockout and the count and leaves the secret alone, which is
// what makes it a different act from [Server.Reset]: somebody who forgot their
// password needs a new one, and somebody who was locked out by an attacker
// needs their old one back.
//
// The version is a precondition, as everywhere else here, and a conflict is
// reported rather than forced -- two operators opening one account at once is
// one of them finding out.
func (s *Server) Unlock(ctx context.Context, req *app.VouchUnlockRequest) (*app.VouchUnlockResponse, error) {
	ref, err := refOf(req.GetWho())
	if err != nil {
		return nil, err
	}
	if ref == nil {
		ref, err = s.byAddress(ctx, req.GetWho().GetTenant(), req.GetWho().GetAddress())
		if err != nil {
			return nil, err
		}
	}

	v, err := s.credential(ctx, s.walled, ref, req.GetKind())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, v.GetHolder().GetId()); err != nil {
		return nil, err
	}

	was := v.GetDateLocked()

	_, err = s.walled.Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateUpdated:    v.GetDateUpdated(),
	}.Build())
	if err != nil {
		return nil, err
	}

	res := app.VouchUnlockResponse_builder{}
	if was != nil {
		// Answered rather than swallowed, so that an operator can tell "I
		// opened it" from "it was not closed" -- which is the question they are
		// about to be asked by whoever called them.
		res.WasLockedUntil = timestamppb.New(was.AsTime())
	}

	return res.Build(), nil
}

// passphrase is thirty-two bytes somebody can read out over a radio.
//
// Base64 rather than words, which is what `roster init` already prints, and it
// is worth knowing what that costs in the deployment this exists for: an
// operator reading one aloud will mis-hear a character, and there is no
// checksum. A word list would be kinder and is a change to make once, in both
// places, rather than differently in each.
func passphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Enrol makes a second factor and answers with it once.
//
// # It does not count until it is proved
//
// The row goes in with `last_step` at zero, which is what **unconfirmed** means
// for this kind: one code has to verify before the column moves. A seed that
// counted the moment it was written would make a mis-scanned QR something
// somebody discovers when they are already half signed in and cannot finish.
//
// An unconfirmed factor still **verifies** -- that is how it gets confirmed --
// and it does not appear in what a person *has*. The two are different
// questions and only the second one is about whether to ask for it.
func (s *Server) Enrol(ctx context.Context, req *app.VouchEnrolRequest) (*app.VouchEnrolResponse, error) {
	if kindOf(req.GetKind()) != KindTotp {
		// A password is `Set` or `Reset`, and neither of those is a thing a
		// phone holds. Refused rather than routed, because a caller asking for
		// one here has misunderstood which act they are doing.
		return nil, status.Errorf(codes.InvalidArgument,
			"kind: %q is not something to enrol; a password is Set or Reset", req.GetKind())
	}
	if s.keys.Current == "" {
		return nil, status.Error(codes.Unimplemented,
			"this deployment holds no key to wrap a seed with, so it cannot hold a second factor")
	}

	ref, err := refOf(req.GetWho())
	if err != nil {
		return nil, err
	}
	if ref == nil {
		ref, err = s.byAddress(ctx, req.GetWho().GetTenant(), req.GetWho().GetAddress())
		if err != nil {
			return nil, err
		}
	}

	who, err := s.walled.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// Adding a way in for somebody is not quite writing their credential, and
	// it is close enough: an operator who could enrol a factor on an
	// administrator's account would hold one of the two things that person
	// signs in with. Same rule, same place.
	if err := s.mayReach(ctx, who.GetId()); err != nil {
		return nil, err
	}

	seed, err := totpSeed()
	if err != nil {
		return nil, status.Error(codes.Internal, "a seed cannot be made just now")
	}

	stored, err := s.keys.Wrap(seed)
	if err != nil {
		return nil, status.Error(codes.Internal, "a seed cannot be stored just now")
	}

	if _, err := s.walled.Credential().Add(ctx, app.CredentialAddRequest_builder{
		Holder: ref,
		Kind:   KindTotp,
		Name:   req.GetName(),
		Secret: stored,
	}.Build()); err != nil {
		return nil, err
	}

	issuer := req.GetIssuer()
	if issuer == "" {
		issuer = "roster"
	}

	return app.VouchEnrolResponse_builder{
		Seed: base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed),
		Uri:  TotpUri(issuer, who.GetAlias(), seed),
	}.Build(), nil
}
