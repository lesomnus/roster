package core

import (
	"context"
	"slices"

	"github.com/lesomnus/protobuf-patch/patch"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
)

// Narrow takes methods out of a role; `role_svc.ext.proto` says why it is a
// verb of its own and not `Patch`, and what it takes out.
//
// # Three rules, and the one it does not have
//
// It is no grant: what it writes hands nobody anything. So it is not held to
// `mayGrant` over what it **leaves** -- that was granted when it was written,
// and leaving it in place hands it out again to nobody -- which is the rule
// [coreRole.Widen] takes from `Patch`, and the reason this does not go through
// `Patch`. What it is held to instead:
//
//   - **Nobody takes away what they could not have given** ([Core.mayTakeAway]).
//     Taking a method from a role stops everybody it reaches calling it, and
//     what somebody holds is also what keeps others from writing their
//     credential (`mayReach`): stripped to less than the caller holds, an
//     administrator is somebody the caller may reset the password of. Held to
//     taking only what they hold, the caller leaves everybody holding at least
//     what the caller does not -- and so leaves them out of reach.
//   - **A declared role is nobody's to narrow**, as it is nobody's to widen.
//   - **A tenant keeps somebody administering it** ([coreRole.keepsItsOwn]).
//
// What it does not have is a version from the caller. Taking out is the same
// whichever order two writes land in, so it reads the role, works out what is
// left, and writes that under the version it read -- reading again, as `Widen`
// does, when a write landed in between.
//
// # Written as a document, not a patch request
//
// A `RolePatchRequest` cannot clear a list -- a repeated field has no presence
// in it -- and taking every method out of a role is a narrowing like any other.
// So the write is a patch document, applied to the server under this layer,
// which `Apply` is for: closed on the wire, and how a verb written by hand writes
// (`grpcx.GeneralWrite`).
func (s coreRole) Narrow(ctx context.Context, req *app.RoleNarrowRequest) (*app.Role, error) {
	if len(req.GetMethods()) == 0 {
		return nil, pderr.Invalidf("methods", "what to take out; a narrowing that names nothing is a mistake rather than a no")
	}

	for range widenTries {
		got, err := s.RoleServiceServer.Get(ctx, app.RoleGetRequest_builder{
			Ref: req.GetRef(),
			Select: app.RoleSelect_builder{
				All:    z.Ptr(true),
				Tenant: app.TenantSelect_builder{}.Build(),
				Site:   app.SiteSelect_builder{}.Build(),
			}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}
		if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
			return nil, err
		}

		out, left := narrowed(got.GetMethods(), req.GetMethods())
		for _, m := range req.GetMethods() {
			if by := slices.IndexFunc(left, func(h string) bool { return frame.Covers(h, m) }); by >= 0 {
				return nil, status.Errorf(codes.FailedPrecondition,
					"methods: the role holds %s through %s, which is wider than what was named and stays; "+
						"take %s out instead, and widen the role by what it should keep", m, left[by], left[by])
			}
		}
		if len(out) == 0 {
			// Nothing to take out, and so no write: the role as it is.
			return got, nil
		}

		at := pdid.Nil
		if b := got.GetSite().GetId(); len(b) > 0 {
			if at, err = pdid.From(b); err != nil {
				return nil, err
			}
		}
		if err := s.mayTakeAway(ctx, "methods", out, at); err != nil {
			return nil, err
		}

		v, moved, err := s.keepsItsOwn(ctx, got, left)
		if moved {
			// Written by somebody else since it was read.
			continue
		}

		return v, err
	}

	return nil, status.Errorf(codes.Aborted,
		"the role changed each of the %d times it was read, so nothing was taken out; ask again", widenTries)
}

// keepsItsOwn writes `left` into the role it was read as, and refuses it when
// that would leave the tenant with nobody administering it: nobody there and
// not suspended who holds [EveryMethod] across the whole tenant -- what the
// tenant was made with ([coreTenant.Add]), and what puts anything back. Without
// it, putting a tenant right is the deployment operator's, at the box.
//
// # The rule, exactly
//
// A narrowing is refused when the role covered [EveryMethod] and what is left
// does not, somebody administered the tenant before it, and nobody does after
// it ([Rules.Administered]: through a binding with no site, to them or to a
// group they are in). So it is about this write and nothing else: a tenant that
// had nobody administering it is not refused a narrowing for it, a role that
// never covered it narrows as freely as any, and an administrator may narrow
// away their own administration while somebody else keeps theirs.
//
// The deployment's own work is not held to it -- a command at the box, the
// operators' port -- because that is who puts a tenant right, and a tenant
// locked out by an operator is one an operator meant.
//
// # Counted where two narrowings cannot both count
//
// It is a count of other rows, [Core.only]'s case: two administrators each
// narrowing one of the last two roles that administer a tenant would each see
// the other's still there, and both go through. So it is counted in one
// transaction with the write, after a write on the **tenant's** row that every
// such narrowing takes first ([Lock]), and asked about that transaction's own
// rows -- before the write, and after it.
//
// It answers whether the write was refused for a version that moved, which is
// read again rather than answered.
func (s coreRole) keepsItsOwn(ctx context.Context, got *app.Role, left []string) (*app.Role, bool, error) {
	write := func(ctx context.Context, sv app.RoleServiceServer) (*app.Role, bool, error) {
		values := make([]patch.Value, len(left))
		for i, m := range left {
			values[i] = patch.Str(m)
		}
		doc, err := patch.New(string((&app.Role{}).ProtoReflect().Descriptor().FullName()),
			patch.Target(patch.Name("date_updated")).Test(stamp(got.GetDateUpdated())),
			patch.Target(patch.Name("methods")).Assign(patch.List(values...)),
		)
		if err != nil {
			return nil, false, err
		}

		v, err := sv.Apply(ctx, app.RoleApplyRequest_builder{
			Ref:   app.RoleRef_builder{Id: got.GetId()}.Build(),
			Patch: doc,
		}.Build())
		if status.Code(err) == codes.FailedPrecondition {
			return nil, true, err
		}

		return v, false, err
	}

	covered := func(ms []string) bool {
		return slices.ContainsFunc(ms, func(m string) bool { return frame.Covers(m, EveryMethod) })
	}
	if s.deployment(ctx) || !covered(got.GetMethods()) || covered(left) {
		return write(ctx, s.RoleServiceServer)
	}
	if s.rules.Administered == nil {
		return nil, false, status.Errorf(codes.PermissionDenied,
			"methods: this server cannot say who else administers the tenant, so it will not take %s out of a role", EveryMethod)
	}

	tenant, err := pdid.From(got.GetTenant().GetId())
	if err != nil {
		return nil, false, err
	}

	var (
		v     *app.Role
		moved bool
	)
	err = s.only(ctx, tenant.Bytes(), func(next app.Server, tx dialect.Driver) error {
		before, err := s.rules.Administered(ctx, tx, tenant, EveryMethod)
		if err != nil {
			return err
		}
		if v, moved, err = write(ctx, next.Role()); err != nil {
			return err
		}
		after, err := s.rules.Administered(ctx, tx, tenant, EveryMethod)
		if err != nil {
			return err
		}
		if before && !after {
			return status.Errorf(codes.FailedPrecondition,
				"methods: taking %s out of %s leaves nobody administering the tenant -- nobody there and not suspended "+
					"holds it across the whole tenant any more, so nobody in it could put it back. "+
					"Bind it to somebody else first", EveryMethod, got.GetAlias())
		}

		return nil
	})
	if moved {
		return nil, true, err
	}

	return v, false, err
}

// narrowed is `have` split into what one of `take` covers -- taken out -- and
// what is left, each in the order the role holds them.
func narrowed(have, take []string) (out, left []string) {
	for _, h := range have {
		if slices.ContainsFunc(take, func(m string) bool { return frame.Covers(m, h) }) {
			out = append(out, h)
		} else {
			left = append(left, h)
		}
	}

	return out, left
}

// mayTakeAway refuses a caller taking out of a role a method they do not hold
// where the role may be bound -- [Core.mayGrant]'s comparison, read for the
// other act: nobody takes away what they could not have given. See
// [coreRole.Narrow] for why it is the rule, and what it keeps out of reach.
//
// Waived for the deployment's own work, with no frame, as `mayGrant` is.
func (s Core) mayTakeAway(ctx context.Context, field string, methods []string, at pdid.Id) error {
	f, ok := frame.From(ctx)
	if !ok {
		return nil
	}
	if s.rules.Granted == nil {
		return status.Error(codes.PermissionDenied,
			"this server cannot say what you hold, so it will not let you take anything away")
	}

	held, err := s.rules.Granted(ctx, f.Actor)
	if err != nil {
		return err
	}

	reaches := reachingAt(held, at)
	for _, m := range methods {
		if !slices.ContainsFunc(reaches, func(v string) bool { return frame.Covers(v, m) }) {
			return status.Errorf(codes.PermissionDenied,
				"%s: you do not hold %s here, so you may not take it away", field, m)
		}
	}

	return nil
}
