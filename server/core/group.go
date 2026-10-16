package core

import (
	"context"
	"slices"
	"strings"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Who may change a group's members.
//
// The rule a team has (`team.go`), for the same reason and in the same two
// halves: the gate answers "may you ever call `GroupMembership.Add`" and
// cannot see which group, so it lets a group's manager through, and this layer
// refuses every group but theirs. Two ways to pass:
//
//   - the method held through a binding, which reaches every group of the
//     scope it was made in -- a tenant's administrator;
//   - the method held through a role in **this** group's own membership -- its
//     managers.
//
// What it does not replace is `mayJoin`. A group bound to roster's roles still
// asks whoever puts somebody into it to hold what the group hands out. A
// manager is a member and holds it already; somebody who edits memberships
// across the tenant without holding it is refused there, as before.
func (s Core) mayChangeGroup(ctx context.Context, method string, ref *app.GroupRef) error {
	f, ok := frame.From(ctx)
	if !ok {
		return nil
	}
	if ref == nil {
		return nil
	}
	if s.rules.Holds == nil {
		return groupDenied(method)
	}

	k, err := s.groupOf(ctx, ref)
	if err != nil {
		return err
	}

	ok, err = s.rules.Holds(ctx, f.Actor, method, k)
	if err != nil {
		return err
	}
	if !ok {
		return groupDenied(method)
	}

	return nil
}

func groupDenied(method string) error {
	return status.Errorf(codes.PermissionDenied,
		"%s: you hold nothing for that group", method)
}

// managing is what a role held in a group may name: the writes that change
// that group's members, and nothing else.
//
// Because the gate unions a role held in a group into what somebody may ever
// call, and only these two are narrowed back to the one group by
// [Core.mayChangeGroup]. A read named here would read every group's members,
// and anything else would be held across the tenant -- a binding by another
// name, written by whoever may appoint a manager.
var managing = []string{
	app.GroupMembershipService_Add_FullMethodName,
	app.GroupMembershipService_Erase_FullMethodName,
}

// mayHoldInGroup refuses a role that would reach past the group it is held in:
// one bound to a site, or naming anything [managing] does not.
func (s Core) mayHoldInGroup(ctx context.Context, ref *app.RoleRef) error {
	if ref == nil {
		return nil
	}

	at, err := s.siteOfRole(ctx, ref)
	if err != nil {
		return err
	}
	if at != pdid.Nil {
		return pderr.Invalidf("role",
			"a role held in a group belongs to no site; it names what changes the group's members, and that is not in a site")
	}

	ms, err := s.methodsOf(ctx, ref)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if !slices.Contains(managing, m) {
			return pderr.Invalidf("role",
				"%s: a role held in a group names only what changes its members (%s); the gate would hold anything else across the tenant",
				m, strings.Join(managing, ", "))
		}
	}

	return nil
}

// coreGroupExposure is the layer over the generated `GroupExposureService`. The
// client a group is shown to is a name its issuer gave it, and is held to the
// shape one has; which tenant the row is in is the group's, through the wall.
type coreGroupExposure struct {
	Core
	app.GroupExposureServiceServer
}

func (s Core) GroupExposure() app.GroupExposureServiceServer {
	return coreGroupExposure{s, s.Next().GroupExposure()}
}

// maxClient is the longest client a group may be shown to. An issuer's
// identifiers are a UUID or a short name, and anything a good deal longer is
// not one of them.
const maxClient = 255

func (s coreGroupExposure) Add(ctx context.Context, req *app.GroupExposureAddRequest) (*app.GroupExposure, error) {
	if err := isClient(req.GetClient()); err != nil {
		return nil, err
	}

	return s.GroupExposureServiceServer.Add(ctx, req)
}

// isClient refuses what no issuer would call a client. Compared as it is
// written, since a `client_id` is the issuer's string and nothing here may
// decide two of them are the same.
func isClient(v string) error {
	switch {
	case v == "":
		return pderr.Invalidf("client", "the client_id of the relying party whose tokens carry the group")
	case len(v) > maxClient:
		return pderr.Invalidf("client", "at most %d bytes, and a client_id is a good deal shorter", maxClient)
	case strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return pderr.Invalidf("client", "%q: a client_id has no space or control character in it", v)
	}

	return nil
}
