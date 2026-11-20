package ldap

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/ldap/wire"
	rstr "github.com/lesomnus/roster/rstr"
)

// The attributes of a group, a team and a site, `docs/ldap.md` § A group, a
// team, a site. `groupOfNames` for the two that have people in them; a site
// is an `organizationalUnit`, as are the fixed nodes of the tree.
const (
	attrMember      = "member"
	attrDescription = "description"
	attrOu          = "ou"
	classGroup      = "groupOfNames"
	classUnit       = "organizationalUnit"
	ouTeams         = "teams"
)

// reads is what one search has already read from roster: the groups, teams
// and sites by identifier, and the people by identifier, so that a group of
// forty and a person in six teams cost one read per row and not one per
// mention. It lives for one search and never longer -- there is no cache
// across searches, on purpose (`docs/ldap.md` § Search).
//
// # What is gone is read once too, and is nothing
//
// A row that is not there is kept as nil, and each of the four reads below
// answers nil for it and no error. An erase stamps the row it names and
// leaves what hung on it: a place in a group, on a team, a relation
// (`docs/usage/rules.md`, *What is gone grants nothing*). So a read by an
// identifier found on such a row is `NotFound` as a matter of course, and
// that is not the search failing or finding nobody -- it is one mention to
// leave out. The `NotFound` used to be handed up, and on its way out of a
// search it is an empty answer ([refusal]): somebody in a group that had been
// erased was dropped from every search that read `memberOf`, with success
// (#281). Whoever asks decides what a mention of nothing means; none of them
// can mistake it for the end of the entry they are making.
type reads struct {
	groups  map[string]*rstr.Group
	teams   map[string]*rstr.Team
	sites   map[string]*rstr.Site
	holders map[string]*rstr.Holder

	// noNumbers and noManagers are that the directory's role names no read of
	// them, which is learned by asking and asked once
	// ([Directory.numbersOf], [Directory.managersOf]).
	noNumbers, noManagers bool
}

func newReads() *reads {
	return &reads{groups: map[string]*rstr.Group{}, teams: map[string]*rstr.Team{}, sites: map[string]*rstr.Site{}, holders: map[string]*rstr.Holder{}}
}

// gone is whether a read by identifier answered that there is no such row:
// erased, or never there.
func gone(err error) bool { return status.Code(err) == codes.NotFound }

// group is a group by identifier, or nil for one that is gone.
func (d *Directory) group(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Group, error) {
	if v, ok := r.groups[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Group().Get(t.on(ctx), rstr.GroupGetRequest_builder{Ref: rstr.GroupRef_builder{Id: id}.Build()}.Build())
	if err != nil && !gone(err) {
		return nil, err
	}
	r.groups[string(id)] = v

	return v, nil
}

// team is a team by identifier, or nil for one that is gone.
func (d *Directory) team(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Team, error) {
	if v, ok := r.teams[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Team().Get(t.on(ctx), rstr.TeamGetRequest_builder{Ref: rstr.TeamRef_builder{Id: id}.Build()}.Build())
	if err != nil && !gone(err) {
		return nil, err
	}
	r.teams[string(id)] = v

	return v, nil
}

// site is a site by identifier, or nil for one that is gone.
func (d *Directory) site(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Site, error) {
	if v, ok := r.sites[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Site().Get(t.on(ctx), rstr.SiteGetRequest_builder{Ref: rstr.SiteRef_builder{Id: id}.Build()}.Build())
	if err != nil && !gone(err) {
		return nil, err
	}
	r.sites[string(id)] = v

	return v, nil
}

// holder is somebody by identifier, or nil for somebody who is gone.
func (d *Directory) holder(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Holder, error) {
	if v, ok := r.holders[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Holder().Get(t.on(ctx), rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Id: id}.Build()}.Build())
	if err != nil && !gone(err) {
		return nil, err
	}
	r.holders[string(id)] = v

	return v, nil
}

// holders reads several people by identifier, those of them this search has
// not read yet, and keeps them: afterwards each is in `r.holders`, as nil if
// they are gone.
//
// One list for thirty-two of them ([chunks]). The members of a group were a
// `Holder.Get` each: a hundred calls, and a hundred rows of the access
// record, to name the members of a group of a hundred (#281).
func (d *Directory) holders(ctx context.Context, t *tenant, r *reads, ids [][]byte) error {
	var unread [][]byte
	asked := map[string]bool{}
	for _, id := range ids {
		if _, ok := r.holders[string(id)]; ok || asked[string(id)] {
			continue
		}
		asked[string(id)] = true
		unread = append(unread, id)
	}
	if len(unread) == 1 {
		_, err := d.holder(ctx, t, r, unread[0])

		return err
	}

	return chunks(unread, func(some [][]byte) error {
		var filters []*rstr.HolderFilter
		for _, ref := range holderRefs(some) {
			filters = append(filters, rstr.HolderFilter_builder{Ref: ref}.Build())
		}
		after := ""
		for {
			vs, err := d.roster.Holder().List(t.on(ctx), rstr.HolderListRequest_builder{
				Filters: filters, Size: DefaultPageSize, After: after,
			}.Build())
			if err != nil {
				return err
			}
			for _, v := range vs.GetItems() {
				r.holders[string(v.GetId())] = v
			}
			if after = vs.GetNext(); after == "" {
				break
			}
		}
		// Whoever the list did not answer is gone.
		for _, id := range some {
			if _, ok := r.holders[string(id)]; !ok {
				r.holders[string(id)] = nil
			}
		}

		return nil
	})
}

// Names in the tree.

func (d *Directory) groupDN(t *tenant, g *rstr.Group) dn {
	return t.base.child(attrOu, ouGroups).child(attrCn, g.GetAlias())
}

func (d *Directory) siteDN(t *tenant, s *rstr.Site) dn {
	return t.base.child(attrOu, ouSites).child(attrOu, s.GetAlias())
}

// teamDN is `cn=<team>,ou=teams,ou=<site>,ou=sites,<suffix>`, or
// `cn=<team>,ou=teams,<suffix>` for a team with no site. Nil for a team whose
// site is gone: it has no name in this tree, so it is not listed and nobody is
// said to be on it.
func (d *Directory) teamDN(ctx context.Context, t *tenant, r *reads, team *rstr.Team) (dn, error) {
	under := t.base
	if id := team.GetSite().GetId(); len(id) > 0 {
		s, err := d.site(ctx, t, r, id)
		if err != nil {
			return nil, err
		}
		if s == nil {
			return nil, nil
		}
		under = d.siteDN(t, s)
	}

	return under.child(attrOu, ouTeams).child(attrCn, team.GetAlias()), nil
}

func (d *Directory) personDN(t *tenant, v *rstr.Holder) dn {
	return t.base.child(attrOu, ouPeople).child(attrUid, v.GetAlias())
}

// Entries.

// groupEntry shapes a group. Nil for one the filter has ruled out before its
// members were read ([search.withMembers]).
func (s *search) groupEntry(ctx context.Context, t *tenant, g *rstr.Group) (*entry, error) {
	d := s.d
	e := newEntry(d.groupDN(t, g))
	e.add(attrObjectClass, "top", classGroup)
	e.add(attrCn, g.GetAlias())
	e.add(attrDescription, firstOf(g.GetDesc(), g.GetName()))
	if id, err := pdid.From(g.GetId()); err == nil {
		e.add(attrEntryUUID, id.Uuid().String())
	}

	return s.withMembers(ctx, t, e, func() ([][]byte, error) { return d.groupMembers(ctx, t, g.GetId()) })
}

// teamEntry shapes a team. Nil for one that has no name here
// ([Directory.teamDN]), and for one the filter has ruled out.
func (s *search) teamEntry(ctx context.Context, t *tenant, team *rstr.Team) (*entry, error) {
	d := s.d
	name, err := d.teamDN(ctx, t, s.reads, team)
	if err != nil {
		return nil, err
	}
	if name == nil {
		return nil, nil
	}
	e := newEntry(name)
	e.add(attrObjectClass, "top", classGroup)
	e.add(attrCn, team.GetAlias())
	e.add(attrDescription, firstOf(team.GetDesc(), team.GetName()))
	if id, err := pdid.From(team.GetId()); err == nil {
		e.add(attrEntryUUID, id.Uuid().String())
	}

	return s.withMembers(ctx, t, e, func() ([][]byte, error) { return d.teamMembers(ctx, t, team.GetId()) })
}

// withMembers adds `member` to the entry of a group or a team, where the
// search has a use for it: the filter cannot be answered without it, or the
// filter keeps the entry and the client asked for it. Nil for an entry the
// filter rules out whoever is in it.
//
// The members were read for every group a search passed, before the filter
// was asked: `(uid=kim)` from a suffix read who is in each group of the
// tenant, to drop every one of them for having no `uid` (#281). The filter is
// asked of the row first, as it is of a person ([entry.eval]).
//
// Whoever is not in the tree is not named ([Directory.listed]), and neither
// is anybody who is gone.
func (s *search) withMembers(ctx context.Context, t *tenant, e *entry, members func() ([][]byte, error)) (*entry, error) {
	d := s.d
	e.await(attrMember)
	switch e.eval(s.req.Filter) {
	case no, undefined:
		return nil, nil
	case yes:
		if !s.ask.member {
			return e, nil
		}
	}

	ids, err := members()
	if err != nil {
		return nil, err
	}
	if err := d.holders(ctx, t, s.reads, ids); err != nil {
		return nil, err
	}
	e.read(attrMember)
	for _, id := range ids {
		if v := s.reads.holders[string(id)]; d.listed(t, v) {
			e.add(attrMember, d.personDN(t, v).String())
		}
	}

	return e, nil
}

// groupMembers and teamMembers are who is in one, as holder identifiers.
func (d *Directory) groupMembers(ctx context.Context, t *tenant, group []byte) ([][]byte, error) {
	var out [][]byte
	var after string
	for {
		vs, err := d.roster.GroupMembership().List(t.on(ctx), rstr.GroupMembershipListRequest_builder{
			Filters: []*rstr.GroupMembershipFilter{rstr.GroupMembershipFilter_builder{Group: rstr.GroupRef_builder{Id: group}.Build()}.Build()},
			Size:    DefaultPageSize, After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, m := range vs.GetItems() {
			out = append(out, m.GetHolder().GetId())
		}
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

func (d *Directory) teamMembers(ctx context.Context, t *tenant, team []byte) ([][]byte, error) {
	var out [][]byte
	var after string
	for {
		vs, err := d.roster.TeamMembership().List(t.on(ctx), rstr.TeamMembershipListRequest_builder{
			Filters: []*rstr.TeamMembershipFilter{rstr.TeamMembershipFilter_builder{Team: rstr.TeamRef_builder{Id: team}.Build()}.Build()},
			Size:    DefaultPageSize, After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, m := range vs.GetItems() {
			out = append(out, m.GetHolder().GetId())
		}
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

func (d *Directory) siteEntry(t *tenant, s *rstr.Site) *entry {
	e := newEntry(d.siteDN(t, s))
	e.add(attrObjectClass, "top", classUnit)
	e.add(attrOu, s.GetAlias())
	e.add(attrDescription, firstOf(s.GetDesc(), s.GetName()))
	if id, err := pdid.From(s.GetId()); err == nil {
		e.add(attrEntryUUID, id.Uuid().String())
	}

	return e
}

func (d *Directory) teamsUnit(t *tenant, under dn) *entry {
	e := newEntry(under.child(attrOu, ouTeams))
	e.add(attrObjectClass, "top", classUnit)
	e.add(attrOu, ouTeams)

	return e
}

// placesOf is `memberOf` for each of several people, by their identifier: the
// groups and teams they are in, as their names, groups first. AD's
// convenience rather than the standard's, and the attribute most clients ask
// for: a `member` list on every group is the truth and this is the same
// truth read from the other end.
//
// A place in a group or on a team that has been erased is skipped, as a
// group skips a member who is gone ([search.withMembers]): the place is
// still listed, on purpose, and what it points at is not read ([reads]).
func (d *Directory) placesOf(ctx context.Context, t *tenant, r *reads, holders [][]byte) (map[string][]string, error) {
	out := map[string][]string{}

	err := chunks(holders, func(some [][]byte) error {
		var filters []*rstr.GroupMembershipFilter
		for _, ref := range holderRefs(some) {
			filters = append(filters, rstr.GroupMembershipFilter_builder{Holder: ref}.Build())
		}
		after := ""
		for {
			vs, err := d.roster.GroupMembership().List(t.on(ctx), rstr.GroupMembershipListRequest_builder{
				Filters: filters, Size: DefaultPageSize, After: after,
			}.Build())
			if err != nil {
				return err
			}
			for _, m := range vs.GetItems() {
				g, err := d.group(ctx, t, r, m.GetGroup().GetId())
				if err != nil {
					return err
				}
				if g == nil {
					continue
				}
				k := string(m.GetHolder().GetId())
				out[k] = append(out[k], d.groupDN(t, g).String())
			}
			if after = vs.GetNext(); after == "" {
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}

	err = chunks(holders, func(some [][]byte) error {
		var filters []*rstr.TeamMembershipFilter
		for _, ref := range holderRefs(some) {
			filters = append(filters, rstr.TeamMembershipFilter_builder{Holder: ref}.Build())
		}
		after := ""
		for {
			vs, err := d.roster.TeamMembership().List(t.on(ctx), rstr.TeamMembershipListRequest_builder{
				Filters: filters, Size: DefaultPageSize, After: after,
			}.Build())
			if err != nil {
				return err
			}
			for _, m := range vs.GetItems() {
				team, err := d.team(ctx, t, r, m.GetTeam().GetId())
				if err != nil {
					return err
				}
				if team == nil {
					continue
				}
				name, err := d.teamDN(ctx, t, r, team)
				if err != nil {
					return err
				}
				if name == nil {
					continue
				}
				k := string(m.GetHolder().GetId())
				out[k] = append(out[k], name.String())
			}
			if after = vs.GetNext(); after == "" {
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// Walks.

// groups sends every group under a suffix the filter keeps. Not paged: a
// tenant's groups are tens, and a page of them is a page of one read.
func (s *search) groups(ctx context.Context, t *tenant) wire.Result {
	d := s.d
	var after string
	for {
		vs, err := d.roster.Group().List(t.on(ctx), rstr.GroupListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return refusal(err)
		}
		for _, g := range vs.GetItems() {
			e, err := s.groupEntry(ctx, t, g)
			if err != nil {
				return refusal(err)
			}
			if res := s.send(e); res.Code != wire.Success {
				return res
			}
		}
		if after = vs.GetNext(); after == "" {
			return wire.Ok
		}
	}
}

// findGroup is the group called `alias`, or nil. A list rather than a `Get`
// by slug because `cn` matches without regard to case and the slug does not;
// a tenant's groups are tens.
func (d *Directory) findGroup(ctx context.Context, t *tenant, r *reads, alias string) (*rstr.Group, error) {
	var after string
	for {
		vs, err := d.roster.Group().List(t.on(ctx), rstr.GroupListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return nil, err
		}
		for _, g := range vs.GetItems() {
			if strings.EqualFold(g.GetAlias(), alias) {
				r.groups[string(g.GetId())] = g

				return g, nil
			}
		}
		if after = vs.GetNext(); after == "" {
			return nil, nil
		}
	}
}

// sites sends every site, the `ou=teams` under each, and every team --
// those with a site under it, those without under the suffix's own
// `ou=teams`. `deep` is the search's scope: one-level from `ou=sites` is the
// sites alone.
func (s *search) sites(ctx context.Context, t *tenant, deep bool) wire.Result {
	d := s.d
	var after string
	for {
		vs, err := d.roster.Site().List(t.on(ctx), rstr.SiteListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return refusal(err)
		}
		for _, site := range vs.GetItems() {
			s.reads.sites[string(site.GetId())] = site
			if res := s.send(d.siteEntry(t, site)); res.Code != wire.Success {
				return res
			}
			if !deep {
				continue
			}
			if res := s.send(d.teamsUnit(t, d.siteDN(t, site))); res.Code != wire.Success {
				return res
			}
			if res := s.teams(ctx, t, siteRef(site)); res.Code != wire.Success {
				return res
			}
		}
		if after = vs.GetNext(); after == "" {
			return wire.Ok
		}
	}
}

// teams sends the teams of one site, or, with `site` nil, every team --
// which the walk from the suffix uses for the teams with no site, keeping
// those that have one for their site's branch.
func (s *search) teams(ctx context.Context, t *tenant, site *rstr.SiteRef) wire.Result {
	d := s.d
	var after string
	for {
		req := rstr.TeamListRequest_builder{Size: int32(d.c.PageSize), After: after}
		if site != nil {
			req.Filters = []*rstr.TeamFilter{rstr.TeamFilter_builder{Site: site}.Build()}
		}
		vs, err := d.roster.Team().List(t.on(ctx), req.Build())
		if err != nil {
			return refusal(err)
		}
		for _, team := range vs.GetItems() {
			if site == nil && len(team.GetSite().GetId()) > 0 {
				continue
			}
			e, err := s.teamEntry(ctx, t, team)
			if err != nil {
				return refusal(err)
			}
			if res := s.send(e); res.Code != wire.Success {
				return res
			}
		}
		if after = vs.GetNext(); after == "" {
			return wire.Ok
		}
	}
}

// findSite is `ou=<alias>,ou=sites,<suffix>`, or nil.
func (d *Directory) findSite(ctx context.Context, t *tenant, r *reads, alias string) (*rstr.Site, error) {
	var after string
	for {
		vs, err := d.roster.Site().List(t.on(ctx), rstr.SiteListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return nil, err
		}
		for _, s := range vs.GetItems() {
			if strings.EqualFold(s.GetAlias(), alias) {
				r.sites[string(s.GetId())] = s

				return s, nil
			}
		}
		if after = vs.GetNext(); after == "" {
			return nil, nil
		}
	}
}

// findTeam is a team by alias under a site, or with no site; or nil.
func (d *Directory) findTeam(ctx context.Context, t *tenant, r *reads, site *rstr.Site, alias string) (*rstr.Team, error) {
	var after string
	for {
		req := rstr.TeamListRequest_builder{Size: int32(d.c.PageSize), After: after}
		if site != nil {
			req.Filters = []*rstr.TeamFilter{rstr.TeamFilter_builder{Site: siteRef(site)}.Build()}
		}
		vs, err := d.roster.Team().List(t.on(ctx), req.Build())
		if err != nil {
			return nil, err
		}
		for _, team := range vs.GetItems() {
			if site == nil && len(team.GetSite().GetId()) > 0 {
				continue
			}
			if strings.EqualFold(team.GetAlias(), alias) {
				r.teams[string(team.GetId())] = team

				return team, nil
			}
		}
		if after = vs.GetNext(); after == "" {
			return nil, nil
		}
	}
}

// membersOf is the people a `(memberOf=<dn>)` filter names, by the group's
// or team's name, so the filter is one membership list and not everybody;
// and the name as this tree writes it, which is the value of `memberOf` each
// of them has. A name that is not a group's or a team's in this tenant names
// nobody, and is answered with no name.
func (d *Directory) membersOf(ctx context.Context, t *tenant, r *reads, name string) ([][]byte, dn, error) {
	parsed, err := parseDN(name)
	if err != nil {
		return nil, nil, nil
	}
	owner, rel, ok := d.tenantOf(parsed)
	if !ok || owner != t {
		return nil, nil, nil
	}

	switch {
	case len(rel) == 2 && rel[0].attr == attrCn && rel[1].attr == attrOu && strings.EqualFold(rel[1].value, ouGroups):
		g, err := d.findGroup(ctx, t, r, rel[0].value)
		if err != nil || g == nil {
			return nil, nil, err
		}
		ids, err := d.groupMembers(ctx, t, g.GetId())
		if err != nil {
			return nil, nil, err
		}

		return ids, d.groupDN(t, g), nil

	case len(rel) >= 2 && rel[0].attr == attrCn && rel[1].attr == attrOu && strings.EqualFold(rel[1].value, ouTeams):
		var site *rstr.Site
		switch {
		case len(rel) == 4 && rel[2].attr == attrOu && rel[3].attr == attrOu && strings.EqualFold(rel[3].value, ouSites):
			site, err = d.findSite(ctx, t, r, rel[2].value)
			if err != nil || site == nil {
				return nil, nil, err
			}
		case len(rel) != 2:
			return nil, nil, nil
		}
		team, err := d.findTeam(ctx, t, r, site, rel[0].value)
		if err != nil || team == nil {
			return nil, nil, err
		}
		ids, err := d.teamMembers(ctx, t, team.GetId())
		if err != nil {
			return nil, nil, err
		}
		as, err := d.teamDN(ctx, t, r, team)
		if err != nil {
			return nil, nil, err
		}

		return ids, as, nil

	default:
		return nil, nil, nil
	}
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}

	return ""
}
