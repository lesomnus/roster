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

	got := claimsOf(v, []string{"openid", "profile"}, "contoso-web")
	require.Equal(t, []string{"ops", "release"}, got["teams"])
	require.NotContains(t, got, "groups")

	require.NotContains(t, claimsOf(v, []string{"openid"}, "contoso-web"), "teams", "a claim the client did not ask the scope for")
}

// A token's `groups` is the person's groups shown to the client it is for, and
// none of the rest -- present whenever the scope is, so that a relying party
// syncing its teams from it sees somebody's last group go.
func TestGroupsAreTheOnesShownToTheClient(t *testing.T) {
	v := rstr.MeGetResponse_builder{
		Alias: "erin",
		Groups: []*rstr.MeGroup{
			rstr.MeGroup_builder{Alias: "cluster-admins", Clients: []string{"kube"}}.Build(),
			rstr.MeGroup_builder{Alias: "dtrack-triage", Clients: []string{"dtrack", "kube"}}.Build(),
			rstr.MeGroup_builder{Alias: "on-call"}.Build(),
		},
	}.Build()

	x := require.New(t)
	x.Equal([]string{"dtrack-triage"}, claimsOf(v, []string{"openid", "groups"}, "dtrack")["groups"])
	x.Equal([]string{"cluster-admins", "dtrack-triage"}, claimsOf(v, []string{"openid", "groups"}, "kube")["groups"])
	x.Equal([]string{}, claimsOf(v, []string{"openid", "groups"}, "wiki")["groups"], "shown none, and saying so")
	x.NotContains(claimsOf(v, []string{"openid", "profile"}, "dtrack"), "groups", "a claim the client did not ask the scope for")
}
