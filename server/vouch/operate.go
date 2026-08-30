package vouch

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
// -- roadmap.md's items 3 and 10. What that person needs is a way to open an
// account and a way to hand somebody a new password, both from a console rather
// than from a shell on the box.
//
// The shape is the one D13 named when it shut the door: not reopening
// `CredentialService`, but a narrow service that takes secrets in and never
// answers with one it was holding. This is that service, and these are three
// more of its methods.
//
// # And why the rule went in first
//
// Resetting a password is a way to **become** somebody. An operator who may
// reset anybody in their tenant effectively holds every permission in it, which
// is `server/core/escalate.go`'s shape arriving through a door that did not
// exist when that file was written. It went in before this did, and the list
// of twelve said so: it is the only pair in that list where the order is a
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

	// Whoever this is about, resolved **here** and once.
	//
	// `Set` resolves an address too, and letting it do so for this call meant
	// resolving twice and then remembering to do it the same way -- which is
	// exactly what was not remembered: the invalidation below asked `refOf`
	// again, got nil for an address, and skipped. So a reset by email changed
	// the password and left every session the takeover had opened alive, which
	// is the one thing the paragraph below says must not happen, silently, on
	// the form an operator actually uses.
	//
	// Resolved before the passphrase is made so that a call about nobody costs
	// nothing, and so that both halves below are about the same person by
	// construction rather than by agreement.
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

	secret, err := passphrase()
	if err != nil {
		return nil, status.Error(codes.Internal, "a secret cannot be made just now")
	}

	// The write is `Credential.Set`'s: it hashes, refuses a leaked secret,
	// runs the escalation rule and upserts -- all in the layer that owns the
	// row. `Reset` is the recovery form on top of that: it resolves the address
	// (`Set` takes a reference, so the email lookup is done here and once,
	// above) and generates the passphrase, then hands both over. No cycle,
	// because `s.walled` answers the generated `Server` interface and
	// `Credential()` on it is a method call, not an import of `server/core`.
	if _, err := s.walled.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    ref,
		Secret: []byte(secret),
	}.Build()); err != nil {
		return nil, err
	}

	// And everything issued before now is void.
	//
	// D26 left this deliberately: *a password reset that leaves old sessions
	// alive is not a reset* is true, and coupling it to `Set` would mean
	// somebody changing their own password signs themselves out of everything
	// with nothing having said so. This is the other act -- somebody else
	// giving them a new one -- and it is where recovery from a takeover
	// happens, so the sessions the takeover opened have to go with it.
	//
	// Best effort after the fact: the password is already changed, and failing
	// the whole call would leave the caller unsure which half happened.
	_, _ = s.walled.Holder().Invalidate(ctx, app.HolderInvalidateRequest_builder{Ref: ref}.Build())

	return app.VouchResetResponse_builder{Secret: secret}.Build(), nil
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
