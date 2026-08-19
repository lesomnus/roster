// Package session keeps a console's cookies in a table.
//
// It is `payday/auth/authsession`'s `Store`, over roster's own rows. What it
// replaces says what it is for: `MemStore` is *right for one replica and
// silently wrong for two*, and lost on restart besides.
//
// `session.proto` carries the reasoning about where this belongs and why the
// key is hashed. What is here is the three methods payday asks for.
//
// # It goes to the ent client and not through the stack
//
// Like `server/me` and `keys.Sweep`, and for the reasons that apply to all
// three. A session write is not a caller's write: there is nobody to narrow it
// to, and putting it through the stack would record an `Audit` row every time a
// browser's idle clock moved -- which is a trail of one fact repeated, in the
// one table nothing erases.
//
// What that gives up is the wall, and there is none to give up: this is the
// control plane, whose one tenant is the deployment's own.
package session

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/session"
)

// Store is [authsession.Store] over a table.
type Store struct{ db *ent.Client }

// New makes the store from the client of whichever plane the sessions belong
// to, which is the control plane.
func New(db *ent.Client) *Store { return &Store{db: db} }

var _ authsession.Store = (*Store)(nil)

// Put writes a session, or moves the one clock that moves.
//
// `authsession` calls this twice for two different reasons: once when a cookie
// is minted, and again -- rarely -- when the idle clock has passed its halfway
// point. So a row that is already there is updated rather than refused, and the
// update touches the one column that can have changed.
func (s *Store) Put(ctx context.Context, v authsession.Session) error {
	sum := Sum(v.Key)

	was, err := s.db.Session.Query().
		Where(session.SecretEQ(sum), session.DateErasedIsNil()).
		Only(ctx)
	switch {
	case err == nil:
		u := s.db.Session.UpdateOneID(was.ID).SetDateUpdated(time.Now())
		if v.Idle.IsZero() {
			u = u.ClearDateIdle()
		} else {
			u = u.SetDateIdle(v.Idle)
		}

		return u.Exec(ctx)

	case !ent.IsNotFound(err):
		return err
	}

	who, err := pdid.Parse(v.Id)
	if err != nil {
		// A session naming something that is not an identifier is one nothing
		// could ever resolve, which is `ErrNobody`'s case one step earlier.
		return authsession.ErrNobody
	}

	grant, err := encode(v.Grant)
	if err != nil {
		return err
	}

	c := s.db.Session.Create().
		SetID(uuid.New()).
		SetHolderID(who.Uuid()).
		SetSecret(sum).
		SetGrant(grant).
		SetDateUpdated(time.Now()).
		SetDateCreated(time.Now())
	if !v.Expires.IsZero() {
		c = c.SetDateExpires(v.Expires)
	}
	if !v.Idle.IsZero() {
		c = c.SetDateIdle(v.Idle)
	}

	return c.Exec(ctx)
}

// Get answers the session a key names.
//
// Expiry is **not** checked here, deliberately: `authsession.Handler` checks it
// itself and says why -- *a store may forget a session at any time; expiry is
// checked here rather than trusted to it.* A store that also checked would be a
// second opinion about the clock, and the two would disagree at the edges.
func (s *Store) Get(ctx context.Context, key string) (authsession.Session, error) {
	v, err := s.db.Session.Query().
		Where(session.SecretEQ(Sum(key)), session.DateErasedIsNil()).
		WithHolder(func(q *ent.HolderQuery) { q.Where(holder.DateErasedIsNil()) }).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return authsession.Session{}, authsession.ErrNoSession
		}

		return authsession.Session{}, err
	}

	h := v.Edges.Holder
	if h == nil {
		// Somebody erased since the cookie was minted.
		//
		// **Written rather than inherited**, and that is the part worth
		// knowing: the narrowing that makes an erased row unreachable lives in
		// the generated layer, and this goes to ent directly for the reasons at
		// the top of this file -- so the predicate on the edge above is the
		// whole of it here.
		//
		// `cmd.Resolver` refuses the same person one hop later, so this is not
		// the only wall. It is the cheaper one, and a session for somebody who
		// is gone is not a session.
		return authsession.Session{}, authsession.ErrNoSession
	}

	out := authsession.Session{
		Key:      key,
		Id:       pdid.Id(h.ID).String(),
		TenantId: tenantOf(h),
		Grant:    decode(v.Grant),
	}
	if v.DateExpires != nil {
		out.Expires = *v.DateExpires
	}
	if v.DateIdle != nil {
		out.Idle = *v.DateIdle
	}

	return out, nil
}

// Del ends a session, and ending one that is not there succeeds.
//
// Erased rather than deleted, so that a trail naming the row still finds
// something -- and out of reach the moment it lands, which is what makes
// signing out immediate everywhere that cookie was used.
func (s *Store) Del(ctx context.Context, key string) error {
	_, err := s.db.Session.Update().
		Where(session.SecretEQ(Sum(key)), session.DateErasedIsNil()).
		SetDateErased(time.Now()).
		SetDateUpdated(time.Now()).
		Save(ctx)

	return err
}

// Sum is the verifier for a cookie value, and the way its row is found.
//
// SHA-256, unsalted, for the reason `keys.Sum` gives about a key: the input is
// 32 bytes from `crypto/rand`, so there is no dictionary to run against it, and
// a per-row salt could not be applied before the row is known.
func Sum(key string) []byte {
	v := sha256.Sum256([]byte(key))

	return v[:]
}

// encode is a grant as `payday.Grant`, through the encoder that owns the rule.
//
// Every axis carries a flag beside its list and an empty list means **nothing**,
// which is the opposite of what an empty list usually means. Spelling that out
// here would be a second place to get it backwards, so it goes through the one
// function that already answers it.
func encode(g frame.Grant) ([]byte, error) {
	id, err := auth.Introspection(auth.Identity{Grant: g})
	if err != nil {
		return nil, err
	}

	return proto.Marshal(id.GetGrant())
}

// decode is the other half, and a row it cannot read allows **nothing**.
//
// `frame.Grant`'s zero value is the safe direction: a session whose grant did
// not survive a schema change is one that may call no method, which is a person
// signing in again rather than a person with more than they had.
func decode(b []byte) frame.Grant {
	v := &pdpb.Grant{}
	if err := proto.Unmarshal(b, v); err != nil {
		return frame.Grant{}
	}

	id, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{Grant: v}.Build())
	if err != nil {
		return frame.Grant{}
	}

	return id.Grant
}

func tenantOf(h *ent.Holder) string {
	if h.TenantID == uuid.Nil {
		return ""
	}

	return pdid.Id(h.TenantID).String()
}
