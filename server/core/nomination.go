package core

import (
	"context"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
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
