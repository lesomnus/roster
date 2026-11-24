package cmd

import (
	"context"
	"slices"

	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rbac"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/apikey"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/predicate"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/pd"
)

// Apps is the wall for what is an app's, read by somebody who holds roster's
// verbs for that app and not across the tenant (`Binding.app`).
//
// # What it is for
//
// A role of roster's bound for one app counts at the gate for the verbs that
// reach an app's rows ([core.ForAnApp]), and the gate never sees which row a
// call is about: it lets the call through, and something has to hold it to
// the app's rows. A write is held there by the layer, which has the request
// (`server/core`, `forapp.go`). A read is a query, and what narrows a query is
// this: a role whose `app` it is, a binding of such a role or one for that app,
// a group whose `app` it is and a place in such a group. Somebody who holds
// `RoleService/List` for Foo alone was answered every role of the tenant.
//
// So a list answers that app's rows only, and a read by reference of another
// row answers as a row the caller does not see does, `NotFound`.
//
// # Asked of the read the call is
//
// Only `Get` and `List` of the four services ([readsForAnApp]), where the call
// says which it is ([Calling]). A write reads the rows it is about through the
// same stack -- the role a binding names, the group a member is put in -- and
// is held to the app's rows where the request is, by the layer; narrowed here
// too, Foo's administrator could not read `app-administrator`, which is
// roster's, to bind it for Foo. A call that says no method is narrowed by
// every app anything its caller holds is for, which is what [Sets] does with
// sites.
//
// # And it fails closed
//
// What is held across the tenant, by a binding of no app and no site, is not
// narrowed: the rule is about a grant for an app. What is held in a site is
// narrowed by the site as before ([Sets] answers the sites a grant of no app
// reaches), and a row of the app's is seen through this whatever site it is
// in; a caller who holds the read both for an app and in a site sees the
// rows of that site that are the app's.
func Apps(db *ent.Client, sources ...rbac.Source) bare.Scope {
	return apps{p: policy{db: db, sources: sources}}
}

type apps struct {
	bare.Unscoped

	p policy
}

var _ bare.Scope = apps{}

// readsForAnApp is the reads of [core.ForAnApp]: what this narrows.
var readsForAnApp = []string{
	app.RoleService_Get_FullMethodName,
	app.RoleService_List_FullMethodName,
	app.BindingService_Get_FullMethodName,
	app.BindingService_List_FullMethodName,
	app.GroupService_Get_FullMethodName,
	app.GroupService_List_FullMethodName,
	app.GroupMembershipService_Get_FullMethodName,
	app.GroupMembershipService_List_FullMethodName,
	app.ApiKeyService_Get_FullMethodName,
	app.ApiKeyService_List_FullMethodName,
}

// held is the apps a caller holds the read the call is for, and the sites a
// grant of no app reaches it in; `narrow` is false where nothing is to be
// narrowed here.
//
// A call made for somebody by a device or an agent that is somebody too
// (`frame.Caller`) is narrowed by both, as [frame.NarrowSet] narrows it: by
// the apps both hold it for, and the sites both reach.
func (x apps) held(ctx context.Context) (of, sites []uuid.UUID, narrow bool, err error) {
	f, ok := frame.From(ctx)
	if !ok || f.AsAccount() {
		// Nobody, which the wall refuses; or in no tenant.
		return nil, nil, false, nil
	}

	m, said := calling(ctx)
	if said && !slices.Contains(readsForAnApp, m) {
		return nil, nil, false, nil
	}

	of, sites, narrow, err = x.heldBy(ctx, f.Actor, m, said)
	if err != nil || f.Caller == nil {
		return of, sites, narrow, err
	}

	theirs, there, by, err := x.heldBy(ctx, f.Caller.Actor, m, said)
	switch {
	case err != nil:
		return nil, nil, false, err
	case !by:
		return of, sites, narrow, nil
	case !narrow:
		return theirs, there, true, nil
	}

	both := func(a, b []uuid.UUID) []uuid.UUID {
		return slices.DeleteFunc(slices.Clone(a), func(v uuid.UUID) bool { return !slices.Contains(b, v) })
	}

	return both(of, theirs), both(sites, there), true, nil
}

// heldBy is [apps.held] for one of the two a call may be made by.
func (x apps) heldBy(ctx context.Context, who pdid.Id, m string, said bool) (of, sites []uuid.UUID, narrow bool, err error) {
	if who.Domain() == pd.ApiKeyDomain {
		// A key, whose methods are its own and say no app.
		return nil, nil, false, nil
	}

	h, err := x.p.of(ctx, who.Uuid(), roster)
	if err != nil {
		return nil, nil, false, err
	}
	for _, r := range h.reaches {
		if said && !slices.ContainsFunc(r.methods, func(p string) bool { return frame.Covers(p, m) }) {
			continue
		}
		if r.of != uuid.Nil() {
			if !slices.Contains(of, r.of) {
				of = append(of, r.of)
			}

			continue
		}
		if r.all {
			// Held across the tenant, or by what reaches it whole.
			return nil, nil, false, nil
		}
		sites = append(sites, r.site)
	}

	return of, sites, len(of) > 0, nil
}

func (x apps) RoleScope(ctx context.Context) (predicate.Role, error) {
	of, sites, narrow, err := x.held(ctx)
	if !narrow || err != nil {
		return nil, err
	}

	ps := []predicate.Role{entrole.AppIdIn(of...)}
	if len(sites) > 0 {
		ps = append(ps, entrole.SiteIdIn(sites...))
	}

	return entrole.Or(ps...), nil
}

func (x apps) BindingScope(ctx context.Context) (predicate.Binding, error) {
	of, sites, narrow, err := x.held(ctx)
	if !narrow || err != nil {
		return nil, err
	}

	ps := []predicate.Binding{binding.AppIdIn(of...), binding.HasRoleWith(entrole.AppIdIn(of...))}
	if len(sites) > 0 {
		ps = append(ps, binding.SiteIdIn(sites...))
	}

	return binding.Or(ps...), nil
}

func (x apps) GroupScope(ctx context.Context) (predicate.Group, error) {
	of, sites, narrow, err := x.held(ctx)
	if !narrow || err != nil {
		return nil, err
	}

	return groupsOf(of, sites), nil
}

func (x apps) GroupMembershipScope(ctx context.Context) (predicate.GroupMembership, error) {
	of, sites, narrow, err := x.held(ctx)
	if !narrow || err != nil {
		return nil, err
	}

	return groupmembership.HasGroupWith(groupsOf(of, sites)), nil
}

// ApiKeyScope is a key of one of the apps the caller holds the read for, or
// one of their own.
//
// # Not as the other four
//
// What is held across the tenant of a key's read is not everybody's keys:
// that is `HolderService/SignsIn` (`server/core`, `mayReadOf`), and the read
// itself, held however widely, answers somebody their own. So the apps the
// read is held for are read whatever else the caller holds of it, and
// nothing is narrowed here only for whoever is told how everybody signs in,
// or holds the read for no app. A site is not asked: a key is in none, and
// [People] narrows it by its holder. The caller's own are theirs as ever.
func (x apps) ApiKeyScope(ctx context.Context) (predicate.ApiKey, error) {
	f, ok := frame.From(ctx)
	if !ok || f.AsAccount() {
		return nil, nil
	}

	m, said := calling(ctx)
	if said && !slices.Contains(readsForAnApp, m) {
		return nil, nil
	}

	of, narrow, err := x.keysHeldBy(ctx, f.Actor, m, said)
	if err != nil || !narrow {
		return nil, err
	}
	if f.Caller != nil {
		// A call made for somebody by a device or an agent is narrowed by
		// both, as [apps.held] narrows it.
		theirs, by, err := x.keysHeldBy(ctx, f.Caller.Actor, m, said)
		if err != nil {
			return nil, err
		}
		if by {
			of = slices.DeleteFunc(of, func(v uuid.UUID) bool { return !slices.Contains(theirs, v) })
		}
	}

	return apikey.Or(apikey.AppIdIn(of...), apikey.HasHolderWith(holder.IdEQ(f.Actor.Uuid()))), nil
}

// keysHeldBy is the apps `who` holds the read `m` of keys for, and `narrow`
// false where they are told how everybody signs in or hold it for no app.
func (x apps) keysHeldBy(ctx context.Context, who pdid.Id, m string, said bool) (of []uuid.UUID, narrow bool, err error) {
	if who.Domain() == pd.ApiKeyDomain {
		return nil, false, nil
	}

	h, err := x.p.of(ctx, who.Uuid(), roster)
	if err != nil {
		return nil, false, err
	}
	for _, r := range h.reaches {
		if r.of == uuid.Nil() {
			if r.all && slices.ContainsFunc(r.methods, func(p string) bool { return frame.Covers(p, app.HolderService_SignsIn_FullMethodName) }) {
				return nil, false, nil
			}

			continue
		}
		if said && !slices.ContainsFunc(r.methods, func(p string) bool { return frame.Covers(p, m) }) {
			continue
		}
		if !slices.Contains(of, r.of) {
			of = append(of, r.of)
		}
	}

	return of, len(of) > 0, nil
}

// groupsOf is a group of one of these apps, or of one of these sites.
func groupsOf(of, sites []uuid.UUID) predicate.Group {
	ps := []predicate.Group{group.AppIdIn(of...)}
	if len(sites) > 0 {
		ps = append(ps, group.SiteIdIn(sites...))
	}

	return group.Or(ps...)
}
