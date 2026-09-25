package core

import (
	"context"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
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

// Update is the narrow write over a tenant: name, note, labels and the
// settings, and never the alias or the identifier. See `tenant_svc.ext.proto`.
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
	// A message does have presence, so an absent one is *leave it* and an empty
	// one is *nothing is set* -- which for `password` means back to the default,
	// and is a thing a caller may want to say.
	if req.HasConfig() {
		patch.Config = req.GetConfig()
	}

	return s.TenantServiceServer.Patch(ctx, patch.Build())
}

// Everyverb is what the first role of a tenant is called, and EveryMethod is
// what it holds.
//
// A name somebody will read in a list of roles and understand without opening
// it, and a **pattern** rather than an enumeration: a list written the day a
// tenant is made is what existed that day, and its administrator is the one
// person who must not have to notice a release adding a method.
//
// `/roster.*/*` and not `/*.*/*`, which would take in payday's own -- the same
// line `roster init` draws for the operator's own role, and `cmd` takes these
// rather than spelling them a second time.
const (
	Everyverb   = "everything"
	EveryMethod = "/roster.*/*"

	// Administers is what the first holder of a tenant is called. A name
	// somebody can guess from outside, because a caller that has just made a
	// tenant has to be able to name its administrator without being told.
	Administers = "admin"
)

// Add is a tenant **and somebody who can administer it**.
//
// # Why the generated verb does more than the row
//
// Because the row on its own is not a thing anybody wanted. A customer was four
// writes -- the tenant, the first holder, a role that administers it, the
// binding -- and what the four left between the first and the last was a tenant
// **nobody could do anything in**. The only way to finish one was a roster
// operator reaching inside through `admin.addr`, the port where standing comes
// from the port rather than from a role, which is the shape #26 is about.
//
// So the four are one act on the verb that already means *make a tenant*,
// rather than a second verb meaning *make a tenant properly*. `docs/usage/customers.md`
// argued the opposite once -- no fifth RPC, because a composite would be a
// fifth thing to hold to the rules -- and that is about a **caller** composing
// them. Every write below goes back through `s.Core`, this layer, so a role
// naming methods the caller does not hold meets `mayGrant` here exactly as it
// would have arriving on its own.
//
// # And what it does not do
//
// Hand out a way in. The first holder gets no password here, because a verb
// that answers with a row cannot answer with a secret as well -- and the verb
// that does is `Credential.Issue`, which needs nothing from this one: the
// holder is `@<tenant>/admin`, by slug, and a caller that has just made the
// tenant knows both halves.
//
//	roster tenant add @newco
//	roster vouch reset @newco/admin
//
// # The control plane is not a customer
//
// There, the one tenant is the deployment itself and its first holder is
// `roster init`'s business -- named by `--operator`, bound by `allow`, and
// given a password in the same act. `WithPrefix` is what tells the two apart,
// the same fact `ApiKey.Issue` and `Credential.Issue` read.
//
// # One transaction
//
// Four writes, and a refusal at any of them leaves nothing: an alias already
// taken, a role wider than the caller holds. Written one at a time a refusal
// would leave a tenant nobody can get into whose alias cannot be used again --
// `roster init`'s failure before #20, at a larger size. A caller who has
// already arranged a transaction -- a batch -- arrives with no driver and runs
// inside theirs.
func (s coreTenant) Add(ctx context.Context, req *app.TenantAddRequest) (*app.Tenant, error) {
	if s.prefix != keys.PrefixTenant {
		return s.TenantServiceServer.Add(ctx, req)
	}

	var out *app.Tenant

	// `below` and not `at.Tenant()`: the row is the write this layer is
	// **in** the middle of, so sending it back through the layer would be
	// this method calling itself. The three after it do go through, because
	// each is a write the layer has rules about.
	run := func(at app.Server, below app.TenantServiceServer) error {
		t, err := below.Add(ctx, req)
		if err != nil {
			return err
		}

		h, err := at.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:  Administers,
		}.Build())
		if err != nil {
			return err
		}

		r, err := at.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:   Everyverb,
			Desc:    "Everything roster serves about this tenant, including what a later release adds.",
			Methods: []string{EveryMethod},
		}.Build())
		if err != nil {
			return err
		}

		if _, err := at.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build()); err != nil {
			return err
		}

		out = t

		return nil
	}

	if s.drv == nil {
		if err := run(s.Core, s.TenantServiceServer); err != nil {
			return nil, err
		}

		return out, nil
	}

	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// This layer again over a rebound one below it, which is what
	// [Core.WithDriver] does and what `only` does beside it: the rules carry
	// over and the driver does not, so nothing inside opens a second
	// transaction in this one.
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}
	if err := run(s.over(next), next.Tenant()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return out, nil
}
