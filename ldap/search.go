package ldap

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lesomnus/roster/ldap/wire"
)

// cursor is where a paged search stopped: which tenant, which part of its
// tree, and roster's own cursor within that part. It travels in the client's
// cookie and nowhere else -- there is no server-side state to time out.
//
// The parts, in the order a subtree walk sends them: the fixed nodes and the
// people (`stagePeople`, roster-paged), then the groups, then the sites with
// their teams. Only the people are paged by roster; a tenant's groups and
// sites are tens, and each of those stages is one client page.
//
// Among the people, roster's cursor is in one arm of the search's plan
// ([planOf]): a filter that is an `or` is several reads, made one after
// another, and `Arm` is which of them the cursor is in.
type cursor struct {
	Tenant string `json:"t,omitempty"`
	Stage  string `json:"s,omitempty"`
	Arm    int    `json:"p,omitempty"`
	After  string `json:"a,omitempty"`
}

const (
	stagePeople = ""
	stageGroups = "g"
	stageSites  = "s"
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
	if c.Stage == stageDone || c == (cursor{}) {
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
	tenants := []*tenant{bound}

	s := &search{d: d, req: req, w: w, ask: asked(req), need: mentioned(req.Filter), plan: planOf(req.Filter), reads: newReads()}
	cur, paged := cursorOf(req)
	if paged {
		s.page = d.c.PageSize
		if req.Paging.Size > 0 && req.Paging.Size < s.page {
			s.page = req.Paging.Size
		}
	}

	// From the empty name downwards is the bound tenant's suffix, the only one
	// this connection sees. One-level from the root is that suffix itself.
	if len(name) == 0 {
		if req.Scope == wire.ScopeOne {
			for _, t := range tenants {
				if res := s.send(d.organisation(t)); res.Code != wire.Success {
					return res
				}
			}

			return wire.Ok
		}
		for i, t := range tenants {
			if paged && cur.Tenant != "" && cur.Tenant != t.alias {
				continue
			}
			next, res := s.walk(ctx, t, nil, cur)
			if res.Code != wire.Success {
				return res
			}
			if !paged {
				continue
			}
			if next.Stage != stageDone {
				next.Tenant = t.alias
				w.Cookie(next.bytes())

				return wire.Ok
			}
			// This tenant is done; the next page starts the next tenant, and
			// there is no page to send between the two.
			cur = cursor{}
			if i+1 < len(tenants) && s.sent > 0 {
				w.Cookie(cursor{Tenant: tenants[i+1].alias}.bytes())

				return wire.Ok
			}
		}

		return wire.Ok
	}

	t, rel, ok := d.tenantOf(name)
	if !ok || t != bound {
		return wire.Refuse(wire.NoSuchObject, "")
	}
	next, res := s.walk(ctx, t, rel, cur)
	if res.Code == wire.Success && paged {
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

	// plan is the reads the filter becomes over the people ([planOf]).
	plan []arm

	reads *reads
	sent  int

	// page is how many people a page of a paged search holds, and zero for a
	// search that is not paged. unpaged is how many entries went out before
	// the people and do not count towards it: the fixed nodes of the tree, on
	// the first page from a suffix.
	page, unpaged int
}

func (s *search) paged() bool { return s.page > 0 }

// holds is how many people this page holds so far, and full is whether that
// is all it may.
func (s *search) holds() int { return s.sent - s.unpaged }
func (s *search) full() bool { return s.paged() && s.holds() >= s.page }

// room is how many people the next read of roster may answer: what is left of
// the page, and never more than one read fetches.
func (s *search) room() int {
	if !s.paged() {
		return s.d.c.PageSize
	}

	return min(s.d.c.PageSize, s.page-s.holds())
}

// send asks the filter of an entry and, where it keeps it, writes it.
func (s *search) send(e *entry) wire.Result {
	if e == nil || !e.matches(s.req.Filter) {
		return wire.Ok
	}

	return s.emit(e)
}

// emit writes an entry the filter has kept, within the size limit.
func (s *search) emit(e *entry) wire.Result {
	if s.req.SizeLimit > 0 && s.w.Sent() >= s.req.SizeLimit {
		return wire.Refuse(wire.SizeLimitExceeded, "")
	}
	if err := s.w.Entry(e.project(s.req.Attributes, s.req.TypesOnly)); err != nil {
		return wire.Refuse(wire.OperationsError, err.Error())
	}
	s.sent++

	return wire.Ok
}

// walk answers a search whose base is `rel` above a tenant's suffix, from
// the cursor given, and says where it stopped. The tree is fixed --
// `docs/ldap.md` § The tree -- so this is a walk of it: which node the base
// names, and from there which nodes the scope reaches.
func (s *search) walk(ctx context.Context, t *tenant, rel dn, cur cursor) (cursor, wire.Result) {
	d, req := s.d, s.req
	done := cursor{Stage: stageDone}
	paged := req.Paging != nil
	scope := req.Scope

	// Names, so the cases below read.
	isOu := func(i int, ou string) bool {
		return i < len(rel) && rel[i].attr == attrOu && strings.EqualFold(rel[i].value, ou)
	}
	// people sends the people from where the cursor is, and says where it
	// stopped: `done` at the end of them.
	people := func() (cursor, wire.Result) {
		next, over, res := s.people(ctx, t, spot{arm: cur.Arm, after: cur.After})
		if res.Code != wire.Success || over {
			return done, res
		}

		return cursor{Stage: stagePeople, Arm: next.arm, After: next.after}, wire.Ok
	}

	switch {
	// The suffix.
	case len(rel) == 0:
		if scope == wire.ScopeBase {
			return done, s.send(d.organisation(t))
		}
		if scope == wire.ScopeOne {
			for _, ou := range units {
				if res := s.send(d.unit(t, ou)); res.Code != wire.Success {
					return done, res
				}
			}

			return done, wire.Ok
		}
		// Subtree: the fixed nodes go with the first page of people, then
		// the groups, then the sites and teams -- each stage one page.
		switch cur.Stage {
		case stagePeople:
			if cur.After == "" && cur.Arm == 0 {
				if res := s.send(d.organisation(t)); res.Code != wire.Success {
					return done, res
				}
				for _, ou := range units {
					if res := s.send(d.unit(t, ou)); res.Code != wire.Success {
						return done, res
					}
				}
				s.unpaged = s.sent
			}
			next, res := people()
			if res.Code != wire.Success || next != done {
				return next, res
			}
			if paged {
				return cursor{Stage: stageGroups}, wire.Ok
			}
			fallthrough
		case stageGroups:
			if res := s.groups(ctx, t); res.Code != wire.Success {
				return done, res
			}
			if paged {
				return cursor{Stage: stageSites}, wire.Ok
			}
			fallthrough
		case stageSites:
			if res := s.teams(ctx, t, nil); res.Code != wire.Success {
				return done, res
			}

			return done, s.sites(ctx, t, true)
		default:
			return done, wire.Ok
		}

	// ou=people, and a person.
	case len(rel) == 1 && isOu(0, ouPeople):
		if scope == wire.ScopeBase {
			return done, s.send(d.unit(t, ouPeople))
		}

		return people()

	case len(rel) == 2 && rel[0].attr == attrUid && isOu(1, ouPeople):
		v, err := d.byAlias(ctx, t, s.reads, rel[0].value)
		if err != nil {
			return done, refusal(err)
		}
		if !d.listed(t, v) {
			return done, wire.Refuse(wire.NoSuchObject, "")
		}
		if scope == wire.ScopeOne {
			// A person has nothing under them; the base exists, which is
			// all a one-level search says about it.
			return done, wire.Ok
		}

		return done, s.show(ctx, t, nil, 0, []row{{holder: v}})

	// ou=groups, and a group.
	case len(rel) == 1 && isOu(0, ouGroups):
		if scope == wire.ScopeBase {
			return done, s.send(d.unit(t, ouGroups))
		}

		return done, s.groups(ctx, t)

	case len(rel) == 2 && rel[0].attr == attrCn && isOu(1, ouGroups):
		g, err := d.findGroup(ctx, t, s.reads, rel[0].value)
		if err != nil {
			return done, refusal(err)
		}
		if g == nil {
			return done, wire.Refuse(wire.NoSuchObject, "")
		}
		if scope == wire.ScopeOne {
			return done, wire.Ok
		}
		e, err := s.groupEntry(ctx, t, g)
		if err != nil {
			return done, refusal(err)
		}

		return done, s.send(e)

	// ou=teams under the suffix: the teams with no site.
	case len(rel) == 1 && isOu(0, ouTeams):
		if scope == wire.ScopeBase {
			return done, s.send(d.teamsUnit(t, t.base))
		}

		return done, s.teams(ctx, t, nil)

	case len(rel) == 2 && rel[0].attr == attrCn && isOu(1, ouTeams):
		team, err := d.findTeam(ctx, t, s.reads, nil, rel[0].value)
		if err != nil {
			return done, refusal(err)
		}
		if team == nil {
			return done, wire.Refuse(wire.NoSuchObject, "")
		}
		if scope == wire.ScopeOne {
			return done, wire.Ok
		}
		e, err := s.teamEntry(ctx, t, team)
		if err != nil {
			return done, refusal(err)
		}

		return done, s.send(e)

	// ou=sites, a site, its ou=teams, and a team.
	case len(rel) == 1 && isOu(0, ouSites):
		if scope == wire.ScopeBase {
			return done, s.send(d.unit(t, ouSites))
		}

		return done, s.sites(ctx, t, scope == wire.ScopeSubtree)

	case len(rel) >= 2 && rel[len(rel)-2].attr == attrOu && isOu(len(rel)-1, ouSites):
		site, err := d.findSite(ctx, t, s.reads, rel[len(rel)-2].value)
		if err != nil {
			return done, refusal(err)
		}
		if site == nil {
			return done, wire.Refuse(wire.NoSuchObject, "")
		}
		under := rel[:len(rel)-2]
		switch {
		case len(under) == 0:
			if scope == wire.ScopeBase {
				return done, s.send(d.siteEntry(t, site))
			}
			if scope == wire.ScopeSubtree {
				if res := s.send(d.siteEntry(t, site)); res.Code != wire.Success {
					return done, res
				}
			}
			if res := s.send(d.teamsUnit(t, d.siteDN(t, site))); res.Code != wire.Success {
				return done, res
			}
			if scope == wire.ScopeOne {
				return done, wire.Ok
			}

			return done, s.teams(ctx, t, siteRef(site))

		case len(under) == 1 && isOu(0, ouTeams):
			if scope == wire.ScopeBase {
				return done, s.send(d.teamsUnit(t, d.siteDN(t, site)))
			}

			return done, s.teams(ctx, t, siteRef(site))

		case len(under) == 2 && under[0].attr == attrCn && isOu(1, ouTeams):
			team, err := d.findTeam(ctx, t, s.reads, site, under[0].value)
			if err != nil {
				return done, refusal(err)
			}
			if team == nil {
				return done, wire.Refuse(wire.NoSuchObject, "")
			}
			if scope == wire.ScopeOne {
				return done, wire.Ok
			}
			e, err := s.teamEntry(ctx, t, team)
			if err != nil {
				return done, refusal(err)
			}

			return done, s.send(e)
		}

		return done, wire.Refuse(wire.NoSuchObject, "")

	default:
		return done, wire.Refuse(wire.NoSuchObject, "")
	}
}
