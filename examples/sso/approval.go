package sso

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// Admissions is the approval gate: somebody may sign in and still do nothing
// until an administrator lets them in.
//
// # It needs no change to roster, and that is the point
//
// roster refuses everything to a holder with no binding -- that is the whole
// of its authorization, deny by default. So "signed in but may do nothing" is
// not a state to build; it is the state a person is already in the moment they
// exist and before anybody has written them a role. `Enrolling` provisions
// exactly that: a Holder, an Identity, and no role. A person it just made can
// read their own record (`MeService.Get` is waived) and nothing else -- every
// gated call is `PermissionDenied` until this gate opens.
//
// So this file is the **other half**: not "how to hold somebody back," which
// roster does on its own, but "how to let them in" -- and letting them in is
// one write.
//
// # Admission is a group, because a group is a grant
//
// An approved account is one that holds *the ordinary role* -- whatever a
// deployment decides a member may do. The cheapest way to hand a role to a
// changing set of people is a `Group` the role is bound to: being in the group
// is holding the role, and letting somebody in is one `GroupMembership.Add`.
//
// That the group **carries a role** is what makes admission safe to grant to a
// tenant's own administrators rather than only to the deployment: adding
// somebody to a group that carries a role hands out that role, so roster holds
// the write to the same rule as `Binding.Add` -- *nobody hands out what they do
// not hold*. An administrator may admit people into no more than they
// themselves have. It is D40 (`server/core/escalate.go`) in a sentence, and it
// is why "who may approve" needs no new concept: it is answered by the
// escalation rule every grant already meets.
//
// # What it is not
//
// Not `Holder.Disable`. A suspended holder cannot sign in at all -- the door is
// shut. An unadmitted one signs in fine and then finds every door inside
// locked, which is the difference the requirement turns on: *they authenticated;
// they may do nothing yet.* Suspend below is the inverse of admission and stays
// on this side of that line -- it takes a role away, it does not stop a
// sign-in.
//
// # Why there is no "who is waiting" here
//
// Because roster does not answer it, deliberately: it lists rows, it does not
// walk a graph back from a group to its members (D17), and there is no filter
// for "a holder with no role." A console that wants a queue keeps its own
// projection -- fed by the provisioning it performs, or by `SyncService` -- and
// checks it against roster, which is the authority and not the index.
// [Admissions.Admitted] is the point check that authority answers.
//
// # The control plane too
//
// Nothing here is about which plane it is. A control-plane holder -- an
// operator or a service the deployment just created -- holds nothing until a
// binding says otherwise, exactly as a customer's person does, because the
// control plane is the same roster on a second database (D15). Point an
// `Admissions` at a control-plane connection and a control-plane group and it
// admits operators the same way; the only thing that changes is who the
// administrator is, which there is the deployment owner rooted in `init`.
type Admissions struct {
	// roster carries the **administrator's** credential, not the front door's.
	// Admitting is a grant, and the front door -- the one service reachable
	// from the open internet -- is the last thing that should hold the right
	// to make one. These are two different callers on purpose.
	roster rstr.Client

	// members is the group whose members hold the ordinary role. A deployment
	// writes it once, with the role bound to it, wherever it decides what an
	// account is; `approval_test.go` shows the three writes.
	members pdid.Id
}

// NewAdmissions is the gate over one group, as one administrator.
func NewAdmissions(roster rstr.Client, membersGroup pdid.Id) Admissions {
	return Admissions{roster: roster, members: membersGroup}
}

// Admit lets somebody in: it adds them to the members group, and being in it is
// holding the role bound to it.
//
// Refused -- `PermissionDenied` -- unless the administrator holds at least what
// that role grants. That is not this function's check; it is roster's, on
// `GroupMembership.Add`, and it is the same one that stops anybody handing out
// a permission they lack.
//
// Idempotent in the way that matters: admitting somebody already in answers an
// error naming the duplicate, which a caller that does not care may ignore --
// the state afterwards is the same either way.
func (a Admissions) Admit(ctx context.Context, person pdid.Id) error {
	_, err := a.roster.GroupMembership().Add(ctx, rstr.GroupMembershipAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: person.Bytes()}.Build(),
		Group:  rstr.GroupRef_builder{Id: a.members.Bytes()}.Build(),
	}.Build())

	return err
}

// Suspend is the inverse, and stays on this side of the sign-in.
//
// It removes them from the members group, so they hold the role no longer and
// every door it opened is shut. What it does **not** do is stop them signing in
// -- that is `Holder.Disable`, a heavier act with a different meaning. Somebody
// suspended this way is back where an unadmitted person is: authenticated, and
// able to do nothing until let in again.
//
// Erasing a membership that is not there succeeds, which is roster's rule for
// every erase and the reason a caller need not check first.
func (a Admissions) Suspend(ctx context.Context, person pdid.Id) error {
	_, err := a.roster.GroupMembership().Erase(ctx, a.membership(person))

	return err
}

// Admitted answers whether somebody has been let in -- a point check, because
// the queue is nobody's to enumerate here (see the type comment).
func (a Admissions) Admitted(ctx context.Context, person pdid.Id) (bool, error) {
	_, err := a.roster.GroupMembership().Get(ctx, rstr.GroupMembershipGetRequest_builder{
		Ref: a.membership(person),
	}.Build())

	switch status.Code(err) {
	case codes.OK:
		return true, nil
	case codes.NotFound:
		return false, nil
	default:
		return false, err
	}
}

// membership names the one row that is this person in the members group.
//
// By its (holder, group) index rather than by listing, because that pair is
// unique and roster generated a way to name it -- the same reason `find` names
// an identity by its three parts instead of filtering.
func (a Admissions) membership(person pdid.Id) *rstr.GroupMembershipRef {
	return rstr.GroupMembershipRef_builder{
		Member: rstr.GroupMembershipRefByMember_builder{
			Holder: rstr.HolderRef_builder{Id: person.Bytes()}.Build(),
			Group:  rstr.GroupRef_builder{Id: a.members.Bytes()}.Build(),
		}.Build(),
	}.Build()
}
