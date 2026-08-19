package vouch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"slices"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// The attempt: what has been proved about somebody, part way through.
//
// PLAN.md D21 is the decision and `continuation.proto` is the row. This is the
// half that moves.

// PrefixContinuation is what a continuation looks like, so that one found in a
// log is recognisable and one presented in the wrong place is refused before it
// is looked up.
const PrefixContinuation = "vc_"

// ContinueFor is how long an attempt stays open.
//
// Fixed here, and there is no field for a caller to say otherwise. D25 let a
// delegation's expiry be named by whoever asked, on the grounds that D21's
// *barely alive* was an argument about a **standalone** bearer and a delegation
// is half of a pair -- and this is exactly the standalone bearer that argument
// was carved out from.
//
// Long enough to read a code off a phone, and no longer.
const ContinueFor = 5 * time.Minute

// Continue proves the next factor.
func (s *Server) Continue(ctx context.Context, req *app.VouchContinueRequest) (*app.VouchContinueResponse, error) {
	res, _, err := s.step(ctx, req.GetContinuation(), req.GetKind(), req.GetName(), req.GetSecret())
	if err != nil {
		return nil, err
	}

	return app.VouchContinueResponse_builder{Verified: res}.Build(), nil
}

// step is the whole of one further factor, and answers the way a first one
// does.
//
// The credential beside the response is the row a caller may mint from, and it
// is nil for every answer that is not a finished attempt -- so [Server.Delegate]
// cannot read one off a refusal or off a sign-in that is still half way.
func (s *Server) step(ctx context.Context, handle, kind, name string, secret []byte) (*app.VouchVerifyResponse, *app.Credential, error) {
	by, err := s.verifierOf(kind)
	if err != nil {
		return nil, nil, err
	}

	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, nil, err
	}

	c, err := s.continuation(ctx, handle, issuer)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, nil, err
		}

		// A handle that was never here, one that has been spent, one that
		// expired, one somebody else was issued: one answer, and it costs what
		// a real comparison costs. Told apart, this says whether a string was
		// ever a real attempt.
		by.Burn(secret)

		return no(), nil, nil
	}

	who := app.HolderRef_builder{Id: c.GetHolder().GetId()}.Build()

	if slices.Contains(c.GetSatisfied(), kindOf(kind)) {
		// Already proved, so there is nothing here to prove again. Refused as
		// an ordinary no rather than as an error: a caller told the difference
		// learns what is in `satisfied` without reading it, which is the same
		// fact by a longer route.
		by.Burn(secret)
		s.spend(ctx, c)

		return no(), nil, nil
	}

	v, err := s.credentialNamed(ctx, s.open, who, kind, name)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, nil, err
		}

		by.Burn(secret)
		s.spend(ctx, c)

		return no(), nil, nil
	}

	if until := v.GetDateLocked(); until != nil && until.AsTime().After(time.Now()) {
		s.spend(ctx, c)

		return app.VouchVerifyResponse_builder{LockedUntil: until}.Build(), nil, nil
	}

	ok, at, err := by.Compare(v.GetSecret(), secret, v.GetLastStep())
	if err != nil {
		return nil, nil, status.Error(codes.Internal, "the stored verifier cannot be read")
	}
	if !ok {
		// Counted against the row the **first** step used, which is what makes
		// D21's fourth condition true: the counters are per credential, so a
		// second factor metering itself would be a guessing surface reached by
		// passing the first and paid for by nobody. Exhausting it closes the
		// door the attempt came through.
		res, err := s.failedAt(ctx, c.GetMeteredBy())
		if err != nil {
			return nil, nil, err
		}

		s.spend(ctx, c)

		return res, nil, nil
	}

	// The old one is spent whatever happens next: single use, and *used* is
	// *not there*.
	if err := s.spend(ctx, c); err != nil {
		return nil, nil, err
	}

	done := append(slices.Clone(c.GetSatisfied()), kindOf(kind))
	metered, err := pdid.From(c.GetMeteredBy())
	if err != nil {
		return nil, nil, err
	}

	res, err := s.answer(ctx, v.GetHolder(), done, metered, issuer)
	if err != nil {
		return nil, nil, err
	}

	// The step is recorded whatever else is true -- D20's replay -- and the
	// counter is cleared only when the sign-in is finished. Clearing it on a
	// factor that is not the last one would pay the bill the next wrong guess
	// is about to run up.
	if err := s.passed(ctx, v, at, res.GetOk()); err != nil {
		return nil, nil, err
	}
	if res.GetOk() {
		// Nothing left to prove, so the attempt is finished and the credential
		// travels back for a caller that wants to mint from it.
		return res, v, nil
	}

	return res, nil, nil
}

// answer is the shape every step of an attempt answers with.
//
// `ok` and a continuation are mutually exclusive, and that is the whole of what
// keeps an app that has not heard of second factors failing **closed**: it goes
// on gating on `ok`, and `ok` is set only when there is nothing left.
func (s *Server) answer(ctx context.Context, h *app.Holder, satisfied []string, metered pdid.Id, issuer pdid.Id) (*app.VouchVerifyResponse, error) {
	holder, err := pdid.From(h.GetId())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(h.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	left, err := s.factors(ctx, holder, satisfied)
	if err != nil {
		return nil, err
	}
	if len(left) == 0 {
		return app.VouchVerifyResponse_builder{
			Ok:     true,
			Holder: holder.Bytes(),
			Tenant: tenant.Bytes(),
		}.Build(), nil
	}

	handle, err := s.begin(ctx, holder, satisfied, metered, issuer)
	if err != nil {
		return nil, err
	}

	return app.VouchVerifyResponse_builder{
		Holder:       holder.Bytes(),
		Tenant:       tenant.Bytes(),
		Satisfied:    satisfied,
		Available:    left,
		Continuation: handle,
	}.Build(), nil
}

// factors is what this person could still prove.
//
// Unconfirmed rows are left out: a TOTP seed that has never had a code verified
// against it is a QR somebody may have mis-scanned, and offering it is offering
// a form that cannot be filled. It still **verifies**, which is how it gets
// confirmed; the two are different questions.
func (s *Server) factors(ctx context.Context, who pdid.Id, satisfied []string) ([]*app.VouchFactor, error) {
	vs, err := s.open.Credential().List(ctx, app.CredentialListRequest_builder{
		Filters: []*app.CredentialFilter{
			app.CredentialFilter_builder{
				Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build(),
		},
	}.Build())
	if err != nil {
		return nil, err
	}

	out := []*app.VouchFactor{}
	for _, v := range vs.GetItems() {
		if slices.Contains(satisfied, v.GetKind()) {
			continue
		}
		if v.GetKind() == KindTotp && v.GetLastStep() == 0 {
			continue
		}

		f := app.VouchFactor_builder{Kind: v.GetKind(), Name: v.GetName()}
		if u := v.GetDateLocked(); u != nil && u.AsTime().After(time.Now()) {
			f.LockedUntil = u
		}

		out = append(out, f.Build())
	}

	return out, nil
}

// begin writes the attempt down and answers with the handle.
func (s *Server) begin(ctx context.Context, who pdid.Id, satisfied []string, metered pdid.Id, issuer pdid.Id) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", status.Error(codes.Internal, "a continuation cannot be made just now")
	}

	handle := PrefixContinuation + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(handle))

	_, err := s.open.Continuation().Add(ctx, app.ContinuationAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Satisfied:   satisfied,
		Secret:      sum[:],
		Issuer:      issuer.Bytes(),
		MeteredBy:   metered.Bytes(),
		DateExpires: timestamppb.New(time.Now().Add(ContinueFor)),
	}.Build())
	if err != nil {
		return "", err
	}

	return handle, nil
}

// continuation finds the attempt a handle names, if it is the caller's.
func (s *Server) continuation(ctx context.Context, handle string, by pdid.Id) (*app.Continuation, error) {
	if len(handle) == 0 || handle[:min(len(handle), len(PrefixContinuation))] != PrefixContinuation {
		return nil, status.Error(codes.NotFound, "no such continuation")
	}

	sum := sha256.Sum256([]byte(handle))

	v, err := s.open.Continuation().Get(ctx, app.ContinuationGetRequest_builder{
		Ref: app.ContinuationRef_builder{Secret: sum[:]}.Build(),
		Select: app.ContinuationSelect_builder{
			Secret:      z.Ptr(true),
			Satisfied:   z.Ptr(true),
			Issuer:      z.Ptr(true),
			MeteredBy:   z.Ptr(true),
			DateExpires: z.Ptr(true),
			DateUpdated: z.Ptr(true),

			Holder: app.HolderSelect_builder{
				Tenant:       app.TenantSelect_builder{}.Build(),
				DateErased:   z.Ptr(true),
				DateDisabled: z.Ptr(true),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// The index found it by this exact value; this is what makes the answer
	// independent of how a mismatched hash sorts.
	if subtle.ConstantTimeCompare(v.GetSecret(), sum[:]) != 1 {
		return nil, status.Error(codes.NotFound, "no such continuation")
	}

	u := v.GetDateExpires()
	if u == nil || !time.Now().Before(u.AsTime()) {
		return nil, status.Error(codes.NotFound, "no such continuation")
	}

	// Somebody who has been erased or suspended since the first form is
	// somebody the second form must not finish for.
	if h := v.GetHolder(); h.GetDateErased() != nil || h.GetDateDisabled() != nil {
		return nil, status.Error(codes.NotFound, "no such continuation")
	}

	if len(v.GetIssuer()) == 0 || by == pdid.Nil ||
		subtle.ConstantTimeCompare(v.GetIssuer(), by.Bytes()) != 1 {
		// D21's third condition. Two empty slices compare equal, so each side
		// is refused before the comparison rather than by it.
		return nil, status.Error(codes.NotFound, "no such continuation")
	}

	return v, nil
}

// spend erases the attempt, which is the whole of what single-use means here.
func (s *Server) spend(ctx context.Context, v *app.Continuation) error {
	_, err := s.open.Continuation().Erase(ctx,
		app.ContinuationRef_builder{Id: v.GetId()}.Build())

	return err
}

// failedAt counts a failure against a credential named by id.
func (s *Server) failedAt(ctx context.Context, id []byte) (*app.VouchVerifyResponse, error) {
	v, err := s.open.Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: app.CredentialRef_builder{Id: id}.Build(),
		Select: app.CredentialSelect_builder{
			Failures:    z.Ptr(true),
			DateLocked:  z.Ptr(true),
			DateUpdated: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// The row the attempt was metered on is gone, which is somebody
			// erased between the two forms. Nothing to count and nothing to
			// finish.
			return no(), nil
		}

		return nil, err
	}

	return s.failed(ctx, v)
}

// credentialNamed is [Server.credential] when there may be more than one of a
// kind.
func (s *Server) credentialNamed(ctx context.Context, from app.Server, ref *app.HolderRef, kind, name string) (*app.Credential, error) {
	return from.Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{
				Holder: ref,
				Kind:   z.Ptr(kindOf(kind)),
				Name:   z.Ptr(name),
			}.Build(),
		}.Build(),
		Select: app.CredentialSelect_builder{
			Secret:      z.Ptr(true),
			Failures:    z.Ptr(true),
			DateLocked:  z.Ptr(true),
			LastStep:    z.Ptr(true),
			DateUpdated: z.Ptr(true),

			Holder: app.HolderSelect_builder{
				Tenant:       app.TenantSelect_builder{}.Build(),
				DateErased:   z.Ptr(true),
				DateDisabled: z.Ptr(true),
			}.Build(),
		}.Build(),
	}.Build())
}
