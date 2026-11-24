package core

import (
	"context"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// roster's verbs, held for one app (`Binding.app`).
//
// # What it is
//
// A binding of a role of roster's that names an app: *this role, over what is
// that app's and nothing else*. It reaches that app's roles, the bindings of
// them, its groups and their memberships, and nothing else of the tenant's, as
// a role held in a group is narrowed back to the group. It is what an app's
// administrator holds: they put people into the app's groups and bind them the
// app's roles, and make a second administrator, and do none of it to another
// app or to the tenant's own.
//
// # What is an app's
//
// A role whose `app` it is; a binding of such a role, and a binding of a role
// of roster's that is for that app; a group whose `app` it is; and a place in
// such a group.
//
// # And it fails closed, twice
//
// At the gate, such a grant counts for [ForAnApp] and for no other method,
// whatever its role names by then (`cmd/policy.go`): for any other it is as if
// it were not held. And since the gate never sees which row a call is about,
// each of those verbs is held to the app's rows below it -- a read by the wall
// (`cmd.Apps`), a write here ([Core.mayWriteOfApp], and a group's through
// [Core.mayChangeGroup]) -- unless the caller also holds the method across the
// tenant.

// AppAdministrator is the alias of the role of roster's an app's
// administrators are bound for the app: a tenant's one role naming
// [ForAnApp], made the first time it is needed (`roster app install`), as
// [Manages] is.
//
// The name a tenant would give its own, on purpose. One already there naming
// fewer is the tenant's word for what an app's administrator does, and is used
// as it stands; one naming anything else is refused where it would be bound
// for an app, as every role naming anything else is.
const AppAdministrator = "app-administrator"

// ForAnApp is what a role of roster's bound for an app may name, and all that
// such a binding counts for at the gate: the verbs that reach an app's rows.
//
// Each by its whole name, and a pattern is no such verb: `/roster.RoleService/*`
// covers `Patch` and `Apply` and whatever the service grows, which reach no
// row of an app's alone.
var ForAnApp = []string{
	app.RoleService_Add_FullMethodName,
	app.RoleService_Get_FullMethodName,
	app.RoleService_List_FullMethodName,
	app.RoleService_Widen_FullMethodName,
	app.RoleService_Narrow_FullMethodName,
	app.RoleService_Erase_FullMethodName,

	app.BindingService_Add_FullMethodName,
	app.BindingService_Get_FullMethodName,
	app.BindingService_List_FullMethodName,
	app.BindingService_Erase_FullMethodName,

	app.GroupService_Add_FullMethodName,
	app.GroupService_Get_FullMethodName,
	app.GroupService_List_FullMethodName,
	app.GroupService_Update_FullMethodName,
	app.GroupService_Erase_FullMethodName,

	app.GroupMembershipService_Add_FullMethodName,
	app.GroupMembershipService_Get_FullMethodName,
	app.GroupMembershipService_List_FullMethodName,
	app.GroupMembershipService_Erase_FullMethodName,

	// And what somebody holds there, which this adds up: answered at that
	// app, and refused at roster and at every other (`coreHolder.reachedAt`).
	app.HolderService_Reaches_FullMethodName,
}

// HeldForAnApp is what of `patterns` a grant for an app counts for: each of
// [ForAnApp] one of them covers, by its whole name.
func HeldForAnApp(patterns []string) []string {
	var out []string
	for _, m := range ForAnApp {
		if slices.ContainsFunc(patterns, func(p string) bool { return frame.Covers(p, m) }) {
			out = append(out, m)
		}
	}

	return out
}

// heldFor is where the caller holds `method`, of roster's roles and through a
// binding: for which apps, in which sites, and whether across the tenant.
//
// The deployment's own work, and a key of the deployment's as itself, is the
// tenant's whole. A stack that cannot say refuses.
func (s Core) heldFor(ctx context.Context, method string) (apps, sites []pdid.Id, whole bool, err error) {
	f, ok := frame.From(ctx)
	if !ok || s.deployment(ctx) {
		return nil, nil, true, nil
	}
	if s.rules.Granted == nil {
		return nil, nil, false, status.Error(codes.PermissionDenied,
			"this server cannot say what you hold, so it will not let you write what is an app's")
	}

	gs, err := s.rules.Granted(ctx, f.Actor)
	if err != nil {
		return nil, nil, false, err
	}
	for _, g := range gs {
		if g.App != pdid.Nil || !slices.ContainsFunc(g.Methods, func(p string) bool { return frame.Covers(p, method) }) {
			continue
		}
		switch {
		case g.For != pdid.Nil:
			if !slices.Contains(apps, g.For) {
				apps = append(apps, g.For)
			}
		case g.Site == pdid.Nil:
			whole = true
		default:
			sites = append(sites, g.Site)
		}
	}

	return apps, sites, whole, nil
}

// heldForApp is whether the caller holds `method` for the app `of`, or across
// the tenant: what an app's row asks where the tenant's whole is otherwise
// asked ([Core.acrossTheTenant]) -- a group of an app's of no site, made; and
// `Reaches` asked at the app.
func (s Core) heldForApp(ctx context.Context, method string, of pdid.Id) (bool, error) {
	apps, _, whole, err := s.heldFor(ctx, method)
	if err != nil {
		return false, err
	}

	return whole || (of != pdid.Nil && slices.Contains(apps, of)), nil
}

// mayWriteOfApp refuses a write of a row that is not the app's to somebody who
// holds `method` for an app and not across the tenant: `row` is the app the row
// is of, [pdid.Nil] for roster's or the tenant's own, and `site` where it is.
//
// Somebody who holds the method for no app is not asked: the gate, the wall
// and the rules of the write are what hold them. One who also holds it in the
// site the row is in passes there, as they would have.
func (s Core) mayWriteOfApp(ctx context.Context, field, method, what string, row, site pdid.Id) error {
	apps, sites, whole, err := s.heldFor(ctx, method)
	if err != nil || whole || len(apps) == 0 {
		return err
	}
	if row != pdid.Nil && slices.Contains(apps, row) {
		return nil
	}
	if site != pdid.Nil && slices.Contains(sites, site) {
		return nil
	}

	return status.Errorf(codes.PermissionDenied, "%s: you hold %s for %s, and this %s is not %s",
		field, method, s.appsNamed(ctx, apps), what, s.whoseOf(ctx, apps))
}

// appsNamed is the apps a refusal names, `@acme/foo` or `@acme/foo, @acme/bar`.
func (s Core) appsNamed(ctx context.Context, apps []pdid.Id) string {
	names := make([]string, 0, len(apps))
	for _, v := range apps {
		names = append(names, s.appNamed(ctx, v))
	}

	return strings.Join(names, ", ")
}

// whoseOf is the end of a refusal: `@acme/foo's`, or `any of theirs`.
func (s Core) whoseOf(ctx context.Context, apps []pdid.Id) string {
	if len(apps) == 1 {
		return s.appNamed(ctx, apps[0]) + "'s"
	}

	return "any of theirs"
}

// bindsFor is the app a binding is held for (`Binding.app`), [pdid.Nil] for
// the tenant's whole, and refuses one that may not be: of a role of an app's
// (`of`), which says whose methods it names itself; and of a role of roster's
// naming anything but [ForAnApp], as a manager's role naming anything but a
// manager's writes is refused ([Core.mayHoldInGroup]).
//
// The app is a holder read as the caller reads one: of the tenant, which
// `Binding.Add` asks with the rest, and not erased.
func (s Core) bindsFor(ctx context.Context, ref *app.HolderRef, of pdid.Id, ms []string) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}
	if of != pdid.Nil {
		return pdid.Nil, pderr.Invalidf("app",
			"this is a role of %s's, which says whose methods it names; a binding of it says no app", s.appNamed(ctx, of))
	}
	for _, m := range ms {
		if !slices.Contains(ForAnApp, m) {
			return pdid.Nil, pderr.Invalidf("role",
				"%s: a role of roster's held for an app names only what reaches an app's rows (%s); the gate would hold anything else across the tenant",
				m, strings.Join(ForAnApp, ", "))
		}
	}

	return s.holderOf(ctx, ref)
}
