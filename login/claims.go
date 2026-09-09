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
// it. That is the OIDC contract and it is also the useful boundary: an operator
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
func claimsOf(v *rstr.MeGetResponse, scope []string) map[string]any {
	claims := map[string]any{}

	if slices.Contains(scope, "profile") {
		claims["preferred_username"] = v.GetAlias()
		if n := v.GetName(); n != "" {
			claims["name"] = n
		}

		teams := []string{}
		for _, t := range v.GetTeams() {
			teams = append(teams, t.GetAlias())
		}
		if len(teams) > 0 {
			claims["groups"] = teams
		}
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
