package ldap

import (
	"slices"
	"sort"
	"strings"

	"github.com/lesomnus/roster/ldap/wire"
)

// entry is one thing in the tree, with its attributes looked up without
// regard to case, as attribute descriptions are.
type entry struct {
	dn    dn
	attrs map[string][]string // by lower-cased name
	names map[string]string   // lower-cased name -> the name as written
	order []string            // lower-cased names, in the order added
}

// Operational attributes: answered only when asked for by name or with `+`,
// RFC 4512 § 3.4.
var operational = map[string]bool{
	"entryuuid":               true,
	"subschemasubentry":       true,
	"namingcontexts":          true,
	"supportedcontrol":        true,
	"supportedextension":      true,
	"supportedldapversion":    true,
	"supportedsaslmechanisms": true,
	"vendorname":              true,
	"vendorversion":           true,
}

func newEntry(d dn) *entry {
	return &entry{dn: d, attrs: map[string][]string{}, names: map[string]string{}}
}

// add appends values, dropping empty ones: an attribute with no values is
// not present, and a `mail` of "" would say somebody has a mailbox.
func (e *entry) add(name string, values ...string) {
	k := strings.ToLower(name)
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := e.names[k]; !ok {
			e.names[k] = name
			e.order = append(e.order, k)
		}
		// An attribute is a set of values: one said twice -- a number that
		// is somebody's `work` and their `other`, both a `telephoneNumber`
		// -- is there once.
		if slices.Contains(e.attrs[k], v) {
			continue
		}
		e.attrs[k] = append(e.attrs[k], v)
	}
}

func (e *entry) values(name string) []string {
	return e.attrs[strings.ToLower(name)]
}

// project is the entry as sent, holding the attributes the client asked for:
// none or `*` is every user attribute, `+` every operational one, `1.1` is
// none, and a name is that one.
func (e *entry) project(requested []string, typesOnly bool) wire.Entry {
	all, ops, none := len(requested) == 0, false, false
	named := map[string]bool{}
	for _, r := range requested {
		switch r {
		case "*":
			all = true
		case "+":
			ops = true
		case "1.1":
			none = true
		default:
			named[strings.ToLower(r)] = true
		}
	}
	if none && len(named) == 0 && !all && !ops {
		return wire.Entry{DN: e.dn.String()}
	}

	out := wire.Entry{DN: e.dn.String()}
	keys := append([]string(nil), e.order...)
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case named[k]:
		case operational[k] && ops:
		case !operational[k] && all:
		default:
			continue
		}
		a := wire.Attribute{Name: e.names[k]}
		if !typesOnly {
			a.Values = e.attrs[k]
		}
		out.Attributes = append(out.Attributes, a)
	}

	return out
}

// named is the attributes whose values are names: the DN of a group, a team
// or a person. Their equality is `distinguishedNameMatch`, which compares a
// name as a name and not as the text somebody happened to write it in.
var named = map[string]bool{
	"memberof": true,
	"member":   true,
	"manager":  true,
}

// matches evaluates the filter over the entry, RFC 4511 § 4.5.1.7, with the
// three values the specification has: true, false, and undefined -- and an
// entry is returned only for true. A value is compared by the rule of its
// attribute: a name as a name ([named]), and everything else as text without
// regard to case (`caseIgnoreMatch`, `caseIgnoreIA5Match` for `mail`; `uid`
// is `caseIgnoreMatch` as well).
//
// # A name is compared as a name
//
// Every value was compared as text, a name among them, so
// `(memberOf=cn=payroll, ou=groups, o=contoso)` -- a space after each comma,
// which is how many clients write one -- matched nobody, and its `not` matched
// everybody, payroll's own members included: a client that keeps a group out
// by a filter let it in (#281). A search base in that spelling was always read
// as a name. So is this: both sides are parsed and compared RDN by RDN, which
// is what a base is held to ([dn.hasSuffix]).
//
// And what was asked for that is not a name at all cannot be compared with
// one, which the specification calls undefined ("the assertion value is
// invalid"): it matches nothing, and neither does its `not`. Text said false
// there, and so true for the `not` -- the same client, let in by a typing
// mistake.
func (e *entry) matches(f *wire.Filter) bool {
	v, ok := e.eval(f)

	return ok && v
}

func (e *entry) eval(f *wire.Filter) (value, defined bool) {
	if f == nil {
		return true, true
	}
	switch f.Op {
	case wire.FilterAnd:
		undefined := false
		for _, c := range f.Children {
			v, ok := e.eval(c)
			switch {
			case ok && !v:
				return false, true
			case !ok:
				undefined = true
			}
		}

		return !undefined, !undefined
	case wire.FilterOr:
		undefined := false
		for _, c := range f.Children {
			v, ok := e.eval(c)
			switch {
			case ok && v:
				return true, true
			case !ok:
				undefined = true
			}
		}

		return false, !undefined
	case wire.FilterNot:
		if len(f.Children) != 1 {
			return false, false
		}
		v, ok := e.eval(f.Children[0])
		if !ok {
			return false, false
		}

		return !v, true
	case wire.FilterPresent:
		if strings.EqualFold(f.Attr, "objectClass") {
			return true, true
		}

		return len(e.values(f.Attr)) > 0, true
	case wire.FilterEqual:
		if named[strings.ToLower(f.Attr)] {
			asked, err := parseDN(f.Value)
			if err != nil {
				return false, false
			}
			for _, v := range e.values(f.Attr) {
				// What is published is this package's own writing of a name,
				// which parses.
				if name, err := parseDN(v); err == nil && name.equal(asked) {
					return true, true
				}
			}

			return false, true
		}
		for _, v := range e.values(f.Attr) {
			if strings.EqualFold(v, f.Value) {
				return true, true
			}
		}

		return false, true
	case wire.FilterSubstrings:
		for _, v := range e.values(f.Attr) {
			if substringMatch(strings.ToLower(v), f) {
				return true, true
			}
		}

		return false, true
	default:
		return false, false
	}
}

func substringMatch(v string, f *wire.Filter) bool {
	if f.Initial != "" {
		i := strings.ToLower(f.Initial)
		if !strings.HasPrefix(v, i) {
			return false
		}
		v = v[len(i):]
	}
	for _, a := range f.Any {
		a = strings.ToLower(a)
		i := strings.Index(v, a)
		if i < 0 {
			return false
		}
		v = v[i+len(a):]
	}
	if f.Final != "" {
		return strings.HasSuffix(v, strings.ToLower(f.Final))
	}

	return true
}
