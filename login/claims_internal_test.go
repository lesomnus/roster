package login

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

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

// TestEveryScopeThatCarriesAClaimIsNamed holds [claiming] to [claimsOf].
//
// The consent hop refuses rather than hand out a token with `sub` alone when it
// cannot read the person and the client asked for more. *Asked for more* is the
// list, so a scope that carries a claim and is missing from it is a token handed
// out short, exactly the one this is meant to stop. Every scope a client could
// send that this repository has heard of, against somebody who has everything.
func TestEveryScopeThatCarriesAClaimIsNamed(t *testing.T) {
	everybody := rstr.MeGetResponse_builder{
		Alias:  "erin",
		Name:   "Erin",
		Emails: []*rstr.MeEmail{rstr.MeEmail_builder{Address: "erin@contoso.example", DateVerified: timestamppb.Now()}.Build()},
		Teams:  []*rstr.MeTeam{rstr.MeTeam_builder{Alias: "ops"}.Build()},
		Groups: []*rstr.MeGroup{rstr.MeGroup_builder{Alias: "dtrack-triage", Clients: []string{"contoso-web"}}.Build()},
	}.Build()

	for _, scope := range []string{"openid", "offline", "offline_access", "profile", "email", "address", "phone", "groups"} {
		got := claimsOf(everybody, []string{scope}, "contoso-web")
		require.Equal(t, len(got) > 0, asksForClaims([]string{scope}),
			"%s: claimsOf writes %v for it, and the consent hop would not know it was asked for them", scope, got)
	}
}
