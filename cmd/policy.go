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
// be too. Here they are named one at a time, and a method added to `MeService`
// tomorrow needs a decision rather than inheriting one.
//
// # The two writes, and why they are on the list
//
// `Unlink` and `SignOutEverywhere` write, which is the part worth stopping on.
// They are here for `Get`'s reason and not by extension of it: neither takes a
// subject, so neither can be pointed at anybody else, and what each does is
// something a person may do to their own account by definition.
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
func aboutYourself(method string) bool {
	switch method {
	case app.MeService_Get_FullMethodName,
		app.MeService_Unlink_FullMethodName,
		app.MeService_SignOutEverywhere_FullMethodName:
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
			groupmembership.HasHolderWith(holder.IDEQ(who)),
		).
		QueryGroup().
		Where(group.DateErasedIsNil()).
		IDs(ctx)
	if err != nil {
		return nil, err
	}

	q := db.Binding.Query().Where(
		binding.DateErasedIsNil(),
		binding.HasHolderWith(holder.IDEQ(who)),
	)
	if len(gs) > 0 {
		q = db.Binding.Query().Where(
			binding.DateErasedIsNil(),
			binding.Or(
				binding.HasHolderWith(holder.IDEQ(who)),
				binding.HasGroupWith(group.IDIn(gs...)),
			),
		)
	}

	return q.WithRole().WithSite().All(ctx)
}

// of reads the bindings a holder has, by being them or by being in a group.
func (p policy) of(ctx context.Context, who uuid.UUID) (held, error) {
	h := held{methods: map[string]struct{}{}}

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

// Rules is everything `server/core` has to know about a caller and cannot work
// out, from the rows this policy already reads.
//
// Four answers to what look like one question and are not, which is what took
// three findings to see. `Holds` is about one call and one team. `Granted` is
// what may be **passed on** and reads bindings alone. `Joining` is what a group
// holds, so that putting somebody in one is weighed like writing the binding.
// `Holding` is everything somebody **has**, by any path -- which is the one
// `mayReach` needs, because there a path not walked allows rather than refuses.
func Rules(db *ent.Client) core.Rules {
	return core.Rules{
		Holds:   Holds(db),
		Granted: Granted(db),
		Joining: Joining(db),
		Holding: Holding(db),
	}
}

// Granted is every pattern somebody holds through a binding, and **where**.
//
// # Why the site travels with the methods
//
// Because a permission held in one place is not one to hand out in another,
// and flattened into a single list the two are the same strings. That is not
// hypothetical: somebody bound to a role in Seoul had those methods in a flat
// list with no trace of Seoul on them, so `mayGrant` compared them against a
// tenant-wide write and agreed. Two RPCs later they held the tenant.
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
func Granted(db *ent.Client) core.Granted {
	return func(ctx context.Context, who pdid.Id) ([]core.Grant, error) {
		vs, err := bindingsReaching(ctx, db, who.Uuid())
		if err != nil {
			return nil, err
		}

		return grantsOf(vs), nil
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
				binding.HasGroupWith(group.IDEQ(id.Uuid()), group.DateErasedIsNil()),
			).
			WithRole().
			WithSite().
			All(ctx)
		if err != nil {
			return nil, err
		}

		return grantsOf(vs), nil
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
func Holding(db *ent.Client) core.Holding {
	return func(ctx context.Context, who pdid.Id) ([]core.Grant, error) {
		vs, err := bindingsReaching(ctx, db, who.Uuid())
		if err != nil {
			return nil, err
		}

		gs := grantsOf(vs)

		ts, err := db.TeamMembership.Query().
			Where(
				teammembership.DateErasedIsNil(),
				teammembership.HasHolderWith(holder.IDEQ(who.Uuid())),
			).
			WithRole().
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

		return gs, nil
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
			g.Site = pdid.Id(v.Edges.Site.ID)
		}

		gs = append(gs, g)
	}

	return gs
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
func Holds(db *ent.Client) core.Holds {
	return func(ctx context.Context, who pdid.Id, method string, team pdid.Id) (bool, error) {
		// The same set the gate let them through on -- see
		// [bindingsReaching]. A narrower reading here would refuse the call
		// after the gate allowed it, which is the two halves of one answer
		// disagreeing rather than one of them being careful.
		vs, err := bindingsReaching(ctx, db, who.Uuid())
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

	for _, m := range r.Methods {
		if frame.Covers(m, method) {
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

		ks := make([]pdid.Id, 0, len(h.sites))
		for _, v := range h.sites {
			ks = append(ks, pdid.Id(v))
		}

		return ms, ks, h.anySite, nil
	}
}
