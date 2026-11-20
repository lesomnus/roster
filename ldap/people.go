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

// wants is which of the attributes that cost a read the search needs: those
// the client asked for, and those its filter mentions -- a filter on `mail`
// has to see the addresses to be evaluated, whether or not they are sent.
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

func wanted(req wire.SearchRequest) wants {
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
	w.manager = w.manager || req.Filter.Mentions(attrManager)
	for _, a := range phoneAttrs {
		w.phone = w.phone || req.Filter.Mentions(a)
	}
	w.mail = w.mail || req.Filter.Mentions(attrMail)
	w.memberOf = w.memberOf || req.Filter.Mentions(attrMemberOf)
	w.member = w.member || req.Filter.Mentions(attrMember)

	return w
}

// person is one entry by alias, or nothing.
func (s *search) person(ctx context.Context, t *tenant, alias string) (*entry, wire.Result) {
	v, err := s.d.byAlias(ctx, t, alias)
	if err != nil {
		return nil, refusal(err)
	}
	if v == nil {
		return nil, wire.Refuse(wire.NoSuchObject, "")
	}
	e, err := s.personEntry(ctx, t, v)
	if err != nil {
		return nil, refusal(err)
	}
	if e == nil {
		return nil, wire.Refuse(wire.NoSuchObject, "")
	}

	return e, wire.Ok
}

// byAlias is the holder called `alias`, without regard to case -- `uid` is
// `caseIgnoreMatch` and a client types what it likes -- or nil for nobody.
// The exact name first, which is one indexed read; then `Search`, whose
// match on the alias is already without regard to case.
func (d *Directory) byAlias(ctx context.Context, t *tenant, alias string) (*rstr.Holder, error) {
	v, err := d.roster.Holder().Get(t.on(ctx), rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias:  proto.String(alias),
			Tenant: rstr.TenantRef_builder{Id: t.id.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	if err == nil {
		return v, nil
	}
	if status.Code(err) != codes.NotFound {
		return nil, err
	}

	after := ""
	for {
		vs, err := d.roster.Holder().Search(t.on(ctx), rstr.HolderSearchRequest_builder{
			Q: proto.String(alias), Size: int32(d.c.PageSize), After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if strings.EqualFold(v.GetAlias(), alias) {
				return v, nil
			}
		}
		if after = vs.GetNext(); after == "" {
			return nil, nil
		}
	}
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

// personEntry shapes a holder. Nil for somebody who is not in the tree
// ([Directory.listed]).
func (s *search) personEntry(ctx context.Context, t *tenant, v *rstr.Holder) (*entry, error) {
	d := s.d
	if !d.listed(t, v) {
		return nil, nil
	}
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

	if s.want.mail {
		addrs, err := d.verifiedAddresses(ctx, t, v.GetId())
		if err != nil {
			return nil, err
		}
		e.add(attrMail, addrs...)
	}
	if s.want.memberOf {
		names, err := d.memberOf(ctx, t, s.reads, v.GetId())
		if err != nil {
			return nil, err
		}
		e.add(attrMemberOf, names...)
	}
	if s.want.phone {
		ns, err := d.numbers(ctx, t, v.GetId())
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			e.add(phoneAttrs[n.GetKind()], n.GetNumber())
		}
	}
	if s.want.manager {
		names, err := d.managers(ctx, t, s.reads, v.GetId())
		if err != nil {
			return nil, err
		}
		e.add(attrManager, names...)
	}

	return e, nil
}

// managers is `manager`: the DNs of who somebody reports to, which is a
// `Relation` of theirs that says so (`managerLabel`). Whoever is not in the
// tree is not named ([Directory.listed]).
//
// A role that does not name the read publishes none, for [Directory.numbers]'s
// reason.
func (d *Directory) managers(ctx context.Context, t *tenant, r *reads, holder []byte) ([]string, error) {
	var out []string
	after := ""
	for {
		vs, err := d.roster.Relation().List(t.on(ctx), rstr.RelationListRequest_builder{
			Filters: []*rstr.RelationFilter{rstr.RelationFilter_builder{Holder: rstr.HolderRef_builder{Id: holder}.Build()}.Build()},
			Size:    int32(d.c.PageSize),
			After:   after,
		}.Build())
		if status.Code(err) == codes.PermissionDenied {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if _, ok := v.GetLabels()[managerLabel]; !ok {
				continue
			}
			who, err := d.holder(ctx, t, r, v.GetOther().GetId())
			if err != nil {
				return nil, err
			}
			if !d.listed(t, who) {
				continue
			}
			out = append(out, d.personDN(t, who).String())
		}
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

// numbers is where somebody is reached: `telephoneNumber`, `mobile` and the
// rest of [phoneAttrs]. Read only when asked for or filtered on, as their
// addresses are, since it costs a list.
//
// A key whose role does not name the read publishes none, rather than failing
// the search: a directory installed before numbers were kept here names what
// it read then, and everything else about a person should go on being
// answered (`docs/ldap.md` § *The key this process holds*).
func (d *Directory) numbers(ctx context.Context, t *tenant, holder []byte) ([]*rstr.Phone, error) {
	var out []*rstr.Phone
	after := ""
	for {
		vs, err := d.roster.Phone().List(t.on(ctx), rstr.PhoneListRequest_builder{
			Filters: []*rstr.PhoneFilter{rstr.PhoneFilter_builder{Holder: rstr.HolderRef_builder{Id: holder}.Build()}.Build()},
			Size:    int32(d.c.PageSize),
			After:   after,
		}.Build())
		if status.Code(err) == codes.PermissionDenied {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, vs.GetItems()...)
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

// verifiedAddresses is `mail`: the person's addresses somebody has checked.
// An unverified one is a claim, and a directory is what other systems believe.
//
// In the order a token reads them (`server/me`, `emails`): by when each was
// proved, and two proved at one instant by their rows' identifiers, which is
// the order they were written in. The first is the address a front door puts
// in a token's `email`, and most clients take the first `mail`; it was the
// first written, which is another address for somebody who proved their second
// one first (#281).
func (d *Directory) verifiedAddresses(ctx context.Context, t *tenant, holder []byte) ([]string, error) {
	var proved []*rstr.Email
	after := ""
	for {
		vs, err := d.roster.Email().List(t.on(ctx), rstr.EmailListRequest_builder{
			Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{Holder: rstr.HolderRef_builder{Id: holder}.Build()}.Build()},
			Size:    int32(d.c.PageSize),
			After:   after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if v.GetDateVerified() != nil {
				proved = append(proved, v)
			}
		}
		if after = vs.GetNext(); after == "" {
			break
		}
	}

	return addresses(proved), nil
}

// addresses is proved addresses in the order a token reads them.
func addresses(proved []*rstr.Email) []string {
	slices.SortStableFunc(proved, func(a, b *rstr.Email) int {
		if c := a.GetDateVerified().AsTime().Compare(b.GetDateVerified().AsTime()); c != 0 {
			return c
		}

		return bytes.Compare(a.GetId(), b.GetId())
	})
	out := make([]string, len(proved))
	for i, v := range proved {
		out[i] = v.GetAddress()
	}

	return out
}

// plan is which roster read a filter over people becomes, `docs/ldap.md`
// § Search. The filter is still evaluated over every entry read; the plan
// only decides how few entries that is.
type plan struct {
	nobody   bool                      // an equality or a substring with nothing in it: no read
	alias    string                    // `(uid=…)`: Holder.Get
	address  string                    // `(mail=…)`: Email.Get, then the holder
	memberOf string                    // `(memberOf=…)`: one membership list
	search   *rstr.HolderSearchRequest // a fragment, a department, an employee number: Holder.Search
	// none of the above: Holder.List
}

func planPeople(f *wire.Filter) plan {
	var p plan
	best := 0
	consider := func(n *wire.Filter) {
		rank, cand := candidate(n)
		if rank > best {
			best, p = rank, cand
		}
	}
	if f != nil && f.Op == wire.FilterAnd {
		for _, c := range f.Children {
			consider(c)
		}
	} else {
		consider(f)
	}

	return p
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
// `sn` and `displayName` are each one of those ([search.personEntry]).
func isCalled(attr string) bool {
	return strings.EqualFold(attr, attrCn) || strings.EqualFold(attr, attrSn) || strings.EqualFold(attr, attrDisplayName)
}

// candidate ranks one node by how few rows it reads: nothing asked for is
// none, a name is one row, a mailbox is one, a group's members are a list, an
// exact profile field is a few, a fragment is some.
func candidate(f *wire.Filter) (int, plan) {
	if f == nil {
		return 0, plan{}
	}
	if asksNothing(f) {
		return 7, plan{nobody: true}
	}
	switch f.Op {
	case wire.FilterEqual:
		switch {
		case strings.EqualFold(f.Attr, attrUid):
			return 6, plan{alias: f.Value}
		case strings.EqualFold(f.Attr, attrMail):
			return 5, plan{address: f.Value}
		case strings.EqualFold(f.Attr, attrMemberOf):
			return 4, plan{memberOf: f.Value}
		case strings.EqualFold(f.Attr, attrEmployeeNo):
			return 3, plan{search: rstr.HolderSearchRequest_builder{EmployeeNo: proto.String(f.Value)}.Build()}
		case strings.EqualFold(f.Attr, attrDepartment):
			return 2, plan{search: rstr.HolderSearchRequest_builder{Department: proto.String(f.Value)}.Build()}
		case isCalled(f.Attr):
			// `Search` is a contains; the filter then keeps the exact ones.
			return 1, plan{search: rstr.HolderSearchRequest_builder{Q: proto.String(f.Value)}.Build()}
		}
	case wire.FilterSubstrings:
		if strings.EqualFold(f.Attr, attrUid) || isCalled(f.Attr) {
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
				return 1, plan{search: rstr.HolderSearchRequest_builder{Q: proto.String(q)}.Build()}
			}
		}
	}

	return 0, plan{}
}

// people sends every person under a suffix the filter keeps, read the way
// the plan says, from roster's cursor `after`. It answers where it stopped:
// empty when there is no more, and otherwise the cursor the next page of a
// paged search carries on from -- and a search that is not paged never
// stops early.
//
// A page of roster is a page of the client: the reads are the client's page
// size, and a page ends where a roster page ends, so that a cookie is always
// a roster cursor and no row is skipped or repeated. Fewer entries than the
// page size means the filter dropped some, which RFC 2696 allows; a page
// that would be empty reads on, so a selective filter over a big tenant does
// not answer with a run of empty pages.
func (s *search) people(ctx context.Context, t *tenant, after string) (string, wire.Result) {
	d, req := s.d, s.req
	p := planPeople(req.Filter)

	one := func(v *rstr.Holder) wire.Result {
		e, err := s.personEntry(ctx, t, v)
		if err != nil {
			return refusal(err)
		}

		return s.send(e)
	}

	switch {
	case p.nobody:
		return "", wire.Ok

	case p.alias != "":
		v, err := d.byAlias(ctx, t, p.alias)
		if err != nil {
			return "", refusal(err)
		}
		if v == nil {
			return "", wire.Ok
		}

		return "", one(v)

	case p.address != "":
		// As roster stores it: the same normalisation `Email.Add` holds the
		// address to, so a client's `Kim@` finds the row `kim@` is.
		v, err := d.roster.Email().Get(t.on(ctx), rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{TenantId: t.id.Bytes(), Address: proto.String(front.Address(p.address))}.Build()}.Build(),
		}.Build())
		if err != nil {
			return "", refusal(err)
		}
		h, err := d.holder(ctx, t, s.reads, v.GetHolder().GetId())
		if err != nil {
			return "", refusal(err)
		}
		if h == nil {
			// The address is still somebody's who has been erased: nobody's
			// here, as they are.
			return "", wire.Ok
		}

		return "", one(h)

	case p.memberOf != "":
		ids, res := d.membersOf(ctx, t, s.reads, p.memberOf)
		if res.Code != wire.Success {
			return "", res
		}
		for _, id := range ids {
			h, err := d.holder(ctx, t, s.reads, id)
			if err != nil {
				return "", refusal(err)
			}
			if h == nil {
				continue
			}
			if res := one(h); res.Code != wire.Success {
				return "", res
			}
		}

		return "", wire.Ok
	}

	size := d.c.PageSize
	paged := req.Paging != nil
	if paged && req.Paging.Size > 0 && req.Paging.Size < size {
		size = req.Paging.Size
	}

	for {
		var items []*rstr.Holder
		var next string
		if p.search != nil {
			q := proto.Clone(p.search).(*rstr.HolderSearchRequest)
			q.SetSize(int32(size))
			q.SetAfter(after)
			vs, err := d.roster.Holder().Search(t.on(ctx), q)
			if err != nil {
				return "", refusal(err)
			}
			items, next = vs.GetItems(), vs.GetNext()
		} else {
			vs, err := d.roster.Holder().List(t.on(ctx), rstr.HolderListRequest_builder{
				Size:  int32(size),
				After: after,
			}.Build())
			if err != nil {
				return "", refusal(err)
			}
			items, next = vs.GetItems(), vs.GetNext()
		}

		sent := 0
		for _, v := range items {
			before := s.sent
			if res := one(v); res.Code != wire.Success {
				return "", res
			}
			if s.sent > before {
				sent++
			}
		}

		if next == "" {
			return "", wire.Ok
		}
		after = next
		if paged && sent > 0 {
			return next, wire.Ok
		}
	}
}

func siteRef(s *rstr.Site) *rstr.SiteRef {
	return rstr.SiteRef_builder{Id: s.GetId()}.Build()
}
