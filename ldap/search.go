package ldap

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/lesomnus/roster/ldap/wire"
	rstr "github.com/lesomnus/roster/rstr"
)

// cursor is where a paged search stopped: which tenant, which part of its
// tree, where in that part, and how many entries went before. It travels in
// the client's cookie and nowhere else -- there is no server-side state to
// time out, so another connection and another replica read on from one.
//
// # What it holds, which a client can read
//
// It is JSON in the clear, unsigned, and `docs/ldap.md` § Search says what is
// in it. Where in a part is roster's own cursor for every part roster lists,
// which is the moment the last row read was made and that row's identifier,
// whether or not the row was shown: somebody suspended who is in no entry can
// be the row a page ended at. It lets nobody read what they could not: every
// read it resumes is made with the key of the tenant the connection is bound
// in, and the wall is in the key.
//
// A cookie this package did not write is read as whatever it says. One whose
// place is no cursor is refused by roster, and one that names a part this
// search does not have has nothing after it.
type cursor struct {
	Tenant string `json:"t,omitempty"`
	// Stage is which part of the walk, by the name of its step ([step]).
	Stage string `json:"s,omitempty"`
	// Arm is which read of the search's plan the cursor is in, among the
	// people: a filter that is an `or` is several reads, made one after
	// another ([planOf]).
	Arm int `json:"p,omitempty"`
	// After is the place in the part: roster's cursor, or for the fixed nodes
	// of the tree how many of them went before.
	After string `json:"a,omitempty"`
	// Sent is how many entries the pages before this one held, which is what
	// a size limit is counted from ([search.emit]).
	Sent int `json:"n,omitempty"`
}

// The parts of a walk, in the order a subtree search from a suffix sends
// them. Each is a step of its own ([step]); a search from lower in the tree
// is some of them.
const (
	stageNodes  = "n" // the nodes of the tree that are there whatever a tenant holds
	stagePeople = "p"
	stageGroups = "g"
	stageSites  = "s"
	stageUnits  = "u" // the `ou=teams` under each site
	stageTeams  = "t"
	stageDone   = "."
)

func cursorOf(req wire.SearchRequest) (cursor, bool) {
	if req.Paging == nil {
		return cursor{}, false
	}
	var c cursor
	if len(req.Paging.Cookie) > 0 {
		// A cookie this package did not write is a protocol error to RFC
		// 2696; answering from the start is kinder and never wrong.
		_ = json.Unmarshal(req.Paging.Cookie, &c)
	}

	return c, true
}

func (c cursor) bytes() []byte {
	if c.Stage == stageDone || c.Stage == "" {
		return nil
	}
	b, _ := json.Marshal(c)

	return b
}

// Search is the wire's.
func (d *Directory) Search(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
	name, err := parseDN(req.BaseDN)
	if err != nil {
		return wire.Refuse(wire.ProtocolError, "base: "+err.Error())
	}

	// Whose directory this connection is reading: the tenant of the DN it
	// bound as, and nobody's before a bind.
	//
	// # A bind is to one tenant, and so is everything after it
	//
	// The search used to ask only *whether* the connection was bound. One
	// process fronts every tenant that nominated its key, so a person bound in
	// contoso searched `o=fabrikam` and read fabrikam's people, and a search
	// from the root walked every tenant; the root entry named every tenant to
	// anybody, bound or not. With per-tenant keys that was a deployment's
	// choice of keys; with one deployment key it is the default. So the bound
	// tenant is the whole tree a connection sees: its suffix is the only naming
	// context, a search under any other is `noSuchObject` -- the answer for a
	// name that is not there, since saying it is somebody else's would say it
	// exists -- and the root walks it alone.
	bound, isBound, stands := d.boundTenant(ctx, c)
	if stands.Code != wire.Success {
		return stands
	}

	// The root DSE is the one thing read before a bind: it says what this
	// server is, and nothing about anybody -- and since a suffix is a tenant's
	// name, it names one only to somebody bound in it.
	if len(name) == 0 && req.Scope == wire.ScopeBase {
		e := d.rootDSE(c, bound)
		if e.matches(req.Filter) {
			if err := w.Entry(e.project(req.Attributes, req.TypesOnly)); err != nil {
				return wire.Refuse(wire.OperationsError, err.Error())
			}
		}

		return wire.Ok
	}
	if !isBound {
		return wire.Refuse(wire.InsufficientAccessRights, "bind first")
	}

	s := &search{d: d, req: req, w: w, ask: asked(req), need: mentioned(req.Filter), plan: planOf(req.Filter), reads: newReads()}
	cur, paged := cursorOf(req)
	if paged {
		// # What a paging control asks, RFC 2696 § 3
		//
		// A page of nothing with the cookie of the page before is the client
		// saying it wants no more: "a sequence of paged search requests is
		// abandoned by the client sending a search request containing a
		// pagedResultsControl with the size set to zero (0) and the cookie set
		// to the last cookie returned by the server". It was read as no size
		// at all, and answered everybody the client had not read (#281).
		if req.Paging.Size == 0 && len(req.Paging.Cookie) > 0 {
			return wire.Ok
		}
		s.page = d.c.PageSize
		if req.Paging.Size > 0 && req.Paging.Size < s.page {
			s.page = req.Paging.Size
		}
		// And a size limit is of the whole search, in however many pages:
		// "the page size specified MAY be greater than zero and less than
		// the sizeLimit value", and where it is not less "the server should
		// ignore the control as the request can be satisfied in a single
		// page". The limit was counted from nothing for each page's request,
		// so a limit of 7 in pages of 5 answered everybody with no error.
		// How many went before is in the cookie, since nothing else is kept.
		if limit := req.SizeLimit; limit > 0 && s.page >= limit && len(req.Paging.Cookie) == 0 {
			s.page, paged, cur = 0, false, cursor{}
		}
		s.before = max(0, cur.Sent)
	}

	var steps []step
	switch {
	// From the empty name downwards is the bound tenant's suffix, the only
	// one this connection sees. One-level from the root is that suffix
	// itself.
	case len(name) == 0 && req.Scope == wire.ScopeOne:
		steps = []step{s.nodes(d.organisation(bound))}
	case len(name) == 0:
		// A cookie that names another tenant was handed out in it, and has
		// nothing after it here.
		if cur.Tenant != "" && cur.Tenant != bound.alias {
			return wire.Ok
		}
		steps = s.suffix(ctx, bound)
	default:
		t, rel, ok := d.tenantOf(name)
		if !ok || t != bound {
			return wire.Refuse(wire.NoSuchObject, "")
		}
		var res wire.Result
		if steps, res = s.under(ctx, t, rel); res.Code != wire.Success {
			return res
		}
	}

	next, res := s.walk(steps, cur)
	if res.Code == wire.Success && paged && next.Stage != stageDone {
		if len(name) == 0 {
			next.Tenant = bound.alias
		}
		next.Sent = s.before + s.sent
		w.Cookie(next.bytes())
	}

	return res
}

// search is one search in flight: what it has a use for, how it reads the
// people, what it has read, and what it has sent.
type search struct {
	d   *Directory
	req wire.SearchRequest
	w   *wire.Search

	// ask is the attributes that cost a read which the client asked to be
	// sent, and need those its filter names. They are read at different
	// times and for different people ([search.show]).
	ask, need wants

	// plan is the reads the filter becomes over the people ([planOf]), and
	// places the groups and teams it names, each found once
	// ([search.place]).
	plan   []arm
	places map[string]*place

	reads *reads

	// sent is how many entries this request has sent, and before how many
	// the pages before it did.
	sent, before int

	// page is how many entries a page of a paged search holds, and zero for
	// a search that is not paged.
	page int
}

func (s *search) paged() bool { return s.page > 0 }

// full is whether the page holds all it may.
func (s *search) full() bool { return s.paged() && s.sent >= s.page }

// room is how many rows the next read of roster may answer: what is left of
// the page, and never more than one read fetches.
func (s *search) room() int {
	if !s.paged() {
		return s.d.c.PageSize
	}

	return min(s.d.c.PageSize, s.page-s.sent)
}

// roomy is whether a part of the walk may begin on this page: the page is
// empty, or at least half of it is left.
//
// A part is read at no more than the page has room for, so that the page is
// never bigger than was asked. Begun on a page with room for three, it would
// be read three rows a call for as long as the filter kept none of them. So a
// page that is mostly full ends where a part does, which RFC 2696 allows a
// page to do, and the part begins on a page of its own.
func (s *search) roomy() bool {
	return !s.paged() || s.sent == 0 || 2*(s.page-s.sent) >= s.page
}

// send asks the filter of an entry and, where it keeps it, writes it.
func (s *search) send(e *entry) wire.Result {
	if e == nil || !e.matches(s.req.Filter) {
		return wire.Ok
	}

	return s.emit(e)
}

// emit writes an entry the filter has kept, within the size limit: the
// client's, counted over every page of the search ([Directory.Search]).
func (s *search) emit(e *entry) wire.Result {
	if limit := s.req.SizeLimit; limit > 0 && s.before+s.sent >= limit {
		return wire.Refuse(wire.SizeLimitExceeded, "")
	}
	if err := s.w.Entry(e.project(s.req.Attributes, s.req.TypesOnly)); err != nil {
		return wire.Refuse(wire.OperationsError, err.Error())
	}
	s.sent++

	return wire.Ok
}

// step is one part of a walk: a run of entries whose order is the same from
// one page of a search to the next, so that a place in it can be carried in a
// cookie. `run` sends the part from `from` and answers where it stopped and
// whether that was its end.
type step struct {
	stage string
	run   func(from spot) (spot, bool, wire.Result)
}

// walk sends the steps of a search from where the cursor is, and says where
// it stopped: `stageDone` at the end of the last.
//
// # A page is what was asked, and only the last can be empty
//
// A page could be bigger than the client asked. The first from a suffix
// carried the fixed nodes beside a page of people; the groups were one page
// whatever their number, and so were the sites with their teams; and the
// people of a `(memberOf=…)` filter were one page, so `docs/ldap.md`'s "no
// page is bigger" was untrue of a group of more than a hundred (#281). And a
// page could be empty with more to come: each of those parts ended the page
// it was on, so `(uid=kim)` from a suffix was pages of 1, 0 and 0.
//
// Every part is a step, read at no more than the page has room for
// ([search.room]), and a page ends in two places only: where it is full, and
// where a read of roster ended that put something in it ([search.through]).
// So no page is bigger than was asked, a page that has nothing in it yet
// reads on, and only the last can be empty -- which it is where the page
// before ended full and nothing after it matched.
func (s *search) walk(steps []step, cur cursor) (cursor, wire.Result) {
	done := cursor{Stage: stageDone}

	i := 0
	if cur.Stage != "" {
		for i < len(steps) && steps[i].stage != cur.Stage {
			i++
		}
	}
	from := spot{arm: cur.Arm, after: cur.After}
	for ; i < len(steps); i++ {
		next, over, res := steps[i].run(from)
		if res.Code != wire.Success {
			return done, res
		}
		if !over {
			return cursor{Stage: steps[i].stage, Arm: next.arm, After: next.after}, wire.Ok
		}
		from = spot{}
		if i+1 < len(steps) && (s.full() || !s.roomy()) {
			return cursor{Stage: steps[i+1].stage}, wire.Ok
		}
	}

	return done, wire.Ok
}

// nodes is the step of entries that are there whatever a tenant holds: the
// suffix, its units, a site that was just found. The place in it is how many
// went before.
func (s *search) nodes(entries ...*entry) step {
	return step{stage: stageNodes, run: func(from spot) (spot, bool, wire.Result) {
		i := 0
		if from.after != "" {
			var err error
			if i, err = strconv.Atoi(from.after); err != nil || i < 0 {
				return spot{}, false, wire.Refuse(wire.ProtocolError, "paging: a cookie that is not one of this directory's")
			}
		}
		for ; i < len(entries); i++ {
			if s.full() {
				return spot{after: strconv.Itoa(i)}, false, wire.Ok
			}
			if res := s.send(entries[i]); res.Code != wire.Success {
				return spot{}, false, res
			}
		}

		return spot{}, true, wire.Ok
	}}
}

// through reads one of roster's lists from `after`, a page of it at a time,
// until the list ends or the client's page does. `read` reads at most `size`
// rows, sends what the filter keeps of them and answers roster's cursor after
// them, empty at the end of the list. It answers where it stopped and whether
// that was the end.
//
// A page of roster is a page of the client: a read asks for no more rows
// than the page has room for, and a page ends where a roster page ends, so
// that a cookie is always a roster cursor and no row is skipped or repeated.
// Fewer entries than the page size means the filter dropped some, which RFC
// 2696 allows; a read that put nothing on the page is followed by another, so
// a selective filter over a big tenant does not answer with a run of empty
// pages.
func (s *search) through(after string, read func(after string, size int) (string, wire.Result)) (string, bool, wire.Result) {
	for {
		if s.full() {
			return after, false, wire.Ok
		}
		before := s.sent
		next, res := read(after, s.room())
		if res.Code != wire.Success {
			return "", false, res
		}
		if next == "" {
			return "", true, wire.Ok
		}
		after = next
		if s.paged() && s.sent > before {
			return after, false, wire.Ok
		}
	}
}

// listing is a step that is one of roster's lists.
func (s *search) listing(stage string, read func(after string, size int) (string, wire.Result)) step {
	return step{stage: stage, run: func(from spot) (spot, bool, wire.Result) {
		after, over, res := s.through(from.after, read)

		return spot{after: after}, over, res
	}}
}

// children is the entries directly under a tenant's suffix: its units, and
// the `ou=teams` that holds the teams with no site.
//
// `ou=teams` answered by name and was in no listing, though the teams under
// it were (#281): a client that walks the tree a level at a time never
// reached them, and one that read the subtree was handed
// `cn=nomads,ou=teams,o=contoso` with nothing above it.
func (d *Directory) children(t *tenant) []*entry {
	var out []*entry
	for _, ou := range units {
		out = append(out, d.unit(t, ou))
	}

	return append(out, d.teamsUnit(t, t.base))
}

// suffix is the steps of a subtree search from a tenant's suffix, which is
// the whole of its tree: `docs/ldap.md` § The tree.
func (s *search) suffix(ctx context.Context, t *tenant) []step {
	d := s.d

	return []step{
		s.nodes(append([]*entry{d.organisation(t)}, d.children(t)...)...),
		s.people(ctx, t),
		s.groups(ctx, t),
		s.sites(ctx, t, stageSites, func(site *rstr.Site) *entry { return d.siteEntry(t, site) }),
		s.sites(ctx, t, stageUnits, func(site *rstr.Site) *entry { return d.teamsUnit(t, d.siteDN(t, site)) }),
		s.teams(ctx, t, nil, func(*rstr.Team) bool { return true }),
	}
}

// under is the steps of a search whose base is `rel` above a tenant's suffix.
// The tree is fixed, so this is which node the base names and from there
// which nodes the scope reaches; a base that names nothing is
// `noSuchObject`.
//
// A subtree is its base and everything under it, RFC 4511 § 4.5.1.2. A
// subtree search of a unit -- `ou=people`, `ou=groups`, either `ou=teams`,
// `ou=sites` -- answered what is under it and left the unit out (#281).
func (s *search) under(ctx context.Context, t *tenant, rel dn) ([]step, wire.Result) {
	d, scope := s.d, s.req.Scope
	missing := wire.Refuse(wire.NoSuchObject, "")
	// nothing is what a one-level search of an entry with nothing under it
	// answers: the base exists, which is all it says.
	var nothing []step

	// Names, so the cases below read.
	isOu := func(i int, ou string) bool {
		return i < len(rel) && rel[i].attr == attrOu && strings.EqualFold(rel[i].value, ou)
	}
	// within is a unit and the one list under it, as the scope has them: the
	// unit, what is under it, or both.
	within := func(unit *entry, below step) []step {
		switch scope {
		case wire.ScopeBase:
			return []step{s.nodes(unit)}
		case wire.ScopeOne:
			return []step{below}
		default:
			return []step{s.nodes(unit), below}
		}
	}
	sited := func(team *rstr.Team) bool { return len(team.GetSite().GetId()) > 0 }
	unsited := func(team *rstr.Team) bool { return !sited(team) }
	everyTeam := func(*rstr.Team) bool { return true }

	switch {
	// The suffix.
	case len(rel) == 0:
		switch scope {
		case wire.ScopeBase:
			return []step{s.nodes(d.organisation(t))}, wire.Ok
		case wire.ScopeOne:
			return []step{s.nodes(d.children(t)...)}, wire.Ok
		default:
			return s.suffix(ctx, t), wire.Ok
		}

	// ou=people, and a person.
	case len(rel) == 1 && isOu(0, ouPeople):
		return within(d.unit(t, ouPeople), s.people(ctx, t)), wire.Ok

	case len(rel) == 2 && rel[0].attr == attrUid && isOu(1, ouPeople):
		v, err := d.byAlias(ctx, t, s.reads, rel[0].value)
		if err != nil {
			return nil, refusal(err)
		}
		if !d.listed(t, v) {
			return nil, missing
		}
		if scope == wire.ScopeOne {
			return nothing, wire.Ok
		}

		return []step{{stage: stagePeople, run: func(spot) (spot, bool, wire.Result) {
			return spot{}, true, s.show(ctx, t, nil, 0, []row{{holder: v}})
		}}}, wire.Ok

	// ou=groups, and a group.
	case len(rel) == 1 && isOu(0, ouGroups):
		return within(d.unit(t, ouGroups), s.groups(ctx, t)), wire.Ok

	case len(rel) == 2 && rel[0].attr == attrCn && isOu(1, ouGroups):
		g, err := d.findGroup(ctx, t, s.reads, rel[0].value)
		if err != nil {
			return nil, refusal(err)
		}
		if g == nil {
			return nil, missing
		}
		if scope == wire.ScopeOne {
			return nothing, wire.Ok
		}
		e, err := s.groupEntry(ctx, t, g)
		if err != nil {
			return nil, refusal(err)
		}

		return []step{s.nodes(e)}, wire.Ok

	// ou=teams under the suffix: the teams with no site.
	case len(rel) == 1 && isOu(0, ouTeams):
		return within(d.teamsUnit(t, t.base), s.teams(ctx, t, nil, unsited)), wire.Ok

	case len(rel) == 2 && rel[0].attr == attrCn && isOu(1, ouTeams):
		team, err := d.findTeam(ctx, t, s.reads, nil, rel[0].value)
		if err != nil {
			return nil, refusal(err)
		}
		if team == nil {
			return nil, missing
		}
		if scope == wire.ScopeOne {
			return nothing, wire.Ok
		}
		e, err := s.teamEntry(ctx, t, team)
		if err != nil {
			return nil, refusal(err)
		}

		return []step{s.nodes(e)}, wire.Ok

	// ou=sites, a site, its ou=teams, and a team.
	case len(rel) == 1 && isOu(0, ouSites):
		sites := s.sites(ctx, t, stageSites, func(site *rstr.Site) *entry { return d.siteEntry(t, site) })
		switch scope {
		case wire.ScopeBase:
			return []step{s.nodes(d.unit(t, ouSites))}, wire.Ok
		case wire.ScopeOne:
			return []step{sites}, wire.Ok
		default:
			return []step{
				s.nodes(d.unit(t, ouSites)),
				sites,
				s.sites(ctx, t, stageUnits, func(site *rstr.Site) *entry { return d.teamsUnit(t, d.siteDN(t, site)) }),
				s.teams(ctx, t, nil, sited),
			}, wire.Ok
		}

	case len(rel) >= 2 && rel[len(rel)-2].attr == attrOu && isOu(len(rel)-1, ouSites):
		site, err := d.findSite(ctx, t, s.reads, rel[len(rel)-2].value)
		if err != nil {
			return nil, refusal(err)
		}
		if site == nil {
			return nil, missing
		}
		under := rel[:len(rel)-2]
		unit := d.teamsUnit(t, d.siteDN(t, site))
		switch {
		case len(under) == 0:
			switch scope {
			case wire.ScopeBase:
				return []step{s.nodes(d.siteEntry(t, site))}, wire.Ok
			case wire.ScopeOne:
				return []step{s.nodes(unit)}, wire.Ok
			default:
				return []step{s.nodes(d.siteEntry(t, site), unit), s.teams(ctx, t, site, everyTeam)}, wire.Ok
			}

		case len(under) == 1 && under[0].attr == attrOu && strings.EqualFold(under[0].value, ouTeams):
			return within(unit, s.teams(ctx, t, site, everyTeam)), wire.Ok

		case len(under) == 2 && under[0].attr == attrCn && under[1].attr == attrOu && strings.EqualFold(under[1].value, ouTeams):
			team, err := d.findTeam(ctx, t, s.reads, site, under[0].value)
			if err != nil {
				return nil, refusal(err)
			}
			if team == nil {
				return nil, missing
			}
			if scope == wire.ScopeOne {
				return nothing, wire.Ok
			}
			e, err := s.teamEntry(ctx, t, team)
			if err != nil {
				return nil, refusal(err)
			}

			return []step{s.nodes(e)}, wire.Ok
		}

		return nil, missing

	default:
		return nil, missing
	}
}
