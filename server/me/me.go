// Package me answers what a caller is, in one round trip.
//
// # Why the join is roster's and not a page's
//
// Every part of the answer is readable on its own. What is not is the
// **union**: which Rpcs somebody effectively holds is bindings, plus the groups
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

	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/apikey"
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

	// walled is the stack the two writes go through: `Unlink` and
	// `SignOutEverywhere`, the waived ones. What a person does beyond those is
	// the entity's verb with their own reference, and not this service's.
	//
	//
	// The reads here go to ent because the missing subject has already narrowed
	// them and there is nothing left for a wall to do. A write is different: the
	// rules that make one safe -- refusing the removal of somebody's only way in
	// -- live in a layer, and going around them would be a button that locks
	// somebody out of their own account.
	//
	// Nil is a server that answers and does not write, which is what `cmd`
	// hands the admin port and what a caller gets `Unimplemented` from.
	walled app.Server
}

func New(db *ent.Client, held Held, opts ...Option) *Server {
	s := &Server{db: db, held: held}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Option is how a deployment says what this service may do.
type Option func(*Server)

// WithWrites gives the service the stack its two writes go through.
func WithWrites(v app.Server) Option { return func(s *Server) { s.walled = v } }

// Get answers about the caller, and takes nothing.
func (s *Server) Get(ctx context.Context, _ *app.MeGetRequest) (*app.MeGetResponse, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	}

	v, err := s.db.Holder.Query().
		Where(holder.IdEQ(f.Actor.Uuid())).
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
	if res.Keys, err = s.keys(ctx, f.Actor); err != nil {
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
		Where(email.DateErasedIsNil(), email.HasHolderWith(holder.IdEQ(who.Uuid()))).
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
			teammembership.HasHolderWith(holder.IdEQ(who.Uuid())),
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
			Id:    t.Id[:],
			Alias: t.Alias,
			Name:  t.Name,
		}
		if t.Edges.Site != nil {
			m.Site = t.Edges.Site.Id[:]
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
func (s *Server) identities(ctx context.Context, who pdid.Id) ([]*app.SignInIdentity, error) {
	vs, err := s.db.Identity.Query().
		Where(identity.DateErasedIsNil(), identity.HasHolderWith(holder.IdEQ(who.Uuid()))).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*app.SignInIdentity, 0, len(vs))
	for _, v := range vs {
		out = append(out, app.SignInIdentity_builder{
			Id:          v.Id[:],
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
func (s *Server) credentials(ctx context.Context, who pdid.Id) ([]*app.SignInCredential, error) {
	vs, err := s.db.Credential.Query().
		Where(credential.DateErasedIsNil(), credential.HasHolderWith(holder.IdEQ(who.Uuid()))).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*app.SignInCredential, 0, len(vs))
	for _, v := range vs {
		c := app.SignInCredential_builder{Kind: v.Kind, Name: v.Name}
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

// keys is what acts as them, and never what verifies one.
//
// Written out like `credentials` above and for the identical reason: `secret`
// is absent rather than deselected, and there is no `Select` here that could
// ask for it. `ApiKeyService` is unregistered everywhere for that fact, and
// this is the read that replaces it for the one case that is safe.
//
// The operator's version of this answer is `HolderService.SignsIn`, which is
// the same message filled in the same order -- two shapes saying one thing is
// two that drift, and the drift would be between what a person sees about
// themselves and what an operator sees about them.
func (s *Server) keys(ctx context.Context, who pdid.Id) ([]*app.SignInKey, error) {
	vs, err := s.db.ApiKey.Query().
		Where(apikey.DateErasedIsNil(), apikey.HasHolderWith(holder.IdEQ(who.Uuid()))).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*app.SignInKey, 0, len(vs))
	for _, v := range vs {
		k := app.SignInKey_builder{
			Id:      v.Id[:],
			Alias:   v.Alias,
			Methods: v.Methods,
		}
		if v.DateExpires != nil {
			k.DateExpires = timestamppb.New(*v.DateExpires)
		}
		if v.DateUsed != nil {
			k.DateUsed = timestamppb.New(*v.DateUsed)
		}

		out = append(out, k.Build())
	}

	return out, nil
}

// IssueKey mints an `rt_` that acts as the caller.
//
// The self-service half of `IssueService.IssueKey`. That one takes a
// `HolderRef` and is an operator's; this takes no subject at all, which is what
// makes the smallest role covering it *may mint a key that acts as you* rather
// than *may mint one for anybody in this tenant*.
//
// # The rule that makes the button safe is not here
//
// It is in `server/core`, and this reaches it by writing through the walled
// stack: `ApiKey.Add` refuses a list of methods the caller does not hold, so a
// person cannot mint themselves something wider than they are. Reaching for the
// database would be a self-service page that hands out permissions.
//
// # And the prefix is not in the request
//

// RevokeKey ends one of the caller's own keys.
//
// The same shape as [Server.Unlink]: the read that finds it is narrowed by the
// caller **before** it is narrowed by the identifier, so one that belongs to
// somebody else is `NotFound` rather than refused. Told apart, this would
// answer whether somebody else's key exists.
//

// Unlink removes one of the caller's own ways in.
//
// # A which, never a whose
//
// The identifier names one of **their** rows and the query says so: the read
// that finds it is narrowed by the caller before it is narrowed by the
// identifier, so one that belongs to somebody else is `NotFound` rather than
// refused. Told apart, this would answer whether an identity exists.
//
// # And it goes through the stack rather than around it
//
// Unlike everything else in this file. The read here goes to ent because there
// is nothing to narrow that the missing subject has not already narrowed; the
// **write** has a rule on it -- `server/core` refuses the removal of somebody's
// only way in -- and that rule lives in a layer. Going around it would be a
// button that locks somebody out of their own account.
func (s *Server) Unlink(ctx context.Context, req *app.MeUnlinkRequest) (*app.MeUnlinkResponse, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	}
	if s.walled == nil {
		return nil, status.Error(codes.Unimplemented, "this server cannot write")
	}

	id, err := entuuid.FromBytes(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %s", err)
	}

	// Theirs, or nothing. The holder is the first predicate and the identifier
	// the second, which is the order that makes the answer about the caller.
	n, err := s.db.Identity.Query().
		Where(
			identity.DateErasedIsNil(),
			identity.HasHolderWith(holder.IdEQ(f.Actor.Uuid())),
			identity.IdEQ(id),
		).
		Count(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, status.Error(codes.NotFound, "no such way in")
	}

	if _, err := s.walled.Identity().Erase(ctx,
		app.IdentityRef_builder{Id: req.GetId()}.Build()); err != nil {
		return nil, err
	}

	return app.MeUnlinkResponse_builder{}.Build(), nil
}

// Link attaches a provider account the caller has just proved they control.
//
// The other half of [Server.Unlink], and the half §4 left undrawn. It writes
// through the **walled** server for `Unlink`'s reason -- so `server/core` runs,
// and with it the rule that a second identity of one provider for one person is
// a link that found the wrong row.
//
// # It hangs off the frame and never off an argument
//
// `f.Actor`, exactly as `SignOutEverywhere` does. There is no field naming
// whose row this is and there will not be: what makes this method the person's
// own is the same absence that makes the other three theirs.
//
// # And what a refusal must not say
//

// SignOutEverywhere voids everything issued to the caller before now.
//
// The self-service half of D26: `HolderService.Invalidate` takes a subject and
// is an operator's, and this takes nothing and is the person's own.
//
// It does not end the session the caller is holding, and cannot: that session
// belongs to whatever app they are talking to and roster does not know it
// exists. The app that draws the button ends its own cookie beside this call.
func (s *Server) SignOutEverywhere(ctx context.Context, _ *app.MeSignOutEverywhereRequest) (*app.MeSignOutEverywhereResponse, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	}
	if s.walled == nil {
		return nil, status.Error(codes.Unimplemented, "this server cannot write")
	}

	v, err := s.walled.Holder().Invalidate(ctx, app.HolderInvalidateRequest_builder{
		Ref: app.HolderRef_builder{Id: f.Actor.Bytes()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	return app.MeSignOutEverywhereResponse_builder{
		DateInvalidated: v.GetDateInvalidated(),
	}.Build(), nil
}
