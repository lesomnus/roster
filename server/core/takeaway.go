package core

import (
	"context"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// The writes that take away what a row granted: a binding, a role, a group and
// somebody's place in one, and a role held in a team.
//
// # Nobody takes away what they could not have given
//
// `Role.Narrow` was held to it first (`narrow.go`) and these for its reason.
// What somebody holds is also what keeps others out of their account --
// [Core.mayReach] lets a credential be written only for somebody who holds
// nothing the writer does not -- so somebody who could take away what they do
// not hold could strip an administrator below themselves, and then reset the
// administrator's password and be them. Held to taking only what they hold,
// they leave everybody holding at least what they do not.
//
// Each is held to it over what the row granted, at the scope it was granted
// at, which is what its `Add` was held to `mayGrant` over: the binding's role
// at the binding's site; a group's bindings, each at its own, for whoever is
// taken out of it or loses it -- `mayJoin`'s question the other way round -- and
// the manager's role a place in it carried, which `mayAppoint` asked across the
// tenant; a role's methods where it may be bound; a team's role at the team's
// site. A tenant's administrator holds what their tenant was given, so it asks
// nothing of them; a group's manager holds what their group is bound to by
// being in it, so it asks nothing of them about their own group's members.
//
// # And a tenant keeps somebody administering it
//
// Each of them but the team's can leave a tenant with nobody administering it,
// and is held to that too (`administered.go`), as are suspending and erasing
// the last person who does -- which take away nothing a row granted, and are
// not held to the first rule: a person suspended keeps their bindings, so
// `mayReach` still sees everything they hold, and somebody on call must be able
// to suspend a compromised account wider than they are (D26).

func (s coreBinding) Erase(ctx context.Context, req *app.BindingRef) (*app.BindingEraseResponse, error) {
	v, err := s.BindingServiceServer.Get(ctx, app.BindingGetRequest_builder{
		Ref: req,
		Select: app.BindingSelect_builder{
			Labels: z.Ptr(true),
			Role: app.RoleSelect_builder{
				Alias:   z.Ptr(true),
				Methods: z.Ptr(true),
				Tenant:  app.TenantSelect_builder{}.Build(),
			}.Build(),
			Site: app.SiteSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", v.GetLabels()); err != nil {
		return nil, err
	}

	at, err := idOr(v.GetSite().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAway(ctx, "ref", v.GetRole().GetMethods(), at); err != nil {
		return nil, err
	}
	tenant, err := pdid.From(v.GetRole().GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	var out *app.BindingEraseResponse
	could := at == pdid.Nil && administers(v.GetRole().GetMethods())
	err = s.keepsAdministered(ctx, tenant, could, "unbinding role "+v.GetRole().GetAlias(), func(next app.Server) error {
		var err error
		out, err = next.Binding().Erase(ctx, req)

		return err
	})

	return out, err
}

func (s coreRole) Erase(ctx context.Context, req *app.RoleRef) (*app.RoleEraseResponse, error) {
	got, err := s.RoleServiceServer.Get(ctx, app.RoleGetRequest_builder{
		Ref: req,
		Select: app.RoleSelect_builder{
			All:    z.Ptr(true),
			Tenant: app.TenantSelect_builder{}.Build(),
			Site:   app.SiteSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	at, err := idOr(got.GetSite().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAway(ctx, "ref", got.GetMethods(), at); err != nil {
		return nil, err
	}
	tenant, err := pdid.From(got.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	var out *app.RoleEraseResponse
	could := at == pdid.Nil && administers(got.GetMethods())
	err = s.keepsAdministered(ctx, tenant, could, "erasing role "+got.GetAlias(), func(next app.Server) error {
		var err error
		out, err = next.Role().Erase(ctx, req)

		return err
	})

	return out, err
}

func (s coreGroup) Erase(ctx context.Context, req *app.GroupRef) (*app.GroupEraseResponse, error) {
	got, err := s.GroupServiceServer.Get(ctx, app.GroupGetRequest_builder{
		Ref:    req,
		Select: app.GroupSelect_builder{Alias: z.Ptr(true), Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		// A group already gone, which the generated `Erase` answers as it
		// always did: nothing erased.
		return s.GroupServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}

	k, err := pdid.From(got.GetId())
	if err != nil {
		return nil, err
	}
	gs, err := s.grantsOfGroup(ctx, k)
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAll(ctx, "ref", gs); err != nil {
		return nil, err
	}
	tenant, err := pdid.From(got.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	var out *app.GroupEraseResponse
	err = s.keepsAdministered(ctx, tenant, grantsAdministration(gs), "erasing group "+got.GetAlias(), func(next app.Server) error {
		var err error
		out, err = next.Group().Erase(ctx, req)

		return err
	})

	return out, err
}

func (s coreHolder) Erase(ctx context.Context, req *app.HolderRef) (*app.HolderEraseResponse, error) {
	if err := s.declaredHolder(ctx, req); err != nil {
		return nil, err
	}

	var out *app.HolderEraseResponse
	err := s.keepsTheirs(ctx, req, "erasing them", func(next app.Server) error {
		var err error
		out, err = next.Holder().Erase(ctx, req)

		return err
	})

	return out, err
}

// keepsTheirs is [Core.keepsAdministered] for a write that ends a person --
// suspends or erases them -- which may leave the tenant with nobody
// administering it when they are somebody who does, through a binding across
// the whole tenant. A person nobody can see is refused by the write itself,
// which is asked as it always was.
func (s coreHolder) keepsTheirs(ctx context.Context, ref *app.HolderRef, what string, write func(next app.Server) error) error {
	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return write(s.Next())
	}
	if err != nil {
		return err
	}

	who, err := pdid.From(v.GetId())
	if err != nil {
		return err
	}
	tenant, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return err
	}

	// Whether they administer it at all, before anything is locked: most of
	// the people a tenant suspends do not, and their suspension takes no lock.
	// A stack that cannot say asks, rather than guessing they do not.
	could := true
	if s.rules.Granted != nil {
		held, err := s.rules.Granted(ctx, who)
		if err != nil {
			return err
		}
		could = grantsAdministration(held)
	}

	return s.keepsAdministered(ctx, tenant, could, what, write)
}

// grantsOfGroup is what a group's bindings grant everybody in it, each at its
// scope -- [Rules.Joining], which is what putting somebody into it hands them.
func (s Core) grantsOfGroup(ctx context.Context, group pdid.Id) ([]Grant, error) {
	if s.rules.Joining == nil {
		return nil, status.Error(codes.PermissionDenied,
			"this server cannot say what a group holds, so it will not take anybody out of one")
	}

	return s.rules.Joining(ctx, group)
}

// idOr is the identifier `b` holds, and [pdid.Nil] for none: a site a row is
// not in.
func idOr(b []byte) (pdid.Id, error) {
	if len(b) == 0 {
		return pdid.Nil, nil
	}

	return pdid.From(b)
}
