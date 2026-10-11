package forget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
)

// An operator's hold on a tenant: a legal claim, a dispute, a regulator's
// letter -- anything that means *keep this as it is*.
//
// # Why roster needed one of its own
//
// payday's trail asks each app what holds a tenant (`trail.Tenant.Hold`), and
// roster answered nothing: `docs/operating.md` said so in as many words. So a
// hold was impossible to place on anything here, and once a tenant can be
// destroyed that is the missing half -- a destruction a court has asked to be
// stopped has to be stoppable by somebody who is not editing a config file.
//
// So it is a fact on the tenant's row (`Tenant.date_held`, `Tenant.hold`),
// written by `roster hold` from a shell and by nothing a caller reaches: a
// tenant's own administrator lifting the hold on their own records is the
// thing a hold exists to prevent. Through the server the wall was never
// installed on rather than through ent, so that placing and lifting it are in
// the trail.
//
// # What it stops
//
// Destroying the tenant, entirely: [DestroyTenant] refuses, the sweep passes
// it by, and both finish once it is lifted. The people of a held tenant whose
// own grace runs out are not forgotten on a clock either ([Due] leaves them).
// And the trail keeps what it holds of the tenant, as payday's holds do --
// which is [Holds], answering `trail.Policy.Tenants`.
//
// What it does not stop is a person's own request to be forgotten
// (`roster forget @tenant/who`): that is erased except what the hold keeps in
// the trail, which is payday's answer to a legal claim against an erasure
// request (`trail.Hold`).

// Hold places an operator's hold on a tenant. Holding one already held says
// what the hold is for again, and does not move when it began.
func Hold(ctx context.Context, at app.Server, t pdid.Id, why string) error {
	why = strings.TrimSpace(why)
	if why == "" {
		return errors.New("what the hold is for: a case, a demand -- somebody will read it later")
	}

	v, err := at.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Select: app.TenantSelect_builder{DateHeld: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}

	patch := app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Hold:             z.Ptr(why),
		DateUpdatedForce: z.Ptr(true),
	}
	if v.GetDateHeld() == nil {
		patch.DateHeld = timestamppb.Now()
	}

	_, err = at.Tenant().Patch(ctx, patch.Build())

	return err
}

// Lift takes an operator's hold off a tenant. What it stopped carries on from
// there: the sweep destroys a tenant whose grace ran out while it was held, on
// its next pass.
func Lift(ctx context.Context, at app.Server, t pdid.Id) error {
	v, err := at.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Id: t.Bytes()}.Build(),
		Select: app.TenantSelect_builder{DateHeld: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	if v.GetDateHeld() == nil {
		return fmt.Errorf("%s is not held, so there is nothing to lift", t)
	}

	_, err = at.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: t.Bytes()}.Build(),
		DateHeldNull:     z.Ptr(true),
		Hold:             z.Ptr(""),
		DateUpdatedForce: z.Ptr(true),
	}.Build())

	return err
}

// Holds answers payday's question about one tenant -- `trail.Policy.Tenants`
// -- with roster's one answer: held or not. Every tenant keeps the
// deployment's windows; a held one keeps everything of its trail besides.
func Holds(db *ent.Client) func(ctx context.Context, t pdid.Id) (trail.Tenant, error) {
	return func(ctx context.Context, t pdid.Id) (trail.Tenant, error) {
		v, err := db.Tenant.Get(ctx, t.Uuid())
		if ent.IsNotFound(err) {
			return trail.Tenant{}, nil
		}
		if err != nil {
			return trail.Tenant{}, err
		}
		if v.DateHeld == nil {
			return trail.Tenant{}, nil
		}

		return trail.Tenant{Hold: &trail.Hold{
			Why: fmt.Sprintf("roster hold since %s: %s", v.DateHeld.Format(time.RFC3339), v.Hold),
		}}, nil
	}
}

// Held is `p` answering to roster's holds.
func Held(p trail.Policy, db *ent.Client) trail.Policy {
	p.Tenants = Holds(db)

	return p
}
