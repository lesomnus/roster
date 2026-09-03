package core

import (
	"context"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// A row that names two tenants, refused.
//
// # What was wrong
//
// The wall reaches a tenant by **one** path per entity -- `holder.tenant` for a
// membership, `tenant` for a team. A row that names two things reaches two, and
// nothing compared them. So this was written and accepted:
//
//	SiteMembership{holder: somebody in contoso, site: a site of fabrikam's}
//
// and whichever path the wall happened to take decided who could see it. One
// tenant read a row naming the other's, which is the one thing the wall exists
// to make impossible. It was found by writing it; nothing refused, nothing
// logged.
//
// # Why the schema cannot say it
//
// Each edge is valid on its own -- that holder exists, that site exists -- and
// there is no constraint over the pair, because the thing they must agree about
// is not on either row. It is a judgement about a combination, which is what
// this package is for.
//
// # Why it is not the wall's job either
//
// The wall is a predicate on reads. This is a **write** naming something the
// writer may not have been able to read, and by the time a read is narrowed the
// row already exists. Refusing it at the write is the only place the answer
// helps.
//
// A caller who cannot see one of the rows gets `NotFound` from the lookup and
// the write is refused, which is also right: you may not point at what you
// cannot see.

// tenantsAgree refuses when the tenants it was given are not all the same.
//
// `pdid.Nil` is "nothing was named" and is skipped, since an optional edge that
// was left out says nothing about which tenant this belongs to.
func tenantsAgree(field string, vs ...pdid.Id) error {
	var was pdid.Id
	for _, v := range vs {
		if v == pdid.Nil {
			continue
		}
		if was == pdid.Nil {
			was = v

			continue
		}
		if was != v {
			return pderr.Invalidf(field,
				"this names rows of two tenants, and a row belongs to one; "+
					"the wall reaches a tenant by one path and the other would be invisible to it")
		}
	}

	return nil
}

// The tenant each kind of reference belongs to, read through this stack so that
// the wall applies: a caller who cannot see the row cannot point at it either.
//
// A nil reference is `pdid.Nil` rather than an error. Whether an edge is
// required is the schema's to say, and saying it twice is how the two come to
// disagree.

func (s Core) tenantOfHolder(ctx context.Context, ref *app.HolderRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfSite(ctx context.Context, ref *app.SiteRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Site().Get(ctx, app.SiteGetRequest_builder{
		Ref:    ref,
		Select: app.SiteSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfTeam(ctx context.Context, ref *app.TeamRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Team().Get(ctx, app.TeamGetRequest_builder{
		Ref:    ref,
		Select: app.TeamSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfRole(ctx context.Context, ref *app.RoleRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    ref,
		Select: app.RoleSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfGroup(ctx context.Context, ref *app.GroupRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Group().Get(ctx, app.GroupGetRequest_builder{
		Ref:    ref,
		Select: app.GroupSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

// tenantOfRef is a `TenantRef` as an identifier, which is the one case that
// needs no read: a tenant names itself.
func (s Core) tenantOfRef(ctx context.Context, ref *app.TenantRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}
	if b := ref.GetId(); len(b) > 0 {
		return pdid.From(b)
	}

	v, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    ref,
		Select: app.TenantSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetId())
}

// coreTenant is the layer over the generated `TenantService`, for its one
// overlay.
type coreTenant struct {
	Core
	app.TenantServiceServer
}

func (s Core) Tenant() app.TenantServiceServer { return coreTenant{s, s.Next().Tenant()} }

// Update is the narrow write over a tenant: name, note and labels, and never
// the alias or the identifier. See `tenant_svc.ext.proto`.
func (s coreTenant) Update(ctx context.Context, req *app.TenantUpdateRequest) (*app.Tenant, error) {
	patch := app.TenantPatchRequest_builder{
		Ref:         req.GetRef(),
		DateUpdated: req.GetDateUpdated(),
	}
	if req.HasName() {
		patch.Name = z.Ptr(req.GetName())
	}
	if req.HasDesc() {
		patch.Desc = z.Ptr(req.GetDesc())
	}
	// A map has no presence: given, it replaces; empty, it is left as it is.
	if len(req.GetLabels()) > 0 {
		patch.Labels = req.GetLabels()
	}

	return s.TenantServiceServer.Patch(ctx, patch.Build())
}
