package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Who may change a group's members.
//
// The rule a team has (`team.go`), for the same reason and in the same two
// halves: the gate answers "may you ever call `GroupMembership.Add`" and
// cannot see which group, so it lets a group's manager through, and this layer
// refuses every group but theirs. Two ways to pass:
//
//   - the method held through a binding, which reaches every group of the
//     scope it was made in -- a tenant's administrator;
//   - the method held through a role in **this** group's own membership -- its
//     managers.
//
// What it does not replace is `mayJoin`. A group bound to roster's roles still
// asks whoever puts somebody into it to hold what the group hands out. A
// manager is a member and holds it already; somebody who edits memberships
// across the tenant without holding it is refused there, as before.
func (s Core) mayChangeGroup(ctx context.Context, method string, ref *app.GroupRef) error {
	f, ok := frame.From(ctx)
	if !ok {
		return nil
	}
	if ref == nil {
		return nil
	}
	if s.rules.Holds == nil {
		return groupDenied(method)
	}

	k, err := s.groupOf(ctx, ref)
	if err != nil {
		return err
	}

	ok, err = s.rules.Holds(ctx, f.Actor, method, k)
	if err != nil {
		return err
	}
	if !ok {
		return groupDenied(method)
	}

	return nil
}

func groupDenied(method string) error {
	return status.Errorf(codes.PermissionDenied,
		"%s: you hold nothing for that group", method)
}

// managing is what a role held in a group may name: the writes that change
// that group -- who is in it, and what it is called -- and nothing else.
//
// Because the gate unions a role held in a group into what somebody may ever
// call, and only these are narrowed back to the one group by
// [Core.mayChangeGroup]. A read named here would read every group's members,
// and anything else would be held across the tenant -- a binding by another
// name, written by whoever may appoint a manager.
var managing = []string{
	app.GroupMembershipService_Add_FullMethodName,
	app.GroupMembershipService_Erase_FullMethodName,
	app.GroupService_Update_FullMethodName,
}

// Manages is the alias of the role a group's maker holds in it: a tenant's
// one role naming exactly [managing], made the first time a group needs it.
//
// The name a tenant would give its own, on purpose. One that is already there
// and is what a role held in a group may be -- of no site, naming nothing
// [managing] does not -- is the tenant's word for what a manager does, and is
// used as it stands: a tenant whose managers do not rename groups keeps them
// so. One that is anything else is refused where a group is made, saying so.
const Manages = "group-manager"

// maker is who is making a group, when that is somebody of its tenant -- and
// so somebody who can be in it.
//
// Nobody is when there is no frame, which is the deployment's own work
// through an unwalled server, or when the caller is not a holder of this
// tenant: a deployment's key, an operator at the admin port. Those make a
// group nobody manages, as every group was before this, and an administrator
// appoints its first manager.
func (s Core) maker(ctx context.Context, tenant pdid.Id) (pdid.Id, bool, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() || s.rules.Named == nil {
		return pdid.Nil, false, nil
	}

	of, err := s.tenantOfHolder(ctx, app.HolderRef_builder{Id: f.Actor.Bytes()}.Build())
	switch status.Code(err) {
	case codes.OK:
	case codes.NotFound, codes.InvalidArgument, codes.PermissionDenied:
		return pdid.Nil, false, nil
	default:
		return pdid.Nil, false, err
	}

	return f.Actor, of == tenant, nil
}

// made is a group and its maker's place in it, carrying the manager's role, in
// one transaction.
//
// # Whoever makes a group manages it
//
// A manager was somebody an administrator appointed
// ([coreGroupMembership.mayAppoint]): attaching the role is held to holding
// its methods through a binding, across the tenant. So a caller who may make
// a group and holds no membership write that wide made a group it could put
// nobody into -- a group nobody manages is not a smaller group, it is an
// unfinished one -- and the only way to finish it was to hand that caller
// every group of the tenant. The maker's place is the rest of the same act,
// which is what `Tenant.Add` writing its administrator is.
//
// # Why this is not handing anything out
//
// The three writes go **below** this layer, past `mayChangeGroup`, `mayJoin`
// and `mayAppoint`, which is the whole of the rule and why it is safe to
// write here and nowhere else. A group made this moment is bound to nothing
// and shown to no client, so being in it gives nothing; and a role held in it
// reaches this group and no other. What it changes is later, and is somebody
// else's decision: whoever binds a role to this group hands it to a manager
// who decides who else holds it, as they hand it to everybody already in it.
//
// # And so the maker is a member
//
// A manager outside the group they manage would protect nothing
// (`entity.md`), and the role is carried by a membership. An administrator
// who makes a group is in it, and takes themselves out when that is not
// wanted.
func (s coreGroup) made(ctx context.Context, req *app.GroupAddRequest, tenant, who pdid.Id) (*app.Group, error) {
	var out *app.Group
	run := func(below app.Server, drv dialect.Driver) error {
		g, err := below.Group().Add(ctx, req)
		if err != nil {
			return err
		}
		role, err := s.managersRole(ctx, below, drv, tenant)
		if err != nil {
			return err
		}
		if _, err := below.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Group:  app.GroupRef_builder{Id: g.GetId()}.Build(),
			Role:   role,
		}.Build()); err != nil {
			return err
		}

		out = g

		return nil
	}

	if s.drv == nil {
		// On somebody else's transaction, or on none: [Core.tx] is which. It
		// is not this layer's to begin again, so a tenant's first groups made
		// at once are said to be, and asked again.
		err := run(s.Next(), s.tx)
		if errors.Is(err, errManagesTaken) {
			return nil, status.Errorf(codes.Aborted,
				"the role %q of this tenant was being made by another group's maker; ask again", Manages)
		}
		if err != nil {
			return nil, err
		}

		return out, nil
	}

	once := func() error {
		drv, tx, err := dialect.BeginTx(ctx, s.drv)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		next, err := enttx.Rebind(s.Next(), drv)
		if err != nil {
			return err
		}
		if err := run(next, drv); err != nil {
			return err
		}

		return tx.Commit()
	}

	// Again, where the role was made by somebody else between this looking
	// for it and making it: a tenant's first groups, made at once
	// ([errManagesTaken]). The transaction that lost is over -- a database
	// that refuses a write refuses the rest of what it was part of -- so it
	// is the whole of it that is begun again, and it finds the role there.
	// Once would do; the second is for a role erased and made in between.
	err := once()
	for range 2 {
		if !errors.Is(err, errManagesTaken) {
			break
		}
		err = once()
	}
	if errors.Is(err, errManagesTaken) {
		return nil, status.Errorf(codes.Aborted,
			"the role %q of this tenant is being made and erased by others; ask again", Manages)
	}
	if err != nil {
		return nil, err
	}

	return out, nil
}

// errManagesTaken is the tenant's [Manages] having been made by somebody else
// between [Core.managersRole] not finding it and making it: two of a tenant's
// first groups, made at once, which is how a directory's first cycle and
// Microsoft's validator make them. Whoever made it made the one this was
// about to, so it is no refusal -- [coreGroup.made] begins again and finds
// it.
var errManagesTaken = errors.New("the manager's role was made by another")

// managersRole is the tenant's [Manages], made when it is not there.
//
// Found by [Named], which reads past what the maker can see: a maker who sees
// one site would not find a role of the whole tenant, and would make a
// second. Made below this layer, like the rest of [coreGroup.made]: a role
// naming what a manager may call is one its maker need not hold, since it is
// held in a group and reaches no further.
func (s Core) managersRole(ctx context.Context, below app.Server, drv dialect.Driver, tenant pdid.Id) (*app.RoleRef, error) {
	v, err := s.rules.Named(ctx, drv, tenant, Manages)
	if err != nil {
		return nil, err
	}
	if v == nil {
		made, err := below.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
			Alias:   Manages,
			Desc:    "What a group's manager may do to their own group: put people in, take them out, and say what it is called.",
			Methods: managing,
		}.Build())
		if status.Code(err) == codes.AlreadyExists {
			return nil, errManagesTaken
		}
		if err != nil {
			return nil, err
		}

		return app.RoleRef_builder{Id: made.GetId()}.Build(), nil
	}

	// The tenant's own, used as it stands where it is what a role held in a
	// group may be.
	if v.Site != pdid.Nil {
		return nil, status.Errorf(codes.FailedPrecondition,
			"the role %q of this tenant belongs to a site, and a group's maker manages it by that role; a role held in a group belongs to none", Manages)
	}
	for _, m := range v.Methods {
		if !slices.Contains(managing, m) {
			return nil, status.Errorf(codes.FailedPrecondition,
				"the role %q of this tenant names %s, and a group's maker manages it by that role; it may name only what changes a group (%s)",
				Manages, m, strings.Join(managing, ", "))
		}
	}

	return app.RoleRef_builder{Id: v.Id.Bytes()}.Build(), nil
}

// coreGroup's `Update` is `Patch` with everything but what a group says about
// itself held back -- `group_svc.ext.proto` says why -- and held as its members
// are: through a binding, or by one of its managers ([Core.mayChangeGroup]).
func (s coreGroup) Update(ctx context.Context, req *app.GroupUpdateRequest) (*app.Group, error) {
	if err := s.mayChangeGroup(ctx, app.GroupService_Update_FullMethodName, req.GetRef()); err != nil {
		return nil, err
	}

	patch := app.GroupPatchRequest_builder{
		Ref:         req.GetRef(),
		DateUpdated: req.GetDateUpdated(),
	}
	if req.HasName() {
		patch.Name = z.Ptr(req.GetName())
	}
	if req.HasDesc() {
		patch.Desc = z.Ptr(req.GetDesc())
	}

	return s.GroupServiceServer.Patch(ctx, patch.Build())
}

// mayHoldInGroup refuses a role that would reach past the group it is held in:
// one bound to a site, or naming anything [managing] does not.
func (s Core) mayHoldInGroup(ctx context.Context, ref *app.RoleRef) error {
	if ref == nil {
		return nil
	}

	at, err := s.siteOfRole(ctx, ref)
	if err != nil {
		return err
	}
	if at != pdid.Nil {
		return pderr.Invalidf("role",
			"a role held in a group belongs to no site; it names what changes the group's members, and that is not in a site")
	}

	ms, err := s.methodsOf(ctx, ref)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if !slices.Contains(managing, m) {
			return pderr.Invalidf("role",
				"%s: a role held in a group names only what changes its members (%s); the gate would hold anything else across the tenant",
				m, strings.Join(managing, ", "))
		}
	}

	return nil
}

// coreGroupExposure is the layer over the generated `GroupExposureService`. The
// client a group is shown to is a name its issuer gave it, and is held to the
// shape one has; which tenant the row is in is the group's, through the wall.
type coreGroupExposure struct {
	Core
	app.GroupExposureServiceServer
}

func (s Core) GroupExposure() app.GroupExposureServiceServer {
	return coreGroupExposure{s, s.Next().GroupExposure()}
}

// maxClient is the longest client a group may be shown to. An issuer's
// identifiers are a UUID or a short name, and anything a good deal longer is
// not one of them.
const maxClient = 255

func (s coreGroupExposure) Add(ctx context.Context, req *app.GroupExposureAddRequest) (*app.GroupExposure, error) {
	if err := isClient(req.GetClient()); err != nil {
		return nil, err
	}

	return s.GroupExposureServiceServer.Add(ctx, req)
}

// isClient refuses what no issuer would call a client. Compared as it is
// written, since a `client_id` is the issuer's string and nothing here may
// decide two of them are the same.
func isClient(v string) error {
	switch {
	case v == "":
		return pderr.Invalidf("client", "the client_id of the relying party whose tokens carry the group")
	case len(v) > maxClient:
		return pderr.Invalidf("client", "at most %d bytes, and a client_id is a good deal shorter", maxClient)
	case strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return pderr.Invalidf("client", "%q: a client_id has no space or control character in it", v)
	}

	return nil
}
