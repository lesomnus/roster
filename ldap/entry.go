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

	// unread is the attributes, by lower-cased name, that have not been read
	// yet: the values here are what is known of them so far, and are not all
	// there may be. An entry is made of the row it is and of lists that cost
	// a read each, and a filter is asked of the row first ([entry.eval]).
	unread map[string]bool
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

// await says attributes have not been read yet.
func (e *entry) await(names ...string) {
	if e.unread == nil {
		e.unread = map[string]bool{}
	}
	for _, name := range names {
		e.unread[strings.ToLower(name)] = true
	}
}

// read says attributes are about to be added as they were read. What was
// known of them so far gives way: it was part of what is read, and the order
// of the whole is the order they are published in.
func (e *entry) read(names ...string) {
	for _, name := range names {
		k := strings.ToLower(name)
		delete(e.unread, k)
		if _, ok := e.names[k]; !ok {
			continue
		}
		delete(e.attrs, k)
		delete(e.names, k)
		e.order = slices.DeleteFunc(e.order, func(o string) bool { return o == k })
	}
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

// exact is the attributes whose values are compared as the characters they
// are written in. One: an employee number, which roster's `Search` compares
// exactly and promises so (`docs/baseline.md`).
var exact = map[string]bool{
	"employeenumber": true,
}

// matches evaluates the filter over the entry, RFC 4511 § 4.5.1.7, with the
// three values the specification has: true, false, and undefined -- and an
// entry is returned only for true. A value is compared by the rule of its
// attribute: a name as a name ([named]), an employee number exactly
// ([exact]), and everything else as text without regard to case
// (`caseIgnoreMatch`, `caseIgnoreIA5Match` for `mail`; `uid` is
// `caseIgnoreMatch` as well).
//
// # The entry and the plan say the same
//
// A filter is asked twice: of roster, as the read it becomes ([planOf]),
// and then of each entry read, here. Where the two disagree one filter means
// two things, by whether it happened to be the plan. `(employeeNumber=ab07)`
// alone was a `Search`, which minds case, and found nobody; inside an `or`,
// where everybody is read, this compared without regard to case and found
// the person whose number is `AB07` (#281). So an employee number minds case
// here too, in an equality and in a substring.
//
// And a value with nothing in it -- empty, or spaces alone -- matches nobody:
// no attribute here has such a value, and the plan reads nobody for it
// ([asksNothing]).
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
	return e.eval(f) == yes
}

// truth is what a filter says of an entry: the three values of RFC 4511
// § 4.5.1.7, and a fourth for an entry that is not all read yet.
type truth int

const (
	no truth = iota
	yes
	// undefined is the specification's third value: the server cannot say,
	// and neither the filter nor its `not` matches.
	undefined
	// unread is that it depends on an attribute not read yet ([entry.unread]):
	// reading it makes this one of the other three.
	unread
)

// eval is the filter asked of what is known of the entry so far.
//
// # A filter is asked before the lists are read
//
// Every list of a person -- their addresses, their numbers, their places in
// groups and on teams, who they report to -- was read before the filter was
// asked, for every person a search passed: five thousand calls to answer
// `(sn=Smith)` with nobody, among a thousand people (#281). So a node that
// names an attribute not read yet says so, with what is known of it counted:
// a value already here that matches is `yes` whatever else there is, and
// nothing else can be said until it is read. An `and` with a `no` in it is
// `no` and an `or` with a `yes` is `yes`, whatever is unread beside them, and
// that is where the saving is: whoever the row alone rules out is read no
// further.
//
// `unread` outranks `undefined` in both, since which of them the node is can
// still change under a `not`.
func (e *entry) eval(f *wire.Filter) truth {
	if f == nil {
		return yes
	}
	switch f.Op {
	case wire.FilterAnd:
		out := yes
		for _, c := range f.Children {
			switch e.eval(c) {
			case no:
				return no
			case unread:
				out = unread
			case undefined:
				if out == yes {
					out = undefined
				}
			}
		}

		return out
	case wire.FilterOr:
		out := no
		for _, c := range f.Children {
			switch e.eval(c) {
			case yes:
				return yes
			case unread:
				out = unread
			case undefined:
				if out == no {
					out = undefined
				}
			}
		}

		return out
	case wire.FilterNot:
		if len(f.Children) != 1 {
			return undefined
		}
		switch v := e.eval(f.Children[0]); v {
		case yes:
			return no
		case no:
			return yes
		default:
			return v
		}
	case wire.FilterPresent:
		if strings.EqualFold(f.Attr, "objectClass") {
			return yes
		}

		return e.has(f.Attr, func(string) bool { return true })
	case wire.FilterEqual:
		if asksNothing(f) {
			return no
		}
		if named[strings.ToLower(f.Attr)] {
			asked, err := parseDN(f.Value)
			if err != nil {
				return undefined
			}

			return e.has(f.Attr, func(v string) bool {
				// What is published is this package's own writing of a name,
				// which parses.
				name, err := parseDN(v)

				return err == nil && name.equal(asked)
			})
		}
		minds := exact[strings.ToLower(f.Attr)]

		return e.has(f.Attr, func(v string) bool {
			return v == f.Value || (!minds && strings.EqualFold(v, f.Value))
		})
	case wire.FilterSubstrings:
		if asksNothing(f) {
			return no
		}
		fold := strings.ToLower
		if exact[strings.ToLower(f.Attr)] {
			fold = func(v string) string { return v }
		}

		return e.has(f.Attr, func(v string) bool { return substringMatch(fold(v), f, fold) })
	default:
		return undefined
	}
}

// has is whether a value of the attribute is one `match` takes: `yes` for one
// that is known, and otherwise `no` or, where the attribute is not read yet,
// `unread`.
func (e *entry) has(attr string, match func(string) bool) truth {
	k := strings.ToLower(attr)
	if slices.ContainsFunc(e.attrs[k], match) {
		return yes
	}
	if e.unread[k] {
		return unread
	}

	return no
}

// substringMatch is whether `v` holds the filter's fragments in their order.
// `fold` is what is done to each fragment before it is looked for, and has
// been done to `v`: lower-casing, for an attribute matched without regard to
// case.
func substringMatch(v string, f *wire.Filter, fold func(string) string) bool {
	if f.Initial != "" {
		i := fold(f.Initial)
		if !strings.HasPrefix(v, i) {
			return false
		}
		v = v[len(i):]
	}
	for _, a := range f.Any {
		a = fold(a)
		i := strings.Index(v, a)
		if i < 0 {
			return false
		}
		v = v[i+len(a):]
	}
	if f.Final != "" {
		return strings.HasSuffix(v, fold(f.Final))
	}

	return true
}
