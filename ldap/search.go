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
type cursor struct {
	Tenant string `json:"t,omitempty"`
	Stage  string `json:"s,omitempty"`
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

	// The root DSE is the one thing read before a bind: it says what this
	// server is, and nothing about anybody.
	if len(name) == 0 && req.Scope == wire.ScopeBase {
		e := d.rootDSE()
		if e.matches(req.Filter) {
			if err := w.Entry(e.project(req.Attributes, req.TypesOnly)); err != nil {
				return wire.Refuse(wire.OperationsError, err.Error())
			}
		}

		return wire.Ok
	}
	if _, ok := c.Bound(); !ok {
		return wire.Refuse(wire.InsufficientAccessRights, "bind first")
	}

	s := &search{d: d, req: req, w: w, want: wanted(req), reads: newReads()}
	cur, paged := cursorOf(req)

	// From the empty name downwards is every suffix: a client that searches
	// the whole server gets every tenant this process fronts, each under its
	// own suffix and read with its own key. One-level from the root is the
	// suffixes themselves.
	if len(name) == 0 {
		if req.Scope == wire.ScopeOne {
			for _, t := range d.tenants {
				if res := s.send(d.organisation(t)); res.Code != wire.Success {
					return res
				}
			}

			return wire.Ok
		}
		for i, t := range d.tenants {
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
			if i+1 < len(d.tenants) && s.sent > 0 {
				w.Cookie(cursor{Tenant: d.tenants[i+1].alias}.bytes())

				return wire.Ok
			}
		}

		return wire.Ok
	}

	t, rel, ok := d.tenantOf(name)
	if !ok {
		return wire.Refuse(wire.NoSuchObject, "")
	}
	next, res := s.walk(ctx, t, rel, cur)
	if res.Code == wire.Success && paged {
		w.Cookie(next.bytes())
	}

	return res
}

// search is one search in flight: what it wants, what it has read, and what
// it has sent.
type search struct {
	d     *Directory
	req   wire.SearchRequest
	w     *wire.Search
	want  wants
	reads *reads
	sent  int
}

// send evaluates the filter and the size limit and writes the entry.
func (s *search) send(e *entry) wire.Result {
	if e == nil || !e.matches(s.req.Filter) {
		return wire.Ok
	}
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
			if cur.After == "" {
				if res := s.send(d.organisation(t)); res.Code != wire.Success {
					return done, res
				}
				for _, ou := range units {
					if res := s.send(d.unit(t, ou)); res.Code != wire.Success {
						return done, res
					}
				}
			}
			next, res := s.people(ctx, t, cur.After)
			if res.Code != wire.Success {
				return done, res
			}
			if next != "" {
				return cursor{Stage: stagePeople, After: next}, wire.Ok
			}
			if paged {
				return cursor{Stage: stageGroups}, wire.Ok
			}
			fallthrough
		case stageGroups:
			if res := d.groups(ctx, t, s.reads, s.want, s.send); res.Code != wire.Success {
				return done, res
			}
			if paged {
				return cursor{Stage: stageSites}, wire.Ok
			}
			fallthrough
		case stageSites:
			if res := d.teams(ctx, t, s.reads, s.want, nil, s.send); res.Code != wire.Success {
				return done, res
			}

			return done, d.sites(ctx, t, s.reads, s.want, true, s.send)
		default:
			return done, wire.Ok
		}

	// ou=people, and a person.
	case len(rel) == 1 && isOu(0, ouPeople):
		if scope == wire.ScopeBase {
			return done, s.send(d.unit(t, ouPeople))
		}
		next, res := s.people(ctx, t, cur.After)
		if res.Code != wire.Success {
			return done, res
		}
		if next != "" {
			return cursor{Stage: stagePeople, After: next}, wire.Ok
		}

		return done, wire.Ok

	case len(rel) == 2 && rel[0].attr == attrUid && isOu(1, ouPeople):
		e, res := s.person(ctx, t, rel[0].value)
		if res.Code != wire.Success {
			return done, res
		}
		if scope == wire.ScopeOne {
			// A person has nothing under them; the base exists, which is
			// all a one-level search says about it.
			return done, wire.Ok
		}

		return done, s.send(e)

	// ou=groups, and a group.
	case len(rel) == 1 && isOu(0, ouGroups):
		if scope == wire.ScopeBase {
			return done, s.send(d.unit(t, ouGroups))
		}

		return done, d.groups(ctx, t, s.reads, s.want, s.send)

	case len(rel) == 2 && rel[0].attr == attrCn && isOu(1, ouGroups):
		g, res := d.findGroup(ctx, t, rel[0].value)
		if res.Code != wire.Success {
			return done, res
		}
		if scope == wire.ScopeOne {
			return done, wire.Ok
		}
		e, err := d.groupEntry(ctx, t, s.reads, g, s.want)
		if err != nil {
			return done, refusal(err)
		}

		return done, s.send(e)

	// ou=teams under the suffix: the teams with no site.
	case len(rel) == 1 && isOu(0, ouTeams):
		if scope == wire.ScopeBase {
			return done, s.send(d.teamsUnit(t, t.base))
		}

		return done, d.teams(ctx, t, s.reads, s.want, nil, s.send)

	case len(rel) == 2 && rel[0].attr == attrCn && isOu(1, ouTeams):
		team, res := d.findTeam(ctx, t, nil, rel[0].value)
		if res.Code != wire.Success {
			return done, res
		}
		if scope == wire.ScopeOne {
			return done, wire.Ok
		}
		e, err := d.teamEntry(ctx, t, s.reads, team, s.want)
		if err != nil {
			return done, refusal(err)
		}

		return done, s.send(e)

	// ou=sites, a site, its ou=teams, and a team.
	case len(rel) == 1 && isOu(0, ouSites):
		if scope == wire.ScopeBase {
			return done, s.send(d.unit(t, ouSites))
		}

		return done, d.sites(ctx, t, s.reads, s.want, scope == wire.ScopeSubtree, s.send)

	case len(rel) >= 2 && rel[len(rel)-2].attr == attrOu && isOu(len(rel)-1, ouSites):
		site, res := d.findSite(ctx, t, s.reads, rel[len(rel)-2].value)
		if res.Code != wire.Success {
			return done, res
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

			return done, d.teams(ctx, t, s.reads, s.want, siteRef(site), s.send)

		case len(under) == 1 && isOu(0, ouTeams):
			if scope == wire.ScopeBase {
				return done, s.send(d.teamsUnit(t, d.siteDN(t, site)))
			}

			return done, d.teams(ctx, t, s.reads, s.want, siteRef(site), s.send)

		case len(under) == 2 && under[0].attr == attrCn && isOu(1, ouTeams):
			team, res := d.findTeam(ctx, t, site, under[0].value)
			if res.Code != wire.Success {
				return done, res
			}
			if scope == wire.ScopeOne {
				return done, wire.Ok
			}
			e, err := d.teamEntry(ctx, t, s.reads, team, s.want)
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
