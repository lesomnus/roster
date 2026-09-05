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
type reads struct {
	groups  map[string]*rstr.Group
	teams   map[string]*rstr.Team
	sites   map[string]*rstr.Site
	holders map[string]*rstr.Holder
}

func newReads() *reads {
	return &reads{groups: map[string]*rstr.Group{}, teams: map[string]*rstr.Team{}, sites: map[string]*rstr.Site{}, holders: map[string]*rstr.Holder{}}
}

func (d *Directory) group(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Group, error) {
	if v, ok := r.groups[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Group().Get(withKey(ctx, t.key), rstr.GroupGetRequest_builder{Ref: rstr.GroupRef_builder{Id: id}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	r.groups[string(id)] = v

	return v, nil
}

func (d *Directory) team(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Team, error) {
	if v, ok := r.teams[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Team().Get(withKey(ctx, t.key), rstr.TeamGetRequest_builder{Ref: rstr.TeamRef_builder{Id: id}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	r.teams[string(id)] = v

	return v, nil
}

func (d *Directory) site(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Site, error) {
	if v, ok := r.sites[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Site().Get(withKey(ctx, t.key), rstr.SiteGetRequest_builder{Ref: rstr.SiteRef_builder{Id: id}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	r.sites[string(id)] = v

	return v, nil
}

func (d *Directory) holder(ctx context.Context, t *tenant, r *reads, id []byte) (*rstr.Holder, error) {
	if v, ok := r.holders[string(id)]; ok {
		return v, nil
	}
	v, err := d.roster.Holder().Get(withKey(ctx, t.key), rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Id: id}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	r.holders[string(id)] = v

	return v, nil
}

// Names in the tree.

func (d *Directory) groupDN(t *tenant, g *rstr.Group) dn {
	return t.base.child(attrOu, ouGroups).child(attrCn, g.GetAlias())
}

func (d *Directory) siteDN(t *tenant, s *rstr.Site) dn {
	return t.base.child(attrOu, ouSites).child(attrOu, s.GetAlias())
}

// teamDN is `cn=<team>,ou=teams,ou=<site>,ou=sites,<suffix>`, or
// `cn=<team>,ou=teams,<suffix>` for a team with no site.
func (d *Directory) teamDN(ctx context.Context, t *tenant, r *reads, team *rstr.Team) (dn, error) {
	under := t.base
	if id := team.GetSite().GetId(); len(id) > 0 {
		s, err := d.site(ctx, t, r, id)
		if err != nil {
			return nil, err
		}
		under = d.siteDN(t, s)
	}

	return under.child(attrOu, ouTeams).child(attrCn, team.GetAlias()), nil
}

func (d *Directory) personDN(t *tenant, v *rstr.Holder) dn {
	return t.base.child(attrOu, ouPeople).child(attrUid, v.GetAlias())
}

// Entries.

func (d *Directory) groupEntry(ctx context.Context, t *tenant, r *reads, g *rstr.Group, w wants) (*entry, error) {
	e := newEntry(d.groupDN(t, g))
	e.add(attrObjectClass, "top", classGroup)
	e.add(attrCn, g.GetAlias())
	e.add(attrDescription, firstOf(g.GetDesc(), g.GetName()))
	if id, err := pdid.From(g.GetId()); err == nil {
		e.add(attrEntryUUID, id.Uuid().String())
	}
	if w.member {
		ids, err := d.groupMembers(ctx, t, g.GetId())
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if err := d.addMember(ctx, t, r, e, id); err != nil {
				return nil, err
			}
		}
	}

	return e, nil
}

func (d *Directory) teamEntry(ctx context.Context, t *tenant, r *reads, team *rstr.Team, w wants) (*entry, error) {
	name, err := d.teamDN(ctx, t, r, team)
	if err != nil {
		return nil, err
	}
	e := newEntry(name)
	e.add(attrObjectClass, "top", classGroup)
	e.add(attrCn, team.GetAlias())
	e.add(attrDescription, firstOf(team.GetDesc(), team.GetName()))
	if id, err := pdid.From(team.GetId()); err == nil {
		e.add(attrEntryUUID, id.Uuid().String())
	}
	if w.member {
		ids, err := d.teamMembers(ctx, t, team.GetId())
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if err := d.addMember(ctx, t, r, e, id); err != nil {
				return nil, err
			}
		}
	}

	return e, nil
}

// groupMembers and teamMembers are who is in one, as holder identifiers.
func (d *Directory) groupMembers(ctx context.Context, t *tenant, group []byte) ([][]byte, error) {
	var out [][]byte
	var after string
	for {
		vs, err := d.roster.GroupMembership().List(withKey(ctx, t.key), rstr.GroupMembershipListRequest_builder{
			Filters: []*rstr.GroupMembershipFilter{rstr.GroupMembershipFilter_builder{Group: rstr.GroupRef_builder{Id: group}.Build()}.Build()},
			Size:    int32(d.c.PageSize), After: after,
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
		vs, err := d.roster.TeamMembership().List(withKey(ctx, t.key), rstr.TeamMembershipListRequest_builder{
			Filters: []*rstr.TeamMembershipFilter{rstr.TeamMembershipFilter_builder{Team: rstr.TeamRef_builder{Id: team}.Build()}.Build()},
			Size:    int32(d.c.PageSize), After: after,
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

// addMember names one person on a group or team -- unless they are not in
// the tree, in which case they are not in its groups either.
func (d *Directory) addMember(ctx context.Context, t *tenant, r *reads, e *entry, holder []byte) error {
	v, err := d.holder(ctx, t, r, holder)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}

		return err
	}
	if v.GetDateDisabled() != nil || v.GetDateErased() != nil {
		return nil
	}
	e.add(attrMember, d.personDN(t, v).String())

	return nil
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

// memberOf is the groups and teams a person is in, as their names. AD's
// convenience rather than the standard's, and the attribute most clients
// ask for: a `member` list on every group is the truth and this is the
// same truth read from the other end.
func (d *Directory) memberOf(ctx context.Context, t *tenant, r *reads, holder []byte) ([]string, error) {
	var out []string
	var after string
	for {
		vs, err := d.roster.GroupMembership().List(withKey(ctx, t.key), rstr.GroupMembershipListRequest_builder{
			Filters: []*rstr.GroupMembershipFilter{rstr.GroupMembershipFilter_builder{Holder: rstr.HolderRef_builder{Id: holder}.Build()}.Build()},
			Size:    int32(d.c.PageSize), After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, m := range vs.GetItems() {
			g, err := d.group(ctx, t, r, m.GetGroup().GetId())
			if err != nil {
				return nil, err
			}
			out = append(out, d.groupDN(t, g).String())
		}
		if after = vs.GetNext(); after == "" {
			break
		}
	}
	after = ""
	for {
		vs, err := d.roster.TeamMembership().List(withKey(ctx, t.key), rstr.TeamMembershipListRequest_builder{
			Filters: []*rstr.TeamMembershipFilter{rstr.TeamMembershipFilter_builder{Holder: rstr.HolderRef_builder{Id: holder}.Build()}.Build()},
			Size:    int32(d.c.PageSize), After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, m := range vs.GetItems() {
			team, err := d.team(ctx, t, r, m.GetTeam().GetId())
			if err != nil {
				return nil, err
			}
			name, err := d.teamDN(ctx, t, r, team)
			if err != nil {
				return nil, err
			}
			out = append(out, name.String())
		}
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

// Walks.

// groups sends every group under a suffix the filter keeps. Not paged: a
// tenant's groups are tens, and a page of them is a page of one read.
func (d *Directory) groups(ctx context.Context, t *tenant, r *reads, w wants, send func(*entry) wire.Result) wire.Result {
	var after string
	for {
		vs, err := d.roster.Group().List(withKey(ctx, t.key), rstr.GroupListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return refusal(err)
		}
		for _, g := range vs.GetItems() {
			e, err := d.groupEntry(ctx, t, r, g, w)
			if err != nil {
				return refusal(err)
			}
			if res := send(e); res.Code != wire.Success {
				return res
			}
		}
		if after = vs.GetNext(); after == "" {
			return wire.Ok
		}
	}
}

// findGroup is the group called `alias`, or nothing. A list rather than a
// `Get` by slug because `cn` matches without regard to case and the slug
// does not; a tenant's groups are tens.
func (d *Directory) findGroup(ctx context.Context, t *tenant, alias string) (*rstr.Group, wire.Result) {
	var after string
	for {
		vs, err := d.roster.Group().List(withKey(ctx, t.key), rstr.GroupListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return nil, refusal(err)
		}
		for _, g := range vs.GetItems() {
			if strings.EqualFold(g.GetAlias(), alias) {
				return g, wire.Ok
			}
		}
		if after = vs.GetNext(); after == "" {
			return nil, wire.Refuse(wire.NoSuchObject, "")
		}
	}
}

// sites sends every site, the `ou=teams` under each, and every team --
// those with a site under it, those without under the suffix's own
// `ou=teams`. `scope` is the search's: one-level from `ou=sites` is the
// sites alone.
func (d *Directory) sites(ctx context.Context, t *tenant, r *reads, w wants, deep bool, send func(*entry) wire.Result) wire.Result {
	var after string
	for {
		vs, err := d.roster.Site().List(withKey(ctx, t.key), rstr.SiteListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return refusal(err)
		}
		for _, s := range vs.GetItems() {
			r.sites[string(s.GetId())] = s
			if res := send(d.siteEntry(t, s)); res.Code != wire.Success {
				return res
			}
			if !deep {
				continue
			}
			if res := send(d.teamsUnit(t, d.siteDN(t, s))); res.Code != wire.Success {
				return res
			}
			if res := d.teams(ctx, t, r, w, rstr.SiteRef_builder{Id: s.GetId()}.Build(), send); res.Code != wire.Success {
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
func (d *Directory) teams(ctx context.Context, t *tenant, r *reads, w wants, site *rstr.SiteRef, send func(*entry) wire.Result) wire.Result {
	var after string
	for {
		req := rstr.TeamListRequest_builder{Size: int32(d.c.PageSize), After: after}
		if site != nil {
			req.Filters = []*rstr.TeamFilter{rstr.TeamFilter_builder{Site: site}.Build()}
		}
		vs, err := d.roster.Team().List(withKey(ctx, t.key), req.Build())
		if err != nil {
			return refusal(err)
		}
		for _, team := range vs.GetItems() {
			if site == nil && len(team.GetSite().GetId()) > 0 {
				continue
			}
			e, err := d.teamEntry(ctx, t, r, team, w)
			if err != nil {
				return refusal(err)
			}
			if res := send(e); res.Code != wire.Success {
				return res
			}
		}
		if after = vs.GetNext(); after == "" {
			return wire.Ok
		}
	}
}

// findSite is `ou=<alias>,ou=sites,<suffix>`, or nothing.
func (d *Directory) findSite(ctx context.Context, t *tenant, r *reads, alias string) (*rstr.Site, wire.Result) {
	var after string
	for {
		vs, err := d.roster.Site().List(withKey(ctx, t.key), rstr.SiteListRequest_builder{Size: int32(d.c.PageSize), After: after}.Build())
		if err != nil {
			return nil, refusal(err)
		}
		for _, s := range vs.GetItems() {
			if strings.EqualFold(s.GetAlias(), alias) {
				r.sites[string(s.GetId())] = s

				return s, wire.Ok
			}
		}
		if after = vs.GetNext(); after == "" {
			return nil, wire.Refuse(wire.NoSuchObject, "")
		}
	}
}

// findTeam is a team by alias under a site, or with no site.
func (d *Directory) findTeam(ctx context.Context, t *tenant, site *rstr.Site, alias string) (*rstr.Team, wire.Result) {
	var after string
	for {
		req := rstr.TeamListRequest_builder{Size: int32(d.c.PageSize), After: after}
		if site != nil {
			req.Filters = []*rstr.TeamFilter{rstr.TeamFilter_builder{Site: rstr.SiteRef_builder{Id: site.GetId()}.Build()}.Build()}
		}
		vs, err := d.roster.Team().List(withKey(ctx, t.key), req.Build())
		if err != nil {
			return nil, refusal(err)
		}
		for _, team := range vs.GetItems() {
			if site == nil && len(team.GetSite().GetId()) > 0 {
				continue
			}
			if strings.EqualFold(team.GetAlias(), alias) {
				return team, wire.Ok
			}
		}
		if after = vs.GetNext(); after == "" {
			return nil, wire.Refuse(wire.NoSuchObject, "")
		}
	}
}

// membersOf is the people a `(memberOf=<dn>)` filter names, by the group's
// or team's name, so the filter is one membership list and not everybody.
// A name that is not a group's or a team's in this tenant names nobody.
func (d *Directory) membersOf(ctx context.Context, t *tenant, r *reads, name string) ([][]byte, wire.Result) {
	parsed, err := parseDN(name)
	if err != nil {
		return nil, wire.Ok
	}
	owner, rel, ok := d.tenantOf(parsed)
	if !ok || owner != t {
		return nil, wire.Ok
	}

	switch {
	case len(rel) == 2 && rel[0].attr == attrCn && rel[1].attr == attrOu && strings.EqualFold(rel[1].value, ouGroups):
		g, res := d.findGroup(ctx, t, rel[0].value)
		if res.Code != wire.Success {
			return nil, okIfMissing(res)
		}
		ids, err := d.groupMembers(ctx, t, g.GetId())
		if err != nil {
			return nil, refusal(err)
		}

		return ids, wire.Ok

	case len(rel) >= 2 && rel[0].attr == attrCn && rel[1].attr == attrOu && strings.EqualFold(rel[1].value, ouTeams):
		var site *rstr.Site
		switch {
		case len(rel) == 4 && rel[2].attr == attrOu && rel[3].attr == attrOu && strings.EqualFold(rel[3].value, ouSites):
			var res wire.Result
			site, res = d.findSite(ctx, t, r, rel[2].value)
			if res.Code != wire.Success {
				return nil, okIfMissing(res)
			}
		case len(rel) != 2:
			return nil, wire.Ok
		}
		team, res := d.findTeam(ctx, t, site, rel[0].value)
		if res.Code != wire.Success {
			return nil, okIfMissing(res)
		}
		ids, err := d.teamMembers(ctx, t, team.GetId())
		if err != nil {
			return nil, refusal(err)
		}

		return ids, wire.Ok

	default:
		return nil, wire.Ok
	}
}

// okIfMissing turns "no such object" into an empty answer: a filter naming
// nothing matches nothing, which is not an error.
func okIfMissing(res wire.Result) wire.Result {
	if res.Code == wire.NoSuchObject {
		return wire.Ok
	}

	return res
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}

	return ""
}
