package login

import (
	"slices"

	rstr "github.com/lesomnus/roster/rstr"
)

// claimsOf is what goes in the `id_token`, from the one read this app makes.
//
// # Scope decides, and nothing else does
//
// A claim is in the token because the client asked for the scope that carries
// it. That is the OIDC contract and it is also the useful boundary: an tenant
// grants a client `email` by registering it with that scope, not by this app
// having an opinion. A scope nobody asked for produces no claim even when the
// read answered with one.
//
// `openid` is the subject and the subject alone, and it is not written here:
// Hydra puts `sub` in from what `acceptLoginRequest` was told, which is where
// it belongs -- one place decides who this is.
//
// # What is deliberately absent
//
// **`methods`.** `MeGetResponse` carries every RPC the person may call, and
// none of it goes in a token: it is roster's answer about roster, it is long,
// and a product reading permissions out of an `id_token` is a product holding a
// copy of an answer that changes without it. What a product wants is its own
// authorisation, keyed on `sub`.
//
// **Anything unverified, under `email`.** An address nobody has confirmed is a
// fact roster keeps on purpose, and it is not one to hand a relying party as
// *this is their email* -- `email_verified: false` reads as a smaller claim
// than it is, because most consumers of it do not branch. So the claim is the
// verified address, or there is no claim.
//
// **Any group not shown to this client.** `groups` is for the relying parties
// that cannot ask roster and grant on a list of names -- Dependency-Track,
// kube-apiserver -- and it carries the person's groups that a `GroupExposure`
// shows to `client`, the one the token is for. Never the rest of their
// membership: a client learns the groups a tenant's administrator chose for
// it, whatever else the person is in.
func claimsOf(v *rstr.MeGetResponse, scope []string, client string) map[string]any {
	claims := map[string]any{}

	if slices.Contains(scope, "profile") {
		claims["preferred_username"] = v.GetAlias()
		if n := v.GetName(); n != "" {
			claims["name"] = n
		}

		// `teams`, and not `groups`. It was `groups`, which every relying
		// party reads as *permission groups* -- Keycloak's, Okta's, Entra's --
		// and a team is an organisation's structure, nothing to grant on. What
		// somebody may do is `HolderService/Reaches`, and `groups`, below, is
		// roster's own `Group`s, for the relying parties that cannot ask.
		teams := []string{}
		for _, t := range v.GetTeams() {
			teams = append(teams, t.GetAlias())
		}
		if len(teams) > 0 {
			claims["teams"] = teams
		}
	}

	// Present whenever the scope is, and empty when none of their groups is
	// shown to this client: a relying party that syncs its teams from the
	// claim takes somebody out of the last one with it.
	if slices.Contains(scope, "groups") {
		groups := []string{}
		for _, g := range v.GetGroups() {
			if slices.Contains(g.GetClients(), client) {
				groups = append(groups, g.GetAlias())
			}
		}
		claims["groups"] = groups
	}

	if slices.Contains(scope, "email") {
		for _, e := range v.GetEmails() {
			if e.GetDateVerified() == nil {
				continue
			}

			claims["email"] = e.GetAddress()
			claims["email_verified"] = true

			break
		}
	}

	return claims
}
