package core

import (
	"context"
	"slices"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
	"github.com/lesomnus/z"

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

// Add puts somebody in a site, which is who a grant made in that site reaches
// (`cmd.People`) -- so it is the tenant's to say, across the whole of it
// ([Core.acrossTheTenant]). Held in the site alone it would be the site
// saying who is in its own reach: somebody bound in Seoul puts anybody in
// Seoul, and is then their helpdesk.
func (s coreSiteMembership) Add(ctx context.Context, req *app.SiteMembershipAddRequest) (*app.SiteMembership, error) {
	if err := s.acrossTheTenant(ctx, app.SiteMembershipService_Add_FullMethodName); err != nil {
		return nil, err
	}

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

// Erase takes somebody out of a site, and is the tenant's to say as putting
// them in is: out of a site is out of the reach of whoever is bound there,
// which somebody bound there could otherwise arrange for themselves.
func (s coreSiteMembership) Erase(ctx context.Context, req *app.SiteMembershipRef) (*app.SiteMembershipEraseResponse, error) {
	if err := s.acrossTheTenant(ctx, app.SiteMembershipService_Erase_FullMethodName); err != nil {
		return nil, err
	}

	return s.SiteMembershipServiceServer.Erase(ctx, req)
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
	ms, of, err := s.methodsOf(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	at, err := s.siteOfTeam(ctx, req.GetTeam())
	if err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "role", ms, at, of, pdid.Nil); err != nil {
		return nil, err
	}

	return s.TeamMembershipServiceServer.Add(ctx, req)
}

// Patch is `Add`'s two questions asked again, and it has to be.
//
// A membership may name no role -- that is how somebody is put in a team
// without being given anything, and `TestAMembershipWithNoRoleIsStillAMembership`
// says so. `Patch` is what turns one of those into a membership that does name
// one, which is the same grant `Add` is refused for, arriving one verb later:
//
//	Alice may call TeamMembership.Add and Patch, and nothing else.
//	Alice adds herself to a team, naming no role. Allowed, and rightly.
//	Alice patches that membership to name the tenant's admin role.
//
// `role` is the only field this request has besides the version, so there is
// nothing else here to guard -- and `role_null` clears it, which takes a
// permission away and is not this rule's business (D26, the same place
// `Disable` sits).
//
// The team is read off the row rather than taken from the request, because the
// request cannot say it: the edge is immutable, so what a patch is about is
// whatever the row already names.
func (s coreTeamMembership) Patch(ctx context.Context, req *app.TeamMembershipPatchRequest) (*app.TeamMembership, error) {
	if req.GetRole() == nil {
		// Nothing being handed out. A clear, a version bump, or a request that
		// changes nothing at all.
		return s.TeamMembershipServiceServer.Patch(ctx, req)
	}

	v, err := s.Next().TeamMembership().Get(ctx, app.TeamMembershipGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.TeamMembershipSelect_builder{
			Team: app.TeamSelect_builder{Site: app.SiteSelect_builder{}.Build()}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	team := app.TeamRef_builder{Id: v.GetTeam().GetId()}.Build()
	if err := s.mayChangeTeam(ctx, app.TeamMembershipService_Patch_FullMethodName, team); err != nil {
		return nil, err
	}

	ms, of, err := s.methodsOf(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}

	at := pdid.Nil
	if b := v.GetTeam().GetSite().GetId(); len(b) > 0 {
		at, err = pdid.From(b)
		if err != nil {
			return nil, err
		}
	}
	if err := s.mayGrant(ctx, "role", ms, at, of, pdid.Nil); err != nil {
		return nil, err
	}

	return s.TeamMembershipServiceServer.Patch(ctx, req)
}

// Erase asks the same question the write does, about the team the row names.
//
// # Why the row not being there is not an error
//
// Asking needs the row: which team a membership is of is not in the reference,
// so it is read first -- and a read of what is gone answers NotFound. Passing
// that on put this layer at odds with the RPC it stands in front of. The
// generated `Erase` answers `{erased: false}` for a row that was already gone
// or was never there, and spend in `server/vouch/step.go` states that as a
// rule rather than as a detail: `keys.Undelegate` erases what may already be
// erased, and so does
// anybody cancelling something twice.
//
// The shape it was found in is two operators removing one person from one team.
// The winner is told what happened; the loser was told the row does not exist,
// for a call whose whole request was the state they both ended up in.
//
// Answered before `mayChangeTeam` rather than after, because there is nothing
// left to ask: the permission question is about a team, and a row that is not
// there names none. Nor does answering early tell an outsider anything -- the
// read goes through `Next()`, so a caller the wall hides the row from gets
// NotFound here and `{erased: false}` from the generated `Erase` below, which
// is the same answer they are getting now.
//
// [coreIdentity.Erase] is these three lines for the same reason, and the
// comment there is the long version.
func (s coreTeamMembership) Erase(ctx context.Context, req *app.TeamMembershipRef) (*app.TeamMembershipEraseResponse, error) {
	v, err := s.Next().TeamMembership().Get(ctx, app.TeamMembershipGetRequest_builder{
		Ref:    req,
		Select: app.TeamMembershipSelect_builder{Team: app.TeamSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return app.TeamMembershipEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	err = s.mayChangeTeam(ctx, app.TeamMembershipService_Erase_FullMethodName,
		app.TeamRef_builder{Id: v.GetTeam().GetId()}.Build())
	if err != nil {
		return nil, err
	}

	// And what the role held there gave them, which `Add` asked `mayGrant`
	// about at the team's site (`takeaway.go`): read from the row, and nothing
	// when the role or the team was erased. A team's role is never held
	// across the tenant, so nothing about who administers it changes here.
	gs, err := s.given(ctx, v.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAll(ctx, "ref", gs); err != nil {
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
	// A team of no site is the tenant's, and is made by whoever holds the
	// write across it. Somebody bound in a site made one, and was then of a
	// team they could not see (#274).
	if req.GetSite() == nil {
		if err := s.acrossTheTenant(ctx, app.TeamService_Add_FullMethodName); err != nil {
			return nil, err
		}
	}

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

	var out *app.Team
	if req.GetSite() == nil && of != pdid.Nil {
		// A team of no site, named once in its tenant ([Unsited]): looked
		// for and written in one transaction, after a write on the tenant's
		// row that two callers making the same team both make first
		// ([Core.only]).
		err = s.only(ctx, of.Bytes(), func(next app.Server, tx dialect.Driver) error {
			if err := s.unsited(ctx, tx, of, req.GetAlias()); err != nil {
				return err
			}

			var err error
			out, err = next.Team().Add(ctx, req)

			return err
		})
		if err != nil {
			return nil, err
		}

		return out, nil
	}

	// In a site that is still one when the team is written, and not one
	// being erased at the same moment ([Core.inSite]). Its name is unique
	// there by the table's own index.
	err = s.inSite(ctx, req.GetSite(), func(next app.Server) error {
		var err error
		out, err = next.Team().Add(ctx, req)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// Unsited is whether a tenant already has a team of this name in no site.
//
// # Why the table cannot say it
//
// A team's name is unique within its site (`team.proto`), and the index that
// holds it is over the name and the site. SQL reads two nulls as different, so
// it binds nothing where the site is null: two teams `board` of no site were
// both made in one tenant, and a team of no site is named by nothing but its
// identifier as it is (#279). A team of no site is the tenant's, so its name
// is held within the tenant, among the teams of no site -- here, where the
// write is made.
//
// On the driver it is handed, as [Administered] is: asked inside the
// transaction the team is written in. Nil is a stack that cannot say, and a
// team is then made as it always was.
type Unsited func(ctx context.Context, drv dialect.Driver, tenant pdid.Id, alias string) (bool, error)

// unsited refuses a team of no site whose name a team of no site already has
// in this tenant, in the words a name taken in a site is refused in.
//
// The name is compared as it is kept: folded, as the write folds it. One that
// was not given is made up by the server, and one that cannot be a name is
// the write's to refuse.
func (s Core) unsited(ctx context.Context, tx dialect.Driver, tenant pdid.Id, alias string) error {
	if s.rules.Unsited == nil || alias == "" {
		return nil
	}
	name, err := slug.ParseAlias(alias)
	if err != nil {
		return nil
	}

	taken, err := s.rules.Unsited(ctx, tx, tenant, name)
	if err != nil {
		return err
	}
	if taken {
		return status.Error(codes.AlreadyExists, "Team already exists")
	}

	return nil
}

// Erase takes from each of a team's people the role they hold in it, and is
// held to what taking one of them out is: [coreTeamMembership.Erase], once
// for each.
//
// A team erased hands out nothing (`cmd/policy.go`, `liveTeam`), so this is
// every one of those erases at once -- and it was asked nothing, where the
// one was asked whether the caller may change the team and holds what the
// role there gave. `Group.Erase` is the same question of a group's bindings
// (`takeaway.go`). Unlike a group's, a team's role is never held across the
// tenant, so nothing about who administers it changes here.
//
// A team that is not there, or that the wall hides, is answered as the
// generated `Erase` answers it.
func (s coreTeam) Erase(ctx context.Context, req *app.TeamRef) (*app.TeamEraseResponse, error) {
	got, err := s.TeamServiceServer.Get(ctx, app.TeamGetRequest_builder{
		Ref:    req,
		Select: app.TeamSelect_builder{}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return s.TeamServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}

	team := app.TeamRef_builder{Id: got.GetId()}.Build()
	if err := s.mayChangeTeam(ctx, app.TeamService_Erase_FullMethodName, team); err != nil {
		return nil, err
	}

	after := ""
	for {
		ms, err := s.Next().TeamMembership().List(ctx, app.TeamMembershipListRequest_builder{
			Filters: []*app.TeamMembershipFilter{app.TeamMembershipFilter_builder{Team: team}.Build()},
			After:   after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, m := range ms.GetItems() {
			if len(m.GetRole().GetId()) == 0 {
				// In the team and holding nothing in it.
				continue
			}

			// What each place gives is read as taking it out alone reads it
			// (`takeaway.go`), one at a time: a team is a handful of people
			// and their roles fewer. It was the role itself that was read,
			// as the caller reads one, and a role erased since -- or one of
			// the whole tenant, to somebody who sees one site -- was a
			// `NotFound` about a role, for the erase of a team (#279).
			gs, err := s.given(ctx, m.GetId())
			if err != nil {
				return nil, err
			}
			if err := s.mayTakeAll(ctx, "ref", gs); err != nil {
				return nil, err
			}
		}

		after = ms.GetNext()
		if after == "" {
			break
		}
	}

	return s.TeamServiceServer.Erase(ctx, req)
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
	role, err := s.tenantOfRole(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("group", who, what, role); err != nil {
		return nil, err
	}

	// And whether this caller may change **this** group -- through a binding,
	// or as one of its managers -- which the gate could not ask. See
	// `group.go`.
	if err := s.mayChangeGroup(ctx, app.GroupMembershipService_Add_FullMethodName, req.GetGroup()); err != nil {
		return nil, err
	}
	// And let through by a grant in a site for a group that is not the app's,
	// whoever is put in is that grant's to see ([Core.reachedInASite]).
	of, err := s.appOfGroup(ctx, req.GetGroup())
	if err != nil {
		return nil, err
	}
	if err := s.reachedInASite(ctx, app.GroupMembershipService_Add_FullMethodName, of,
		func(ctx context.Context) error { _, err := s.tenantOfHolder(ctx, req.GetHolder()); return err },
	); err != nil {
		return nil, err
	}

	// And what being in that group hands them, which is every binding written
	// to it -- the other half of the write `Binding.Add` already asks about.
	// See `escalate.go`, "it is the same act as binding, one service along".
	if err := s.mayJoin(ctx, req.GetGroup()); err != nil {
		return nil, err
	}

	// And, naming a role, the manager it appoints: a role that reaches no
	// further than this group, held to what the appointer holds **through a
	// binding** -- so a manager puts people in and does not make another.
	if err := s.mayAppoint(ctx, req.GetRole(), req.GetGroup()); err != nil {
		return nil, err
	}

	return s.GroupMembershipServiceServer.Add(ctx, req)
}

// mayAppoint is attaching a role to somebody's membership of a group: one
// [Core.mayHoldInGroup] lets be held there, and one the caller could bind.
//
// Read through [Core.mayGrant] at the whole tenant, so what counts is a
// binding's -- the scope a role held in a group is handed out at, since the
// gate holds it everywhere -- and a role held in a group counts for nothing.
// `TeamMembership.Add` asks the same of a team's role.
//
// For the app the group is of (`Group.app`): a manager's role reaches that
// group alone, so in a group of an app's it is handed out for that app, and
// whoever holds roster's verbs for the app appoints one there.
func (s coreGroupMembership) mayAppoint(ctx context.Context, ref *app.RoleRef, group *app.GroupRef) error {
	if ref == nil {
		return nil
	}
	if err := s.mayHoldInGroup(ctx, ref); err != nil {
		return err
	}
	ms, of, err := s.methodsOf(ctx, ref)
	if err != nil {
		return err
	}
	to, err := s.appOfGroup(ctx, group)
	if err != nil {
		return err
	}

	return s.mayGrant(ctx, "role", ms, pdid.Nil, of, to)
}

// appOfGroup is the app a group is of (`Group.app`), [pdid.Nil] for the
// tenant's own, read through this stack.
func (s Core) appOfGroup(ctx context.Context, ref *app.GroupRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Group().Get(ctx, app.GroupGetRequest_builder{
		Ref:    ref,
		Select: app.GroupSelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return idOr(v.GetApp().GetId())
}

// Patch is `Add`'s questions about a role, asked when one is attached later --
// `TeamMembership.Patch`'s reason: a plain membership turned into a manager's
// is the appointment `Add` is refused, one verb on.
//
// The group is read off the row, since the edge is immutable and the request
// cannot say it. `role_null` takes a manager's role away, which hands out
// nothing.
func (s coreGroupMembership) Patch(ctx context.Context, req *app.GroupMembershipPatchRequest) (*app.GroupMembership, error) {
	if req.GetRole() == nil {
		return s.GroupMembershipServiceServer.Patch(ctx, req)
	}

	v, err := s.Next().GroupMembership().Get(ctx, app.GroupMembershipGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.GroupMembershipSelect_builder{Group: app.GroupSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	group := app.GroupRef_builder{Id: v.GetGroup().GetId()}.Build()
	if err := s.mayChangeGroup(ctx, app.GroupMembershipService_Patch_FullMethodName, group); err != nil {
		return nil, err
	}
	if err := s.mayAppoint(ctx, req.GetRole(), group); err != nil {
		return nil, err
	}

	return s.GroupMembershipServiceServer.Patch(ctx, req)
}

// Erase asks [Core.mayChangeGroup] about the group the row names, which a
// manager passes for their own group and nobody else's. A row already gone is
// `{erased: false}`, for [coreTeamMembership.Erase]'s reason.
func (s coreGroupMembership) Erase(ctx context.Context, req *app.GroupMembershipRef) (*app.GroupMembershipEraseResponse, error) {
	v, err := s.Next().GroupMembership().Get(ctx, app.GroupMembershipGetRequest_builder{
		Ref: req,
		Select: app.GroupMembershipSelect_builder{
			Group: app.GroupSelect_builder{Alias: z.Ptr(true), Tenant: app.TenantSelect_builder{}.Build()}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return app.GroupMembershipEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	err = s.mayChangeGroup(ctx, app.GroupMembershipService_Erase_FullMethodName,
		app.GroupRef_builder{Id: v.GetGroup().GetId()}.Build())
	if err != nil {
		return nil, err
	}

	// And what being in it gave them -- the group's bindings, each at its
	// scope, and a manager's role -- which nobody takes away who could not
	// have given it (`takeaway.go`); and the tenant's administration, when
	// the group carries it. Both are read from the rows, and are nothing for a
	// group that was erased.
	group, err := pdid.From(v.GetGroup().GetId())
	if err != nil {
		return nil, err
	}
	gs, err := s.grantsOfGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAll(ctx, "ref", gs); err != nil {
		return nil, err
	}
	theirs, err := s.given(ctx, v.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAll(ctx, "ref", theirs); err != nil {
		return nil, err
	}
	if !grantsAdministration(gs) {
		// Nothing a tenant is administered through: a group bound to less,
		// and one that is not there any more, whose places give nothing.
		return s.GroupMembershipServiceServer.Erase(ctx, req)
	}

	// A group that is there, bound across the tenant, which whoever passed
	// for it above reads with the row.
	tenant, err := pdid.From(v.GetGroup().GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	var out *app.GroupMembershipEraseResponse
	err = s.keepsAdministered(ctx, tenant, true, "taking them out of group "+v.GetGroup().GetAlias(), func(next app.Server) error {
		var err error
		out, err = next.GroupMembership().Erase(ctx, req)

		return err
	})

	return out, err
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

	// The app it names the methods of is a holder of its own tenant, read as
	// the caller reads one -- and that is all that is asked of it (`Role.app`).
	whose, err := s.tenantOfHolder(ctx, req.GetApp())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("app", of, whose); err != nil {
		return nil, err
	}
	by, err := s.appOf(ctx, req.GetApp())
	if err != nil {
		return nil, err
	}
	if err := s.keptBy(ctx, by, req.GetAlias()); err != nil {
		return nil, err
	}
	if err := mayBeOwned(req.GetAlias(), req.GetSite() != nil, req.GetMethods()); err != nil {
		return nil, err
	}

	// And one of roster's names what roster serves ([Serves]): one naming
	// another app's methods grants nothing anywhere.
	if err := s.namesRosters(by, req.GetMethods()); err != nil {
		return nil, err
	}

	// A role nobody may bind is a delayed version of binding it, so writing one
	// is held to the same rule as granting one. See `escalate.go`.
	//
	// The scope is the role's **own** site, because that is where it may be
	// bound: a role of no site is bindable across the tenant, so writing one is
	// a tenant-wide grant however narrow the writer is. And its own app: what
	// it names is held there, and only what the writer holds there covers it.
	at, err := s.siteOf(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}

	// Made by somebody who holds this for an app alone, it is that app's
	// role: refused without that `app`, and not quietly given it
	// ([Core.mayWriteOfApp]).
	if err := s.mayWriteOfApp(ctx, "app", app.RoleService_Add_FullMethodName, "role", by, at); err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), at, by, pdid.Nil); err != nil {
		return nil, err
	}

	// In a site that is still one when the role is written ([Core.inSite]).
	var out *app.Role
	err = s.inSite(ctx, req.GetSite(), func(next app.Server) error {
		var err error
		out, err = next.Role().Add(ctx, req)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
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
	if err := s.declaredRole(ctx, req.GetRef()); err != nil {
		return nil, err
	}
	// Read off the row rather than taken from the request: `Role.site` and
	// `Role.app` are immutable, so the request has no say in them, and asking
	// the request would be asking the caller which rules to hold them to.
	where, of, err := s.siteOfRole(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	// `Role.Widen` is this, and is held to it here: one of roster's names
	// what roster serves ([Serves]).
	if err := s.namesRosters(of, req.GetMethods()); err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), where, of, pdid.Nil); err != nil {
		return nil, err
	}
	if len(req.GetMethods()) == 0 {
		// The methods are left as they are: a list has no presence in a
		// patch request, so an empty one says nothing.
		return s.RoleServiceServer.Patch(ctx, req)
	}

	// And what a new list leaves out is taken away, and held to what taking
	// away is held to (`takeaway.go`): `Role.Narrow` is this, on the wire.
	got, err := s.RoleServiceServer.Get(ctx, app.RoleGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.RoleSelect_builder{Alias: z.Ptr(true), Methods: z.Ptr(true), Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range got.GetMethods() {
		if !slices.Contains(req.GetMethods(), m) {
			out = append(out, m)
		}
	}
	if err := s.mayTakeAway(ctx, "methods", out, where, of, pdid.Nil); err != nil {
		return nil, err
	}
	tenant, err := pdid.From(got.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	var v *app.Role
	could := where == pdid.Nil && of == pdid.Nil && administers(got.GetMethods()) && !administers(req.GetMethods())
	err = s.keepsAdministered(ctx, tenant, could, "taking "+EveryMethod+" out of "+got.GetAlias(), func(next app.Server) error {
		var err error
		v, err = next.Role().Patch(ctx, req)

		return err
	})

	return v, err
}

type coreGroup struct {
	Core
	app.GroupServiceServer
}

func (s Core) Group() app.GroupServiceServer { return coreGroup{s, s.Next().Group()} }

func (s coreGroup) Add(ctx context.Context, req *app.GroupAddRequest) (*app.Group, error) {
	// The app whose group it is, [pdid.Nil] for the tenant's own: the
	// holder, read as the caller reads one.
	by, err := s.appOf(ctx, req.GetApp())
	if err != nil {
		return nil, err
	}

	// A group of no site is the tenant's, as a team of none is
	// ([coreTeam.Add]): its maker manages it, and somebody bound in a site
	// would manage a group of the whole tenant. A group of an app's of no site
	// is that app's whole, which a grant for the app reaches.
	if req.GetSite() == nil {
		held, err := s.heldForApp(ctx, app.GroupService_Add_FullMethodName, by)
		if err != nil {
			return nil, err
		}
		if !held {
			if err := s.acrossTheTenant(ctx, app.GroupService_Add_FullMethodName); err != nil {
				return nil, err
			}
		}
	}

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

	// The app whose group it is, a holder of its own tenant read as the
	// caller reads one -- and that is all that is asked of it (`Group.app`),
	// as of a role's.
	whose, err := s.tenantOfHolder(ctx, req.GetApp())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("app", of, whose); err != nil {
		return nil, err
	}

	// Made by somebody who holds this for an app alone, it is that app's
	// group: refused without that `app`, and not quietly given it.
	site, err := s.siteOf(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteOfApp(ctx, "app", app.GroupService_Add_FullMethodName, "group", by, site); err != nil {
		return nil, err
	}

	// And whoever makes a group manages it, when they are somebody of its
	// tenant: the row, and their place in it carrying the manager's role, in
	// one write or none. See `group.go`.
	who, ok, err := s.maker(ctx, of)
	if err != nil {
		return nil, err
	}

	if ok {
		return s.made(ctx, req, of, who)
	}

	// In a site that is still one when the group is written, and not one
	// being erased at the same moment ([Core.inSite]). A group its maker
	// manages is written in a transaction of its own, which begins with the
	// same write ([coreGroup.made]).
	var out *app.Group
	err = s.inSite(ctx, req.GetSite(), func(next app.Server) error {
		var err error
		out, err = next.Group().Add(ctx, req)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
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
	case req.GetApp() != nil && req.GetSite() != nil:
		// Both narrow what the role reaches, the one to an app's rows and the
		// other to a site's, and a binding says one of them or neither.
		return nil, pderr.Invalidf("app",
			"a binding is held for an app or in a site, and this says both; one for an app reaches that app's rows wherever they are")
	}

	who, err := s.tenantOfHolder(ctx, holder)
	if err != nil {
		return nil, err
	}
	whose, err := s.tenantOfHolder(ctx, req.GetApp())
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
	if err := tenantsAgree("role", who, what, role, where, whose); err != nil {
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

	ms, of, err := s.methodsOf(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}

	// And a group is bound the roles of its own app: see [Core.boundInItsApp].
	if group != nil {
		if err := s.boundInItsApp(ctx, req.GetRole(), of, group); err != nil {
			return nil, err
		}
	}

	// And for an app: roster's verbs over that app's rows (`forapp.go`).
	to, err := s.bindsFor(ctx, req.GetApp(), of, ms)
	if err != nil {
		return nil, err
	}

	at, err := s.siteOf(ctx, req.GetSite())
	if err != nil {
		return nil, err
	}

	// Written by somebody who holds this for an app alone, it is a binding of
	// that app's: of a role of its own, or of roster's for it.
	row := of
	if row == pdid.Nil {
		row = to
	}
	// Let through by a grant in a site, the role and whoever it is bound to are
	// that grant's to see as well.
	if err := s.mayWriteOfApp(ctx, "role", app.BindingService_Add_FullMethodName, "binding", row, at,
		func(ctx context.Context) error { _, err := s.tenantOfRole(ctx, req.GetRole()); return err },
		func(ctx context.Context) error { _, err := s.tenantOfHolder(ctx, holder); return err },
		func(ctx context.Context) error { _, err := s.tenantOfGroup(ctx, group); return err },
	); err != nil {
		return nil, err
	}
	if err := s.mayGrant(ctx, "role", ms, at, of, to); err != nil {
		return nil, err
	}

	// And once ([Bound]). Which role and to whom, as identifiers: the request
	// may have named either by a name.
	which, err := s.roleOf(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	var subject pdid.Id
	if holder != nil {
		subject, err = s.holderOf(ctx, holder)
	} else {
		subject, err = s.groupOf(ctx, group)
	}
	if err != nil {
		return nil, err
	}
	if which == pdid.Nil {
		// A binding of no role, which the write refuses.
		return s.BindingServiceServer.Add(ctx, req)
	}

	// Looked for and written in one transaction, after a write two callers
	// making the same binding both make first ([Core.only]). In a site it is
	// the site's row, which is also what keeps the binding out of a site
	// being erased at the same moment ([Core.inSite]): a binding that
	// outlived its site is the row the site's erase is refused for. Across
	// the tenant it is the role's.
	on := at
	if on == pdid.Nil {
		on = which
	}

	var out *app.Binding
	err = s.only(ctx, on.Bytes(), func(next app.Server, tx dialect.Driver) error {
		if s.rules.Bound != nil {
			made, err := s.rules.Bound(ctx, tx, which, subject, at, to)
			if err != nil {
				return err
			}
			if made {
				return status.Error(codes.AlreadyExists, "Binding already exists")
			}
		}

		var err error
		out, err = next.Binding().Add(ctx, req)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// Bound is whether a role is already bound to somebody, or to a group, in a
// place: `to` is the holder or the group, told apart by its identifier,
// `site` is [pdid.Nil] for a binding across the tenant, and `of` the app it is
// held for (`Binding.app`), [pdid.Nil] for the tenant's whole. One for Foo
// and one for Bar are two grants.
//
// # A grant is made once
//
// A binding declared no uniqueness, and an index could not have held it: it
// names a holder **or** a group and a site **or** none, and SQL reads two
// nulls as different. So the same binding added twice was two rows, and after
// one was erased the person was still answered -- somebody who takes a grant
// away, and sees it gone, has not taken it away (#279). One role to one
// holder or group in one place is one row, and the second is refused where
// the write is made.
//
// Among the bindings that are there: one erased is one that may be made
// again. And asked of the rows, not through a server, for [Sited]'s reason:
// whether a grant was made is the same whoever is making it again.
//
// On the driver it is handed, as [Administered] is: asked inside the
// transaction the binding is written in. Nil is a stack that cannot say, and
// a binding is then made as it always was.
type Bound func(ctx context.Context, drv dialect.Driver, role, to, site, of pdid.Id) (bool, error)

// The roles roster's own front doors are provisioned with, each found by its
// alias in every tenant they answer in (`cli/login.go`, `cli/provision.go`,
// `cli/scim.go`).
const (
	LoginRole   = "login-app"
	AccountRole = "account"
	ScimRole    = "scim"
)

// Kept is every alias roster finds a role of its own by: the tenant's
// [Owns], its [Manages], the role it is administered by ([Everyverb]), what
// an app's administrators hold for it ([AppAdministrator]), and the front
// doors' roles.
//
// A role's alias is one within its tenant, whichever app's it is. So a role
// of an app's that took one of these stood where roster looks for its own:
// the group-manager role an app had written stopped the tenant making its
// first group, and one called `self-service` would have been asked what
// everybody does to their own account.
var Kept = []string{Owns, Manages, Everyverb, AppAdministrator, LoginRole, AccountRole, ScimRole}

// keptBy refuses a role of an app's -- `of` is not [pdid.Nil] -- an alias
// roster keeps ([Kept]). One that is no alias is the write's to refuse.
func (s Core) keptBy(ctx context.Context, of pdid.Id, alias string) error {
	if of == pdid.Nil || alias == "" {
		return nil
	}
	name, err := slug.ParseAlias(alias)
	if err != nil || !slices.Contains(Kept, name) {
		return nil
	}

	return pderr.Invalidf("alias", "%q is roster's own: roster finds a role of its own by it, and this is a role of %s's", name, s.appNamed(ctx, of))
}

// boundInItsApp refuses a role bound to a group of another app's (`Group.app`):
// a role of an app's to the tenant's own group or to another app's, and a role
// of roster's to a group of an app's. `of` is the role's app, [pdid.Nil] for
// roster's.
//
// # An app's users are the people in its groups
//
// And the people bound its roles, so that taking one of them out is ending a
// membership or a binding: there is no exclusion and no denial. Bound to a
// group somebody else keeps -- the directory's `employees` -- a person held
// the app's role for as long as they were in that group, and the app could
// not take them out. And a group of an app's is that app's list of its users,
// which a role of roster's or of another app's bound to it would make
// something else: a grant at roster to whoever the app enrols.
//
// A role of roster's bound to a group of the tenant's is as it always was.
func (s Core) boundInItsApp(ctx context.Context, role *app.RoleRef, of pdid.Id, group *app.GroupRef) error {
	v, err := s.Next().Group().Get(ctx, app.GroupGetRequest_builder{
		Ref: group,
		Select: app.GroupSelect_builder{
			Alias:  z.Ptr(true),
			Tenant: app.TenantSelect_builder{Alias: z.Ptr(true)}.Build(),
			App:    app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	theirs, err := idOr(v.GetApp().GetId())
	if err != nil {
		return err
	}
	if theirs == of {
		return nil
	}

	g := "@" + v.GetTenant().GetAlias() + "/" + v.GetAlias()
	if of != pdid.Nil {
		whose := "the tenant's own"
		if theirs != pdid.Nil {
			whose = s.appNamed(ctx, theirs) + "'s"
		}
		named := s.appNamed(ctx, of)

		return status.Errorf(codes.PermissionDenied,
			"group: a role of %s's is bound to a person or to a group of %s's, and %s is %s", named, named, g, whose)
	}

	named := s.appNamed(ctx, theirs)

	return status.Errorf(codes.PermissionDenied,
		"group: %s is a group of %s's, which is bound a role of %s's and no other, and %s is roster's", g, named, named, s.roleNamed(ctx, role))
}

// roleNamed is how a refusal names a role: `@tenant/alias`, read through this
// stack, and its reference as it was sent where the caller cannot read it.
func (s Core) roleNamed(ctx context.Context, ref *app.RoleRef) string {
	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: ref,
		Select: app.RoleSelect_builder{
			Alias:  z.Ptr(true),
			Tenant: app.TenantSelect_builder{Alias: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	if err != nil || v.GetAlias() == "" || v.GetTenant().GetAlias() == "" {
		return "the role"
	}

	return "@" + v.GetTenant().GetAlias() + "/" + v.GetAlias()
}

// appOf is the identifier a role's `app` names, [pdid.Nil] for none -- a
// role of roster's own. [Core.holderOf], so one the caller cannot see is
// nobody's.
func (s Core) appOf(ctx context.Context, ref *app.HolderRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	return s.holderOf(ctx, ref)
}

// roleOf is the identifier a `RoleRef` names, read when it named something
// else -- [Core.groupOf]'s shape, through `Next()`.
func (s Core) roleOf(ctx context.Context, ref *app.RoleRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}
	if b := ref.GetId(); len(b) > 0 {
		return sentId(b)
	}

	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    ref,
		Select: app.RoleSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetId())
}
