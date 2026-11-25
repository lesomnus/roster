package cmd

import (
	"context"
	"uuid"

	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/payday/frame"

	"github.com/lesomnus/roster/internal/ent/apikey"
	"github.com/lesomnus/roster/internal/ent/badge"
	"github.com/lesomnus/roster/internal/ent/consent"
	"github.com/lesomnus/roster/internal/ent/continuation"
	"github.com/lesomnus/roster/internal/ent/credential"
	"github.com/lesomnus/roster/internal/ent/delegation"
	"github.com/lesomnus/roster/internal/ent/email"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/groupexposure"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/identity"
	"github.com/lesomnus/roster/internal/ent/link"
	"github.com/lesomnus/roster/internal/ent/phone"
	"github.com/lesomnus/roster/internal/ent/predicate"
	"github.com/lesomnus/roster/internal/ent/relation"
	"github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/internal/ent/sitemembership"
	entteam "github.com/lesomnus/roster/internal/ent/team"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	"github.com/lesomnus/roster/server/bare"
)

// People is the second axis for the rows that carry no site: a person, and
// what is theirs.
//
// # What it is for
//
// `pd.Grouped` narrows a row to the site it names. A person names none --
// somebody works at one factory and audits another (`membership.proto`) -- and
// neither does their address, their key, their password. So a role bound in
// one site and naming `HolderService/*` listed every person of the tenant,
// read every key's facts, and suspended its administrator: the rows were "in
// no set", and what is in no set was narrowed by nothing (#274).
//
// A person is in a site by a [SiteMembership], which is many to many and was
// read by nothing. It is read here: **a grant made in a site reaches the
// people who are in that site**, and what is theirs with them. Somebody bound
// a helpdesk's role in Seoul resets the password of somebody in Seoul, and is
// answered `NotFound` for somebody who is not.
//
// # Themselves, always
//
// A caller reads and writes what is their own whatever site they are in, or
// none: the rule is about reaching somebody else. Without it nobody bound in a
// site alone could read their own keys.
//
// # And a place in a team or a group, by where that is
//
// A `TeamMembership` names no site either, and is of a team that does. It was
// read across sites -- who is in which team of another site, holding which
// role -- and is narrowed by its team's site, as a `GroupMembership` and a
// `GroupExposure` are by their group's.
//
// # Who is in a site is not a site's to say
//
// If somebody bound in Seoul could put anybody in Seoul, everybody would be
// within their reach by one write. So a `SiteMembership` is written across the
// tenant (`server/core`, `acrossTheTenant`), and so is a person made or
// erased.
//
// `of` is [Sets], the same answer `pd.Grouped` is given: where the caller
// holds the method the call is.
func People(of frame.Sets) bare.Scope { return people{of: of} }

type people struct {
	bare.Unscoped

	of frame.Sets
}

var _ bare.Scope = people{}

// reached is the sites to narrow by and who is asking; `all` is nothing to
// narrow by.
func (x people) reached(ctx context.Context) (sites []uuid.UUID, self uuid.UUID, all bool, err error) {
	if _, ok := frame.AccountOf(ctx); ok {
		// In no tenant, and the wall's to answer.
		return nil, uuid.Nil(), true, nil
	}

	sites, all, err = frame.NarrowSet(ctx, x.of)
	if all || err != nil {
		return nil, uuid.Nil(), all, err
	}

	f, _ := frame.From(ctx)

	return sites, f.Actor.Uuid(), false, nil
}

// inSites is somebody with a place in one of these sites, or the caller.
//
// A subquery on the membership's own columns and not an edge predicate: a
// `Holder` has no edge to its memberships, on purpose (`team.proto`).
func inSites(sites []uuid.UUID, self uuid.UUID) predicate.Holder {
	return func(s *sql.Selector) {
		own := sql.EQ(s.C(holder.FieldId), self)
		if len(sites) == 0 {
			s.Where(own)

			return
		}

		vs := make([]any, len(sites))
		for i, v := range sites {
			vs[i] = v
		}

		t := sql.Table(sitemembership.Table)
		s.Where(sql.Or(own, sql.In(s.C(holder.FieldId),
			sql.Select(t.C(sitemembership.FieldHolderId)).From(t).Where(sql.And(
				sql.In(t.C(sitemembership.FieldSiteId), vs...),
				sql.IsNull(t.C(sitemembership.FieldDateErased)),
			)),
		)))
	}
}

func (x people) HolderScope(ctx context.Context) (predicate.Holder, error) {
	sites, self, all, err := x.reached(ctx)
	if all || err != nil {
		return nil, err
	}

	return inSites(sites, self), nil
}

func (x people) ApiKeyScope(ctx context.Context) (predicate.ApiKey, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return apikey.HasHolderWith(p), nil
}

func (x people) BadgeScope(ctx context.Context) (predicate.Badge, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return badge.HasHolderWith(p), nil
}

func (x people) ConsentScope(ctx context.Context) (predicate.Consent, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return consent.HasHolderWith(p), nil
}

func (x people) ContinuationScope(ctx context.Context) (predicate.Continuation, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return continuation.HasHolderWith(p), nil
}

func (x people) CredentialScope(ctx context.Context) (predicate.Credential, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return credential.HasHolderWith(p), nil
}

func (x people) DelegationScope(ctx context.Context) (predicate.Delegation, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return delegation.HasHolderWith(p), nil
}

func (x people) EmailScope(ctx context.Context) (predicate.Email, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return email.HasHolderWith(p), nil
}

func (x people) IdentityScope(ctx context.Context) (predicate.Identity, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return identity.HasHolderWith(p), nil
}

func (x people) LinkScope(ctx context.Context) (predicate.Link, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return link.HasHolderWith(p), nil
}

func (x people) PhoneScope(ctx context.Context) (predicate.Phone, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return phone.HasHolderWith(p), nil
}

func (x people) RelationScope(ctx context.Context) (predicate.Relation, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return relation.HasHolderWith(p), nil
}

func (x people) SessionScope(ctx context.Context) (predicate.Session, error) {
	p, err := x.HolderScope(ctx)
	if p == nil || err != nil {
		return nil, err
	}

	return session.HasHolderWith(p), nil
}

// TeamMembershipScope: of a team in one of the caller's sites, or the caller's
// own place.
func (x people) TeamMembershipScope(ctx context.Context) (predicate.TeamMembership, error) {
	sites, self, all, err := x.reached(ctx)
	if all || err != nil {
		return nil, err
	}

	own := teammembership.HasHolderWith(holder.IdEQ(self))
	if len(sites) == 0 {
		return own, nil
	}

	return teammembership.Or(own, teammembership.HasTeamWith(entteam.SiteIdIn(sites...))), nil
}

// GroupMembershipScope: of a group in one of the caller's sites, or the
// caller's own place.
func (x people) GroupMembershipScope(ctx context.Context) (predicate.GroupMembership, error) {
	sites, self, all, err := x.reached(ctx)
	if all || err != nil {
		return nil, err
	}

	own := groupmembership.HasHolderWith(holder.IdEQ(self))
	if len(sites) == 0 {
		return own, nil
	}

	return groupmembership.Or(own, groupmembership.HasGroupWith(group.SiteIdIn(sites...))), nil
}

// GroupExposureScope: of a group in one of the caller's sites.
func (x people) GroupExposureScope(ctx context.Context) (predicate.GroupExposure, error) {
	sites, _, all, err := x.reached(ctx)
	if all || err != nil {
		return nil, err
	}

	return groupexposure.HasGroupWith(group.SiteIdIn(sites...)), nil
}
