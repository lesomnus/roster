package login

import (
	"testing"

	"github.com/stretchr/testify/require"

	rstr "github.com/lesomnus/roster/rstr"
)

// The person's teams are `teams`, and not `groups`: every relying party reads
// `groups` as permission groups, and a team is an organisation's structure.
func TestTeamsAreTeamsInAToken(t *testing.T) {
	v := rstr.MeGetResponse_builder{
		Alias: "erin",
		Teams: []*rstr.MeTeam{rstr.MeTeam_builder{Alias: "ops"}.Build(), rstr.MeTeam_builder{Alias: "release"}.Build()},
	}.Build()

	got := claimsOf(v, []string{"openid", "profile"})
	require.Equal(t, []string{"ops", "release"}, got["teams"])
	require.NotContains(t, got, "groups")

	require.NotContains(t, claimsOf(v, []string{"openid"}), "teams", "a claim the client did not ask the scope for")
}
