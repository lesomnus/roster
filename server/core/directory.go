package core

import (
	"context"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// A directory's say over the people it provisions: `Provision`, `Deactivate`
// and `Activate`, which `holder_svc.ext.proto` argues for one at a time, and
// `Holder.directory`, which says whose a suspension is.
const (
	directoryInactive = "inactive"
	directoryDeleted  = "deleted"
)

// provisioning is the connection a tenant's directory provisions its people
// through (`Connection.provisions`). FailedPrecondition when there is none:
// a directory with nowhere to link the people it makes has nothing to do.
func (s Core) provisioning(ctx context.Context, tenant pdid.Id) (*app.Connection, error) {
	after := ""
	for {
		vs, err := s.Next().Connection().List(ctx, app.ConnectionListRequest_builder{
			Filters: []*app.ConnectionFilter{app.ConnectionFilter_builder{
				Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if v.GetProvisions() {
				return v, nil
			}
		}

		after = vs.GetNext()
		if after == "" {
			return nil, status.Error(codes.FailedPrecondition,
				"no connection of this tenant provisions its people; `Connection.provisions` names the one a directory does")
		}
	}
}

// speaksFor refuses somebody their directory does not sign in: a person with
// no identity at the connection it provisions through. So what a directory may
// suspend is the people who arrive through it -- not an app's holder, not the
// front door's, not somebody who signs in another way.
func (s Core) speaksFor(ctx context.Context, holder []byte, tenant pdid.Id) error {
	c, err := s.provisioning(ctx, tenant)
	if err != nil {
		return err
	}

	after := ""
	for {
		vs, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{
				Holder: app.HolderRef_builder{Id: holder}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return err
		}
		for _, v := range vs.GetItems() {
			if v.GetProvider() == c.GetName() {
				return nil
			}
		}

		after = vs.GetNext()
		if after == "" {
			return status.Errorf(codes.PermissionDenied,
				"the directory speaks for the people who sign in through %s, and this is not one of them", c.GetName())
		}
	}
}

// Provision makes somebody a directory says exists, with their identity and
// their address, in one write or none.
//
// Every write goes through this layer's own rules -- the identity is held to
// its connection's claim, the address to its normal form, both to
// [Core.mayWriteAWayIn] -- over a stack bound to one transaction, as
// `Tenant.Add` writes its four. The way-in rules pass because the person is
// new and holds nothing; that they are new is what makes this safe to hold
// where `Identity.Add` is not.
func (s coreHolder) Provision(ctx context.Context, req *app.HolderProvisionRequest) (*app.Holder, error) {
	t, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    req.GetTenant(),
		Select: app.TenantSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(t.GetId())
	if err != nil {
		return nil, err
	}

	c, err := s.provisioning(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if req.GetProvider() != c.GetName() {
		return nil, pderr.Invalidf("provider",
			"%q is not the connection this tenant provisions through, which is %q", req.GetProvider(), c.GetName())
	}
	if err := subjectIsStable(req.GetSubject()); err != nil {
		return nil, err
	}
	if subjectClaimOf(c.GetSubjectClaim()) == claimOid && !isGUID(req.GetSubject()) {
		return nil, pderr.Invalidf("subject",
			"the %s connection names people by `oid`, which is a GUID, and %q is not one", c.GetName(), req.GetSubject())
	}
	if a := req.GetAddress(); a != "" {
		if err := normalised("address", a, front.Address); err != nil {
			return nil, err
		}
	}
	if req.GetAlias() == "" {
		return nil, pderr.Invalidf("alias", "what roster calls them; a directory names one from their address")
	}
	alias, err := s.freeAlias(ctx, t.GetId(), req.GetAlias())
	if err != nil {
		return nil, err
	}

	var out *app.Holder
	run := func(at Core) error {
		h, err := at.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:   alias,
			Name:    req.GetName(),
			Profile: req.GetProfile(),
		}.Build())
		if err != nil {
			return err
		}
		ref := app.HolderRef_builder{Id: h.GetId()}.Build()

		if _, err := at.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder:   ref,
			Provider: req.GetProvider(),
			Subject:  req.GetSubject(),
		}.Build()); err != nil {
			return err
		}
		if a := req.GetAddress(); a != "" {
			// Unverified, on purpose: see `HolderService.Provision`.
			if _, err := at.Email().Add(ctx, app.EmailAddRequest_builder{
				Holder:  ref,
				Address: a,
			}.Build()); err != nil {
				return err
			}
		}

		out = h

		return nil
	}

	if s.drv == nil {
		if err := run(s.Core); err != nil {
			return nil, err
		}

		return out, nil
	}

	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}
	if err := run(s.over(next)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return out, nil
}

// freeAlias is `want`, or `want` with a suffix when somebody has it -- the
// rule a sign-in that enrols somebody follows, so two people named alike are
// two people rather than a refusal the directory retries forever.
//
// A read and not a guarantee: two at once may pick the same, and the second
// `Add` is refused as taken, which the directory retries.
func (s coreHolder) freeAlias(ctx context.Context, tenant []byte, want string) (string, error) {
	try := want
	for range 8 {
		_, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(try),
				Tenant: app.TenantRef_builder{Id: tenant}.Build(),
			}.Build()}.Build(),
			Select: app.HolderSelect_builder{}.Build(),
		}.Build())
		switch status.Code(err) {
		case codes.NotFound:
			return try, nil
		case codes.OK:
		default:
			return "", err
		}

		try = want + "-" + slug.RandomAliasN(4)
	}

	return "", status.Errorf(codes.AlreadyExists, "alias: %q and every suffix tried are taken", want)
}

// Deactivate suspends somebody on their directory's word.
//
// An operator's suspension stays the operator's: the directory saying
// `inactive` about somebody already suspended changes nothing, because what
// it may lift afterwards is only its own. `deleted` is written over it all the
// same -- the directory will not speak of them again, and that is no power to
// lift anything (`Activate` refuses the deleted).
func (s coreHolder) Deactivate(ctx context.Context, req *app.HolderDeactivateRequest) (*app.Holder, error) {
	v, err := s.directed(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}

	patch := app.HolderPatchRequest_builder{
		Ref: app.HolderRef_builder{Id: v.GetId()}.Build(),
	}
	switch {
	case req.GetDeleted():
		patch.Directory = z.Ptr(directoryDeleted)
	case v.GetDirectory() == directoryDeleted:
		return v, nil
	case v.GetDateDisabled() != nil && v.GetDirectory() == "":
		return v, nil
	default:
		patch.Directory = z.Ptr(directoryInactive)
	}
	if v.GetDateDisabled() == nil {
		patch.DateDisabled = timestamppb.Now()
	}
	lock(&patch, req.GetDateUpdated())

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

// Activate lifts the suspension the directory made, and refuses the rest.
func (s coreHolder) Activate(ctx context.Context, req *app.HolderActivateRequest) (*app.Holder, error) {
	v, err := s.directed(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}

	switch v.GetDirectory() {
	case directoryInactive:
	case directoryDeleted:
		return nil, status.Error(codes.FailedPrecondition,
			"the directory deleted them; bringing them back is an operator's, with `holder enable`")
	default:
		if v.GetDateDisabled() != nil {
			return nil, status.Error(codes.PermissionDenied,
				"an operator suspended them, and that is not the directory's to lift")
		}

		return v, nil
	}

	patch := app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: v.GetId()}.Build(),
		DateDisabledNull: z.Ptr(true),
		Directory:        z.Ptr(""),
	}
	lock(&patch, req.GetDateUpdated())

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

// directed is somebody a directory may speak for, read with what deciding
// about their suspension needs: never a row a file declared, and only a
// person who signs in through the connection the directory provisions.
func (s coreHolder) directed(ctx context.Context, ref *app.HolderRef) (*app.Holder, error) {
	if err := s.declaredHolder(ctx, ref); err != nil {
		return nil, err
	}

	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref: ref,
		Select: app.HolderSelect_builder{
			Tenant:       app.TenantSelect_builder{}.Build(),
			DateDisabled: z.Ptr(true),
			Directory:    z.Ptr(true),
			DateUpdated:  z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.speaksFor(ctx, v.GetId(), tenant); err != nil {
		return nil, err
	}

	return v, nil
}
