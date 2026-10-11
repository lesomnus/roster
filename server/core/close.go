package core

import (
	"context"
	"time"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// A tenant leaving, as the person lifecycle is one level up (#98).
//
//	close   -> nobody of it gets in, nothing is destroyed      TenantService.Close
//	restore -> while `tenant.restore_within` lasts             TenantService.Restore
//	destroy -> the grace over, unless an operator holds it     server/forget
//	alias   -> held for `tenant.alias_hold`, then free         server/forget
//
// The first two are verbs on the entity, here, because they are writes on one
// row that a role can name. The last two are not reachable over the wire at
// all: destroying is `roster forget` and a sweep, through ent and past every
// server, for the reason forgetting a person is -- see `server/forget`.
//
// What closing *reaches* is not here either. It is read where each way in
// arrives -- `cmd.Resolver`, `server/keys`, `server/vouch`, `server/front` --
// because that is where a person's disabled flag is already read, and a
// tenant being closed is the same refusal for everybody in it.

// Close stamps `date_closed` and destroys nothing. See `tenant_svc.ext.proto`.
//
// # Refused twice over
//
// A tenant a file declared is the file's to close, as it is to edit
// (`declared.go`), so this is refused on one exactly as `Update` is. And the
// control plane's one tenant is the deployment itself: closing it would shut
// out the deployment's own operators, with nobody left who could restore it.
//
// A tenant closed already is refused rather than stamped again, because the
// stamp is what the grace runs from: closing twice would quietly restart the
// clock on destroying it.
func (s coreTenant) Close(ctx context.Context, req *app.TenantCloseRequest) (*app.Tenant, error) {
	if s.prefix != keys.PrefixTenant {
		return nil, status.Error(codes.FailedPrecondition,
			"the control plane's tenant is this deployment, and closing it would leave nobody to restore it")
	}

	got, err := s.TenantServiceServer.Get(ctx, app.TenantGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.TenantSelect_builder{
			Labels:     z.Ptr(true),
			DateClosed: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}
	if v := got.GetDateClosed(); v != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "closed already, since %s", v.AsTime().Format(time.RFC3339))
	}

	patch := app.TenantPatchRequest_builder{
		Ref:        req.GetRef(),
		DateClosed: timestamppb.Now(),
	}
	lockTenant(&patch, req.GetDateUpdated())

	return s.TenantServiceServer.Patch(ctx, patch.Build())
}

// Restore clears `date_closed` while the grace lasts. See
// `tenant_svc.ext.proto`.
//
// After the grace it is refused even if the sweep has not got to the tenant
// yet: what decides is the window the deployment configured and not how
// recently a sweep ran, so that the answer to *can this still be undone* does
// not depend on timing nobody can see. With no window configured, destroying
// is an operator's act and the tenant can be brought back until somebody does
// it.
func (s coreTenant) Restore(ctx context.Context, req *app.TenantRestoreRequest) (*app.Tenant, error) {
	got, err := s.TenantServiceServer.Get(ctx, app.TenantGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.TenantSelect_builder{
			DateClosed:    z.Ptr(true),
			DateDestroyed: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	closed := got.GetDateClosed()
	switch {
	case got.GetDateDestroyed() != nil:
		return nil, status.Error(codes.FailedPrecondition, "destroyed: what it held is gone, so there is nothing to bring back")

	case closed == nil:
		return nil, status.Error(codes.FailedPrecondition, "not closed, so there is nothing to undo")

	case s.restoreWithin > 0 && !time.Now().Before(closed.AsTime().Add(s.restoreWithin)):
		return nil, status.Errorf(codes.FailedPrecondition,
			"the grace ran out at %s (tenant.restore_within), and it is being destroyed",
			closed.AsTime().Add(s.restoreWithin).Format(time.RFC3339))
	}

	patch := app.TenantPatchRequest_builder{
		Ref:            req.GetRef(),
		DateClosedNull: z.Ptr(true),
	}
	lockTenant(&patch, req.GetDateUpdated())

	return s.TenantServiceServer.Patch(ctx, patch.Build())
}

// lockTenant is [lock] for a tenant: the caller's version when they sent one,
// and otherwise none, for that function's reason -- a decision about a tenant
// that fails because somebody edited a label is one that can be prevented.
func lockTenant(patch *app.TenantPatchRequest_builder, was *timestamppb.Timestamp) {
	if was != nil {
		patch.DateUpdated = was

		return
	}

	patch.DateUpdatedForce = z.Ptr(true)
}

// unlived refuses an `Add` that arrives already somewhere along the way out.
//
// A tenant is made open, unheld and whole. The four columns are written by the
// verbs that say so -- `Close`, `Restore`, `roster hold`, `roster forget` -- and
// a tenant that began life closed, or held by a case nobody opened, would be a
// row whose history starts in the middle.
func unlived(req *app.TenantAddRequest) error {
	switch {
	case req.HasDateClosed():
		return pderr.Invalidf("date_closed", "a tenant is made open; `TenantService.Close` closes one")
	case req.HasDateDestroyed():
		return pderr.Invalidf("date_destroyed", "a tenant is made whole; destroying one is `roster forget`")
	case req.HasDateHeld(), req.HasHold():
		return pderr.Invalidf("date_held", "a hold is placed by an operator, with `roster hold`")
	}

	return nil
}
