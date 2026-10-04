package core

import (
	"bytes"
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

type coreNomination struct {
	Core
	app.NominationServiceServer
}

func (s Core) Nomination() app.NominationServiceServer {
	return coreNomination{s, s.Next().Nomination()}
}

// Add writes who a deployment key is answered as in one tenant, and asks of it
// the two questions a way into an account is asked.
//
// **Whose holder.** One of this tenant's: a row naming somebody else's would
// hand a caller a frame in a tenant that never agreed to it -- one row reaching
// two, which is what the agreements in `agree.go` exist for. It was
// `actsAsIsTheirs` on `Host` and is the same rule.
//
// **How wide a holder.** A narrowed `rk_` is answered with the nominated
// holder's bindings and not the key's methods (`server/keys/at.go`), so this
// row is a way to act as that holder, held by whoever holds the key. Nominating
// somebody wider than yourself is writing a way into an account wider than your
// own, refused on the terms a key or a credential is (`escalate.go`,
// [Core.mayWriteAWayIn]). The deployment's own work has no frame and passes, as
// it does everywhere in that file: `roster login provision` nominates the
// holder it made.
func (s coreNomination) Add(ctx context.Context, req *app.NominationAddRequest) (*app.Nomination, error) {
	if err := byATenant(ctx); err != nil {
		return nil, err
	}
	if len(req.GetBorrowerId()) == 0 {
		return nil, pderr.Invalidf("borrower_id", "the control-plane holder whose keys this nominates for; a nomination for nobody answers nothing")
	}
	if _, err := pdid.From(req.GetBorrowerId()); err != nil {
		return nil, pderr.Invalidf("borrower_id", "%v", err)
	}

	if err := s.nominatesTheirOwn(ctx, req.GetTenant(), req.GetActsAs()); err != nil {
		return nil, err
	}
	if err := s.mayWriteAWayIn(ctx, "acts_as", req.GetActsAs()); err != nil {
		return nil, err
	}

	return s.NominationServiceServer.Add(ctx, req)
}

// Patch is `Add`'s two questions asked again about the one field a patch may
// change, because re-pointing a nomination is nominating somebody.
//
// The tenant is read off the row: the edge is immutable, so what a patch is
// about is whatever the row already names. `Apply` stands on the transport
// alone, which is where every general write in this package stands and why
// (`escalate.go`, "the one place it is not").
func (s coreNomination) Patch(ctx context.Context, req *app.NominationPatchRequest) (*app.Nomination, error) {
	if err := byATenant(ctx); err != nil {
		return nil, err
	}
	if who := req.GetActsAs(); who != nil {
		v, err := s.Next().Nomination().Get(ctx, app.NominationGetRequest_builder{
			Ref:    req.GetRef(),
			Select: app.NominationSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}

		at := app.TenantRef_builder{Id: v.GetTenant().GetId()}.Build()
		if err := s.nominatesTheirOwn(ctx, at, who); err != nil {
			return nil, err
		}
		if err := s.mayWriteAWayIn(ctx, "acts_as", who); err != nil {
			return nil, err
		}
	}

	return s.NominationServiceServer.Patch(ctx, req)
}

// Erase is a tenant ending an app's nomination, and refused to a deployment
// key as itself for [byATenant]'s reason.
func (s coreNomination) Erase(ctx context.Context, req *app.NominationRef) (*app.NominationEraseResponse, error) {
	if err := byATenant(ctx); err != nil {
		return nil, err
	}

	return s.NominationServiceServer.Erase(ctx, req)
}

// byATenant refuses a deployment key answered as itself.
//
// That key is `frame.Everything` held to its methods, so one allowed `Add`
// could write a nomination in any tenant -- its own holder borrowing as
// somebody there who holds nothing, whom the reach rule lets anybody name. A
// nomination is a tenant's decision, or the deployment's own work through the
// unwalled server (`roster app install`), which has no frame. A key narrowed to
// a tenant's holder is that holder and is asked what anybody there is.
func byATenant(ctx context.Context) error {
	if f, ok := frame.From(ctx); ok && f.Actor.Domain() == pd.ApiKeyDomain {
		return status.Error(codes.PermissionDenied,
			"a nomination is a tenant's to write, and a deployment key as itself is nobody in any tenant")
	}

	return nil
}

// nominatesTheirOwn refuses a nomination of somebody else's holder.
//
// An unnamed tenant is whatever the frame is, which payday fills in below; a
// holder of another tenant than the frame's is refused there by the gate, so
// [tenantsAgree] skipping an unnamed side is not a hole.
func (s coreNomination) nominatesTheirOwn(ctx context.Context, at *app.TenantRef, who *app.HolderRef) error {
	if who == nil {
		return pderr.Invalidf("acts_as", "who the key is answered as; a nomination of nobody is no nomination")
	}

	whose, err := s.tenantOfHolder(ctx, who)
	if err != nil {
		return err
	}

	where := pdid.Nil
	if at != nil {
		v, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
		if err != nil {
			return err
		}

		if where, err = pdid.From(v.GetId()); err != nil {
			return err
		}
	}

	return tenantsAgree("acts_as", whose, where)
}

// List is every nomination the caller may see -- and for an unnarrowed
// deployment key, only its own.
//
// # Why a key is narrowed here and not by the wall
//
// An app run for many tenants has to learn which tenants it acts in before it
// can name one, so it asks with its key unnarrowed, and an unnarrowed key is
// `frame.Everything`: the wall shows it every tenant's nominations, every
// other app's included. What it is owed is its own, and which are its own is a
// fact about the **key** -- the control-plane holder it hangs off -- that the
// wall has no way to read.
//
// So the filter is the layer's to write rather than the caller's: every filter
// is held to the key's own `borrower_id`, and one naming another app's is
// refused rather than quietly rewritten. No twin verb beside `List` -- CLAUDE.md,
// *no self-only twin of a verb* -- the same `List`, answering about the
// caller's own rows when the caller is a key.
//
// Anybody else is read as before: a person, or a key narrowed to a holder,
// sees the nominations of the tenant the wall leaves them, which is what a
// tenant administrator managing which apps act as whom needs.
func (s coreNomination) List(ctx context.Context, req *app.NominationListRequest) (*app.NominationListResponse, error) {
	own, ok, err := s.ownBorrower(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		fs, err := heldTo(req.GetFilters(), own)
		if err != nil {
			return nil, err
		}

		req = app.NominationListRequest_builder{Filters: fs, Size: req.GetSize(), After: req.GetAfter()}.Build()
	}

	return s.NominationServiceServer.List(ctx, req)
}

// Watch is refused to an unnarrowed deployment key, where [coreNomination.List]
// answers.
//
// A payday watch names the rows it is about by reference and refuses anything
// else, and a key's own nominations are found by `borrower_id`, which it cannot
// filter on. So a key polls `List`: an app learning that a tenant installed it
// a minute late is a smaller cost than a stream that would have to be shown
// every app's rows to be useful. Anybody else watches as before.
func (s coreNomination) Watch(req *app.NominationWatchRequest, stream app.NominationService_WatchServer) error {
	if _, ok, err := s.ownBorrower(stream.Context()); err != nil {
		return err
	} else if ok {
		return status.Error(codes.Unimplemented,
			"a deployment key cannot watch nominations: a watch names its rows, and a key's are found by borrower_id; poll List")
	}

	return s.NominationServiceServer.Watch(req, stream)
}

// Get is held to the same: a key reads its own nominations and is told nothing
// of any other's, not even that it exists.
func (s coreNomination) Get(ctx context.Context, req *app.NominationGetRequest) (*app.Nomination, error) {
	own, ok, err := s.ownBorrower(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		yes := true
		v, err := s.Next().Nomination().Get(ctx, app.NominationGetRequest_builder{
			Ref:    req.GetRef(),
			Select: app.NominationSelect_builder{BorrowerId: &yes}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(v.GetBorrowerId(), own.Bytes()) {
			return nil, status.Error(codes.NotFound, "no such nomination")
		}
	}

	return s.NominationServiceServer.Get(ctx, req)
}

// ownBorrower is the `borrower_id` an unnarrowed deployment key may read, and
// whether the caller is one at all.
//
// No frame is the deployment's own work through an unwalled server, which reads
// everything as it does everywhere in this package. A frame whose actor is a
// holder -- a person, or a key narrowed to the holder a tenant nominated -- is
// the wall's to narrow.
func (s coreNomination) ownBorrower(ctx context.Context) (pdid.Id, bool, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.Domain() != pd.ApiKeyDomain {
		return pdid.Nil, false, nil
	}
	if s.rules.Borrower == nil {
		return pdid.Nil, false, status.Error(codes.Unimplemented,
			"this server cannot say whose a deployment key is, so it shows a key no nominations")
	}

	own, err := s.rules.Borrower(ctx, f.Actor)
	if err != nil {
		return pdid.Nil, false, err
	}

	return own, true, nil
}

// heldTo is every filter narrowed to `own`, and one where there were none.
// A filter that named somebody else's borrower is refused: rewriting it would
// answer a question the caller did not ask.
func heldTo(fs []*app.NominationFilter, own pdid.Id) ([]*app.NominationFilter, error) {
	if len(fs) == 0 {
		return []*app.NominationFilter{app.NominationFilter_builder{BorrowerId: own.Bytes()}.Build()}, nil
	}

	out := make([]*app.NominationFilter, 0, len(fs))
	for _, f := range fs {
		if b := f.GetBorrowerId(); len(b) > 0 && !bytes.Equal(b, own.Bytes()) {
			return nil, status.Error(codes.PermissionDenied,
				"borrower_id: a deployment key reads its own nominations and nobody else's")
		}

		out = append(out, app.NominationFilter_builder{
			Ref:        f.GetRef(),
			Tenant:     f.GetTenant(),
			BorrowerId: own.Bytes(),
			ActsAs:     f.GetActsAs(),
		}.Build())
	}

	return out, nil
}
