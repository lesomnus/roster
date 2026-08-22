package core

import (
	"context"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Nobody may hand out what they do not hold.
//
// # The hole this closes
//
// Being allowed to call `Binding.Add` was, until this, being allowed
// **everything**:
//
//	Alice may call Binding.Add and nothing else.
//	Alice writes a Role holding Holder.Erase, and binds it to herself.
//	Alice may now erase anybody.
//
// Two RPCs and one round trip, from a permission an administrator would grant
// without hesitating -- "Alice manages who is in what". The permission system
// was a formality that anybody inside it could step over.
//
// # The rule
//
// Kubernetes calls it escalation prevention, and it is one sentence: **what you
// grant must be a subset of what you hold.** It applies to writing a `Role`
// as well as to binding one, because a role nobody may bind is only a delayed
// version of the same move.
//
// # Held through a binding, and not through a team
//
// What counts is what a caller holds **wide** -- through a `Binding`, which is
// the tenant or a site. A role they hold in one team does not let them write a
// tenant-wide binding of it, because that would be widening a scope rather than
// passing on a permission.
//
// Conservative on purpose. The failure it produces is somebody being told they
// cannot grant something they arguably could, which is a conversation. The
// other direction is silent.
//
// # And through a team, which the gate cannot narrow
//
// The paragraph above is about what a caller may **hand out**. What they may
// hand out *with* is the other half, and it was missing: `TeamMembership.Add`
// names a role, and `policy.of` unions the methods of a role somebody holds in
// a team into the set the gate answers from -- deliberately, because the gate
// is outermost and never sees which team a call is about. So attaching a role
// **is** granting its methods, and the same three lines that guard
// `Binding.Add` guard it now.
//
// The scope it is granted at is the team's **site**, which is where a team
// sits; a team with no site answers the tenant. Both are scopes this rule
// already compares, which is why the check fits without inventing a third one.
//
// # What counts as held, and why the direction matters
//
// A binding reaches somebody by naming them or by naming a group they are in,
// and both count -- [Granted] walks the same rows the gate walks.
//
// Missing one is not symmetric. In [Core.mayGrant] it reads what the *caller*
// holds, so a path not walked only refuses a grant they could have made: the
// conversation above. In [Core.mayReach] it reads what the *target* holds and
// allows the write when that is nothing, so a path not walked is an
// administrator who reads as holding nothing and can be reset by anybody. That
// was true of a group binding until it was written down here.
//
// # What is not covered, and why it is enough
//
// `Patch` and `Apply` are how a role could grow methods after it was written,
// and both are closed at the transport by `grpcx.GeneralWrite` -- `Closed` in
// the chain and in `batch.Guard`. A deployment that opens them opens this with
// them, which is worth knowing and is not a hole this can close from here.

// Granted is every pattern somebody holds through a binding, and therefore may
// pass on.
//
// Patterns rather than methods, so "everything" is a value in this list rather
// than a second return beside it. It briefly was one, while the widest grant
// was a boolean on the row; `frame.Covers` made the boolean unnecessary and
// says four useful things between one method and all of them besides.
type Granted func(ctx context.Context, who pdid.Id) ([]Grant, error)

// Joining is what somebody takes on by being put into a group.
//
// A group is a subject of a binding exactly as a person is -- that is what a
// group is for, and `cmd/policy.go` counts a binding that names one as held by
// everybody in it. So putting somebody into a group hands them every binding
// that names it, and the person doing the putting is handing out something.
//
// A separate answer from [Granted] rather than the same one asked about a
// group, because the two questions are asked of different things: `Granted`
// takes a holder and this takes a group, and a binding names one or the other.
// One function taking either would be a function whose caller has to say which
// kind of identifier it just handed over, which is the thing an identifier
// already says and a signature should not have to repeat.
type Joining func(ctx context.Context, group pdid.Id) ([]Grant, error)

// Grant is a set of patterns and where they are held.
//
// `Site` is `pdid.Nil` for a binding made across the tenant, and otherwise the
// site it was made in. Keeping the two together is the whole of what lets a
// site administrator delegate inside their own site without being able to
// delegate outside it -- flattened into one list, the two are the same strings
// and the wider one wins.
type Grant struct {
	Methods []string
	Site    pdid.Id
}

// mayGrant refuses a caller handing out a method they do not hold.
//
// A request with no frame is the deployment's own work through an unwalled
// server: `init`, the key command, a migration. There is nobody to refuse, and
// that door is a line of wiring a reader can find rather than a privilege
// anybody holds.
// `at` is the scope being written to: `pdid.Nil` for the whole tenant, and
// otherwise the site. What somebody holds tenant-wide they may hand out
// anywhere; what they hold in a site they may hand out **in that site**.
//
// That asymmetry is the rule rather than a special case. Narrowing a permission
// is free and widening one is what needs permission, so a grant made in Seoul
// covers a grant being made in Seoul and covers nothing wider.
func (s Core) mayGrant(ctx context.Context, field string, methods []string, at pdid.Id) error {
	f, ok := frame.From(ctx)
	if !ok {
		return nil
	}
	if len(methods) == 0 {
		return nil
	}
	if s.rules.Granted == nil {
		return status.Error(codes.PermissionDenied,
			"this server cannot say what you hold, so it will not let you grant anything")
	}

	held, err := s.rules.Granted(ctx, f.Actor)
	if err != nil {
		return err
	}

	// What of theirs reaches the scope being written to. A tenant-wide grant
	// reaches everywhere; a grant made in a site reaches that site alone.
	var reaches []string
	for _, g := range held {
		if g.Site == pdid.Nil || g.Site == at {
			reaches = append(reaches, g.Methods...)
		}
	}

	for _, m := range methods {
		// One of theirs has to cover it **on its own**. Asking whether the
		// union covers it would let somebody holding every service of a package
		// hand out the package -- true today and wrong the moment a service is
		// added, which is the widening this exists to refuse. See
		// `frame.Covers`.
		if !slices.ContainsFunc(reaches, func(v string) bool { return frame.Covers(v, m) }) {
			return status.Errorf(codes.PermissionDenied,
				"%s: you do not hold %s here, so you may not grant it", field, m)
		}
	}

	return nil
}

// mayJoin refuses putting somebody into a group that holds more than the
// caller does.
//
// # It is the same act as binding, one service along
//
// `Binding.Add` asks [Core.mayGrant] because writing a binding hands out its
// role's methods. A binding to a **group** is handed out to everybody in that
// group -- which is what a group is -- so the membership is the other half of
// the same write, and it was asked nothing.
//
// What that cost is the shape `escalate.go` opens with, for the third time:
//
//	Alice may call GroupMembership.Add and nothing else.
//	Alice puts herself in the group the deployment binds its admin role to.
//	Alice may now erase anybody.
//
// Two RPCs, from "Alice manages who is in what group" -- which is a permission
// an administrator grants without hesitating, and is the same sentence that
// made `Binding.Add` and `TeamMembership.Add` dangerous.
//
// # Every binding, each at its own scope
//
// A group may be bound more than once, and the bindings need not agree about
// where: one across the tenant, one in a site. Each is checked at the scope it
// was made in, which is what keeps a site administrator able to add somebody to
// a group bound inside their own site and unable to add them to one bound
// across the tenant.
//
// # And removing somebody is not this
//
// Taking a permission away is a denial of service rather than an escalation,
// which is where D26 left `Disable` and for the same reason: somebody who can
// remove an administrator from a group cannot become them.
func (s Core) mayJoin(ctx context.Context, ref *app.GroupRef) error {
	if _, ok := frame.From(ctx); !ok {
		// The deployment's own work through an unwalled server, as everywhere
		// else in this file: `init` puts the first operator in a group before
		// there is anybody to refuse.
		return nil
	}
	if ref == nil {
		// A membership of no group hands out nothing. The generated Add
		// refuses it for its own reasons.
		return nil
	}
	if s.rules.Joining == nil {
		return status.Error(codes.PermissionDenied,
			"this server cannot say what a group holds, so it will not put anybody into one")
	}

	k, err := s.groupOf(ctx, ref)
	if err != nil {
		return err
	}

	vs, err := s.rules.Joining(ctx, k)
	if err != nil {
		return err
	}

	for _, v := range vs {
		if err := s.mayGrant(ctx, "group", v.Methods, v.Site); err != nil {
			return err
		}
	}

	return nil
}

// groupOf is the identifier a `GroupRef` names, read when it named something
// else -- the same shape as [Core.teamOf], and read through `Next()` so that a
// caller who cannot see the group cannot join it either.
func (s Core) groupOf(ctx context.Context, ref *app.GroupRef) (pdid.Id, error) {
	if b := ref.GetId(); len(b) > 0 {
		return pdid.From(b)
	}

	v, err := s.Next().Group().Get(ctx, app.GroupGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetId())
}

// bindableIn refuses a binding that would put a role somewhere it does not
// belong.
//
// # The rule the schema states and nothing enforced
//
// `Role.site` is where a role may be **bound**, and `role.proto` says what it
// is for: *a role in a site may only be bound in that site -- Kubernetes' rule,
// and it is the whole of what keeps somebody who administers one site from
// writing a rule that lands outside it.* It was written down and never
// checked.
//
// What that cost, found by writing it: somebody bound to a Seoul role, in
// Seoul, could bind that same role to themselves with **no site**. The second
// axis then answered "every site" for them and they held the tenant. Two RPCs,
// no method they did not already hold, and nothing refused or logged.
//
// # Why it is not the tenant check
//
// `tenantsAgree` already runs over the same four references and passes: every
// row here belongs to one tenant, which is exactly the case this is about. The
// tenant is the wall and the site is the axis inside it, and agreeing about one
// says nothing about the other.
//
// # A role with no site
//
// Bindable anywhere in its tenant -- it is this schema's `ClusterRole`, and
// writing one is the tenant operator's to do. That asymmetry is the rule, not a
// gap in it: narrowing is free and widening is what needs permission.
func (s Core) bindableIn(ctx context.Context, role *app.RoleRef, at *app.SiteRef) error {
	if role == nil {
		return nil
	}

	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    role,
		Select: app.RoleSelect_builder{Site: app.SiteSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return err
	}

	where := v.GetSite().GetId()
	if len(where) == 0 {
		return nil
	}

	if at == nil {
		return status.Error(codes.PermissionDenied,
			"site: this role belongs to a site, so it may only be bound in that site -- and this binding names none, which is the whole tenant")
	}

	to, err := s.siteOf(ctx, at)
	if err != nil {
		return err
	}

	k, err := pdid.From(where)
	if err != nil {
		return err
	}
	if k != to {
		return status.Error(codes.PermissionDenied,
			"site: this role belongs to another site, and a role is bound only where it belongs")
	}

	return nil
}

// siteOf is which site a reference names, read through this stack.
func (s Core) siteOf(ctx context.Context, ref *app.SiteRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Site().Get(ctx, app.SiteGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetId())
}

// siteOfRole is where a role belongs, read off the row.
//
// Separate from [Core.siteOf] because a patch names the role and not the site:
// `Role.site` is immutable, so the request has no say in it, and asking the
// request would be asking the caller which rules to hold them to.
func (s Core) siteOfRole(ctx context.Context, ref *app.RoleRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    ref,
		Select: app.RoleSelect_builder{Site: app.SiteSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	if b := v.GetSite().GetId(); len(b) > 0 {
		return pdid.From(b)
	}

	return pdid.Nil, nil
}

// siteOfTeam is the site a team is in, which is the scope a role attached
// there is granted at.
//
// A team is inside a site and a site is inside the tenant, so a role attached
// in a team is a grant made in that site: [Core.mayGrant] then counts the
// caller's tenant-wide grants and the ones made in that same site, and nothing
// from a site next door. A team with no site is the tenant's own, and answers
// [pdid.Nil] -- which `mayGrant` reads as the widest scope, so only a
// tenant-wide grant of the caller's covers it.
func (s Core) siteOfTeam(ctx context.Context, ref *app.TeamRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Team().Get(ctx, app.TeamGetRequest_builder{
		Ref:    ref,
		Select: app.TeamSelect_builder{Site: app.SiteSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	if b := v.GetSite().GetId(); len(b) > 0 {
		return pdid.From(b)
	}

	return pdid.Nil, nil
}

// methodsOf is what a role allows, read through this stack so that a caller who
// cannot see the role cannot bind it either.
//
// What comes back are patterns, and the widest role in a deployment is one of
// them rather than an empty column beside a flag. That is the whole gain: a
// caller checking `methods` cannot find nothing to refuse and hand out
// everything, because "everything" is in the list it is checking.
func (s Core) methodsOf(ctx context.Context, ref *app.RoleRef) ([]string, error) {
	if ref == nil {
		return nil, nil
	}

	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return nil, err
	}

	return v.GetMethods(), nil
}

// mayReach refuses somebody writing a credential for a person who holds more
// than they do.
//
// # Why it is here and not obvious
//
// Resetting a password is a way to **become** somebody. So an operator who may
// reset anybody in their tenant effectively holds every permission in it --
// two operations, and it is exactly the shape [Core.mayGrant] exists to close,
// arriving through a door nobody had put a lock on because the door did not
// exist yet.
//
// It went in **before** the surface did, which is the order PLAN.md's list
// insisted on and the only pair in that list where the order is a correctness
// question rather than a convenience.
//
// # The rule, and the one it is not
//
// **You may only write the credential of somebody whose permissions are a
// subset of yours.** Not *whose permissions you may grant* -- the same
// comparison, in the other direction: [Core.mayGrant] asks whether the caller
// covers what they are handing out, and this asks whether they cover what the
// person they are becoming already holds.
//
// It is conservative on purpose, for `mayGrant`'s stated reason: the failure it
// produces is somebody being told they may not, which is a conversation, and
// the other direction is silent.
//
// # The alternative, named because it is defensible
//
// Accept it, and say plainly that a tenant operator is a tenant administrator.
// That is honest and it is what most deployments would find true anyway -- and
// it makes "operator" a smaller word than the permission it carries, which is
// the thing that gets forgotten when somebody hands the role out.
//
// # What is not covered
//
// Suspending somebody (D26) is a denial of service rather than an escalation,
// and is not here. Somebody who may `Disable` an administrator cannot become
// them; they can only stop them. That is a real gap and it is a different one.
func (s Core) mayReach(ctx context.Context, field string, target pdid.Id) error {
	f, ok := frame.From(ctx)
	if !ok {
		// The deployment's own work through an unwalled server -- `init`, a
		// migration, the sandbox. There is nobody to refuse, and that door is a
		// line of wiring a reader can find rather than a privilege anybody
		// holds. Same reading as `mayGrant`.
		return nil
	}
	if target == f.Actor {
		// Changing your own credential is not becoming somebody else. Without
		// this, nobody could change their own password unless they held every
		// permission they held, which is true and is a strange way to write it
		// -- and is false the moment the union is computed in a different
		// order.
		return nil
	}
	if s.rules.Granted == nil {
		return status.Error(codes.PermissionDenied,
			"this server cannot say what anybody holds, so it will not let you write their credential")
	}

	theirs, err := s.rules.Granted(ctx, target)
	if err != nil {
		return err
	}
	if len(theirs) == 0 {
		// Somebody with no binding holds nothing, so there is nothing to
		// escalate to. This is the common case and it is worth not paying for
		// the second read.
		return nil
	}

	mine, err := s.rules.Granted(ctx, f.Actor)
	if err != nil {
		return err
	}

	for _, g := range theirs {
		for _, m := range g.Methods {
			if !holdsAt(mine, g.Site, m) {
				return status.Errorf(codes.PermissionDenied,
					"%s: they hold %s and you do not, so you may not write their credential", field, m)
			}
		}
	}

	return nil
}

// holdsAt is whether one of `mine` covers `m` where `at` is held.
//
// A grant made across the tenant reaches everywhere and one made in a site
// reaches that site, which is `mayGrant`'s asymmetry unchanged -- and one of
// them has to cover it **on its own**, for `mayGrant`'s reason: asking whether
// the union covers it would let somebody holding every service of a package
// stand in for the package, which is true today and wrong the moment a service
// is added.
func holdsAt(mine []Grant, at pdid.Id, m string) bool {
	for _, h := range mine {
		if h.Site != pdid.Nil && h.Site != at {
			continue
		}
		if slices.ContainsFunc(h.Methods, func(v string) bool { return frame.Covers(v, m) }) {
			return true
		}
	}

	return false
}

// Reaching is [Core.mayReach] as a function, for the services that are not
// layers.
//
// `VouchService` is written by hand and is not part of `app.Server`, so no
// layer wraps it -- and it is the service that writes credentials. Rather than
// a second implementation of the rule, it is handed this one.
//
// The generated `CredentialService` is not covered and does not need to be: it
// is unregistered and in `closed`, so nothing on the wire or in a batch reaches
// it. What does reach it is this process, through `Ungated`, where there is no
// frame and the rule reads that as the deployment's own work.
func Reaching(rules Rules) func(ctx context.Context, target pdid.Id) error {
	s := Core{rules: rules}

	return func(ctx context.Context, target pdid.Id) error {
		return s.mayReach(ctx, "who", target)
	}
}
