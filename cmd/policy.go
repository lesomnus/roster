package cmd

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/holder"
	entteam "github.com/lesomnus/roster/internal/ent/team"
	"github.com/lesomnus/roster/internal/ent/teammembership"
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
// this app does that is nothing, and custody's catalogue is the precedent. The
// reason is the same one the resolver has: **working out what a caller may do
// cannot itself require permission.** A read through the walled stack would ask
// this function to answer before it can answer.
//
// It is also why this is short. Every predicate here is this file's, so there
// is nothing to keep in step with a rule written elsewhere.
//
// # What it does not do
//
// It does not look at what a call is **about**. `gate.Call` carries the actor,
// their tenant, the actor's own row and the method, and never the request -- so
// "may manage the team they are in" is not here. That is `server/core`, which
// is given the request; see PLAN.md, D17.
func Policy(db *ent.Client) gate.Policy { return policy{db} }

type policy struct{ db *ent.Client }

var _ gate.Policy = policy{}

// May answers whether this caller may call this method at all.
func (p policy) May(ctx context.Context, c gate.Call) error {
	if c.Actor.Domain() == pd.ApiKeyDomain {
		// A key's methods are its own, applied by `auth.Interceptor` from the
		// credential before this runs. Asking again here would mean a key
		// needed a binding as well, and a key is not somebody who can hold one.
		return nil
	}

	if aboutYourself(c.Action) {
		return nil
	}

	held, err := p.of(ctx, c.Actor.Uuid())
	if err != nil {
		return err
	}
	if !held.allows(c.Action) {
		return status.Errorf(codes.PermissionDenied,
			"%s: no role of yours allows that", c.Action)
	}

	return nil
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
// A list of methods and not a prefix, which is the opposite of custody's
// catalogue. There every RPC is public and a second one added tomorrow should
// be too. Here it is exactly this one, and a method added to `MeService`
// tomorrow should need a decision rather than inherit one.
func aboutYourself(method string) bool {
	return method == app.MeService_Get_FullMethodName
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
func Sets(db *ent.Client) frame.Sets { return policy{db}.sets }

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

	// every is a role that said so rather than a role that listed everything;
	// see `Role.every_method`. It is a field beside the map rather than the map
	// being filled in from the descriptors, because the two answers differ
	// after an upgrade: a list is what was true when it was written and this is
	// what is true now.
	every bool
}

func (h held) allows(method string) bool {
	if h.every {
		return true
	}

	_, ok := h.methods[method]

	return ok
}

// of reads the bindings a holder has, by being them or by being in a group.
func (p policy) of(ctx context.Context, who uuid.UUID) (held, error) {
	h := held{methods: map[string]struct{}{}}

	// The groups they are in, which is the other way a binding reaches them.
	gs, err := p.db.GroupMembership.Query().
		Where(
			groupmembership.DateErasedIsNil(),
			groupmembership.HasHolderWith(holder.IDEQ(who)),
		).
		QueryGroup().
		Where(group.DateErasedIsNil()).
		IDs(ctx)
	if err != nil {
		return held{}, err
	}

	q := p.db.Binding.Query().Where(
		binding.DateErasedIsNil(),
		binding.HasHolderWith(holder.IDEQ(who)),
	)
	if len(gs) > 0 {
		q = p.db.Binding.Query().Where(
			binding.DateErasedIsNil(),
			binding.Or(
				binding.HasHolderWith(holder.IDEQ(who)),
				binding.HasGroupWith(group.IDIn(gs...)),
			),
		)
	}

	vs, err := q.WithRole().WithSite().All(ctx)
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
	// written to be read together; see PLAN.md, D17.
	ts, err := p.db.TeamMembership.Query().
		Where(
			teammembership.DateErasedIsNil(),
			teammembership.HasHolderWith(holder.IDEQ(who)),
		).
		WithRole().
		WithTeam(func(q *ent.TeamQuery) { q.WithSite() }).
		All(ctx)
	if err != nil {
		return held{}, err
	}
	for _, v := range ts {
		if v.Edges.Role != nil {
			if v.Edges.Role.EveryMethod {
				h.every = true
			}
			for _, m := range v.Edges.Role.Methods {
				h.methods[m] = struct{}{}
			}
		}

		// And the **site** their team is in, which they have to be able to see
		// or they cannot see their own team.
		//
		// A site is coarser than a team, and that is the second axis being one
		// axis: `pd.Grouped` narrows to a set, `Site` is the set, and there is
		// no third level to narrow to. So being in a team means seeing its
		// site's rows, and seeing only your own team is the app filtering
		// rather than the wall narrowing. Written down here because it is the
		// kind of thing that leaks by being forgotten; see PLAN.md, D17.
		if v.Edges.Team != nil && v.Edges.Team.Edges.Site != nil {
			h.sites = append(h.sites, v.Edges.Team.Edges.Site.ID)
		}
	}

	for _, v := range vs {
		if v.Edges.Role != nil {
			if v.Edges.Role.EveryMethod {
				h.every = true
			}
			for _, m := range v.Edges.Role.Methods {
				h.methods[m] = struct{}{}
			}
		}

		if v.Edges.Site == nil {
			// Bound across the tenant, so there is no site to narrow by.
			h.anySite = true

			continue
		}

		h.sites = append(h.sites, v.Edges.Site.ID)
	}

	return h, nil
}

// Holds is `server/core`'s question, answered from the rows this policy already
// reads: may this person call this method, for this team?
//
// Two ways to yes, and they are the two scopes a role is referenced at. A
// **binding** is the tenant or a site and is never about one team, so holding
// one is enough by itself. A **team membership** is that team, so it counts
// only when the call is about it.
//
// One function rather than two, because it is one question. The gate asks a
// weaker version of it -- may you ever call this -- and a layer asks this one,
// and both read the same rows.
// Rules is what `server/core` needs to know about a caller, from the rows this
// policy already reads.
func Rules(db *ent.Client) core.Rules {
	return core.Rules{Holds: Holds(db), Granted: Granted(db)}
}

// Granted is every method somebody holds **through a binding**, which is what
// they may pass on.
//
// Bindings only. A role held in a team is scoped to that team, and letting it
// be bound tenant-wide would widen a scope rather than pass on a permission.
func Granted(db *ent.Client) core.Granted {
	return func(ctx context.Context, who pdid.Id) ([]string, bool, error) {
		vs, err := db.Binding.Query().
			Where(
				binding.DateErasedIsNil(),
				binding.HasHolderWith(holder.IDEQ(who.Uuid())),
			).
			WithRole().
			All(ctx)
		if err != nil {
			return nil, false, err
		}

		var ms []string
		for _, v := range vs {
			if v.Edges.Role == nil {
				continue
			}
			if v.Edges.Role.EveryMethod {
				return nil, true, nil
			}

			ms = append(ms, v.Edges.Role.Methods...)
		}

		return ms, false, nil
	}
}

func Holds(db *ent.Client) core.Holds {
	return func(ctx context.Context, who pdid.Id, method string, team pdid.Id) (bool, error) {
		vs, err := db.Binding.Query().
			Where(
				binding.DateErasedIsNil(),
				binding.HasHolderWith(holder.IDEQ(who.Uuid())),
			).
			WithRole().
			All(ctx)
		if err != nil {
			return false, err
		}
		for _, v := range vs {
			if allows(v.Edges.Role, method) {
				return true, nil
			}
		}

		if team == pdid.Nil {
			return false, nil
		}

		ts, err := db.TeamMembership.Query().
			Where(
				teammembership.DateErasedIsNil(),
				teammembership.HasHolderWith(holder.IDEQ(who.Uuid())),
				teammembership.HasTeamWith(entteam.IDEQ(team.Uuid())),
			).
			WithRole().
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

func allows(r *ent.Role, method string) bool {
	if r == nil {
		return false
	}
	if r.EveryMethod {
		return true
	}

	for _, m := range r.Methods {
		if m == method {
			return true
		}
	}

	return false
}

// Everything is what a caller effectively holds, for `server/me`.
//
// The same union the gate enforces, so that what a page shows and what the
// server allows come from one function rather than two that agree today.
func Everything(db *ent.Client) me.Held {
	p := policy{db}

	return func(ctx context.Context, who pdid.Id) ([]string, []pdid.Id, bool, error) {
		h, err := p.of(ctx, who.Uuid())
		if err != nil {
			return nil, nil, false, err
		}

		// A role that says "everything" is expanded here and nowhere else. A
		// page needs the list to decide what to draw; the gate reads the flag,
		// so what is enforced cannot fall behind what this enumerates.
		ms := make([]string, 0, len(h.methods))
		if h.every {
			ms = everyMethod()
		} else {
			for m := range h.methods {
				ms = append(ms, m)
			}
			slices.Sort(ms)
		}

		ks := make([]pdid.Id, 0, len(h.sites))
		for _, v := range h.sites {
			ks = append(ks, pdid.Id(v))
		}

		return ms, ks, h.anySite, nil
	}
}
