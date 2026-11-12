package core

import (
	"context"
	"slices"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// A tenant keeps somebody administering it.
//
// # The rule, exactly
//
// Somebody administers a tenant when they are there and not suspended, and
// hold a pattern that covers [EveryMethod] on its own -- what the tenant was
// made with ([coreTenant.Add]), and what puts anything back -- through a
// binding with no site, to them or to a group of the tenant's they are in
// ([Rules.Administered]). A write that leaves the tenant with nobody doing
// that, when somebody did before it, is refused: without them, putting the
// tenant right is the deployment's operator's, at the box.
//
// It is about the write and nothing else. A tenant nobody administered is not
// refused anything for it, a write that never touched administration is not
// asked, and an administrator may give up their own while somebody else keeps
// theirs. And it is every write that can do it on the wire, because the one
// left open is the one a tenant locks itself out with: taking the pattern out
// of a role (`Role.Narrow`), erasing the role, unbinding it, erasing the group
// it is bound to or somebody's membership of that group, suspending or erasing
// the last person who holds it -- and `Role.Patch`, which is closed on the
// wire and checked here as `escalate.go` checks it.
//
// A directory's suspension is not (`Holder.Deactivate`, `directory.go`): it is
// the directory's word about who works there, and refused, the person it
// removed would go on signing in -- worse than a tenant an operator unlocks.
//
// The deployment's own work is not held to it -- a command at the box, the
// operators' port -- because that is who puts a tenant right, and a tenant
// locked out by an operator is one an operator meant.
//
// # Counted where two writes cannot both count
//
// It is a count of other rows, [Core.only]'s case: two administrators each
// taking away one of the last two ways the tenant is administered would each
// see the other's still there, and both go through. So it is counted in one
// transaction with the write, after a write on the **tenant's** row that every
// such write takes first ([Lock]), and asked about that transaction's own rows
// -- before the write, and after it.

// administers is whether one of `methods` covers [EveryMethod] on its own:
// whether a grant of them administers the tenant.
func administers(methods []string) bool {
	return slices.ContainsFunc(methods, func(m string) bool { return frame.Covers(m, EveryMethod) })
}

// keepsAdministered makes `write` and refuses it when it leaves `tenant` with
// nobody administering it; `could` is whether it may at all, and a write that
// may not is made as it always was, with no lock taken. `what` is the write,
// in words, for the refusal: "unbinding everything from admin".
//
// `write` is handed the server to write through: inside the transaction, the
// one under this layer bound to it.
func (s Core) keepsAdministered(ctx context.Context, tenant pdid.Id, could bool, what string, write func(next app.Server) error) error {
	if !could || s.deployment(ctx) {
		return write(s.Next())
	}
	if s.rules.Administered == nil {
		return status.Errorf(codes.PermissionDenied,
			"this server cannot say who else administers the tenant, so it will not go on with %s", what)
	}

	return s.only(ctx, tenant.Bytes(), func(next app.Server, tx dialect.Driver) error {
		before, err := s.rules.Administered(ctx, tx, tenant, EveryMethod)
		if err != nil {
			return err
		}
		if err := write(next); err != nil {
			return err
		}
		after, err := s.rules.Administered(ctx, tx, tenant, EveryMethod)
		if err != nil {
			return err
		}
		if before && !after {
			return status.Errorf(codes.FailedPrecondition,
				"%s leaves nobody administering the tenant -- nobody there and not suspended holds %s across the whole tenant any more, "+
					"so nobody in it could put it back. Give it to somebody else first", what, EveryMethod)
		}

		return nil
	})
}

// grantsAdministration is whether any of `gs` is held across the whole tenant
// and administers it.
func grantsAdministration(gs []Grant) bool {
	return slices.ContainsFunc(gs, func(g Grant) bool { return g.Site == pdid.Nil && administers(g.Methods) })
}

// mayTakeAll is [Core.mayTakeAway] over every grant in `gs`, each at its own
// scope: what a row granted, taken away with it.
func (s Core) mayTakeAll(ctx context.Context, field string, gs []Grant) error {
	for _, g := range gs {
		if err := s.mayTakeAway(ctx, field, g.Methods, g.Site); err != nil {
			return err
		}
	}

	return nil
}
