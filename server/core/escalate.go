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
type Granted func(ctx context.Context, who pdid.Id) ([]string, error)

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

	held, err := s.rules.Granted(ctx, f.Actor)
	if err != nil {
		return err
	}

	for _, m := range methods {
		// One of theirs has to cover it **on its own**. Asking whether the
		// union covers it would let somebody holding every service of a package
		// hand out the package -- true today and wrong the moment a service is
		// added, which is the widening this exists to refuse. See
		// `frame.Covers`.
		if !slices.ContainsFunc(held, func(v string) bool { return frame.Covers(v, m) }) {
			return status.Errorf(codes.PermissionDenied,
				"%s: you do not hold %s, so you may not grant it", field, m)
		}
	}

	return nil
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
