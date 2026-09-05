package ldap

import (
	"context"
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
// schemas spell them; matching is without regard to case everywhere.
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
		}
	}
	if all {
		w = wants{mail: true, memberOf: true, member: true}
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
	v, err := d.roster.Holder().Get(withKey(ctx, t.key), rstr.HolderGetRequest_builder{
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
		vs, err := d.roster.Holder().Search(withKey(ctx, t.key), rstr.HolderSearchRequest_builder{
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

// personEntry shapes a holder. Nil for somebody who is not in the tree:
// disabled, which a directory says by absence rather than by a flag.
func (s *search) personEntry(ctx context.Context, t *tenant, v *rstr.Holder) (*entry, error) {
	if v.GetDateDisabled() != nil || v.GetDateErased() != nil {
		return nil, nil
	}
	id, err := pdid.From(v.GetId())
	if err != nil {
		return nil, err
	}
	d := s.d

	e := newEntry(d.personDN(t, v))
	e.add(attrObjectClass, "top", classPersonPlain, classOrgPerson, classPerson)
	e.add(attrUid, v.GetAlias())
	e.add(attrCn, v.GetName())
	// `inetOrgPerson` requires one and roster does not split names: the
	// whole name, rather than a guess at half of it.
	e.add(attrSn, v.GetName())
	p := v.GetProfile()
	e.add(attrDisplayName, p.GetDisplayName())
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

	return e, nil
}

// verifiedAddresses is `mail`: the person's addresses somebody has checked.
// An unverified one is a claim, and a directory is what other systems believe.
func (d *Directory) verifiedAddresses(ctx context.Context, t *tenant, holder []byte) ([]string, error) {
	var out []string
	after := ""
	for {
		vs, err := d.roster.Email().List(withKey(ctx, t.key), rstr.EmailListRequest_builder{
			Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{Holder: rstr.HolderRef_builder{Id: holder}.Build()}.Build()},
			Size:    int32(d.c.PageSize),
			After:   after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if v.GetDateVerified() != nil {
				out = append(out, v.GetAddress())
			}
		}
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

// plan is which roster read a filter over people becomes, `docs/ldap.md`
// § Search. The filter is still evaluated over every entry read; the plan
// only decides how few entries that is.
type plan struct {
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

// candidate ranks one node by how few rows it reads: a name is one row, a
// mailbox is one, a group's members are a list, an exact profile field is a
// few, a fragment is some.
func candidate(f *wire.Filter) (int, plan) {
	if f == nil {
		return 0, plan{}
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
		case strings.EqualFold(f.Attr, attrCn), strings.EqualFold(f.Attr, attrDisplayName):
			// `Search` is a contains; the filter then keeps the exact ones.
			return 1, plan{search: rstr.HolderSearchRequest_builder{Q: proto.String(f.Value)}.Build()}
		}
	case wire.FilterSubstrings:
		if strings.EqualFold(f.Attr, attrUid) || strings.EqualFold(f.Attr, attrCn) || strings.EqualFold(f.Attr, attrDisplayName) {
			// The longest literal part is the most selective fragment.
			q := f.Initial
			for _, a := range append(f.Any, f.Final) {
				if len(a) > len(q) {
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
		v, err := d.roster.Email().Get(withKey(ctx, t.key), rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{TenantId: t.id.Bytes(), Address: proto.String(front.Address(p.address))}.Build()}.Build(),
		}.Build())
		if err != nil {
			return "", refusal(err)
		}
		h, err := d.holder(ctx, t, s.reads, v.GetHolder().GetId())
		if err != nil {
			return "", refusal(err)
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
				if status.Code(err) == codes.NotFound {
					continue
				}

				return "", refusal(err)
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
			vs, err := d.roster.Holder().Search(withKey(ctx, t.key), q)
			if err != nil {
				return "", refusal(err)
			}
			items, next = vs.GetItems(), vs.GetNext()
		} else {
			vs, err := d.roster.Holder().List(withKey(ctx, t.key), rstr.HolderListRequest_builder{
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
