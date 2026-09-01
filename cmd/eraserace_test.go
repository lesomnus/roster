package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	entteammembership "github.com/lesomnus/roster/internal/ent/teammembership"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// Two callers asking for the same erasure, and what each of them is told.
//
// Both tests here are about a race, and they are opposite failures of the same
// habit: reading a row in a layer, deciding on what the read said, and writing
// afterwards. One reads to find out who to ask about and turns "already gone"
// into an error; the other reads to count and lets the count go stale.

// TestErasingAMembershipThatIsNotThereSucceeds, which is the rule spend in
// `server/vouch/step.go` states and `keys.Undelegate` leans on.
//
// It is the layer that has to agree with it: the generated `Erase` answers
// `{erased: false}` for a row that was already gone, and `server/core` reads
// the row first -- to learn which team to ask about -- so it was the read's
// NotFound that came back instead. Two operators removing one person from one
// team is how it shows up, and the loser is told the row does not exist for a
// call that asked for exactly the state they got.
func TestErasingAMembershipThatIsNotThereSucceeds(t *testing.T) {
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	team := b.team(t, ctx, seoul, "operators")
	role := b.role(t, ctx, "operator")

	joins := func(t *testing.T, who pdid.Id) *app.TeamMembership {
		t.Helper()

		v, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
			Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}

	t.Run("the second erase of one membership", func(t *testing.T) {
		x := require.New(t)

		v := joins(t, b.ContosoUser)

		first, err := b.Ungated.TeamMembership().Erase(ctx, v.Ref())
		x.NoError(err)
		x.True(first.GetErased())

		second, err := b.Ungated.TeamMembership().Erase(ctx, v.Ref())
		x.NoError(err, "the loser of a race to remove one member was refused")
		x.False(second.GetErased(), "the second erase claimed to have done something")
	})

	// A reference that never named anything: the same answer, because the
	// caller asked for a state and that is the state.
	t.Run("and one that was never there", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ungated.TeamMembership().Erase(ctx,
			app.TeamMembershipRef_builder{Id: pdid.New(pd.TeamMembershipDomain).Bytes()}.Build())
		x.NoError(err)
		x.False(v.GetErased())
	})

	// And the row not being there is the only thing that is answered early.
	// The team is still asked about for a row that is, or this would be a way
	// past `mayChangeTeam` rather than an agreement with the generated `Erase`.
	t.Run("and the team is still asked about a row that is there", func(t *testing.T) {
		x := require.New(t)

		yours := b.team(t, ctx, seoul, "yours")
		admin := b.role(t, ctx, "team-admin", "/roster.TeamMembershipService/Erase")

		// Alice administers `operators` and holds no binding at all, so what
		// she may do to `yours` is nothing.
		alice := b.holder(t, ctx, b.Contoso, "alice")
		_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
			Role:   app.RoleRef_builder{Id: admin.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		bob := b.holder(t, ctx, b.Contoso, "bob")
		theirs, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
			Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(),
			Team:   app.TeamRef_builder{Id: yours.Bytes()}.Build(),
			Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		conn := served(t, b.Server)
		_, err = app.NewTeamMembershipServiceClient(conn).
			Erase(asOverTheWire(ctx, alice), theirs.Ref())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a team administrator erased a membership of a team they do not administer")

		// And the row is still live, which is the half that says the refusal
		// happened before the write rather than after it. Counted with the
		// predicate the wall uses -- an erase here is a `date_erased` stamp,
		// so a bare count is the same number either way and would assert
		// nothing at all.
		live, err := b.Ent.TeamMembership.Query().
			Where(entteammembership.DateErasedIsNil(), entteammembership.IdEQ(mustId(t, theirs.GetId()).Uuid())).
			Count(ctx)
		x.NoError(err)
		x.Equal(1, live, "the membership was erased by somebody who was refused")
	})
}
