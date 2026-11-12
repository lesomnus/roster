package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// reachedBy reads until something about each of `who` has arrived, and
// answers what did, by person. It fails rather than hangs, as [about] does.
func reachedBy(t *testing.T, c <-chan *app.SyncEvent, who ...pdid.Id) map[pdid.Id]*app.SyncEvent {
	t.Helper()

	got := map[pdid.Id]*app.SyncEvent{}
	for len(got) < len(who) {
		v := about2(t, c)
		for _, k := range who {
			if string(v.GetHolder()) == string(k.Bytes()) {
				got[k] = v
			}
		}
	}

	return got
}

// reachIn is a caller who may write the access model in contoso, a stream they
// hold open, and the clients they write with -- everything over the wire,
// because what publishes is the interceptor on the served stack.
type reachIn struct {
	as context.Context
	c  <-chan *app.SyncEvent

	roles       app.RoleServiceClient
	bindings    app.BindingServiceClient
	groups      app.GroupServiceClient
	memberships app.GroupMembershipServiceClient
	teams       app.TeamMembershipServiceClient
	conn        *grpc.ClientConn
	canary      pdid.Id
}

func reaching(t *testing.T, b *built, ctx context.Context) *reachIn {
	t.Helper()

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)
	canary := b.holder(t, ctx, b.Contoso, "canary")

	return &reachIn{
		as: as, c: syncing(t, as, conn, canary), conn: conn, canary: canary,
		roles:       app.NewRoleServiceClient(conn),
		bindings:    app.NewBindingServiceClient(conn),
		groups:      app.NewGroupServiceClient(conn),
		memberships: app.NewGroupMembershipServiceClient(conn),
		teams:       app.NewTeamMembershipServiceClient(conn),
	}
}

// settled returns once the stream has read every write so far, and holds what
// it still had to say of `who` to the one thing it may be: what they may call,
// again.
//
// The stream reads a row when it gets to it, as the row is then ([caughtUp]
// says so at length). So a role written just before its binding and read just
// after it is a role that reaches them, and the binding is read for them too:
// two events about what they may call, for one change to it. Both are true,
// and an app drops what it knew of them on each. But a step that took the next
// event about them as its own took the one before it, every step after was
// one behind, and the first that waits for something other than REACH --
// their facts -- was handed REACH: "expected: 0, actual: 7", on a busy
// machine. So every step begins with nothing left from the one before.
func (r *reachIn) settled(t *testing.T, who pdid.Id) {
	t.Helper()

	for _, v := range caughtUp(t, r.as, r.conn, r.c, r.canary) {
		require.Equal(t, who.Bytes(), v.GetHolder(), "somebody else was told about")
		require.Equal(t, app.SyncReason_SYNC_REASON_REACH, v.GetReason(), "what was left was something other than what they may call")
	}
}

// TestAnAppHearsThatWhatSomebodyMayCallChanged: the second half of what an app
// holds (`sync.proto`, *And what they may do*) -- a role bound to somebody, the
// role widened, the binding erased -- is heard as an event about them that
// says so, with their facts as they are and none of them moved.
func TestAnAppHearsThatWhatSomebodyMayCallChanged(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	r := reaching(t, b, ctx)

	who := b.holder(t, ctx, b.Contoso, "dana")
	role, err := r.roles.Add(r.as, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{"/roster.HolderService/Get"},
	}.Build())
	x.NoError(err)
	bound, err := r.bindings.Add(r.as, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	v := about(t, r.c, who)
	x.Equal(app.SyncReason_SYNC_REASON_REACH, v.GetReason())
	x.Equal(b.Contoso.Bytes(), v.GetTenant(), "an app serving several tenants was not told which")
	x.Nil(v.GetDateInvalidated())
	x.Nil(v.GetDateDisabled())

	t.Run("a role they hold, widened", func(t *testing.T) {
		r.settled(t, who)
		_, err := r.roles.Widen(r.as, app.RoleWidenRequest_builder{
			Ref:     app.RoleRef_builder{Id: role.GetId()}.Build(),
			Methods: []string{"/roster.HolderService/List"},
		}.Build())
		require.NoError(t, err)
		require.Equal(t, app.SyncReason_SYNC_REASON_REACH, about(t, r.c, who).GetReason())
	})

	t.Run("a role they hold, narrowed", func(t *testing.T) {
		r.settled(t, who)
		_, err := r.roles.Narrow(r.as, app.RoleNarrowRequest_builder{
			Ref:     app.RoleRef_builder{Id: role.GetId()}.Build(),
			Methods: []string{"/roster.HolderService/List"},
		}.Build())
		require.NoError(t, err)
		require.Equal(t, app.SyncReason_SYNC_REASON_REACH, about(t, r.c, who).GetReason(),
			"what is taken out of a role is taken from everybody it reaches, and an app holding what they could call has to hear it")
	})

	t.Run("and the binding erased", func(t *testing.T) {
		r.settled(t, who)
		_, err := r.bindings.Erase(r.as, app.BindingRef_builder{Id: bound.GetId()}.Build())
		require.NoError(t, err)
		require.Equal(t, app.SyncReason_SYNC_REASON_REACH, about(t, r.c, who).GetReason(),
			"what is taken away is the write most worth hearing, and its row is gone from the walled server")
	})

	t.Run("and their facts, which it moves none of", func(t *testing.T) {
		x := require.New(t)
		r.settled(t, who)
		h := app.NewHolderServiceClient(r.conn)
		ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
		_, err := h.Disable(r.as, app.HolderDisableRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		// Told as a fact, on an event of its own: what the stream compares
		// the facts against is what it told of them, and an event about what
		// they may call told nothing -- so this is the first it says of their
		// facts, which has no movement to name.
		v := about(t, r.c, who)
		x.Equal(app.SyncReason_SYNC_REASON_UNSPECIFIED, v.GetReason())
		x.NotNil(v.GetDateDisabled())

		_, err = r.bindings.Add(r.as, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: ref,
		}.Build())
		x.NoError(err)
		v = about(t, r.c, who)
		x.Equal(app.SyncReason_SYNC_REASON_REACH, v.GetReason())
		x.NotNil(v.GetDateDisabled(), "their facts as they are")
	})
}

// TestARoleIsHeardByEverybodyItReaches: a write to a role is news about
// everybody it reaches -- bound to them, bound to a group they are in, held in
// a team -- and a group they join or that is erased is news about them.
func TestARoleIsHeardByEverybodyItReaches(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	r := reaching(t, b, ctx)

	direct := b.holder(t, ctx, b.Contoso, "direct")
	member := b.holder(t, ctx, b.Contoso, "member")
	joiner := b.holder(t, ctx, b.Contoso, "joiner")
	teamed := b.holder(t, ctx, b.Contoso, "teamed")
	role := b.role(t, ctx, "staff", "/roster.HolderService/Get")
	roleRef := app.RoleRef_builder{Id: role.Bytes()}.Build()

	g, err := r.groups.Add(r.as, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "everyone",
	}.Build())
	x.NoError(err)
	group := app.GroupRef_builder{Id: g.GetId()}.Build()
	_, err = r.memberships.Add(r.as, app.GroupMembershipAddRequest_builder{
		Group: group, Holder: app.HolderRef_builder{Id: member.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	reachedBy(t, r.c, member)

	for _, v := range []*app.BindingAddRequest{
		app.BindingAddRequest_builder{Role: roleRef, Holder: app.HolderRef_builder{Id: direct.Bytes()}.Build()}.Build(),
		app.BindingAddRequest_builder{Role: roleRef, Group: group}.Build(),
	} {
		_, err := r.bindings.Add(r.as, v)
		x.NoError(err)
	}
	reachedBy(t, r.c, direct, member)

	team := b.team(t, ctx, b.site(t, ctx, b.Contoso, "seoul"), "desk")
	_, err = r.teams.Add(r.as, app.TeamMembershipAddRequest_builder{
		Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: teamed.Bytes()}.Build(),
		Role:   roleRef,
	}.Build())
	x.NoError(err)
	reachedBy(t, r.c, teamed)

	_, err = r.roles.Widen(r.as, app.RoleWidenRequest_builder{Ref: roleRef, Methods: []string{"/roster.HolderService/List"}}.Build())
	x.NoError(err)
	for k, v := range reachedBy(t, r.c, direct, member, teamed) {
		x.Equal(app.SyncReason_SYNC_REASON_REACH, v.GetReason(), "%s", k)
	}

	_, err = r.memberships.Add(r.as, app.GroupMembershipAddRequest_builder{
		Group: group, Holder: app.HolderRef_builder{Id: joiner.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	reachedBy(t, r.c, joiner)

	_, err = r.groups.Erase(r.as, group)
	x.NoError(err)
	reachedBy(t, r.c, member, joiner)
}

// TestNobodyIsToldWhatAnotherCustomerMayCall: a binding written in another
// tenant reaches somebody this stream may not read, and is heard as nothing --
// the same wall [TestOneCustomerIsNotToldAboutAnother] is about, on the paths
// a role takes to people.
func TestNobodyIsToldWhatAnotherCustomerMayCall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	r := reaching(t, b, ctx)

	other := b.holder(t, ctx, b.Fabrikam, "theirs")
	b.mayAnything(other, b.Fabrikam)
	theirs := asOverTheWire(ctx, other)
	role, err := r.roles.Add(theirs, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{"/roster.HolderService/Get"},
	}.Build())
	x.NoError(err)
	_, err = r.bindings.Add(theirs, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	x.Empty(caughtUp(t, r.as, r.conn, r.c, r.canary), "an app was told what somebody in a tenant it cannot read may call")
}
