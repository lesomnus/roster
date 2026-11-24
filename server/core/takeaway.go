package core

import (
	"bytes"
	"context"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
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
//
// # What a row gave is read from the row
//
// A binding's erase, and a place's on a team or in a group, read what the row
// gave through the role it names, as their caller reads it. That had two
// wrong answers, and they looked the same: a role that did not come back.
//
// One erased since is not read, so the erase of a binding of one failed on an
// identifier with no bytes -- `Unknown`, to the tenant's administrator and at
// the box -- as a place's in a group erased since did, and a team somebody
// held one in answered `NotFound` for a role. A site counts every binding that
// names it ([Sited]), so one whose last binding was of an erased role could
// not be erased by anybody (#279).
//
// And a role of the whole tenant is in no site, so somebody who sees one site
// does not see it ([Named] says the same of making a group). For them a
// binding made in their own site of such a role was the `Unknown` above, and
// a place on a team or a manager's place in a group that carried one was taken
// away with nothing asked: read as an erased role is, which gives nothing.
// Whether a role is there is not what its taker sees of it.
//
// So it is asked of the rows ([Giving]), as what a group hands out already
// was ([Joining]): what the gate counts for the row today. A row that gives
// nothing -- its role erased, or the team or the group it is of -- takes
// nothing from anybody by going, and is erased by whoever may erase where it
// is, which the gate and the wall answered before this layer was reached.

// Giving is what one row hands whoever it names, each grant at the scope it
// is held at: a binding's role where it was bound, the role a place on a team
// carries at the team's site, and the role a manager's place in a group
// carries across the tenant, where whoever appointed them was asked about it
// ([coreGroupMembership.mayAppoint]). Which of the three `row` is, is in its
// identifier.
//
// And nothing where the gate counts nothing for it: the role it names was
// erased, the team or the group it is a place in was, or the group it binds
// was.
//
// Asked of the rows and not through a server, for [Sited]'s reason: it answers
// the same whoever is asking, and somebody erasing a row need not be somebody
// who can read the role it names.
type Giving func(ctx context.Context, row pdid.Id) ([]Grant, error)

// given is [Rules.Giving] for the row of an identifier: what taking it away
// takes from whoever it names.
//
// The deployment's own work, with no frame, is not asked what it holds
// ([Core.mayTakeAway]), so what the row gave is not read for it. A stack that
// cannot say refuses, rather than take away what it has not weighed.
func (s Core) given(ctx context.Context, row []byte) ([]Grant, error) {
	if _, ok := frame.From(ctx); !ok {
		return nil, nil
	}
	if s.rules.Giving == nil {
		return nil, status.Error(codes.PermissionDenied,
			"this server cannot say what a row gives, so it will not take one away")
	}

	k, err := pdid.From(row)
	if err != nil {
		return nil, err
	}

	return s.rules.Giving(ctx, k)
}

func (s coreBinding) Erase(ctx context.Context, req *app.BindingRef) (*app.BindingEraseResponse, error) {
	v, err := s.BindingServiceServer.Get(ctx, app.BindingGetRequest_builder{
		Ref: req,
		Select: app.BindingSelect_builder{
			Labels: z.Ptr(true),
			Role: app.RoleSelect_builder{
				Alias:  z.Ptr(true),
				Tenant: app.TenantSelect_builder{}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		// Not there to this caller, which the generated `Erase` answers:
		// nothing erased (`declared.go`, above the erases there).
		return s.BindingServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", v.GetLabels()); err != nil {
		return nil, err
	}

	gs, err := s.given(ctx, v.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAll(ctx, "ref", gs); err != nil {
		return nil, err
	}
	if !grantsAdministration(gs) {
		// Nothing a tenant is administered through, and so nothing to count:
		// a binding made in a site, one of a role that names less, and one
		// that gives nothing any more.
		return s.BindingServiceServer.Erase(ctx, req)
	}

	// A binding across the tenant of a role that is there, which whoever reads
	// a binding of no site reads with it.
	tenant, err := pdid.From(v.GetRole().GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	var out *app.BindingEraseResponse
	err = s.keepsAdministered(ctx, tenant, true, "unbinding role "+v.GetRole().GetAlias(), func(next app.Server) error {
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
			App:    app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return s.RoleServiceServer.Erase(ctx, req)
	}
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
	of, err := idOr(got.GetApp().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAway(ctx, "ref", got.GetMethods(), at, of); err != nil {
		return nil, err
	}
	tenant, err := pdid.From(got.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	var out *app.RoleEraseResponse
	could := at == pdid.Nil && of == pdid.Nil && administers(got.GetMethods())
	err = s.keepsAdministered(ctx, tenant, could, "erasing role "+got.GetAlias(), func(next app.Server) error {
		var err error
		out, err = next.Role().Erase(ctx, req)

		return err
	})

	return out, err
}

// Erase asks [Core.mayChangeGroup] about the group first, which its manager
// passes for their own group and nobody else's: the method is one a role held
// in a group may name (`managing`), so the gate lets every group's manager
// through for it, and this is where each is held to theirs. Somebody who
// holds it through a binding passes for every group of the scope, as before.
//
// Then what an erase of a group was always asked, by whoever asks: what the
// group handed out, which nobody takes away who could not have given it, and
// that the tenant is still administered.
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

	err = s.mayChangeGroup(ctx, app.GroupService_Erase_FullMethodName,
		app.GroupRef_builder{Id: got.GetId()}.Build())
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
	// A person is no site's, and erasing one is the tenant's as a whole, as
	// making one is ([coreHolder.Add]). Somebody bound in a site suspends a
	// person of that site, which stops them, and does not take them out of
	// the tenant. Their own is theirs to end: somebody leaving
	// (`MeService/Leave`) erases themselves through here.
	if err := s.erasesAcrossTheTenant(ctx, req); err != nil {
		return nil, err
	}
	switch err := s.declaredHolder(ctx, req); {
	case status.Code(err) == codes.NotFound:
		return s.HolderServiceServer.Erase(ctx, req)
	case err != nil:
		return nil, err
	}

	who, err := s.holderOf(ctx, req)
	if err != nil {
		return nil, err
	}

	// And the roles and the groups that are theirs as an app (`Role.app`,
	// `Group.app`), in the same act: a role of an app that has ended names
	// the methods of nobody, a group of one is the users of nothing, and what
	// either handed out is gone as an erased role's or group's is. One
	// transaction, on the person's row, so the erase and theirs are all made
	// or none; a restore of the person brings the row back and not these.
	var out *app.HolderEraseResponse
	err = s.only(ctx, who.Bytes(), func(next app.Server, tx dialect.Driver) error {
		in := coreHolder{s.over(next, tx), next.Holder()}

		return in.keepsTheirs(ctx, req, "erasing them", func(next app.Server) error {
			v, err := next.Holder().Get(ctx, app.HolderGetRequest_builder{
				Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
				Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
			}.Build())
			if status.Code(err) == codes.NotFound {
				// Nobody this caller sees, which the erase answers.
				out, err = next.Holder().Erase(ctx, req)

				return err
			}
			if err != nil {
				return err
			}
			if out, err = next.Holder().Erase(ctx, req); err != nil || !out.GetErased() {
				return err
			}

			return erasedWithTheir(ctx, next, v.GetTenant().GetId(), who)
		})
	})

	return out, err
}

// erasedWithTheir erases every role and every group of the app `who` is, of
// its tenant and still there: what [coreHolder.Erase] ends with them. Through
// `next`, the server under this layer, so each is the erase a role's or a
// group's is, on the trail and on the stream; and asked nothing of what they
// gave, which the person's erase already was.
func erasedWithTheir(ctx context.Context, next app.Server, tenant []byte, who pdid.Id) error {
	var roles [][]byte
	for after := ""; ; {
		page, err := next.Role().List(ctx, app.RoleListRequest_builder{
			Filters: []*app.RoleFilter{app.RoleFilter_builder{Tenant: app.TenantRef_builder{Id: tenant}.Build()}.Build()},
			Size:    100,
			After:   after,
		}.Build())
		if err != nil {
			return err
		}
		for _, r := range page.GetItems() {
			if bytes.Equal(r.GetApp().GetId(), who.Bytes()) {
				roles = append(roles, r.GetId())
			}
		}
		if after = page.GetNext(); after == "" {
			break
		}
	}

	var groups [][]byte
	for after := ""; ; {
		page, err := next.Group().List(ctx, app.GroupListRequest_builder{
			Filters: []*app.GroupFilter{app.GroupFilter_builder{Tenant: app.TenantRef_builder{Id: tenant}.Build()}.Build()},
			Size:    100,
			After:   after,
		}.Build())
		if err != nil {
			return err
		}
		for _, g := range page.GetItems() {
			if bytes.Equal(g.GetApp().GetId(), who.Bytes()) {
				groups = append(groups, g.GetId())
			}
		}
		if after = page.GetNext(); after == "" {
			break
		}
	}

	for _, id := range roles {
		if _, err := next.Role().Erase(ctx, app.RoleRef_builder{Id: id}.Build()); err != nil {
			return err
		}
	}
	for _, id := range groups {
		if _, err := next.Group().Erase(ctx, app.GroupRef_builder{Id: id}.Build()); err != nil {
			return err
		}
	}

	return nil
}

// erasesAcrossTheTenant is [Core.acrossTheTenant] for erasing a person, which
// passes for the caller's own row.
func (s coreHolder) erasesAcrossTheTenant(ctx context.Context, ref *app.HolderRef) error {
	if f, ok := frame.From(ctx); ok && !s.deployment(ctx) {
		who, err := s.holderOf(ctx, ref)
		if status.Code(err) == codes.NotFound {
			// Nobody this caller can see, which the erase answers as it
			// always did.
			return nil
		}
		if err != nil {
			return err
		}
		if who == f.Actor {
			return nil
		}
	}

	return s.acrossTheTenant(ctx, app.HolderService_Erase_FullMethodName)
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
