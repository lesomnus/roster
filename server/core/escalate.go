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
