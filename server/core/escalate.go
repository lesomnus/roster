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

// Granted is every method somebody holds through a binding, and therefore may
// pass on.
//
// The bool is "all of them", which is a second return rather than a list with
// everything in it -- the same shape `frame.Narrow` and `frame.Sets` use, for
// the same reason. An empty list has to mean **none**, or the safe answer and
// the open one are one value and a bug that loses the list opens the door.
//
// It is what `Role.every_method` becomes here: a role that says "everything"
// rather than listing it, so that a method added by an upgrade is covered by a
// binding written before it existed.
type Granted func(ctx context.Context, who pdid.Id) ([]string, bool, error)

// mayGrant refuses a caller handing out a method they do not hold.
//
// A request with no frame is the deployment's own work through an unwalled
// server: `init`, the key command, a migration. There is nobody to refuse, and
// that door is a line of wiring a reader can find rather than a privilege
// anybody holds.
func (s Core) mayGrant(ctx context.Context, field string, methods []string) error {
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

	held, every, err := s.rules.Granted(ctx, f.Actor)
	if err != nil {
		return err
	}
	if every {
		return nil
	}

	for _, m := range methods {
		if !slices.Contains(held, m) {
			return status.Errorf(codes.PermissionDenied,
				"%s: you do not hold %s, so you may not grant it", field, m)
		}
	}

	return nil
}

// mayGrantEverything refuses a caller writing `Role.every_method` unless they
// already hold it.
//
// It is separate from [Core.mayGrant] because it cannot be expressed as a list.
// "Everything" is not the methods that exist today -- that is the whole reason
// the flag is a flag -- so it cannot be compared element by element against
// what somebody holds. Only somebody who already holds everything holds this.
//
// Which makes it the one privilege that never widens by being passed on: the
// first comes from `roster init`, through an unwalled server where there is no
// frame, and every one after it descends from somebody who had it.
func (s Core) mayGrantEverything(ctx context.Context, field string) error {
	f, ok := frame.From(ctx)
	if !ok {
		return nil
	}
	if s.rules.Granted == nil {
		return status.Error(codes.PermissionDenied,
			"this server cannot say what you hold, so it will not let you grant anything")
	}

	_, every, err := s.rules.Granted(ctx, f.Actor)
	if err != nil {
		return err
	}
	if !every {
		return status.Errorf(codes.PermissionDenied,
			"%s: you do not hold every method, so you may not grant it", field)
	}

	return nil
}

// methodsOf is what a role allows, read through this stack so that a caller who
// cannot see the role cannot bind it either.
//
// The bool is `Role.every_method`, and it has to come back separately for the
// reason that flag exists at all: such a role's `methods` column is **empty**,
// so a caller checking only the list would find nothing to refuse and bind the
// widest role there is to anybody. It is the empty-list-means-two-things trap
// one layer up from `frame.Grant`, and it is answered the same way.
func (s Core) methodsOf(ctx context.Context, ref *app.RoleRef) ([]string, bool, error) {
	if ref == nil {
		return nil, false, nil
	}

	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return nil, false, err
	}

	return v.GetMethods(), v.GetEveryMethod(), nil
}
