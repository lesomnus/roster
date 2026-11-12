package core

import (
	"context"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
)

// widenTries is how many times [coreRole.Widen] reads a role again because a
// write landed between its read and its patch.
const widenTries = 3

// Widen is `Patch` that only adds; `role_svc.ext.proto` says why it is a verb
// of its own. This is how it stays one rule rather than two: it reads the
// role, works out what is new, and hands the whole list to [coreRole.Patch] --
// which holds it to `mayGrant` at the role's own site and refuses a declared
// row, exactly as it would a patch the caller had written.
//
// Under the version it read, so a write that landed in between is not undone:
// the patch is refused for it and the role is read again. Adding is the same
// whichever order two writes land in, so reading again is always right, and
// it does so a few times before saying the role would not hold still.
func (s coreRole) Widen(ctx context.Context, req *app.RoleWidenRequest) (*app.Role, error) {
	if len(req.GetMethods()) == 0 {
		return nil, pderr.Invalidf("methods", "what to add; a widen that names nothing is a mistake rather than a no")
	}

	for range widenTries {
		got, err := s.RoleServiceServer.Get(ctx, app.RoleGetRequest_builder{
			Ref:    req.GetRef(),
			Select: app.RoleSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}
		// Before anything is worked out, and on every read: a declared role
		// is refused whether or not there is anything to add, and the refusal
		// is the same code a version that moved is answered with -- so it is
		// never left to the patch to say.
		if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
			return nil, err
		}

		next := widened(got.GetMethods(), req.GetMethods())
		if len(next) == len(got.GetMethods()) {
			// Nothing new, and so no write: the role as it is.
			return got, nil
		}

		v, err := s.Patch(ctx, app.RolePatchRequest_builder{
			Ref:         app.RoleRef_builder{Id: got.GetId()}.Build(),
			Methods:     next,
			DateUpdated: got.GetDateUpdated(),
		}.Build())
		if status.Code(err) == codes.FailedPrecondition {
			// Written by somebody else since it was read.
			continue
		}

		return v, err
	}

	return nil, status.Errorf(codes.Aborted,
		"the role changed each of the %d times it was read, so nothing was added; ask again", widenTries)
}

// widened is `have` and each of `add` that nothing in it covers yet, in the
// order they were named and each once.
//
// Covered and not merely spelled the same: `/loft.Permission/RunWork` added to
// a role holding `/loft.*/*` would grant nothing more, and kept beside it, it
// would outlive the wider pattern being taken out -- which a tenant narrowing
// the role would not expect.
func widened(have, add []string) []string {
	next := slices.Clone(have)
	for _, m := range add {
		if slices.ContainsFunc(next, func(h string) bool { return frame.Covers(h, m) }) {
			continue
		}
		next = append(next, m)
	}

	return next
}
