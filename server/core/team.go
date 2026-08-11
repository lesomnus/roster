package core

import (
	"context"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Who may change a team's membership.
//
// # Why it is here and not in the policy
//
// `gate.Policy` answers "may this actor call this method", and that is all it
// can answer: `gate.Call` carries the actor, their tenant, the actor's own row
// and the method, and **never the request**. "May you add somebody to *this*
// team" is a question about the request, so it is asked where the request is --
// a layer.
//
// The two halves are written to be read together. The gate lets a team's
// administrator through for `TeamMembership.Add` at all, because it is
// outermost and a refusal there means they never reach here. So this refusing
// the wrong team is not a second opinion, it is the other half of one answer.
//
// # Why it is built in rather than configured
//
// "The administrator of a team manages its members" is true of every deployment
// there will be. A configurable invariant is one that every deployment
// configures identically until one of them gets it wrong, and the one that gets
// it wrong is not the one that reads this comment.
//
// So it is roster's rule, tested once, and there is no row anywhere that turns
// it off.

// Holds answers what somebody may do, and is **given** to this package rather
// than queried here.
//
// The reason is not tidiness. What a caller holds is read out of bindings,
// group memberships and team memberships at once, and `cmd` already does that
// for `gate.Policy` -- against ent, because working out what somebody may do
// cannot itself require permission. Asking again here would be a second
// implementation of one question, and the two would drift.
//
// `team` is which team the call is about, and `pdid.Nil` asks only about the
// scopes that are not one. A nil `Holds` refuses everything a frame carries,
// which is the safe direction for a stack somebody built without it.
type Holds func(ctx context.Context, who pdid.Id, method string, team pdid.Id) (bool, error)

// mayChangeTeam refuses somebody who holds nothing for this team.
//
// Two ways to pass, and they are the two scopes a role is referenced at:
//
//   - a `Binding`, which is the tenant or a site, and reaches every team in it
//   - a `TeamMembership` of **this** team, whose role names the method
//
// A request with no frame is the deployment's own work through an unwalled
// server, and there is nobody to refuse; that is the door `init` and the key
// command come through, and it is a line of wiring rather than a privilege.
func (s Core) mayChangeTeam(ctx context.Context, method string, ref *app.TeamRef) error {
	f, ok := frame.From(ctx)
	if !ok {
		return nil
	}
	if ref == nil {
		return nil
	}
	if s.holds == nil {
		return statusDenied(method)
	}

	k, err := s.teamOf(ctx, ref)
	if err != nil {
		return err
	}

	ok, err = s.holds(ctx, f.Actor, method, k)
	if err != nil {
		return err
	}
	if !ok {
		return statusDenied(method)
	}

	return nil
}

// teamOf is the identifier a `TeamRef` names, read when it named something
// else.
func (s Core) teamOf(ctx context.Context, ref *app.TeamRef) (pdid.Id, error) {
	if b := ref.GetId(); len(b) > 0 {
		return pdid.From(b)
	}

	v, err := s.Next().Team().Get(ctx, app.TeamGetRequest_builder{
		Ref:    ref,
		Select: app.TeamSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetId())
}

func statusDenied(method string) error {
	return status.Errorf(codes.PermissionDenied,
		"%s: you hold nothing for that team", method)
}
