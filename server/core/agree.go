package core

import (
	"context"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

// The writes that name more than one thing, each checked that the things belong
// to one tenant. See `tenant.go` for why the schema cannot say this and why the
// wall cannot either.
//
// Written out one by one rather than derived. An entity added tomorrow is not
// checked until somebody adds a case here, and that is the direction to fail in
// for a rule about **writes**: the alternative is a generic walk that quietly
// stops covering a shape it was not written for.

type coreSiteMembership struct {
	Core
	app.SiteMembershipServiceServer
}

func (s Core) SiteMembership() app.SiteMembershipServiceServer {
	return coreSiteMembership{s, s.Next().SiteMembership()}
}

func (s coreSiteMembership) Add(ctx context.Context, req *app.SiteMembershipAddRequest) (*app.SiteMembership, error) {
	who, err := s.tenantOfHolder(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}
	where, err := s.tenantOfSite(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("site", who, where); err != nil {
		return nil, err
	}

	return s.SiteMembershipServiceServer.Add(ctx, req)
}

type coreTeamMembership struct {
	Core
	app.TeamMembershipServiceServer
}

func (s Core) TeamMembership() app.TeamMembershipServiceServer {
	return coreTeamMembership{s, s.Next().TeamMembership()}
}

func (s coreTeamMembership) Add(ctx context.Context, req *app.TeamMembershipAddRequest) (*app.TeamMembership, error) {
	who, err := s.tenantOfHolder(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}
	where, err := s.tenantOfTeam(ctx, req.GetTeam())
	if err != nil {
		return nil, err
	}
	what, err := s.tenantOfRole(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("team", who, where, what); err != nil {
		return nil, err
	}

	// And whether this caller may change **this** team, which the gate could
	// not ask because it never sees the request. See `team.go`.
	if err := s.mayChangeTeam(ctx, app.TeamMembershipService_Add_FullMethodName, req.GetTeam()); err != nil {
		return nil, err
	}

	// And what attaching that role hands out, which is the same question
	// `Binding.Add` asks and for the same reason: the gate unions the methods
	// of a role somebody holds in a team into what they may ever call, so
	// naming one here **is** granting it. See `escalate.go`, "and through a
	// team, which the gate cannot narrow".
	ms, err := s.methodsOf(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	at, err := s.siteOfTeam(ctx, req.GetTeam())
	if err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "role", ms, at); err != nil {
		return nil, err
	}

	return s.TeamMembershipServiceServer.Add(ctx, req)
}

// Erase asks the same question the write does, about the team the row names.
func (s coreTeamMembership) Erase(ctx context.Context, req *app.TeamMembershipRef) (*app.TeamMembershipEraseResponse, error) {
	v, err := s.Next().TeamMembership().Get(ctx, app.TeamMembershipGetRequest_builder{
		Ref:    req,
		Select: app.TeamMembershipSelect_builder{Team: app.TeamSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	err = s.mayChangeTeam(ctx, app.TeamMembershipService_Erase_FullMethodName,
		app.TeamRef_builder{Id: v.GetTeam().GetId()}.Build())
	if err != nil {
		return nil, err
	}

	return s.TeamMembershipServiceServer.Erase(ctx, req)
}

type coreTeam struct {
	Core
	app.TeamServiceServer
}

func (s Core) Team() app.TeamServiceServer { return coreTeam{s, s.Next().Team()} }

func (s coreTeam) Add(ctx context.Context, req *app.TeamAddRequest) (*app.Team, error) {
	of, err := s.tenantOfRef(ctx, req.GetTenant())
	if err != nil {
		return nil, err
	}
	where, err := s.tenantOfSite(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("site", of, where); err != nil {
		return nil, err
	}

	return s.TeamServiceServer.Add(ctx, req)
}

type coreGroupMembership struct {
	Core
	app.GroupMembershipServiceServer
}

func (s Core) GroupMembership() app.GroupMembershipServiceServer {
	return coreGroupMembership{s, s.Next().GroupMembership()}
}

func (s coreGroupMembership) Add(ctx context.Context, req *app.GroupMembershipAddRequest) (*app.GroupMembership, error) {
	who, err := s.tenantOfHolder(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}
	what, err := s.tenantOfGroup(ctx, req.GetGroup())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("group", who, what); err != nil {
		return nil, err
	}

	return s.GroupMembershipServiceServer.Add(ctx, req)
}

type coreRole struct {
	Core
	app.RoleServiceServer
}

func (s Core) Role() app.RoleServiceServer { return coreRole{s, s.Next().Role()} }

func (s coreRole) Add(ctx context.Context, req *app.RoleAddRequest) (*app.Role, error) {
	of, err := s.tenantOfRef(ctx, req.GetTenant())
	if err != nil {
		return nil, err
	}
	where, err := s.tenantOfSite(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("site", of, where); err != nil {
		return nil, err
	}

	// A role nobody may bind is a delayed version of binding it, so writing one
	// is held to the same rule as granting one. See `escalate.go`.
	//
	// The scope is the role's **own** site, because that is where it may be
	// bound: a role of no site is bindable across the tenant, so writing one is
	// a tenant-wide grant however narrow the writer is.
	at, err := s.siteOf(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), at); err != nil {
		return nil, err
	}

	return s.RoleServiceServer.Add(ctx, req)
}

// Patch is held to the same rule as Add, and it has to be.
//
// A role that can grow methods after it was written is a role whose first
// version says nothing about what it allows: write "reader", get it bound, then
// patch `Holder/Erase` into it. Everybody it was ever granted to gains that,
// at once, without a binding being touched.
//
// It was missing, and what hid it is that nothing grants `RoleService/Patch` by
// default -- deny-by-default meant nobody could reach it, so the hole opened
// only for a deployment that started using the feature. Found by writing it:
// somebody holding just `RoleService/Patch` widened their own role to a method
// they did not hold, and nothing refused.
//
// The whole list is checked rather than the difference. Working that out means
// reading the row, and a caller who may not grant a method they are leaving in
// place should not be writing this row at all.
//
// `every_method` is not patchable into a role for the same reason, and by the
// same call: `mayGrant` refuses anybody who does not already hold everything,
// and holding everything is the only way to hand it out.
func (s coreRole) Patch(ctx context.Context, req *app.RolePatchRequest) (*app.Role, error) {
	// Read off the row rather than taken from the request: `Role.site` is
	// immutable, so the request has no say in it, and asking the request would
	// be asking the caller which rules to hold them to.
	where, err := s.siteOfRole(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), where); err != nil {
		return nil, err
	}

	return s.RoleServiceServer.Patch(ctx, req)
}

type coreGroup struct {
	Core
	app.GroupServiceServer
}

func (s Core) Group() app.GroupServiceServer { return coreGroup{s, s.Next().Group()} }

func (s coreGroup) Add(ctx context.Context, req *app.GroupAddRequest) (*app.Group, error) {
	of, err := s.tenantOfRef(ctx, req.GetTenant())
	if err != nil {
		return nil, err
	}
	where, err := s.tenantOfSite(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("site", of, where); err != nil {
		return nil, err
	}

	return s.GroupServiceServer.Add(ctx, req)
}

type coreBinding struct {
	Core
	app.BindingServiceServer
}

func (s Core) Binding() app.BindingServiceServer { return coreBinding{s, s.Next().Binding()} }

// Add refuses the two ways a binding is wrong, and they are different in kind.
//
// One is the tenant agreement every row above is checked for. The other is that
// a binding grants to a holder **or** a group, and a schema cannot say "exactly
// one of these" -- two nullable edges is what it can say, and both set or
// neither set are rows that mean nothing.
//
// Two nullable edges rather than a subject table, because a group of one is
// ceremony people resent. The cost is this function, which is a fair trade for
// a schema that does not make everybody invent a group to grant one person
// something.
func (s coreBinding) Add(ctx context.Context, req *app.BindingAddRequest) (*app.Binding, error) {
	holder, group := req.GetHolder(), req.GetGroup()
	switch {
	case holder != nil && group != nil:
		return nil, pderr.Invalidf("group",
			"a binding grants to a holder or to a group, and this names both")
	case holder == nil && group == nil:
		return nil, pderr.Invalidf("holder",
			"a binding grants to somebody; this names nobody, and would allow nothing forever")
	}

	who, err := s.tenantOfHolder(ctx, holder)
	if err != nil {
		return nil, err
	}
	what, err := s.tenantOfGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	role, err := s.tenantOfRole(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	where, err := s.tenantOfSite(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("role", who, what, role, where); err != nil {
		return nil, err
	}

	// And what it hands out. Being allowed to write bindings was, until this,
	// being allowed everything: write a role holding anything, bind it to
	// yourself, and the permission system is a formality.
	// Where the role may be bound, which is the schema's rule and was nobody's
	// to enforce until this. See `bindableIn`.
	if err := s.bindableIn(ctx, req.GetRole(), req.GetSite()); err != nil {
		return nil, err
	}

	ms, err := s.methodsOf(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	at, err := s.siteOf(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "role", ms, at); err != nil {
		return nil, err
	}

	return s.BindingServiceServer.Add(ctx, req)
}
