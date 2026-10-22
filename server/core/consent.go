package core

import (
	"bytes"
	"context"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// coreConsent is the layer over the generated `ConsentService`: an app's
// ledger of what its accounts agreed to (`proto/app/consent.proto`).
//
// # Whose rows
//
// An app's. The app is the holder the call is made as -- its key's, or the one
// a deployment key is answered as in the tenant -- and it is written onto every
// act it records and compared on every read: `Get` finds nothing of another
// app's, and `List` is narrowed to the caller whatever it asked. A call made as
// nobody -- the deployment's own commands, unframed -- reads every app's and
// records for none, since a consent with no app is a consent to nobody's
// documents.
//
// # Not a way in, and not a grant
//
// So neither of the rules every other write about a person is held to applies
// here. Recording that somebody agreed to an app's terms hands nobody a way
// into their account and changes nothing the gate answers, and holding an app
// to `mayReach` would refuse it the accounts wider than its own role -- which
// is every account with a role. What it is held to is that the account is one
// it may see: a holder read through the wall, before anything is written.
//
// # Every act is a row
//
// `Agree` and `Withdraw` each add a row after the ones before it on the same
// document, and nothing changes a row: the generated writes are closed on the
// wire, and refused here to a caller with a frame besides. What is in force is
// the latest act, by its sequence ([Consent.seq]); two callers acting at once
// on one document both reach for the same next number, and the index that is
// unique over it refuses the second, which is then asked again from the start.
type coreConsent struct {
	Core
	app.ConsentServiceServer
}

func (s Core) Consent() app.ConsentServiceServer {
	return coreConsent{s, s.Next().Consent()}
}

// consentApp is the app a consent is recorded and read as: the frame's actor.
func consentApp(ctx context.Context) (pdid.Id, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return pdid.Nil, status.Error(codes.FailedPrecondition,
			"a consent is an app's, and this call is made as nobody: an app records and asks with its own key")
	}

	return f.Actor, nil
}

// consentAccount is the account a request names, read through the wall: an app
// records and asks only about somebody it may see, and the rows it writes name
// them by identifier.
func (s coreConsent) consentAccount(ctx context.Context, ref *app.HolderRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, pderr.Invalidf("holder", "whose consent")
	}
	v, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetId())
}

// actsOf is every act of one account on one document of one app in one scope
// at one version: a range of the index `acts`, read a page at a time.
func actsOf(ctx context.Context, next app.Server, account, by pdid.Id, scope, document, version string) ([]*app.Consent, error) {
	filter := app.ConsentFilter_builder{
		Holder:   app.HolderRef_builder{Id: account.Bytes()}.Build(),
		App:      by.Bytes(),
		Scope:    z.Ptr(scope),
		Document: z.Ptr(document),
		Version:  z.Ptr(version),
	}.Build()

	out := []*app.Consent{}
	after := ""
	for {
		vs, err := next.Consent().List(ctx, app.ConsentListRequest_builder{
			Filters: []*app.ConsentFilter{filter},
			Size:    100,
			After:   after,
		}.Build())
		if err != nil {
			return nil, err
		}
		out = append(out, vs.GetItems()...)
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

// inForce is the latest of a document's acts, and nil when there are none.
func inForce(vs []*app.Consent) *app.Consent {
	var out *app.Consent
	for _, v := range vs {
		if out == nil || v.GetSeq() > out.GetSeq() {
			out = v
		}
	}

	return out
}

// recording runs `fn` as one transaction, and again when it lost a race for a
// document's next act: what it read is no longer the latest, so it reads again.
// Only in a transaction of its own -- inside a batch, the refused write has
// already ended the batch's, and the batch is what is asked again.
func (s coreConsent) recording(ctx context.Context, fn func(next app.Server) error) error {
	for tries := 1; ; tries++ {
		err := s.atomically(ctx, "record consent in more than one write", fn)
		if status.Code(err) != codes.AlreadyExists || s.drv == nil || tries == 3 {
			return err
		}
	}
}

// Agree records an account agreeing to an app's documents, each at the version
// it read. See `consent_svc.ext.proto`.
func (s coreConsent) Agree(ctx context.Context, req *app.ConsentAgreeRequest) (*app.ConsentAgreeResponse, error) {
	by, err := consentApp(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetDocuments()) == 0 {
		return nil, pderr.Invalidf("documents", "what was agreed to: at least one document")
	}
	for _, d := range req.GetDocuments() {
		if d.GetDocument() == "" || d.GetVersion() == "" {
			return nil, pderr.Invalidf("documents", "each is a document and the version that was read")
		}
	}
	account, err := s.consentAccount(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}

	var out []*app.Consent
	if err := s.recording(ctx, func(next app.Server) error {
		out = make([]*app.Consent, 0, len(req.GetDocuments()))
		for _, d := range req.GetDocuments() {
			vs, err := actsOf(ctx, next, account, by, req.GetScope(), d.GetDocument(), d.GetVersion())
			if err != nil {
				return err
			}
			was := inForce(vs)
			if was.GetAct() == app.ConsentAct_CONSENT_ACT_AGREE {
				// Agreed already, at this version: nothing happened, and
				// nothing is written.
				out = append(out, was)

				continue
			}

			v, err := next.Consent().Add(ctx, app.ConsentAddRequest_builder{
				Holder:   app.HolderRef_builder{Id: account.Bytes()}.Build(),
				App:      by.Bytes(),
				Scope:    req.GetScope(),
				Document: d.GetDocument(),
				Version:  d.GetVersion(),
				Act:      app.ConsentAct_CONSENT_ACT_AGREE,
				Optional: d.GetOptional(),
				Client:   req.GetClient(),
				Seq:      was.GetSeq() + 1,
			}.Build())
			if err != nil {
				return err
			}
			out = append(out, v)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return app.ConsentAgreeResponse_builder{Acts: out}.Build(), nil
}

// Withdraw records an account withdrawing its agreement to an optional
// document. See `consent_svc.ext.proto`.
func (s coreConsent) Withdraw(ctx context.Context, req *app.ConsentWithdrawRequest) (*app.Consent, error) {
	by, err := consentApp(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetDocument() == "" || req.GetVersion() == "" {
		return nil, pderr.Invalidf("document", "which agreement: a document and its version")
	}
	account, err := s.consentAccount(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}

	var out *app.Consent
	if err := s.recording(ctx, func(next app.Server) error {
		vs, err := actsOf(ctx, next, account, by, req.GetScope(), req.GetDocument(), req.GetVersion())
		if err != nil {
			return err
		}
		was := inForce(vs)
		switch {
		case was.GetAct() != app.ConsentAct_CONSENT_ACT_AGREE:
			return status.Errorf(codes.FailedPrecondition,
				"there is no agreement to %s at %s in force to withdraw", req.GetDocument(), req.GetVersion())
		case !was.GetOptional():
			return status.Errorf(codes.FailedPrecondition,
				"%s is required: an account does not withdraw from what is required, it leaves", req.GetDocument())
		}

		out, err = next.Consent().Add(ctx, app.ConsentAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: account.Bytes()}.Build(),
			App:      by.Bytes(),
			Scope:    req.GetScope(),
			Document: req.GetDocument(),
			Version:  req.GetVersion(),
			Act:      app.ConsentAct_CONSENT_ACT_WITHDRAW,
			Optional: true,
			Client:   req.GetClient(),
			Seq:      was.GetSeq() + 1,
		}.Build())

		return err
	}); err != nil {
		return nil, err
	}

	return out, nil
}

// Settled says which of the documents an app names an account owes. See
// `consent_svc.ext.proto`.
func (s coreConsent) Settled(ctx context.Context, req *app.ConsentSettledRequest) (*app.ConsentSettledResponse, error) {
	by, err := consentApp(ctx)
	if err != nil {
		return nil, err
	}
	account, err := s.consentAccount(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}

	owed := []*app.ConsentDocument{}
	for _, d := range req.GetDocuments() {
		vs, err := actsOf(ctx, s.Next(), account, by, req.GetScope(), d.GetDocument(), d.GetVersion())
		if err != nil {
			return nil, err
		}
		if inForce(vs).GetAct() != app.ConsentAct_CONSENT_ACT_AGREE {
			owed = append(owed, d)
		}
	}

	return app.ConsentSettledResponse_builder{Settled: len(owed) == 0, Owed: owed}.Build(), nil
}

// Get answers an act of the caller's own, and finds nothing of another app's.
func (s coreConsent) Get(ctx context.Context, req *app.ConsentGetRequest) (*app.Consent, error) {
	by, err := consentApp(ctx)
	if err != nil {
		if _, framed := frame.From(ctx); framed {
			return nil, err
		}

		// The deployment's own: every app's.
		return s.ConsentServiceServer.Get(ctx, req)
	}

	whose, err := s.ConsentServiceServer.Get(ctx, app.ConsentGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.ConsentSelect_builder{App: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(whose.GetApp(), by.Bytes()) {
		return nil, status.Error(codes.NotFound, "Consent not found")
	}

	return s.ConsentServiceServer.Get(ctx, req)
}

// List answers the caller's own acts, whatever app a filter named.
func (s coreConsent) List(ctx context.Context, req *app.ConsentListRequest) (*app.ConsentListResponse, error) {
	by, err := consentApp(ctx)
	if err != nil {
		if _, framed := frame.From(ctx); framed {
			return nil, err
		}

		return s.ConsentServiceServer.List(ctx, req)
	}

	req = proto.Clone(req).(*app.ConsentListRequest)
	filters := req.GetFilters()
	if len(filters) == 0 {
		filters = []*app.ConsentFilter{{}}
	}
	for _, f := range filters {
		f.SetApp(by.Bytes())
	}
	req.SetFilters(filters)

	return s.ConsentServiceServer.List(ctx, req)
}

// Add is not how a consent is written by anybody with a frame: `Agree` and
// `Withdraw` stamp the app, the act and its place, which a request must not
// say. Closed on the wire as well; this is the same refusal for whatever
// reaches the stack another way.
func (s coreConsent) Add(ctx context.Context, req *app.ConsentAddRequest) (*app.Consent, error) {
	if _, framed := frame.From(ctx); framed {
		return nil, status.Error(codes.PermissionDenied, "a consent is recorded by Agree and Withdraw, and never written as a row")
	}

	return s.ConsentServiceServer.Add(ctx, req)
}

// Erase is not how a consent goes: it is kept as proof, and destroyed by the
// deployment once its account is gone and `consent.keep` is over.
func (s coreConsent) Erase(ctx context.Context, req *app.ConsentRef) (*app.ConsentEraseResponse, error) {
	if _, framed := frame.From(ctx); framed {
		return nil, status.Error(codes.PermissionDenied, "a consent is kept as proof, and the deployment destroys it once its account is gone")
	}

	return s.ConsentServiceServer.Erase(ctx, req)
}
