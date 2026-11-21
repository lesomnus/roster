package ldap

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/ldap/wire"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// The attributes of a person, `docs/ldap.md` § A person. Names as the
// schemas spell them; how a value is matched is its attribute's to say
// ([entry.matches]).
const (
	attrUid          = "uid"
	attrCn           = "cn"
	attrSn           = "sn"
	attrDisplayName  = "displayName"
	attrMail         = "mail"
	attrEmployeeNo   = "employeeNumber"
	attrDepartment   = "departmentNumber"
	attrLanguage     = "preferredLanguage"
	attrLabeledURI   = "labeledURI"
	attrMemberOf     = "memberOf"
	attrTelephone    = "telephoneNumber"
	attrMobile       = "mobile"
	attrFax          = "facsimileTelephoneNumber"
	attrHomePhone    = "homePhone"
	attrPager        = "pager"
	attrManager      = "manager"
	attrEntryUUID    = "entryUUID"
	attrObjectClass  = "objectClass"
	classPerson      = "inetOrgPerson"
	classOrgPerson   = "organizationalPerson"
	classPersonPlain = "person"
)

// wants is which of the attributes that cost a read a search has a use for.
// There are two uses and a search keeps them apart ([search]): the client
// asked for the attribute to be sent ([asked]), or its filter names it
// ([mentioned]) -- a filter on `mail` has to see the addresses to be
// evaluated, whether or not they are sent.
type wants struct {
	mail     bool
	memberOf bool
	member   bool
	phone    bool
	manager  bool
}

// managerLabel is the label that says a relation's `other` is who its
// `holder` reports to: the word the SCIM endpoint writes (`scim/manager.go`)
// and this reads. roster itself reads no label of a relation.
const managerLabel = "manager"

// phoneAttrs is the attributes a person's numbers are published as, by which
// of their numbers each is (`Phone.kind`). One of no kind, or of a kind with
// no attribute of its own, is their `telephoneNumber`.
var phoneAttrs = map[string]string{
	"":       attrTelephone,
	"work":   attrTelephone,
	"other":  attrTelephone,
	"mobile": attrMobile,
	"fax":    attrFax,
	"home":   attrHomePhone,
	"pager":  attrPager,
}

// isPhoneAttr is whether a name is one of [phoneAttrs]'s.
func isPhoneAttr(name string) bool {
	for _, a := range phoneAttrs {
		if strings.EqualFold(a, name) {
			return true
		}
	}

	return false
}

// asked is the attributes the client asked to be sent.
func asked(req wire.SearchRequest) wants {
	all := len(req.Attributes) == 0
	var w wants
	for _, a := range req.Attributes {
		switch {
		case a == "*":
			all = true
		case strings.EqualFold(a, attrMail):
			w.mail = true
		case strings.EqualFold(a, attrMemberOf):
			w.memberOf = true
		case strings.EqualFold(a, attrMember):
			w.member = true
		case isPhoneAttr(a):
			w.phone = true
		case strings.EqualFold(a, attrManager):
			w.manager = true
		}
	}
	if all {
		w = wants{mail: true, memberOf: true, member: true, phone: true, manager: true}
	}

	return w
}

// mentioned is the attributes a filter names.
func mentioned(f *wire.Filter) wants {
	w := wants{
		mail:     f.Mentions(attrMail),
		memberOf: f.Mentions(attrMemberOf),
		member:   f.Mentions(attrMember),
		manager:  f.Mentions(attrManager),
	}
	for _, a := range phoneAttrs {
		w.phone = w.phone || f.Mentions(a)
	}

	return w
}

func (w wants) or(o wants) wants {
	return wants{
		mail:     w.mail || o.mail,
		memberOf: w.memberOf || o.memberOf,
		member:   w.member || o.member,
		phone:    w.phone || o.phone,
		manager:  w.manager || o.manager,
	}
}

// list is one of the reads, beside their own row, that a person's entry is
// made of. Each is a list of roster's and costs a call, so each is read only
// for whom it is needed, and for a page of people at once ([chunks]).
type list int

const (
	listMail list = iota
	listPhone
	listMemberOf
	listManager
	listCount
)

// lists is every one of them, in the order they are read where a filter
// needs several: the ones a tenant fills for most of its people first, since
// those are the ones most likely to settle it.
var lists = []list{listMail, listPhone, listMemberOf, listManager}

func (l list) in(w wants) bool {
	switch l {
	case listMail:
		return w.mail
	case listPhone:
		return w.phone
	case listMemberOf:
		return w.memberOf
	default:
		return w.manager
	}
}

// attrs is the attributes a list is published as.
func (l list) attrs() []string {
	switch l {
	case listMail:
		return []string{attrMail}
	case listPhone:
		return []string{attrTelephone, attrMobile, attrFax, attrHomePhone, attrPager}
	case listMemberOf:
		return []string{attrMemberOf}
	default:
		return []string{attrManager}
	}
}

// byAlias is the holder called `alias`, or nil for nobody: one read.
//
// `uid` is `caseIgnoreMatch` and a client types what it likes, so what was
// asked is folded the way roster folds a name somebody typed
// ([front.Alias]): an alias is kept in small letters by rule
// (`server/core`, `spelled`), so there is one row it can be and one way to
// ask for it.
//
// It used to ask for the name as it was typed and, at `NotFound`, page
// `Holder.Search` for it in another case. So a sign-in by a name nobody has
// read every person of the tenant -- eleven calls among a thousand, and a
// thousand people on the access record for a lookup that named none of them
// (#281).
func (d *Directory) byAlias(ctx context.Context, t *tenant, r *reads, alias string) (*rstr.Holder, error) {
	alias = front.Alias(alias)
	if alias == "" {
		return nil, nil
	}
	v, err := d.roster.Holder().Get(t.on(ctx), rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias:  proto.String(alias),
			Tenant: rstr.TenantRef_builder{Id: t.id.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	if gone(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.holders[string(v.GetId())] = v

	return v, nil
}

// declared is the label a row a file or a provisioner declared carries
// (`cmd.Declared`), named here because a consumer does not import `cmd`.
const declared = "roster.declared"

// listed is whether a holder is one of the tenant's people, as this tree
// shows them. Not somebody disabled or erased, which a directory says by
// absence rather than by a flag; and not a machine.
//
// # A machine is not a person here
//
// Every holder was listed: the one this directory is answered as, the one
// each front door is, whatever else a file declared -- each an
// `inetOrgPerson` with a `uid`, imported by a client that imports users and
// named by a group among its members (#281). The SCIM endpoint is asked the
// same question, who a tenant's people are, and leaves out a row that is
// declared and its own (`scim/user.go`). This is that test, and it is asked
// wherever a person is named: as an entry, as a `member`, as a `manager`.
//
// What it does not know is a machine nothing declared: an app installed by
// hand is a holder like any other, and so is the holder of a tenant's own key
// for this directory ([tenant.self]). `docs/ldap.md` § A person says so.
func (d *Directory) listed(t *tenant, v *rstr.Holder) bool {
	switch {
	case v == nil, v.GetDateDisabled() != nil, v.GetDateErased() != nil:
		return false
	case v.GetLabels()[declared] != "":
		return false
	case len(t.self) > 0 && bytes.Equal(v.GetId(), t.self):
		return false
	}

	return true
}

// called is what somebody is called now, as `MeService/Get` and so a token's
// `name` say it (`server/me`, `called`): the profile's display name, which is
// what every write that renames somebody writes, and the name they were added
// with where there is none. Empty for somebody added by their alias alone.
func called(v *rstr.Holder) string {
	return firstOf(v.GetProfile().GetDisplayName(), v.GetName())
}

// personEntry is somebody as their own row says them: every attribute that
// costs no further read. The rest are read into it by whoever needs them
// ([search.show]).
func (d *Directory) personEntry(t *tenant, v *rstr.Holder) (*entry, error) {
	id, err := pdid.From(v.GetId())
	if err != nil {
		return nil, err
	}

	e := newEntry(d.personDN(t, v))
	e.add(attrObjectClass, "top", classPersonPlain, classOrgPerson, classPerson)
	e.add(attrUid, v.GetAlias())
	// What somebody is called now, and never missing: `person` requires a
	// `cn` and an `sn`, and it was `Holder.name`, which no served verb
	// changes and which somebody added by alias alone does not have (#281).
	// `sn` is the same string. roster does not split names: the whole name,
	// rather than a guess at half of it, and no `givenName` for the same
	// reason.
	name := firstOf(called(v), v.GetAlias())
	e.add(attrCn, name)
	e.add(attrSn, name)
	// A token's `name` exactly, so absent where a token says none (#353).
	e.add(attrDisplayName, called(v))
	p := v.GetProfile()
	e.add(attrEmployeeNo, p.GetEmployeeNo())
	e.add(attrDepartment, p.GetDepartment())
	e.add(attrLanguage, p.GetLocale())
	e.add(attrLabeledURI, p.GetPicture())
	e.add(attrEntryUUID, id.Uuid().String())

	return e, nil
}

// row is somebody a read answered, and what that read already says of them
// beside their row: an address looked up is theirs, and a group whose members
// were read has them in it.
type row struct {
	holder *rstr.Holder
	known  map[string][]string
}

// shown is somebody on their way into an answer: their entry so far, and
// which of the lists have been read into it.
type shown struct {
	id   []byte
	e    *entry
	have [listCount]bool
}

// show sends, of the people a read answered, those the filter keeps -- and
// reads for them, on the way, no more than that takes.
//
// # What is read, for whom, and when
//
// Each person's five lists were read as their entry was made, one call a
// list a person, and the filter was asked of the finished entry. A search
// that kept nobody had read everything about everybody (#281).
//
// So the filter is asked of the row first, with the lists standing as unread
// ([entry.eval]). Whoever the row rules out goes no further. Then, of the
// lists the filter names, one at a time: it is read for whoever is still
// undecided and the filter asked again, so a list is not read for somebody
// an earlier one ruled out or let in. Only then are the lists the client
// asked to be sent read, for whoever is left. Each of those reads is one for
// the whole page ([chunks]).
//
// What the read that found somebody already proved ([row.known]) is counted
// before anything is read: `(memberOf=<group>)` is answered by that group's
// own list of members, and reading each member's places to ask them the
// same question again was two calls a member.
//
// # An `or` is each of its arms, and everybody is sent once
//
// `arms` is the reads the filter became and `i` the one these rows came
// from. Somebody two arms read is sent by the first arm whose own part of
// the filter they match, and by no other. That is decided from the entry
// alone, with no memory of who was sent, so it holds across the pages of a
// paged search as it does within one.
func (s *search) show(ctx context.Context, t *tenant, arms []arm, i int, rows []row) wire.Result {
	d, f := s.d, s.req.Filter

	live := make([]*shown, 0, len(rows))
	for _, r := range rows {
		if !d.listed(t, r.holder) {
			continue
		}
		e, err := d.personEntry(t, r.holder)
		if err != nil {
			return refusal(err)
		}
		for _, l := range lists {
			e.await(l.attrs()...)
		}
		for name, values := range r.known {
			e.add(name, values...)
		}
		live = append(live, &shown{id: r.holder.GetId(), e: e})
	}

	// settle reads what a node needs to be said of everybody it is still
	// unread for, and keeps those `keep` takes.
	settle := func(node *wire.Filter, keep func(truth) bool) error {
		need := mentioned(node)
		for _, l := range lists {
			if !l.in(need) {
				continue
			}
			var ask []*shown
			for _, p := range live {
				if !p.have[l] && p.e.eval(node) == unread {
					ask = append(ask, p)
				}
			}
			if err := s.fill(ctx, t, l, ask); err != nil {
				return err
			}
			live = slices.DeleteFunc(live, func(p *shown) bool {
				v := p.e.eval(node)

				return v != unread && !keep(v)
			})
		}
		live = slices.DeleteFunc(live, func(p *shown) bool { return !keep(p.e.eval(node)) })

		return nil
	}
	is := func(v truth) bool { return v == yes }
	isNot := func(v truth) bool { return v != yes }

	if err := settle(f, is); err != nil {
		return refusal(err)
	}
	if len(arms) > 1 {
		if err := settle(arms[i].node, is); err != nil {
			return refusal(err)
		}
		for _, earlier := range arms[:i] {
			if err := settle(earlier.node, isNot); err != nil {
				return refusal(err)
			}
		}
	}

	for _, l := range lists {
		if !l.in(s.ask) {
			continue
		}
		var ask []*shown
		for _, p := range live {
			if !p.have[l] {
				ask = append(ask, p)
			}
		}
		if err := s.fill(ctx, t, l, ask); err != nil {
			return refusal(err)
		}
	}

	for _, p := range live {
		if res := s.emit(p.e); res.Code != wire.Success {
			return res
		}
	}

	return wire.Ok
}

// fill reads one list for several people and adds it to their entries.
func (s *search) fill(ctx context.Context, t *tenant, l list, who []*shown) error {
	if len(who) == 0 {
		return nil
	}
	d := s.d
	ids := make([][]byte, len(who))
	for i, p := range who {
		ids[i] = p.id
	}

	switch l {
	case listMail:
		got, err := d.addressesOf(ctx, t, ids)
		if err != nil {
			return err
		}
		for _, p := range who {
			p.e.read(attrMail)
			p.e.add(attrMail, got[string(p.id)]...)
		}

	case listPhone:
		got, err := d.numbersOf(ctx, t, s.reads, ids)
		if err != nil {
			return err
		}
		for _, p := range who {
			p.e.read(l.attrs()...)
			for _, n := range got[string(p.id)] {
				p.e.add(phoneAttrs[n.GetKind()], n.GetNumber())
			}
		}

	case listMemberOf:
		got, err := d.placesOf(ctx, t, s.reads, ids)
		if err != nil {
			return err
		}
		for _, p := range who {
			p.e.read(attrMemberOf)
			p.e.add(attrMemberOf, got[string(p.id)]...)
		}

	case listManager:
		got, err := d.managersOf(ctx, t, s.reads, ids)
		if err != nil {
			return err
		}
		for _, p := range who {
			p.e.read(attrManager)
			p.e.add(attrManager, got[string(p.id)]...)
		}
	}
	for _, p := range who {
		p.have[l] = true
	}

	return nil
}

// filterLimit is how many filters one list of roster's carries, joined by
// `or` (`server/pd`, `EmailFilterLimit` and its siblings, which are the same
// number for every kind). One more is refused, not clamped.
const filterLimit = 32

// chunks asks about several rows at once, [filterLimit] of them a call.
//
// A list beside a person -- their addresses, their numbers -- was asked for
// one person a call, for every person a search passed whether or not they
// had any: a page of a hundred people was a hundred calls of each (#281).
// A list takes thirty-two filters, so a page is four.
func chunks(ids [][]byte, ask func(some [][]byte) error) error {
	for len(ids) > 0 {
		n := min(len(ids), filterLimit)
		if err := ask(ids[:n]); err != nil {
			return err
		}
		ids = ids[n:]
	}

	return nil
}

// holderRefs is people as the filters of a list name them.
func holderRefs(ids [][]byte) []*rstr.HolderRef {
	out := make([]*rstr.HolderRef, len(ids))
	for i, id := range ids {
		out[i] = rstr.HolderRef_builder{Id: id}.Build()
	}

	return out
}

// addressesOf is `mail` for each of several people, by their identifier: the
// addresses of theirs somebody has checked. An unverified one is a claim, and
// a directory is what other systems believe.
//
// In the order a token reads them (`server/me`, `emails`): by when each was
// proved, and two proved at one instant by their rows' identifiers, which is
// the order they were written in. The first is the address a front door puts
// in a token's `email`, and most clients take the first `mail`; it was the
// first written, which is another address for somebody who proved their second
// one first (#281).
func (d *Directory) addressesOf(ctx context.Context, t *tenant, holders [][]byte) (map[string][]string, error) {
	proved := map[string][]*rstr.Email{}
	err := chunks(holders, func(some [][]byte) error {
		var filters []*rstr.EmailFilter
		for _, ref := range holderRefs(some) {
			filters = append(filters, rstr.EmailFilter_builder{Holder: ref}.Build())
		}
		after := ""
		for {
			vs, err := d.roster.Email().List(t.on(ctx), rstr.EmailListRequest_builder{
				Filters: filters, Size: DefaultPageSize, After: after,
			}.Build())
			if err != nil {
				return err
			}
			for _, v := range vs.GetItems() {
				if v.GetDateVerified() != nil {
					k := string(v.GetHolder().GetId())
					proved[k] = append(proved[k], v)
				}
			}
			if after = vs.GetNext(); after == "" {
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}

	out := make(map[string][]string, len(proved))
	for k, vs := range proved {
		slices.SortStableFunc(vs, func(a, b *rstr.Email) int {
			if c := a.GetDateVerified().AsTime().Compare(b.GetDateVerified().AsTime()); c != 0 {
				return c
			}

			return bytes.Compare(a.GetId(), b.GetId())
		})
		for _, v := range vs {
			out[k] = append(out[k], v.GetAddress())
		}
	}

	return out, nil
}

// numbersOf is where each of several people is reached: `telephoneNumber`,
// `mobile` and the rest of [phoneAttrs].
//
// A key whose role does not name the read publishes none, rather than failing
// the search: a directory installed before numbers were kept here names what
// it read then, and everything else about a person should go on being
// answered (`docs/ldap.md` § *The key this process holds*). It is asked once
// a search and not again.
func (d *Directory) numbersOf(ctx context.Context, t *tenant, r *reads, holders [][]byte) (map[string][]*rstr.Phone, error) {
	out := map[string][]*rstr.Phone{}
	err := chunks(holders, func(some [][]byte) error {
		if r.noNumbers {
			return nil
		}
		var filters []*rstr.PhoneFilter
		for _, ref := range holderRefs(some) {
			filters = append(filters, rstr.PhoneFilter_builder{Holder: ref}.Build())
		}
		after := ""
		for {
			vs, err := d.roster.Phone().List(t.on(ctx), rstr.PhoneListRequest_builder{
				Filters: filters, Size: DefaultPageSize, After: after,
			}.Build())
			if status.Code(err) == codes.PermissionDenied {
				r.noNumbers = true

				return nil
			}
			if err != nil {
				return err
			}
			for _, v := range vs.GetItems() {
				k := string(v.GetHolder().GetId())
				out[k] = append(out[k], v)
			}
			if after = vs.GetNext(); after == "" {
				return nil
			}
		}
	})
	if err != nil || r.noNumbers {
		return nil, err
	}

	return out, nil
}

// managersOf is `manager` for each of several people: the DNs of who they
// report to, which is a `Relation` of theirs that says so (`managerLabel`).
// Whoever is not in the tree is not named ([Directory.listed]).
//
// A role that does not name the read publishes none, for
// [Directory.numbersOf]'s reason.
func (d *Directory) managersOf(ctx context.Context, t *tenant, r *reads, holders [][]byte) (map[string][]string, error) {
	type report struct{ holder, to []byte }
	var reports []report
	err := chunks(holders, func(some [][]byte) error {
		if r.noManagers {
			return nil
		}
		var filters []*rstr.RelationFilter
		for _, ref := range holderRefs(some) {
			filters = append(filters, rstr.RelationFilter_builder{Holder: ref}.Build())
		}
		after := ""
		for {
			vs, err := d.roster.Relation().List(t.on(ctx), rstr.RelationListRequest_builder{
				Filters: filters, Size: DefaultPageSize, After: after,
			}.Build())
			if status.Code(err) == codes.PermissionDenied {
				r.noManagers = true

				return nil
			}
			if err != nil {
				return err
			}
			for _, v := range vs.GetItems() {
				if _, ok := v.GetLabels()[managerLabel]; ok {
					reports = append(reports, report{holder: v.GetHolder().GetId(), to: v.GetOther().GetId()})
				}
			}
			if after = vs.GetNext(); after == "" {
				return nil
			}
		}
	})
	if err != nil || r.noManagers {
		return nil, err
	}

	whom := make([][]byte, len(reports))
	for i, v := range reports {
		whom[i] = v.to
	}
	if err := d.holders(ctx, t, r, whom); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, v := range reports {
		if who := r.holders[string(v.to)]; d.listed(t, who) {
			out[string(v.holder)] = append(out[string(v.holder)], d.personDN(t, who).String())
		}
	}

	return out, nil
}

// arm is one read of roster that answers part of a filter over people: every
// person `node` matches is among the rows it reads. A filter is one arm, or
// several where it is an `or` ([planOf]).
type arm struct {
	node *wire.Filter
	kind armKind
	// what is looked for: an alias, an identifier, an address, the name of a
	// group or a team.
	value string
	// and for the three kinds that are a `Holder.Search`, what it is asked.
	search *rstr.HolderSearchRequest
}

// armKind is which read an arm is, from the one that reads fewest rows: a
// kind's number is how an `and` chooses between its children.
type armKind int

const (
	armNobody     armKind = iota // an equality or a substring with nothing in it: no read
	armAlias                     // `(uid=…)`: Holder.Get by name
	armID                        // `(entryUUID=…)`: Holder.Get by identifier
	armAddress                   // `(mail=…)`: Email.Get, then whose it is
	armMembers                   // `(memberOf=…)`: the group's or the team's own list of members
	armEmployee                  // `(employeeNumber=…)`: Holder.Search
	armDepartment                // `(departmentNumber=…)`: Holder.Search
	armFragment                  // a name or a fragment of one: Holder.Search, which is a contains
	armEverybody                 // none of the above: Holder.List
)

// planOf is the reads a filter over people becomes, `docs/ldap.md` § Search.
// The filter is still asked of every row read; the plan only decides how few
// rows that is.
//
// # An `or` is planned
//
// Only the direct children of a top `and` were looked at, so
// `(&(objectClass=inetOrgPerson)(|(uid=x)(mail=x)))` -- the filter many
// clients ship with for a sign-in -- was a read of everybody, with their
// addresses: more than a thousand calls among a thousand people, at every
// sign-in (#281). An `or` whose every arm is a read is the union of them; an
// `and` is the cheapest of its children that is one; and a filter with an
// arm only a read of everybody answers is a read of everybody, as before.
func planOf(f *wire.Filter) []arm {
	if arms := narrow(f); arms != nil {
		return arms
	}

	return []arm{{node: nil, kind: armEverybody}}
}

// narrow is the reads that between them answer everybody a node matches, or
// nil where only a read of everybody does.
func narrow(f *wire.Filter) []arm {
	if f == nil {
		return nil
	}
	switch f.Op {
	case wire.FilterAnd:
		// Whoever the `and` matches, each child matches: any one child's
		// reads answer it, and the one that reads fewest is taken.
		var best []arm
		for _, c := range f.Children {
			if got := narrow(c); got != nil && (best == nil || dearest(got) < dearest(best)) {
				best = got
			}
		}

		return best
	case wire.FilterOr:
		// An `or` of nothing matches nobody, RFC 4526.
		out := []arm{}
		for _, c := range f.Children {
			got := narrow(c)
			if got == nil {
				return nil
			}
			out = append(out, got...)
		}
		if len(out) == 0 {
			out = append(out, arm{node: f, kind: armNobody})
		}

		return out
	case wire.FilterEqual, wire.FilterSubstrings:
		if a, ok := leaf(f); ok {
			return []arm{a}
		}
	}

	return nil
}

// dearest is the kind of the arm that reads most.
func dearest(arms []arm) armKind {
	worst := armNobody
	for _, a := range arms {
		worst = max(worst, a.kind)
	}

	return worst
}

// blank is whether a value has nothing in it: empty, or spaces alone.
func blank(v string) bool { return strings.TrimSpace(v) == "" }

// asksNothing is whether an equality or a substring node asks for nothing:
// its value is blank, or every fragment of it is. No attribute here has such
// a value, so the node matches nobody ([entry.eval]) and reading for it is
// reading for nothing.
//
// It was read for: `(cn=)` became a `Holder.Search` for nothing, which
// roster refuses, and the refusal went back to the client as `protocolError`
// with roster's sentence, for a filter that is well formed (#281).
func asksNothing(f *wire.Filter) bool {
	switch f.Op {
	case wire.FilterEqual:
		return blank(f.Value)
	case wire.FilterSubstrings:
		if !blank(f.Initial) || !blank(f.Final) {
			return false
		}
		for _, a := range f.Any {
			if !blank(a) {
				return false
			}
		}

		return true
	}

	return false
}

// isCalled is whether an attribute is one of the three that say what somebody
// is called, each of which `Holder.Search` finds by a fragment: it looks in
// the alias, the name somebody was added with and the display name, and `cn`,
// `sn` and `displayName` are each one of those ([Directory.personEntry]).
func isCalled(attr string) bool {
	return strings.EqualFold(attr, attrCn) || strings.EqualFold(attr, attrSn) || strings.EqualFold(attr, attrDisplayName)
}

// leaf is the read one equality or substring becomes, if it becomes one.
func leaf(f *wire.Filter) (arm, bool) {
	if asksNothing(f) {
		return arm{node: f, kind: armNobody}, true
	}
	is := func(name string) bool { return strings.EqualFold(f.Attr, name) }
	fragment := func(q string) (arm, bool) {
		return arm{node: f, kind: armFragment, search: rstr.HolderSearchRequest_builder{Q: proto.String(q)}.Build()}, true
	}

	switch f.Op {
	case wire.FilterEqual:
		switch {
		case is(attrUid):
			return arm{node: f, kind: armAlias, value: f.Value}, true
		case is(attrEntryUUID):
			return arm{node: f, kind: armID, value: f.Value}, true
		case is(attrMail):
			return arm{node: f, kind: armAddress, value: f.Value}, true
		case is(attrMemberOf):
			return arm{node: f, kind: armMembers, value: f.Value}, true
		case is(attrEmployeeNo):
			return arm{node: f, kind: armEmployee, search: rstr.HolderSearchRequest_builder{EmployeeNo: proto.String(f.Value)}.Build()}, true
		case is(attrDepartment):
			return arm{node: f, kind: armDepartment, search: rstr.HolderSearchRequest_builder{Department: proto.String(f.Value)}.Build()}, true
		case isCalled(f.Attr):
			// `Search` is a contains; the filter then keeps the exact ones.
			return fragment(f.Value)
		}
	case wire.FilterSubstrings:
		if is(attrUid) || isCalled(f.Attr) {
			// The longest literal part is the most selective fragment. One
			// that is spaces alone is no fragment: `Search` trims what it is
			// asked, and would be asked for nothing.
			q := ""
			for _, a := range append([]string{f.Initial, f.Final}, f.Any...) {
				if !blank(a) && len(a) > len(q) {
					q = a
				}
			}
			if q != "" {
				return fragment(q)
			}
		}
	}

	return arm{}, false
}

// read is the rows of one arm from roster's cursor `after`, at most `size`
// of them where the arm is a list of people, and the cursor after them: empty
// when the arm has no more.
func (s *search) read(ctx context.Context, t *tenant, a arm, after string, size int) ([]row, string, error) {
	d := s.d
	switch a.kind {
	case armNobody:
		return nil, "", nil

	case armAlias:
		v, err := d.byAlias(ctx, t, s.reads, a.value)
		if err != nil || v == nil {
			return nil, "", err
		}

		return []row{{holder: v}}, "", nil

	case armID:
		// One read by identifier. It was a read of everybody, and it is the
		// attribute a client is told to key on (`docs/ldap.md` § A person).
		// What is not an identifier, or is one of another kind of row, is
		// nobody's.
		id, err := pdid.Parse(strings.TrimSpace(a.value))
		if err != nil {
			return nil, "", nil
		}
		v, err := d.holder(ctx, t, s.reads, id.Bytes())
		if status.Code(err) == codes.InvalidArgument {
			return nil, "", nil
		}
		if err != nil || v == nil {
			return nil, "", err
		}

		return []row{{holder: v}}, "", nil

	case armAddress:
		// As roster stores it: the same normalisation `Email.Add` holds the
		// address to, so a client's `Kim@` finds the row `kim@` is.
		v, err := d.roster.Email().Get(t.on(ctx), rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{TenantId: t.id.Bytes(), Address: proto.String(front.Address(a.value))}.Build()}.Build(),
		}.Build())
		if gone(err) {
			return nil, "", nil
		}
		if err != nil {
			return nil, "", err
		}
		h, err := d.holder(ctx, t, s.reads, v.GetHolder().GetId())
		if err != nil || h == nil {
			// Nil is an address still held by somebody who has been erased:
			// nobody's here, as they are.
			return nil, "", err
		}
		r := row{holder: h}
		if v.GetDateVerified() != nil {
			r.known = map[string][]string{attrMail: {v.GetAddress()}}
		}

		return []row{r}, "", nil

	case armMembers:
		// A page of the group's or the team's own list of members, which is
		// roster's to page as the people are: it was read whole, and was one
		// page of the client's however many it held.
		at, err := s.place(ctx, t, a.value)
		if err != nil || at == nil {
			return nil, "", err
		}
		ids, next, err := d.members(ctx, t, at, after, size)
		if err != nil {
			return nil, "", err
		}
		if err := d.holders(ctx, t, s.reads, ids); err != nil {
			return nil, "", err
		}
		rows := make([]row, 0, len(ids))
		for _, id := range ids {
			if h := s.reads.holders[string(id)]; h != nil {
				rows = append(rows, row{holder: h, known: map[string][]string{attrMemberOf: {at.name.String()}}})
			}
		}

		return rows, next, nil

	case armEmployee, armDepartment, armFragment:
		q := proto.Clone(a.search).(*rstr.HolderSearchRequest)
		q.SetSize(int32(size))
		q.SetAfter(after)
		vs, err := d.roster.Holder().Search(t.on(ctx), q)
		if err != nil {
			return nil, "", err
		}

		return s.rows(vs.GetItems()), vs.GetNext(), nil

	default:
		vs, err := d.roster.Holder().List(t.on(ctx), rstr.HolderListRequest_builder{
			Size:  int32(size),
			After: after,
		}.Build())
		if err != nil {
			return nil, "", err
		}

		return s.rows(vs.GetItems()), vs.GetNext(), nil
	}
}

// rows is a page of people as roster listed them, kept for the rest of the
// search: somebody on it who is then named as a manager is not read again.
func (s *search) rows(vs []*rstr.Holder) []row {
	out := make([]row, len(vs))
	for i, v := range vs {
		s.reads.holders[string(v.GetId())] = v
		out[i] = row{holder: v}
	}

	return out
}

// spot is where the people of a search have been read to: which arm of its
// plan, and roster's cursor in it.
type spot struct {
	arm   int
	after string
}

// people is the step of every person under a suffix the filter keeps, read
// the way the plan says: each arm of it in turn, through roster's cursor in
// that arm ([search.through]).
func (s *search) people(ctx context.Context, t *tenant) step {
	return step{stage: stagePeople, run: func(from spot) (spot, bool, wire.Result) {
		for i := max(0, from.arm); i < len(s.plan); i++ {
			after := ""
			if i == from.arm {
				after = from.after
			}
			after, over, res := s.through(after, func(after string, size int) (string, wire.Result) {
				rows, next, err := s.read(ctx, t, s.plan[i], after, size)
				if err != nil {
					return "", refusal(err)
				}

				return next, s.show(ctx, t, s.plan, i, rows)
			})
			if res.Code != wire.Success {
				return spot{}, false, res
			}
			if !over {
				return spot{arm: i, after: after}, false, wire.Ok
			}
			// An arm begins on a page as a part of the walk does
			// ([search.roomy]).
			if i+1 < len(s.plan) && (s.full() || !s.roomy()) {
				return spot{arm: i + 1}, false, wire.Ok
			}
		}

		return spot{}, true, wire.Ok
	}}
}

func siteRef(s *rstr.Site) *rstr.SiteRef {
	return rstr.SiteRef_builder{Id: s.GetId()}.Build()
}
