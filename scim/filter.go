package scim

import (
	"encoding/json"
	"strings"
	"unicode"
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

// parseFilter is `attribute eq "value"`, for a person.
func parseFilter(v string) (string, string, error) {
	attr, value, err := splitEq(v)
	if err != nil {
		return "", "", err
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

// parseGroupFilter is `attribute eq "value"`, for a group: what it is called,
// which is how a directory looks one up before it makes it, or what the
// directory calls it.
func parseGroupFilter(v string) (string, string, error) {
	attr, value, err := splitEq(v)
	if err != nil {
		return "", "", err
	}

	switch a := strings.ToLower(attr); a {
	case byDisplayName, byExternalId, byId:
		return a, value, nil
	default:
		return "", "", badRequest("invalidFilter", "displayName, externalId or id")
	}
}

// splitEq is the two sides of `attribute eq "value"`.
//
// Split at the ` eq ` whose remainder is the quoted value whole, which is
// what lets an attribute path carry a filter of its own --
// `emails[type eq "work"].value eq "erin@contoso.example"`, Entra's -- and a
// value say "eq" without being cut at it.
//
// In one pass. The value ends the string, so it begins at the last quote
// before the end that no backslash escapes: a value that began earlier would
// hold that quote unescaped. That one place is asked of JSON once, and it is
// the split where an ` eq ` ends just before it. Trying each ` eq ` from the
// left and parsing what was left after each read the string again for every
// one, which is time that grows with the square of its length; the answers
// are the same, `null` for a value among them, and so is which ` eq ` of
// ` eq eq ` is one, since that reading skipped an ` eq ` that began inside the
// last.
func splitEq(v string) (string, string, error) {
	refused := badRequest("invalidFilter", `a filter here is attribute eq "value", the value quoted`)

	v = strings.TrimSpace(v)
	at := valueAt(v)
	if at < 0 {
		return "", "", refused
	}

	// What is before the value is the attribute, ` eq `, and white space.
	left := strings.TrimRightFunc(v[:at], unicode.IsSpace)
	eq := len(left) - 3
	if eq < 0 || len(left) == at || v[len(left)] != ' ' || !strings.EqualFold(left[eq:], " eq") || !counted(v, eq) {
		return "", "", refused
	}

	var s string
	if err := json.Unmarshal([]byte(v[at:]), &s); err != nil {
		return "", "", refused
	}

	return strings.TrimSpace(v[:eq]), s, nil
}

// valueAt is where the value that ends v begins: the last quote before the
// closing one that no backslash escapes, or `null`; -1 where v ends in
// neither.
func valueAt(v string) int {
	if strings.HasSuffix(v, "null") {
		return len(v) - 4
	}
	if len(v) < 2 || v[len(v)-1] != '"' {
		return -1
	}
	for i := len(v) - 2; i >= 0; i-- {
		if v[i] != '"' {
			continue
		}
		run := 0
		for j := i - 1; j >= 0 && v[j] == '\\'; j-- {
			run++
		}
		if run%2 == 0 {
			return i
		}
		i -= run
	}

	return -1
}

// counted is whether the ` eq ` at i is one a reading from the left takes:
// one that begins on the last space of another is skipped, so of a run of
// them that overlap, every other one is.
func counted(v string, i int) bool {
	n := 0
	for j := i - 3; j >= 0 && strings.EqualFold(v[j:j+4], " eq "); j -= 3 {
		n++
	}

	return n%2 == 0
}
