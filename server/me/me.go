// Package me answers what a caller is, in one round trip.
//
// # Why the join is roster's and not a page's
//
// Every part of the answer is readable on its own. What is not is the
// **union**: which RPCs somebody effectively holds is bindings, plus the groups
// carrying bindings, plus a role held in a team, and an app that added those up
// from the parts would be a second implementation of `gate.Policy` -- one that
// decides what to show, drifting from the one that decides what is allowed.
//
// So this asks the same function the gate asks, and the two cannot disagree.
//
// # It reads unwalled, and that is not a hole
//
// The rows it returns are the caller's own, selected by the frame's actor. A
// read through the wall would answer the same thing for a person and would
// still need the unwalled path for the union, since working out what somebody
// may do cannot itself require permission.
//
// What keeps it honest is that there is **nothing to ask for**: `MeGetRequest`
// is empty. It cannot be pointed at somebody else, so there is no narrowing for
// the wall to do that the absence of a subject has not already done.
package me

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/credential"
	"github.com/lesomnus/roster/internal/ent/email"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/identity"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	app "github.com/lesomnus/roster/rstr"
)

// Held is the union `gate.Policy` enforces, asked here so that what a page
// shows and what the server allows come from one place.
//
// Given rather than computed, for the reason `server/core`'s rules are: `cmd`
// already reads it, and a second implementation of one question is two that
// drift.
type Held func(ctx context.Context, who pdid.Id) (methods []string, sites []pdid.Id, every bool, err error)

type Server struct {
	app.UnimplementedMeServiceServer

	db   *ent.Client
	held Held
}

func New(db *ent.Client, held Held) *Server { return &Server{db: db, held: held} }

// Get answers about the caller, and takes nothing.
func (s *Server) Get(ctx context.Context, _ *app.MeGetRequest) (*app.MeGetResponse, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	}

	v, err := s.db.Holder.Query().
		Where(holder.IDEQ(f.Actor.Uuid())).
		WithTenant().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// The credential resolved a moment ago and the row is gone, which
			// is somebody erased between the interceptor and here. Not found is
			// the truthful answer and re-authenticating will not help.
			return nil, status.Error(codes.NotFound, "there is no such person any more")
		}

		return nil, err
	}

	res := app.MeGetResponse_builder{
		Id:     f.Actor.Bytes(),
		Tenant: f.Tenant.Bytes(),
		Alias:  v.Alias,
		Name:   v.Name,
	}

	if res.Emails, err = s.emails(ctx, f.Actor); err != nil {
		return nil, err
	}
	if res.Teams, err = s.teams(ctx, f.Actor); err != nil {
		return nil, err
	}
	if res.Identities, err = s.identities(ctx, f.Actor); err != nil {
		return nil, err
	}
	if res.Credentials, err = s.credentials(ctx, f.Actor); err != nil {
		return nil, err
	}

	if s.held != nil {
		ms, sites, every, err := s.held(ctx, f.Actor)
		if err != nil {
			return nil, err
		}

		// Narrowed to what this **credential** may do, not only to what the
		// person may.
		//
		// The field is here so that *what a page shows and what it is allowed
		// to do cannot drift*, and a caller reached through a delegation or an
		// api key can call less than its holder: the row's `methods` are an
		// attenuation the gate applies on every call. Answering with the
		// person's whole union would draw buttons that are refused when
		// pressed -- the drift this exists to prevent, in the other direction.
		//
		// `frame.Whole` allows everything, so somebody signing in the ordinary
		// way sees no difference.
		res.Methods = ms[:0]
		for _, m := range ms {
			if f.Grant.Allows(m) {
				res.Methods = append(res.Methods, m)
			}
		}

		res.EverySite = every
		for _, k := range sites {
			res.Sites = append(res.Sites, k.Bytes())
		}
	}

	return res.Build(), nil
}

// emails is every address roster holds for them, verified or not.
//
// Unverified ones are included rather than filtered: "we have this and nobody
// has confirmed it" is a different fact from not having it, and a page that
// wants to prompt somebody needs the difference.
func (s *Server) emails(ctx context.Context, who pdid.Id) ([]*app.MeEmail, error) {
	vs, err := s.db.Email.Query().
		Where(email.DateErasedIsNil(), email.HasHolderWith(holder.IDEQ(who.Uuid()))).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*app.MeEmail, 0, len(vs))
	for _, v := range vs {
		e := app.MeEmail_builder{Address: v.Address}
		if v.DateVerified != nil {
			e.DateVerified = timestamppb.New(*v.DateVerified)
		}

		out = append(out, e.Build())
	}

	return out, nil
}

// teams is the teams they are in, each with the site it is in.
//
// The site travels with the team because a team's name is unique within one and
// means nothing without it: a page showing "operators" for somebody in two
// sites would be showing one word for two different groups of people.
func (s *Server) teams(ctx context.Context, who pdid.Id) ([]*app.MeTeam, error) {
	vs, err := s.db.TeamMembership.Query().
		Where(
			teammembership.DateErasedIsNil(),
			teammembership.HasHolderWith(holder.IDEQ(who.Uuid())),
		).
		WithRole().
		WithTeam(func(q *ent.TeamQuery) { q.WithSite() }).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*app.MeTeam, 0, len(vs))
	for _, v := range vs {
		t := v.Edges.Team
		if t == nil {
			continue
		}

		m := app.MeTeam_builder{
			Id:    t.ID[:],
			Alias: t.Alias,
			Name:  t.Name,
		}
		if t.Edges.Site != nil {
			m.Site = t.Edges.Site.ID[:]
			m.SiteAlias = t.Edges.Site.Alias
		}
		if v.Edges.Role != nil {
			m.Role = v.Edges.Role.Alias
		}

		out = append(out, m.Build())
	}

	return out, nil
}

// identities is how they arrive from outside, and it is here because it cannot
// be anywhere else.
//
// `IdentityService` narrows by the tenant, so a person reading their own
// through it reads their whole tenant's and filters -- the leak D17 named and
// D23 exists to remove. This one takes no subject, so there is nothing to point
// at anybody else.
func (s *Server) identities(ctx context.Context, who pdid.Id) ([]*app.MeIdentity, error) {
	vs, err := s.db.Identity.Query().
		Where(identity.DateErasedIsNil(), identity.HasHolderWith(holder.IDEQ(who.Uuid()))).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*app.MeIdentity, 0, len(vs))
	for _, v := range vs {
		out = append(out, app.MeIdentity_builder{
			Id:          v.ID[:],
			Provider:    v.Provider,
			Subject:     v.Subject,
			DateCreated: timestamppb.New(v.DateCreated),
		}.Build())
	}

	return out, nil
}

// credentials is what roster holds for them, and never what it holds.
//
// The fields are written out, so `secret` is absent rather than deselected --
// there is no `Select` here to get wrong, and nothing downstream that could ask
// for one. `CredentialService` is unregistered for the same fact and this is
// the read that replaces it for the one case that is safe: somebody's own.
func (s *Server) credentials(ctx context.Context, who pdid.Id) ([]*app.MeCredential, error) {
	vs, err := s.db.Credential.Query().
		Where(credential.DateErasedIsNil(), credential.HasHolderWith(holder.IDEQ(who.Uuid()))).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*app.MeCredential, 0, len(vs))
	for _, v := range vs {
		c := app.MeCredential_builder{Kind: v.Kind}
		if v.DateRotated != nil {
			c.DateRotated = timestamppb.New(*v.DateRotated)
		}
		if v.DateLocked != nil && v.DateLocked.After(time.Now()) {
			// Only while it is closed. A stamp left over from a lockout that
			// has expired would put "locked until" on a page for an account
			// somebody can sign into right now.
			c.DateLocked = timestamppb.New(*v.DateLocked)
		}

		out = append(out, c.Build())
	}

	return out, nil
}
