package scim

import (
	"encoding/json"
	"strings"
)

// The filters a directory asks before it writes, and no others.
//
// A directory looks somebody up before it creates them -- Entra by `userName`,
// then by `externalId` if told to; Okta by `userName` -- and that is the whole
// use: one attribute, `eq`, one value. A filter language beyond that would be
// a query engine over rows roster pages by id, for questions nobody here is
// asking. Anything else is `invalidFilter`, which a directory reports rather
// than misreads.
const (
	byUserName   = "username"
	byExternalId = "externalid"
	byId         = "id"
	byEmail      = "emails"
)

// parseFilter is `attribute eq "value"`.
//
// Split at the ` eq ` whose remainder is the quoted value whole, which is
// what lets an attribute path carry a filter of its own --
// `emails[type eq "work"].value eq "erin@contoso.example"`, Entra's -- and a
// value say "eq" without being cut at it.
func parseFilter(v string) (string, string, error) {
	v = strings.TrimSpace(v)
	lower := strings.ToLower(v)

	attr, value, found := "", "", false
	for i := 0; ; {
		j := strings.Index(lower[i:], " eq ")
		if j < 0 {
			break
		}
		at := i + j
		var s string
		if err := json.Unmarshal([]byte(strings.TrimSpace(v[at+4:])), &s); err == nil {
			attr, value, found = strings.TrimSpace(v[:at]), s, true

			break
		}
		i = at + 4
	}
	if !found {
		return "", "", badRequest("invalidFilter", `a filter here is attribute eq "value", the value quoted`)
	}

	switch a := strings.ToLower(attr); a {
	case byUserName, byExternalId, byId:
		return a, value, nil
	case "emails.value", `emails[type eq "work"].value`, "emails[primary eq true].value":
		return byEmail, value, nil
	default:
		return "", "", badRequest("invalidFilter", "userName, externalId, id or emails.value")
	}
}
