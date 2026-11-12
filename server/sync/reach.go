package sync

import (
	"context"
	"slices"
	"uuid"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/predicate"
	"github.com/lesomnus/roster/internal/ent/role"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/pd"
)

// Reached is who a write to a row of the access model -- a role, a binding, a
// membership of a group or a team, a group -- may have changed what they may
// call for: the paths `cmd/policy.go` walks to answer `Holder.Reaches`, read
// from the other end. Nobody, for a row this caller may not see.
//
// What it answers is not news yet. Each of them is read through the wall
// before anything is said of them ([server.reach]), so a path that reaches
// past the caller's view tells the caller nothing.
type Reached func(ctx context.Context, k pdid.Id) ([]pdid.Id, error)

// Reaching is [Reached] on this deployment's database: the row that was
// written read by its identifier, narrowed by `narrow` -- **the** scope the
// walled server narrows by, handed over as [Erased] takes it -- and read
// whether or not it is erased. An erased binding is the write most likely to
// take something away from somebody, and the walled server answers nothing
// about it at all.
//
// Who it reaches is read as it is now: the live bindings of a role, the live
// members of a group. A member who left in the same write is the write to
// their membership, read on its own.
func Reaching(db *ent.Client, narrow bare.Scope) Reached {
	r := reaching{db: db, narrow: narrow}

	return func(ctx context.Context, k pdid.Id) ([]pdid.Id, error) {
		switch k.Domain() {
		case pd.BindingDomain:
			return r.binding(ctx, k.Uuid())
		case pd.GroupMembershipDomain:
			return r.groupMembership(ctx, k.Uuid())
		case pd.TeamMembershipDomain:
			return r.teamMembership(ctx, k.Uuid())
		case pd.RoleDomain:
			return r.role(ctx, k.Uuid())
		case pd.GroupDomain:
			return r.group(ctx, k.Uuid())
		}

		return nil, nil
	}
}

type reaching struct {
	db     *ent.Client
	narrow bare.Scope
}

// binding is the person a binding names, or everybody in the group it names.
func (r reaching) binding(ctx context.Context, k uuid.UUID) ([]pdid.Id, error) {
	q := r.db.Binding.Query().Where(binding.Id(k))
	p, err := r.narrow.BindingScope(ctx)
	if err != nil {
		return nil, err
	}
	if p != nil {
		q = q.Where(p)
	}
	v, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return r.bound(ctx, v)
}

func (r reaching) bound(ctx context.Context, v *ent.Binding) ([]pdid.Id, error) {
	if h := pdid.Id(v.HolderId); !h.IsZero() {
		return []pdid.Id{h}, nil
	}
	if g := pdid.Id(v.GroupId); !g.IsZero() {
		return r.members(ctx, groupmembership.GroupIdEQ(v.GroupId))
	}

	return nil, nil
}

// groupMembership is the person in a group, or out of it: what the group's
// bindings hand them, and the role a manager of it holds there.
func (r reaching) groupMembership(ctx context.Context, k uuid.UUID) ([]pdid.Id, error) {
	q := r.db.GroupMembership.Query().Where(groupmembership.Id(k))
	p, err := r.narrow.GroupMembershipScope(ctx)
	if err != nil {
		return nil, err
	}
	if p != nil {
		q = q.Where(p)
	}
	v, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return []pdid.Id{pdid.Id(v.HolderId)}, nil
}

// teamMembership is the person holding a role in a team, or not any more.
func (r reaching) teamMembership(ctx context.Context, k uuid.UUID) ([]pdid.Id, error) {
	q := r.db.TeamMembership.Query().Where(teammembership.Id(k))
	p, err := r.narrow.TeamMembershipScope(ctx)
	if err != nil {
		return nil, err
	}
	if p != nil {
		q = q.Where(p)
	}
	v, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return []pdid.Id{pdid.Id(v.HolderId)}, nil
}

// role is everybody a role reaches: through a live binding, to them or to a
// group they are in, and by holding it in a group or a team.
func (r reaching) role(ctx context.Context, k uuid.UUID) ([]pdid.Id, error) {
	q := r.db.Role.Query().Where(role.Id(k))
	p, err := r.narrow.RoleScope(ctx)
	if err != nil {
		return nil, err
	}
	if p != nil {
		q = q.Where(p)
	}
	if ok, err := q.Exist(ctx); err != nil || !ok {
		return nil, err
	}

	bs, err := r.db.Binding.Query().Where(binding.RoleIdEQ(k), binding.DateErasedIsNil()).All(ctx)
	if err != nil {
		return nil, err
	}
	var out []pdid.Id
	for _, b := range bs {
		who, err := r.bound(ctx, b)
		if err != nil {
			return nil, err
		}
		out = append(out, who...)
	}

	managers, err := r.members(ctx, groupmembership.RoleIdEQ(k))
	if err != nil {
		return nil, err
	}
	teams, err := r.db.TeamMembership.Query().
		Where(teammembership.RoleIdEQ(k), teammembership.DateErasedIsNil()).
		QueryHolder().
		Ids(ctx)
	if err != nil {
		return nil, err
	}
	out = append(out, managers...)
	for _, v := range teams {
		out = append(out, pdid.Id(v))
	}
	slices.SortFunc(out, func(a, b pdid.Id) int { return slices.Compare(a[:], b[:]) })

	return slices.Compact(out), nil
}

// group is everybody in a group that was erased, whose bindings stopped
// reaching them with it (`bindingsReaching` reads only live groups). A group
// that was renamed or relabelled reaches nobody differently, and is nobody's
// news.
func (r reaching) group(ctx context.Context, k uuid.UUID) ([]pdid.Id, error) {
	q := r.db.Group.Query().Where(group.Id(k), group.DateErasedNotNil())
	p, err := r.narrow.GroupScope(ctx)
	if err != nil {
		return nil, err
	}
	if p != nil {
		q = q.Where(p)
	}
	if ok, err := q.Exist(ctx); err != nil || !ok {
		return nil, err
	}

	return r.members(ctx, groupmembership.GroupIdEQ(k))
}

// members is the people of the live memberships `which` picks.
func (r reaching) members(ctx context.Context, which predicate.GroupMembership) ([]pdid.Id, error) {
	vs, err := r.db.GroupMembership.Query().
		Where(which, groupmembership.DateErasedIsNil()).
		QueryHolder().
		Ids(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]pdid.Id, len(vs))
	for i, v := range vs {
		out[i] = pdid.Id(v)
	}

	return out, nil
}
