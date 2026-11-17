package cmd

import (
	"bytes"
	"context"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"uuid"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rbac"

	app "github.com/lesomnus/roster/rstr"

	"github.com/lesomnus/roster/internal/ent"
	entapikey "github.com/lesomnus/roster/internal/ent/apikey"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/host"
	"github.com/lesomnus/roster/internal/ent/predicate"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	entsite "github.com/lesomnus/roster/internal/ent/site"
	entteam "github.com/lesomnus/roster/internal/ent/team"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	enttenant "github.com/lesomnus/roster/internal/ent/tenant"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/me"
	"github.com/lesomnus/roster/server/pd"
)

// Policy is what a caller may do, read out of this deployment's own rows.
//
// # Deny by default
//
// Somebody with no binding may call nothing. That is the only defensible
// default for a store of people: the alternative is that adding the first role
// **takes away** permissions everybody silently had, which is a change nobody
// can review because there is no before-state written down.
//
// It means a fresh deployment answers nothing until `roster init` has bound
// somebody, which is the same shape as a database with no rows in it.
//
// # Where it reads from
//
// ent, directly, rather than through a generated server -- the one other place
// this app does that is nothing. The reason is the same one the resolver has:
// **working out what a caller may do cannot itself require permission.** A
// read through the walled stack would ask this function to answer before it
// can answer.
//
// It is also why this is short. Every predicate here is this file's, so there
// is nothing to keep in step with a rule written elsewhere.
//
// # What it does not do
//
// It does not look at what a call is **about**. `gate.Call` carries the actor,
// their tenant, the actor's own row and the method, and never the request -- so
// "may manage the team they are in" is not here. That is `server/core`, which
// is given the request; see docs/position.md, § 'Authorization'.
func Policy(db *ent.Client, sources ...rbac.Source) gate.Policy {
	return policy{db: db, sources: sources}
}

// PolicyOfKeys is [Policy] for the plane the deployment's keys are rows of:
// the control plane, where a key is asked what its holder holds.
//
// # Why there, and why its holder
//
// A key's list names methods and no plane, and the same services answer at
// both listeners -- `HolderService` is a customer's people at one and the
// deployment's operators at the other. On the data plane a key's list is the
// whole of what it may call, since a key is the deployment's and is not
// somebody a role is written for: that is [policy.May]'s first line, and it
// was its answer at the control listener too. So a key made for an app that
// manages customers -- `Holder.List`, `Holder.Disable` -- listed the operators
// there and suspended one (#274): every key the deployment had minted was, at
// that listener, a key to the deployment.
//
// At the control listener the rows are who runs the deployment and what it
// may be called by, and who may do what to those is a role and a binding
// there, as for any operator. A key is not somebody who holds one; the holder
// it hangs off is. So a key calls there what its **holder** was bound, of what
// its own list names -- and an app's holder, bound nothing, calls nothing.
//
// No field says which plane a key is for. What says it is what its holder was
// given on the plane that is about the deployment, which is what says it of
// everybody else.
func PolicyOfKeys(db *ent.Client, sources ...rbac.Source) gate.Policy {
	return policy{db: db, sources: sources, keysAreHolders: true}
}

type policy struct {
	db      *ent.Client
	sources []rbac.Source

	// keysAreHolders is whether a key is asked what its holder holds, which is
	// so on the plane keys are rows of and nowhere else ([PolicyOfKeys]).
	keysAreHolders bool
}

var _ gate.Policy = policy{}

// May answers whether this caller may call this method at all.
func (p policy) May(ctx context.Context, c gate.Call) error {
	if aboutYourself(c.Action) {
		return nil
	}

	if c.Actor.Domain() == pd.ApiKeyDomain {
		// A key's methods are its own, applied by `auth.Interceptor` from the
		// credential before this runs. Asking again here would mean a key
		// needed a binding as well, and a key is not somebody who can hold one.
		if !p.keysAreHolders {
			return nil
		}

		// But its holder is, and on this plane that is who is asked.
		holder, err := p.db.ApiKey.Query().
			Where(entapikey.IdEQ(c.Actor.Uuid()), entapikey.DateErasedIsNil()).
			QueryHolder().
			OnlyId(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return status.Errorf(codes.PermissionDenied, "%s: this key hangs off nobody here", c.Action)
			}

			return err
		}

		held, err := p.of(ctx, holder)
		if err != nil {
			return err
		}
		if !held.allows(c.Action) {
			return status.Errorf(codes.PermissionDenied,
				"%s: at this listener a key calls what its holder was bound on the control plane, and no role of theirs allows that",
				c.Action)
		}

		return nil
	}

	held, err := p.of(ctx, c.Actor.Uuid())
	if err != nil {
		return err
	}
	if !held.allows(c.Action) {
		// Nothing they were bound. What is left is what everybody of their
		// tenant does to their own account, which nobody is bound ([policy.own]):
		// let through here, and narrowed to their own rows by the wall, since
		// they hold it nowhere ([held.sitesFor], [People]).
		theirs, err := p.own(ctx, c.Tenant.Uuid())
		if err != nil {
			return err
		}
		if !slices.Contains(theirs, c.Action) {
			return status.Errorf(codes.PermissionDenied,
				"%s: no role of yours allows that", c.Action)
		}

		return nil
	}
	if ofTheWholeTenant(c.Action) && !held.allowsEverywhere(c.Action) {
		return status.Errorf(codes.PermissionDenied,
			"%s is about the whole tenant, and you hold it only in part of it -- a site, or a team", c.Action)
	}

	return nil
}

// own is what everybody of a tenant holds over what is their own: the methods
// of the tenant's role named [core.Owns], of the ones such a role may name.
//
// It is in no binding, so it is in nothing [policy.of] reads, and that is on
// purpose three times over. It reaches nowhere, so a call it lets through is
// narrowed to the caller's own rows. It is not what somebody **holds** for
// the rules that compare holdings -- whose account a way in may be written
// into, what may be granted or taken away -- since everybody has it and it
// reaches nobody else. And it is not held across the tenant.
//
// A role of that name in a site, or erased, is no such role; and a method it
// names that is not somebody's own to call is not held through it
// ([core.Owned]).
func (p policy) own(ctx context.Context, tenant uuid.UUID) ([]string, error) {
	if tenant == uuid.Nil() {
		return nil, nil
	}

	r, err := p.db.Role.Query().
		Where(entrole.TenantIdEQ(tenant), entrole.AliasEQ(core.Owns), entrole.SiteIdIsNil(), entrole.DateErasedIsNil()).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return core.Owned(r.Methods), nil
}

// Own is [policy.own] for `server/me`, which answers with it beside what
// somebody holds and apart from it.
func Own(db *ent.Client) me.Own {
	p := policy{db: db}

	return func(ctx context.Context, tenant pdid.Id) ([]string, error) {
		return p.own(ctx, tenant.Uuid())
	}
}

// ofTheWholeTenant is the reads that are no site's, held to a binding across
// the tenant here because no layer of `server/core` is over them: the trail.
//
// A line of the trail is about whatever was written, in whichever site, by
// whoever wrote it, and carries what the write said. Narrowed to nothing by
// the second axis -- it is in no set -- it answered somebody bound in one site
// with every row of every other: a role of another site, and what it allows,
// read by its id (#274). The writes that are the tenant's as a whole ask the
// same in `server/core/tenantwide.go`, where the request is.
func ofTheWholeTenant(method string) bool {
	return frame.Covers("/roster.AuditService/*", method)
}

// aboutYourself is the methods a binding is not required for.
//
// One, and it has to be: `MeService.Get` takes nothing and answers only the
// caller's own facts, including **which roles they hold**. Requiring a role to
// learn that you have none is a deployment where somebody who has just been
// given an account cannot be told what it is for -- and where the page that
// would say so is the one that cannot load.
//
// It reveals nothing a caller does not already have. There is no subject
// argument, so it cannot be pointed at anybody else, and the absence of that
// argument is what makes this safe rather than a judgement about the handler.
//
// A list of methods and not a prefix. A prefix is for a service whose every RPC
// is public, where a second one added tomorrow should be too. Here they are
// named one at a time, and a method added to `MeService` tomorrow needs a
// decision rather than inheriting one.
//
// # The writes, and why they are on the list
//
// `Unlink` and `SignOutEverywhere` write, which is the part worth
// stopping on. They are here for `Get`'s reason and not by extension of it:
// neither takes a subject, so neither can be pointed at anybody else, and what
// each does is something a person may do to their own account by definition.
//
// The alternative is a role, and a role is the wrong shape twice over. It would
// have to reach every identity in the tenant, since `Identity` narrows by
// tenant and there is no permission smaller than that -- so "may remove their
// own way in" would be granted as "may remove anybody's". And requiring one at
// all means somebody who has just been given an account cannot sign themselves
// out of a session they no longer trust, which is the moment they most want to.
//
// What keeps them safe is the same absence that keeps `Get` safe, plus the
// rules in the layer: `server/core` refuses the removal of a last way in, so
// the button cannot lock somebody out of their own account.
//
// # And `Leave`, which a tenant offers and a role cannot
//
// `Link` came off this list for being a feature a deployment offers, and
// `Leave` is one too -- so why it is on is worth a paragraph. The alternative
// to waiving it is a role naming it, and there is nothing such a role could
// mean but the method itself: `Leave` takes no subject, so *may delete their
// own account* is the whole of what it grants, and binding it to every person
// a directory has is work for an operator that changes nothing about who may
// do what. Whether it is offered at all is a fact about the **tenant**
// (`TenantConfig.leave`, off unless it said so), which `server/me` enforces on
// every call -- the same place the other thing it asks is enforced, that the
// credential the call came with is a sign-in minutes old. So the switch is the
// tenant's setting and the safety is the missing subject, and a binding would
// add a third thing to keep in step with the first.
//
// # And `AuthService.SignOut`, which is the same call one door over
//
// It takes an empty request and reads which session to end from the cookie in
// the caller's own metadata, so it is subject-less in the strongest sense:
// there is not a field it could be pointed with. That is `SignOutEverywhere`'s
// property exactly, and the two do the same thing at different scopes -- one
// session, or every credential the person holds.
//
// It was not on this list and the effect was quiet, which is why it is worth
// writing down. An operator whose role did not happen to name it was refused,
// and the admin console calls it in a `.finally`, so the page reset, the button
// looked like it worked, and the cookie went on opening the control plane. The
// only operator role most deployments have is `/roster.*/*`, so it was a hole
// waiting for the first narrow role somebody wrote.
//
// Signing out is the one act that must never need a permission. Somebody who
// has just been given an account, somebody whose role was taken away a moment
// ago, somebody who no longer trusts the machine they are on: each of them is
// exactly who wants this, and each of them is who a binding check refuses.
//
// # And `AuthService.Which`, which a page asks before it draws anything
//
// The name a page keeps its store under, for the session the call's cookie
// names (`auth.proto`). An empty request and the cookie in the caller's own
// metadata, which is `SignOut`'s shape exactly, and what comes back is a
// digest of that session: nothing it could be pointed at anybody with, and
// nothing about anybody. It is public, for a page nobody is signed in to; it
// is here for a page somebody is, which asks it before it draws and again
// whenever a call is refused. Behind a binding check, a person whose role did
// not happen to name it would be signed in and drawn a sign-in form -- every
// person at the user console whose role is anything less than all of roster.
//
// # And nothing else, which is now a rule rather than a list
//
// `Link`, `IssueKey` and `RevokeKey` sat beside these for a while, not waived
// but subject-less, for the grant that let a role name -- *may add a way into
// your own account* -- without the entity permission that reaches the tenant.
// They are gone: CLAUDE.md, *no self-only twin of a verb*. A person calls
// `Identity.Add`, `ApiKey.Issue` and `ApiKey.Erase` with their own
// reference, RBAC grants the method as it grants any other, and *whose row* is
// `server/core`'s (`mayReach`, `mayWriteAWayIn`: yourself always passes). So
// what is on this list is exactly what cannot be asked for with a role, and a
// method that takes a subject can never join it -- `cmd/asself_test.go`
// refuses one that carries a field that could name somebody.
func aboutYourself(method string) bool {
	switch method {
	case app.MeService_Get_FullMethodName,
		app.MeService_Unlink_FullMethodName,
		app.MeService_SignOutEverywhere_FullMethodName,
		app.MeService_Leave_FullMethodName,
		app.AuthService_SignOut_FullMethodName,
		app.AuthService_Which_FullMethodName:
		return true
	}

	return false
}

// Where is which tenants this caller sees.
func (p policy) Where(ctx context.Context, c gate.Call) (frame.Tenants, error) {
	if c.Actor.Domain() == pd.ApiKeyDomain {
		// A key belongs to the deployment and the deployment is every tenant in
		// it. What narrows it is its methods, not its tenants.
		return frame.Everything, nil
	}

	// A person sees their own tenant, which is what `gate` answers with no
	// policy at all. Roles narrow **within** a tenant and never across one:
	// there is no binding that makes somebody see another customer, and that is
	// deliberate -- the wall is not something a row may open.
	return frame.Only(c.Tenant), nil
}

// Sets is the second axis as `pd.Grouped` takes it, over the same rows the
// policy reads.
//
// A function of its own rather than a method, because `bare.Scopes` is built
// where the sink is and the policy is built where the interceptors are. Both
// read the same bindings, which is the point: what narrows a query and what
// permits a call are one set of rows.
func Sets(db *ent.Client, sources ...rbac.Source) frame.Sets {
	return policy{db: db, sources: sources}.sets
}

// sets is which sites this caller is narrowed to.
//
// This is what `pd.Grouped` has been generated for since the first week and
// what nothing had answered until now -- roster's own plan said so, and said
// the tests were handing it in.
//
// A binding with no site is the tenant's whole width, so somebody holding one
// is narrowed by nothing. Otherwise it is the sites their bindings name, and a
// caller with no binding at all sees no site -- which matches [policy.May]
// refusing them anyway, and is the safe direction if the two ever disagree.
//
// # And it is asked of the method
//
// Somebody holds more than one grant, and each reaches where it was made. So
// a call is narrowed to where its caller holds **the method it is**
// ([held.sitesFor]), when the context says which that is ([Calling]): a role
// across the tenant that reads teams does not carry a role held in Seoul that
// erases them into Busan. It was asked of the caller alone -- every site
// anything they hold reaches, and nothing once one thing was held across the
// tenant -- and that is still the answer where no method was said.
func (p policy) sets(ctx context.Context) ([]uuid.UUID, bool, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return nil, false, status.Error(codes.Unauthenticated, "who is asking?")
	}
	if f.Actor.Domain() == pd.ApiKeyDomain {
		return nil, true, nil
	}

	held, err := p.of(ctx, f.Actor.Uuid())
	if err != nil {
		return nil, false, err
	}
	if m, ok := calling(ctx); ok {
		vs, all := held.sitesFor(m)

		return vs, all, nil
	}

	return held.sites, held.anySite, nil
}

// held is what one caller's bindings add up to.
//
// A union, which is the whole of the evaluation. There are no deny rules, so
// the order they are read in cannot matter and there is no precedence table for
// anybody to hold in their head.
type held struct {
	methods map[string]struct{}
	sites   []uuid.UUID
	anySite bool

	// everywhere is the methods of the bindings with no site: the part of
	// `methods` the wall does not narrow. A role held in a team is not in it.
	everywhere map[string]struct{}

	// reaches is the same holdings kept apart: each with where it reaches,
	// which is what a call is narrowed by ([held.sitesFor]).
	reaches []reach
}

// reach is one thing somebody holds, and where: a site, or the whole tenant.
//
//   - a binding reaches the site it was made in, or the tenant when it names
//     none -- to them, or to a group they are in;
//   - a role held in a team reaches the team's site, a site being as fine as
//     the second axis gets (see [policy.of]); or the tenant when the team is
//     in none, which only somebody holding it across the tenant could have
//     given (`mayGrant`);
//   - a manager's role in a group reaches the group's site, or the tenant
//     when it is in none -- and names only what changes that group's members;
//   - what a file grants reaches the tenant.
type reach struct {
	methods []string
	site    uuid.UUID
	all     bool
}

// sitesFor is where this caller holds `method`: the sites, or everywhere.
func (h held) sitesFor(method string) (sites []uuid.UUID, all bool) {
	for _, r := range h.reaches {
		if !slices.ContainsFunc(r.methods, func(m string) bool { return frame.Covers(m, method) }) {
			continue
		}
		if r.all {
			return nil, true
		}
		if !slices.Contains(sites, r.site) {
			sites = append(sites, r.site)
		}
	}

	return sites, false
}

// allowsEverywhere is whether they hold `method` through a binding across the
// tenant, which is [Everywhere]'s reading of the same rows.
func (h held) allowsEverywhere(method string) bool {
	for m := range h.everywhere {
		if frame.Covers(m, method) {
			return true
		}
	}

	return false
}

// allows asks each pattern rather than looking the method up.
//
// A map lookup is what this was, and it is what a set of whole method names
// deserves. What it cannot do is answer for `/roster.HolderService/*`, and the
// point of a pattern is that it covers a method nobody had written down when
// the role was -- so the answer has to be computed and cannot be indexed.
//
// The cost is a walk over what one caller holds, per call, and that is small:
// it is the roles of one person, not of a deployment.
func (h held) allows(method string) bool {
	for m := range h.methods {
		if frame.Covers(m, method) {
			return true
		}
	}

	return false
}

// live loads a role only while it is there: an erased role grants nothing.
//
// It did, until the writes that take a role away were looked at (loft#341):
// a role's row carries its erasure, and a binding, a membership or a team's
// role reached through an edge loaded the role whatever its `date_erased`
// said -- so erasing a role in a console took nothing from anybody it was
// bound to. Every load of a role here is one of
// these, so that the gate and every rule that reads what somebody holds agree
// that an erased role is gone.
func live(q *ent.RoleQuery) { q.Where(entrole.DateErasedIsNil()) }

// liveTeam is a place on a team that is still a team.
//
// A membership's row is not erased with its team, so every read of what a
// team's role hands out asks this beside the membership's own `date_erased`:
// without it `Team.Erase` took nothing from anybody on the team (#274), which
// is [live]'s finding one edge along. [groupRoles] asks the same of a group.
var liveTeam = teammembership.HasTeamWith(entteam.DateErasedIsNil())

// groupRoles is the roles somebody holds in the groups they are in: what makes
// them a manager of each, and one row per group, held or not.
func groupRoles(ctx context.Context, db *ent.Client, who uuid.UUID) ([]*ent.Role, error) {
	vs, err := db.GroupMembership.Query().
		Where(
			groupmembership.DateErasedIsNil(),
			groupmembership.HasHolderWith(holder.IdEQ(who)),
			groupmembership.HasGroupWith(group.DateErasedIsNil()),
			groupmembership.HasRoleWith(entrole.DateErasedIsNil()),
		).
		WithRole(live).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*ent.Role, 0, len(vs))
	for _, v := range vs {
		if v.Edges.Role != nil {
			out = append(out, v.Edges.Role)
		}
	}

	return out, nil
}

// bindingsReaching is every live binding that reaches somebody: the ones
// written to them, and the ones written to a group they are in.
//
// One function because it is one set, and three readers of it that disagreed
// would be three answers to "what does this person hold". They did disagree:
// this walk was written here for the gate, and `Granted` had a query of its
// own that stopped at the holder edge -- so a group-provisioned administrator
// could call every method their role names and was, to the rule that protects
// them from being reset, somebody who held nothing. See [Granted].
func bindingsReaching(ctx context.Context, db *ent.Client, who uuid.UUID) ([]*ent.Binding, error) {
	// The groups they are in, which is the other way a binding reaches them.
	gs, err := db.GroupMembership.Query().
		Where(
			groupmembership.DateErasedIsNil(),
			groupmembership.HasHolderWith(holder.IdEQ(who)),
		).
		QueryGroup().
		Where(group.DateErasedIsNil()).
		Ids(ctx)
	if err != nil {
		return nil, err
	}

	q := db.Binding.Query().Where(
		binding.DateErasedIsNil(),
		binding.HasHolderWith(holder.IdEQ(who)),
	)
	if len(gs) > 0 {
		q = db.Binding.Query().Where(
			binding.DateErasedIsNil(),
			binding.Or(
				binding.HasHolderWith(holder.IdEQ(who)),
				binding.HasGroupWith(group.IdIn(gs...)),
			),
		)
	}

	return q.WithRole(live).WithSite().All(ctx)
}

// of reads the bindings a holder has, by being them or by being in a group.
func (p policy) of(ctx context.Context, who uuid.UUID) (held, error) {
	h := held{methods: map[string]struct{}{}, everywhere: map[string]struct{}{}}

	vs, err := bindingsReaching(ctx, p.db, who)
	if err != nil {
		return held{}, err
	}

	// And the roles they hold **in a team**, which is the other place a role is
	// referenced -- there the scope is that team.
	//
	// They count here, because this answers "may you ever call this" and the
	// gate is outermost: a team administrator who was refused here would never
	// reach the layer that knows which team the call is about. What that costs
	// is that the gate now lets them through for **any** team, which is why
	// `server/core` refusing the wrong one is not optional. The two halves are
	// written to be read together.
	ts, err := p.db.TeamMembership.Query().
		Where(
			teammembership.DateErasedIsNil(),
			teammembership.HasHolderWith(holder.IdEQ(who)),
			liveTeam,
		).
		WithRole(live).
		WithTeam(func(q *ent.TeamQuery) { q.WithSite() }).
		All(ctx)
	if err != nil {
		return held{}, err
	}
	for _, v := range ts {
		if v.Edges.Role != nil {
			for _, m := range v.Edges.Role.Methods {
				h.methods[m] = struct{}{}
			}

			r := reach{methods: v.Edges.Role.Methods, all: true}
			if v.Edges.Team != nil && v.Edges.Team.Edges.Site != nil {
				r = reach{methods: v.Edges.Role.Methods, site: v.Edges.Team.Edges.Site.Id}
			}
			h.reaches = append(h.reaches, r)
		}

		// And the **site** their team is in, which they have to be able to see
		// or they cannot see their own team.
		//
		// A site is coarser than a team, and that is the second axis being one
		// axis: `pd.Grouped` narrows to a set, `Site` is the set, and there is
		// no third level to narrow to. So being in a team means seeing its
		// site's rows, and seeing only your own team is the app filtering
		// rather than the wall narrowing. Written down here because it is the
		// kind of thing that leaks by being forgotten.
		if v.Edges.Team != nil && v.Edges.Team.Edges.Site != nil {
			h.sites = append(h.sites, v.Edges.Team.Edges.Site.Id)
		}
	}

	// And the roles they hold **in a group**, which is what makes somebody its
	// manager -- counted for a team role's reason: the gate never sees which
	// group a call is about, so it lets them through, and `server/core`
	// refuses every group but theirs (`mayChangeGroup`). Such a role names
	// only what a group's manager writes (`mayHoldInGroup`, `core`'s
	// `managing`), so what this adds is those methods, and no site.
	gs, err := p.db.GroupMembership.Query().
		Where(
			groupmembership.DateErasedIsNil(),
			groupmembership.HasHolderWith(holder.IdEQ(who)),
			groupmembership.HasGroupWith(group.DateErasedIsNil()),
			groupmembership.HasRoleWith(entrole.DateErasedIsNil()),
		).
		WithRole(live).
		WithGroup(func(q *ent.GroupQuery) { q.WithSite() }).
		All(ctx)
	if err != nil {
		return held{}, err
	}
	for _, v := range gs {
		if v.Edges.Role == nil {
			continue
		}
		for _, m := range v.Edges.Role.Methods {
			h.methods[m] = struct{}{}
		}

		r := reach{methods: v.Edges.Role.Methods, all: true}
		if v.Edges.Group != nil && v.Edges.Group.Edges.Site != nil {
			r = reach{methods: v.Edges.Role.Methods, site: v.Edges.Group.Edges.Site.Id}
		}
		h.reaches = append(h.reaches, r)
	}

	for _, v := range vs {
		if v.Edges.Role == nil {
			// A binding of a role that was erased, which grants nothing --
			// not even the sight of the site it was made in.
			continue
		}
		for _, m := range v.Edges.Role.Methods {
			h.methods[m] = struct{}{}
		}

		if v.Edges.Site == nil {
			// Bound across the tenant, so there is no site to narrow by.
			h.anySite = true
			if v.Edges.Role != nil {
				for _, m := range v.Edges.Role.Methods {
					h.everywhere[m] = struct{}{}
				}
			}
			h.reaches = append(h.reaches, reach{methods: v.Edges.Role.Methods, all: true})

			continue
		}

		h.sites = append(h.sites, v.Edges.Site.Id)
		h.reaches = append(h.reaches, reach{methods: v.Edges.Role.Methods, site: v.Edges.Site.Id})
	}

	extra, err := configuredGrants(ctx, p.db, who, p.sources)
	if err != nil {
		return held{}, err
	}
	for _, g := range extra {
		if len(g.Methods) > 0 {
			h.anySite = true
			h.reaches = append(h.reaches, reach{methods: g.Methods, all: true})
		}
		for _, m := range g.Methods {
			h.methods[m] = struct{}{}
			h.everywhere[m] = struct{}{}
		}
	}
	return h, nil
}

// Locking is the write `server/core` takes to make two callers contend.
//
// One rule there is not a judgement about a request but a **count** of other
// rows -- may this identity go, or is it the last way somebody can sign in --
// and a count taken outside a transaction is a fact about a moment that has
// passed. What closes it is the count, the erase and one write on the person's
// own row inside a single transaction, so that the second caller blocks on the
// row and is refused rather than counting a set that is about to change.
//
// # Why the write is here and not there
//
// Because there is nothing in the generated API that makes one. A `Patch`
// carrying only a version precondition compiles to an existence check and no
// write at all -- which is D34's finding, arriving in a second place a year
// later -- and a `Patch` naming a real field would be this rule editing
// somebody's record to take a lock, which is a strange thing to find in a
// trail.
//
// So it is a write against ent, which `server/core` deliberately holds no
// client for. This file already reads ent directly and says why: what a caller
// may do cannot itself require permission. The same argument covers this --
// taking a lock is not a write anybody asked for, so it is not one the layers
// should record or narrow.
//
// # What it writes
//
// The version column, to what it already is. What two callers contend for is
// the write and not what it writes: a database holds the row for whoever
// wrote it first until they are done, whatever they wrote.
//
// It moved the column to now, on the reasoning that a version is a token and
// not a fact about the person. But a deployment that keeps digests checks its
// rows against its trail (`roster trail rows`), and a row written past the
// recorder is what that check is for: after an identity was erased it found
// the person, `differs holder`, for a row nobody had touched (#279). So the
// row is left as the trail keeps it, and a console holding its version is not
// told to read again about a change that was none.
//
// # Whose row
//
// The person's, for a count of what is theirs; the tenant's, for whether
// anybody still administers it, and whether it has a team of a name in no
// site; the site's, for whether anything is in it when it is erased, and
// whether a role is bound there already; and the role's, for whether it is
// bound across the tenant already. Which of them `who` is, is in the
// identifier.
func Locking(db *ent.Client) core.Lock {
	return func(ctx context.Context, drv dialect.Driver, who pdid.Id) error {
		// On the driver it is handed, which is the transaction `server/core`
		// opened or was rebuilt on. A write on the client this was built from
		// would be a write outside it, and then there is nothing to contend
		// for.
		//
		// Into a client of this call's own, and not into `db`: that is the
		// variable every call shares, and writing it made the next call's
		// lock -- or a concurrent one's -- land on this call's transaction.
		c := db
		if drv != nil {
			c = ent.NewClient(ent.Driver(drv))
		}

		// A rule about a site -- whether anything is in it when it is erased
		// -- contends on the site's row, which the erase and whatever is put
		// in a site both write first. On a site that is still there: one
		// erased by whoever took the lock before is no site to put anything
		// in, and is answered as it is a moment later.
		if who.Domain() == pd.SiteDomain {
			n, err := c.Site.Update().
				Where(entsite.IdEQ(who.Uuid()), entsite.DateErasedIsNil()).
				Modify(asItIs(entsite.FieldDateUpdated)).
				Save(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				return status.Error(codes.NotFound, "Site not found")
			}

			return nil
		}

		// A rule about a role's bindings across the tenant -- whether this
		// one was made already -- contends on the role's row, which two
		// callers making the same binding both name. On a role that is still
		// there, as a site's is.
		if who.Domain() == pd.RoleDomain {
			n, err := c.Role.Update().
				Where(entrole.IdEQ(who.Uuid()), entrole.DateErasedIsNil()).
				Modify(asItIs(entrole.FieldDateUpdated)).
				Save(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				return status.Error(codes.NotFound, "Role not found")
			}

			return nil
		}

		// A rule about a whole tenant -- whether anybody is left administering
		// it, whether it has a team of a name already -- contends on the
		// tenant's row, which every write that could change the answer has to
		// make.
		if who.Domain() == pd.TenantDomain {
			n, err := c.Tenant.Update().
				Where(enttenant.IdEQ(who.Uuid())).
				Modify(asItIs(enttenant.FieldDateUpdated)).
				Save(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				return status.Error(codes.NotFound, "Tenant not found")
			}

			return nil
		}

		n, err := c.Holder.Update().
			Where(holder.IdEQ(who.Uuid())).
			Modify(asItIs(holder.FieldDateUpdated)).
			Save(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return status.Error(codes.NotFound, "Holder not found")
		}

		return nil
	}
}

// asItIs is a write of a column to the value it has: what [Locking] writes, so
// that the row is held and nothing of it is changed.
func asItIs(column string) func(*entsql.UpdateBuilder) {
	return func(u *entsql.UpdateBuilder) { u.Set(column, entsql.Expr(column)) }
}

// Administered is [core.Administered] on this deployment's rows: the tenant's
// roles whose patterns cover `method`, bound with no site to somebody there and
// not suspended, or to a group of the tenant's with such somebody in it.
//
// Bindings and groups, and not a team: a role held in a team is held at the
// team's site, which is not the whole tenant. And not what a deployment's
// policy file grants (`configuredGrants`): a tenant's administration is its own
// rows, and a file changes under it without asking -- counted here, it would
// let a narrowing take the last row away and a later change to the file take
// the rest.
func Administered(db *ent.Client) core.Administered {
	return func(ctx context.Context, drv dialect.Driver, tenant pdid.Id, method string) (bool, error) {
		// On the driver it is handed, which is the transaction `server/core`
		// opened, so that what it counts includes what that transaction wrote.
		c := db
		if drv != nil {
			c = ent.NewClient(ent.Driver(drv))
		}

		rs, err := c.Role.Query().
			Where(entrole.TenantIdEQ(tenant.Uuid()), entrole.DateErasedIsNil()).
			Select(entrole.FieldId, entrole.FieldMethods).
			All(ctx)
		if err != nil {
			return false, err
		}
		var ids []uuid.UUID
		for _, r := range rs {
			if allows(r, method) {
				ids = append(ids, r.Id)
			}
		}
		if len(ids) == 0 {
			return false, nil
		}

		live := []predicate.Holder{holder.DateErasedIsNil(), holder.DateDisabledIsNil()}
		across := []predicate.Binding{binding.RoleIdIn(ids...), binding.DateErasedIsNil(), binding.Not(binding.HasSite())}
		ok, err := c.Binding.Query().Where(append(across, binding.HasHolderWith(live...))...).Exist(ctx)
		if err != nil || ok {
			return ok, err
		}

		gs, err := c.Binding.Query().Where(across...).QueryGroup().Where(group.DateErasedIsNil()).Ids(ctx)
		if err != nil || len(gs) == 0 {
			return false, err
		}

		return c.GroupMembership.Query().
			Where(
				groupmembership.GroupIdIn(gs...),
				groupmembership.DateErasedIsNil(),
				groupmembership.HasHolderWith(live...),
			).
			Exist(ctx)
	}
}

// Rules is everything `server/core` has to know about a caller and cannot work
// out, from the rows this policy already reads.
//
// Four answers to what look like one question and are not, which is what took
// three findings to see. `Holds` is about one call and one team. `Granted` is
// what may be **passed on** and reads bindings alone. `Joining` is what a group
// holds, so that putting somebody in one is weighed like writing the binding.
// `Holding` is everything somebody **has**, by any path -- which is the one
// `mayReach` needs, because there a path not walked allows rather than refuses.
func Rules(db *ent.Client, sources ...rbac.Source) core.Rules {
	return core.Rules{
		Holds:        Holds(db, sources...),
		Granted:      Granted(db, sources...),
		Joining:      Joining(db),
		Holding:      Holding(db, sources...),
		Giving:       Giving(db),
		Held:         core.Held(Everything(db, sources...)),
		Everywhere:   Everywhere(db, sources...),
		Administered: Administered(db),
		Releasing:    Releasing(db, audit.Contents{}),
		Named:        Named(db),
		Sited:        Sited(db),
		Bound:        Bound(db),
		Unsited:      Unsited(db),
	}
}

// Bound is whether a role is already bound to somebody, or to a group, in a
// place, for [core.Bound]: a live binding of that role to that holder or
// group, in that site or -- where `site` is the nil identifier -- in none.
//
// Written out where an index would say it, because an index cannot: a null
// site is a value here, and no two nulls are equal to SQL.
func Bound(db *ent.Client) core.Bound {
	return func(ctx context.Context, drv dialect.Driver, role, to, site pdid.Id) (bool, error) {
		// On the driver it is handed, as [Administered] is: the transaction
		// the binding is written in.
		c := db
		if drv != nil {
			c = ent.NewClient(ent.Driver(drv))
		}

		ps := []predicate.Binding{binding.DateErasedIsNil(), binding.RoleIdEQ(role.Uuid())}
		if to.Domain() == pd.GroupDomain {
			ps = append(ps, binding.GroupIdEQ(to.Uuid()))
		} else {
			ps = append(ps, binding.HolderIdEQ(to.Uuid()))
		}
		if site == pdid.Nil {
			ps = append(ps, binding.SiteIdIsNil())
		} else {
			ps = append(ps, binding.SiteIdEQ(site.Uuid()))
		}

		return c.Binding.Query().Where(ps...).Exist(ctx)
	}
}

// Unsited is whether a tenant already has a team of a name in no site, for
// [core.Unsited]: what the index over a team's name and its site would hold
// if a null site were a value.
func Unsited(db *ent.Client) core.Unsited {
	return func(ctx context.Context, drv dialect.Driver, tenant pdid.Id, alias string) (bool, error) {
		// On the driver it is handed, as [Administered] is.
		c := db
		if drv != nil {
			c = ent.NewClient(ent.Driver(drv))
		}

		return c.Team.Query().
			Where(
				entteam.DateErasedIsNil(),
				entteam.TenantIdEQ(tenant.Uuid()),
				entteam.SiteIdIsNil(),
				entteam.AliasEQ(alias),
			).
			Exist(ctx)
	}
}

// Sited is what a site still has in it, for [core.Sited]: the live roles,
// bindings, teams and groups that name it, counted from the rows.
func Sited(db *ent.Client) core.Sited {
	return func(ctx context.Context, drv dialect.Driver, site pdid.Id) (core.InSite, error) {
		// On the driver it is handed, as [Administered] is: the transaction
		// the erase is made in, so that what it counts is what a write that
		// took the site's lock before it has left.
		db := db
		if drv != nil {
			db = ent.NewClient(ent.Driver(drv))
		}

		var (
			in  core.InSite
			err error
			at  = site.Uuid()
		)
		if in.Bindings, err = db.Binding.Query().
			Where(binding.DateErasedIsNil(), binding.HasSiteWith(entsite.IdEQ(at))).Count(ctx); err != nil {
			return in, err
		}
		if in.Roles, err = db.Role.Query().
			Where(entrole.DateErasedIsNil(), entrole.HasSiteWith(entsite.IdEQ(at))).Count(ctx); err != nil {
			return in, err
		}
		if in.Teams, err = db.Team.Query().
			Where(entteam.DateErasedIsNil(), entteam.HasSiteWith(entsite.IdEQ(at))).Count(ctx); err != nil {
			return in, err
		}
		if in.Groups, err = db.Group.Query().
			Where(group.DateErasedIsNil(), group.HasSiteWith(entsite.IdEQ(at))).Count(ctx); err != nil {
			return in, err
		}

		return in, nil
	}
}

// Named is a tenant's role by its alias, for [core.Named]: read from the rows
// and not through a server, so that it answers the same whoever is asking.
func Named(db *ent.Client) core.Named {
	return func(ctx context.Context, drv dialect.Driver, tenant pdid.Id, alias string) (*core.NamedRole, error) {
		// On the driver it is handed, as [Administered] is.
		c := db
		if drv != nil {
			c = ent.NewClient(ent.Driver(drv))
		}

		v, err := c.Role.Query().
			Where(entrole.TenantIdEQ(tenant.Uuid()), entrole.AliasEQ(alias), entrole.DateErasedIsNil()).
			WithSite().
			Only(ctx)
		if ent.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}

		out := &core.NamedRole{Id: pdid.Id(v.Id), Methods: v.Methods}
		if v.Edges.Site != nil {
			out.Site = pdid.Id(v.Edges.Site.Id)
		}

		return out, nil
	}
}

// Releasing erases whichever `Host` row holds a name, whoever's it is.
//
// What it is for is a tenant proving a name another tenant holds: the unique
// index on `name` covers the rows that are not erased, so the incumbent has to
// let go before the new row can be written, and no walled server can see the
// incumbent. `core.Releasing` is where the argument for it being a function
// rather than a server is written down.
//
// A **soft** erase, unlike the sweeps: the row is what a trail entry naming it
// resolves through, and a name changing hands is exactly the event somebody
// looks that up for. What makes it release the name anyway is the index being
// partial.
//
// Bounded by the name and by nothing else, which is the whole of why this is
// safe to hand to a layer a caller reaches: there is no argument here that could
// be widened, and at most one live row can match.
//
// # And it is written down where whoever held the name reads
//
// The incumbent is not asked, and what makes that bearable is that they are
// told: `host.proto`, `docs/operating.md` and this function's own comment each
// said the erase is on the trail. It was not. A row erased beneath the servers
// is on the trail only if something puts it there, and nothing did: a tenant
// whose name had gone found its row erased and nothing saying who, when or how
// (#277). So the row is put there ([Released]), by whoever `ctx` is framed as,
// filed under the row's own tenant -- `pd.Record`, which is what a row written
// beneath the servers on purpose is recorded by. `cs` is what the trail keeps
// of a row, as the recorder is handed it.
//
// # A row a file declares is not taken
//
// Every other road refuses a write to one, since the file is where it is
// written from (`core.mayWriteDeclared`); this one erased it, and the next
// `resources apply` then met a name that was somebody else's.
func Releasing(db *ent.Client, cs audit.Contents) core.Releasing {
	return func(ctx context.Context, drv dialect.Driver, name string) error {
		// On the driver it is handed, which is the transaction `server/core`
		// opened, for `Locking`'s reason: a write outside it could not be undone
		// with the rest -- and into a client of this call's own, for the other
		// half of `Locking`'s.
		c := db
		if drv != nil {
			c = ent.NewClient(ent.Driver(drv))
		}

		// Not an error when nothing holds it. Taking a free name is the ordinary
		// case, and refusing here would make the caller read first.
		held, err := c.Host.Query().Where(host.NameEQ(name), host.DateErasedIsNil()).Only(ctx)
		if ent.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if from, ok := held.Labels[Declared]; ok {
			return status.Errorf(codes.FailedPrecondition,
				"%s: the name is declared in %s, and a name a file declares is not taken by a proof; "+
					"whoever runs this deployment takes it out of the file first", name, from)
		}

		if err := c.Host.UpdateOneId(held.Id).SetDateErased(time.Now()).Exec(ctx); err != nil {
			return err
		}

		_, err = pd.Record(ctx, c, cs, Released, []pdid.Id{pdid.Id(held.Id)})

		return err
	}
}

// Released is the act a name taken by a newer proof is on the trail as, in the
// trail of the tenant that held it.
const Released = "roster.host.release"

// Granted is every pattern somebody holds through a binding, and **where**.
//
// # Why the site travels with the methods
//
// Because a permission held in one place is not one to hand out in another,
// and flattened into a single list the two are the same strings. That is not
// hypothetical: somebody bound to a role in Seoul had those methods in a flat
// list with no trace of Seoul on them, so `mayGrant` compared them against a
// tenant-wide write and agreed. Two Rpcs later they held the tenant.
//
// Kept together, `mayGrant` takes the scope being written to and only what
// reaches it counts. A site administrator delegates inside their own site,
// which is correct, and nowhere else, which is the point.
//
// # Bindings only
//
// A role held in a **team** is still left out entirely. Its scope is a team
// and the scopes here are the tenant and a site, so there is nothing to
// compare it against -- `server/core.Holds` is what asks about a team, per
// call, with the team in hand.
//
// A binding reaching somebody **through a group** is not left out, and the
// direction is why it is worth saying. [Core.mayGrant] reads what the caller
// holds, where missing one only refuses a grant somebody could have made --
// the conversation `escalate.go` is willing to have. [Core.mayReach] reads
// what the **target** holds and allows the write when that is nothing, so the
// same blindness there is silent: an administrator provisioned by a group read
// as holding nothing, and anybody who could reset a password could become
// them. Which is why this walks the same rows [policy.of] walks -- see
// [bindingsReaching].
func Granted(db *ent.Client, sources ...rbac.Source) core.Granted {
	return func(ctx context.Context, who pdid.Id) ([]core.Grant, error) {
		vs, err := bindingsReaching(ctx, db, who.Uuid())
		if err != nil {
			return nil, err
		}

		extra, err := configuredGrants(ctx, db, who.Uuid(), sources)
		if err != nil {
			return nil, err
		}
		return append(grantsOf(vs), extra...), nil
	}
}

// Joining is what a group holds, which is what putting somebody into it hands
// them.
//
// The same rows [Granted] reads, asked from the other end: a binding names a
// holder **or** a group, and this is the group half. A separate query rather
// than a widened one, for the reason [core.Joining] gives -- the two are asked
// about different kinds of identifier.
//
// The site travels with the methods for [Granted]'s reason, and it matters
// more here: a group may be bound twice, once across the tenant and once
// inside a site, and a site administrator who may put somebody into the second
// must not be able to put them into the first.
func Joining(db *ent.Client) core.Joining {
	return func(ctx context.Context, id pdid.Id) ([]core.Grant, error) {
		vs, err := db.Binding.Query().
			Where(
				binding.DateErasedIsNil(),
				binding.HasGroupWith(group.IdEQ(id.Uuid()), group.DateErasedIsNil()),
			).
			WithRole(live).
			WithSite().
			All(ctx)
		if err != nil {
			return nil, err
		}

		return grantsOf(vs), nil
	}
}

// Giving is what one row hands whoever it names, for [core.Giving]: a
// binding, a place on a team, or a manager's place in a group, told apart by
// the identifier.
//
// Each is read as [policy.of] reads it, so that what an erase is held to is
// what the gate stops counting when the row goes. A role that was erased
// gives nothing ([live]); nor does a place on a team that was ([liveTeam]),
// a place in a group that was ([groupRoles]), or a binding to one
// ([bindingsReaching]).
//
// Where each is held is where granting it was asked about, which for two of
// the three is where the gate holds it too: a binding at its site, a team's
// role at the team's site. A manager's role is answered across the tenant,
// whichever site the group is in, since that is what appointing one asks
// (`core`'s `mayAppoint`).
func Giving(db *ent.Client) core.Giving {
	return func(ctx context.Context, row pdid.Id) ([]core.Grant, error) {
		switch row.Domain() {
		case pd.BindingDomain:
			vs, err := db.Binding.Query().
				Where(
					binding.IdEQ(row.Uuid()),
					binding.DateErasedIsNil(),
					binding.Or(binding.Not(binding.HasGroup()), binding.HasGroupWith(group.DateErasedIsNil())),
				).
				WithRole(live).
				WithSite().
				All(ctx)
			if err != nil {
				return nil, err
			}

			return grantsOf(vs), nil

		case pd.TeamMembershipDomain:
			vs, err := db.TeamMembership.Query().
				Where(teammembership.IdEQ(row.Uuid()), teammembership.DateErasedIsNil(), liveTeam).
				WithRole(live).
				WithTeam(func(q *ent.TeamQuery) { q.WithSite() }).
				All(ctx)
			if err != nil {
				return nil, err
			}

			var gs []core.Grant
			for _, v := range vs {
				if v.Edges.Role == nil {
					continue
				}

				g := core.Grant{Methods: v.Edges.Role.Methods}
				if v.Edges.Team != nil && v.Edges.Team.Edges.Site != nil {
					g.Site = pdid.Id(v.Edges.Team.Edges.Site.Id)
				}
				gs = append(gs, g)
			}

			return gs, nil

		case pd.GroupMembershipDomain:
			vs, err := db.GroupMembership.Query().
				Where(
					groupmembership.IdEQ(row.Uuid()),
					groupmembership.DateErasedIsNil(),
					groupmembership.HasGroupWith(group.DateErasedIsNil()),
				).
				WithRole(live).
				All(ctx)
			if err != nil {
				return nil, err
			}

			var gs []core.Grant
			for _, v := range vs {
				if v.Edges.Role != nil {
					gs = append(gs, core.Grant{Methods: v.Edges.Role.Methods})
				}
			}

			return gs, nil
		}

		return nil, nil
	}
}

// Holding is everything somebody holds, by any path -- which is `policy.of`
// expressed as grants rather than as a set of methods.
//
// [Granted] answers what may be **passed on** and reads bindings only, for the
// reason `escalate.go` gives: a role held in one team is not a role to bind
// across the tenant. This answers what somebody **has**, which is the question
// `core.mayReach` asks about the person whose credential is being written --
// and there the two readings are not interchangeable. A path missing from the
// first refuses a grant somebody could have made; missing from the second it
// allows a password to be reset on an administrator who reads as holding
// nothing.
//
// The team roles are reported across the tenant rather than at their team's
// site, because that is what the gate will actually let them call: `policy.of`
// unions a team role into the set `May` answers from, and the only thing that
// narrows one back down is `core.mayChangeTeam`, which guards the membership
// writes and nothing else. Reporting them narrower would be reporting less
// than the person can do, which is the direction that lets somebody be reached.
func Holding(db *ent.Client, sources ...rbac.Source) core.Holding {
	return func(ctx context.Context, who pdid.Id) ([]core.Grant, error) {
		vs, err := bindingsReaching(ctx, db, who.Uuid())
		if err != nil {
			return nil, err
		}

		gs := grantsOf(vs)

		ts, err := db.TeamMembership.Query().
			Where(
				teammembership.DateErasedIsNil(),
				teammembership.HasHolderWith(holder.IdEQ(who.Uuid())),
				liveTeam,
			).
			WithRole(live).
			All(ctx)
		if err != nil {
			return nil, err
		}

		for _, v := range ts {
			if v.Edges.Role == nil {
				continue
			}

			gs = append(gs, core.Grant{Methods: v.Edges.Role.Methods})
		}

		// And a manager's, across the tenant for the same reason: the gate
		// holds a role in a group everywhere, and only the membership writes
		// are narrowed back to the group.
		rs, err := groupRoles(ctx, db, who.Uuid())
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			gs = append(gs, core.Grant{Methods: r.Methods})
		}

		extra, err := configuredGrants(ctx, db, who.Uuid(), sources)
		if err != nil {
			return nil, err
		}
		return append(gs, extra...), nil
	}
}

// grantsOf is a set of bindings as the rule reads them: what each allows, and
// where.
//
// One function because both answers are the same rows read the same way, and
// two copies of this loop would be two places to remember that a binding with
// no site is the whole tenant.
func grantsOf(vs []*ent.Binding) []core.Grant {
	gs := make([]core.Grant, 0, len(vs))
	for _, v := range vs {
		if v.Edges.Role == nil {
			continue
		}

		g := core.Grant{Methods: v.Edges.Role.Methods}
		if v.Edges.Site != nil {
			g.Site = pdid.Id(v.Edges.Site.Id)
		}

		gs = append(gs, g)
	}

	return gs
}

// Holds is `server/core`'s question, answered from the rows this policy already
// reads: may this person call this method, for this team -- or this group?
//
// Two ways to yes, and they are the scopes a role is referenced at. A
// **binding** is the tenant or a site and is never about one team or group, so
// holding one is enough by itself. A **team membership** is that team, and a
// **group membership** that group -- its manager's -- so each counts only when
// the call is about it. Which of the two `scope` names is in its identifier.
//
// One function rather than two, because it is one question. The gate asks a
// weaker version of it -- may you ever call this -- and a layer asks this one,
// and both read the same rows.
func Holds(db *ent.Client, sources ...rbac.Source) core.Holds {
	return func(ctx context.Context, who pdid.Id, method string, scope pdid.Id) (bool, error) {
		// The same set the gate let them through on -- see
		// [bindingsReaching]. A narrower reading here would refuse the call
		// after the gate allowed it, which is the two halves of one answer
		// disagreeing rather than one of them being careful.
		vs, err := bindingsReaching(ctx, db, who.Uuid())
		if err != nil {
			return false, err
		}

		// A binding across the tenant reaches every team and group in it, and
		// one made in a site reaches the ones of that site: where it was made
		// is compared, and was not -- any binding naming the method was
		// enough, so a role held in Seoul changed a team of Busan's once the
		// wall had shown it (#274).
		var (
			at     uuid.UUID
			looked bool
		)
		for _, v := range vs {
			if !allows(v.Edges.Role, method) {
				continue
			}
			if v.Edges.Site == nil || scope == pdid.Nil {
				return true, nil
			}
			if !looked {
				if at, err = siteOf(ctx, db, scope); err != nil {
					return false, err
				}
				looked = true
			}
			if at != uuid.Nil() && at == v.Edges.Site.Id {
				return true, nil
			}
		}

		extra, err := configuredGrants(ctx, db, who.Uuid(), sources)
		if err != nil {
			return false, err
		}
		for _, g := range extra {
			for _, m := range g.Methods {
				if frame.Covers(m, method) {
					return true, nil
				}
			}
		}
		if scope == pdid.Nil {
			return false, nil
		}
		if scope.Domain() == pd.GroupDomain {
			gs, err := db.GroupMembership.Query().
				Where(
					groupmembership.DateErasedIsNil(),
					groupmembership.HasHolderWith(holder.IdEQ(who.Uuid())),
					groupmembership.HasGroupWith(group.IdEQ(scope.Uuid()), group.DateErasedIsNil()),
				).
				WithRole(live).
				All(ctx)
			if err != nil {
				return false, err
			}
			for _, v := range gs {
				if allows(v.Edges.Role, method) {
					return true, nil
				}
			}

			return false, nil
		}

		ts, err := db.TeamMembership.Query().
			Where(
				teammembership.DateErasedIsNil(),
				teammembership.HasHolderWith(holder.IdEQ(who.Uuid())),
				teammembership.HasTeamWith(entteam.IdEQ(scope.Uuid())),
				liveTeam,
			).
			WithRole(live).
			All(ctx)
		if err != nil {
			return false, err
		}
		for _, v := range ts {
			if allows(v.Edges.Role, method) {
				return true, nil
			}
		}

		return false, nil
	}
}

// siteOf is the site a team or a group is in, and the nil identifier for one in none
// -- which a binding made in a site does not reach.
func siteOf(ctx context.Context, db *ent.Client, scope pdid.Id) (uuid.UUID, error) {
	if scope.Domain() == pd.GroupDomain {
		v, err := db.Group.Query().Where(group.IdEQ(scope.Uuid())).Only(ctx)
		if ent.IsNotFound(err) {
			return uuid.Nil(), nil
		}
		if err != nil {
			return uuid.Nil(), err
		}

		return v.SiteId, nil
	}

	v, err := db.Team.Query().Where(entteam.IdEQ(scope.Uuid())).Only(ctx)
	if ent.IsNotFound(err) {
		return uuid.Nil(), nil
	}
	if err != nil {
		return uuid.Nil(), err
	}

	return v.SiteId, nil
}

func allows(r *ent.Role, method string) bool {
	if r == nil {
		return false
	}

	for _, m := range r.Methods {
		if frame.Covers(m, method) {
			return true
		}
	}

	return false
}

// Everywhere is [Everything] without a site or a team: the patterns held
// through a binding across the whole tenant, for `Holder.Reaches`.
func Everywhere(db *ent.Client, sources ...rbac.Source) core.Everywhere {
	p := policy{db: db, sources: sources}

	return func(ctx context.Context, who pdid.Id) ([]string, error) {
		h, err := p.of(ctx, who.Uuid())
		if err != nil {
			return nil, err
		}

		ms := make([]string, 0, len(h.everywhere))
		for m := range h.everywhere {
			ms = append(ms, m)
		}
		slices.Sort(ms)

		return ms, nil
	}
}

// Everything is what a caller effectively holds, for `server/me`.
//
// The same union the gate enforces, so that what a page shows and what the
// server allows come from one function rather than two that agree today.
func Everything(db *ent.Client, sources ...rbac.Source) me.Held {
	p := policy{db: db, sources: sources}

	return func(ctx context.Context, who pdid.Id) ([]string, []pdid.Id, bool, error) {
		h, err := p.of(ctx, who.Uuid())
		if err != nil {
			return nil, nil, false, err
		}

		// The patterns themselves, not what they expand to.
		//
		// Expanding was what this did while "everything" was a flag with no
		// wire form: a page cannot act on a boolean it does not understand. A
		// pattern it can -- `frame.Covers` is the same three comparisons in
		// TypeScript -- and an expansion here would be a list of what exists in
		// **this** binary, which during a rolling deploy is not the same list
		// every replica would give.
		ms := make([]string, 0, len(h.methods))
		for m := range h.methods {
			ms = append(ms, m)
		}

		slices.Sort(ms)

		// Each site once, in the order of their identifiers, which is the
		// order they were made in.
		//
		// `h.sites` is a site for every binding and every team that names
		// it, as the rows came, and that is what a query is narrowed by: a
		// site named twice narrows as it does named once. Answered as it
		// stood, somebody bound twice in Seoul, once in Busan and on two of
		// Seoul's teams was told `[seoul seoul seoul busan seoul]`, in an
		// order that was the store's (#279).
		ks := make([]pdid.Id, 0, len(h.sites))
		for _, v := range h.sites {
			ks = append(ks, pdid.Id(v))
		}
		slices.SortFunc(ks, func(a, b pdid.Id) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
		ks = slices.Compact(ks)

		return ms, ks, h.anySite, nil
	}
}

// configuredGrants uses immutable tenant/holder IDs. Aliases are writable and
// must never select an external grant: renaming or recreating a holder would
// otherwise bypass the permission-delegation checks. File groups have
// their own membership; a database GroupMembership never changes these grants.
func configuredGrants(ctx context.Context, db *ent.Client, who uuid.UUID, sources []rbac.Source) ([]core.Grant, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	h, err := db.Holder.Query().Where(holder.IdEQ(who), holder.DateErasedIsNil()).WithTenant().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if h.Edges.Tenant == nil {
		return nil, nil
	}
	gs, err := (rbac.Union(sources)).Grants(ctx, "@"+h.Edges.Tenant.Id.String()+"/"+h.Id.String())
	if err != nil {
		return nil, err
	}
	out := make([]core.Grant, 0, len(gs))
	for _, g := range gs {
		out = append(out, core.Grant{Methods: g.Methods})
	}
	return out, nil
}
