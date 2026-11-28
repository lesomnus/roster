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
// to, no version a caller holds, and no stream that hears of one.
//
// It said that this gave up nothing -- *this is the control plane, whose one
// tenant is the deployment's own* -- and that a line of the trail for the idle
// clock was one fact repeated. But `sign_in.enabled` keeps a roster user's
// sessions here too, on the data plane, where a session is a kind a check of
// the rows reads (`roster trail rows`): and a row written past the servers
// with nothing on the trail about it is what that check exists to find. Every
// sign-in was a finding nothing cleared, since the row's identifier was of no
// kind and the recorder passes over one (#280).
//
// # So what it writes is on the trail, as the act it is
//
// Each write and its line in one transaction (`pd.Record`): a session made is
// [Minted], its idle clock moved is [Used], and its ending is [Ended]; the
// sweep's collection is [Expired]. The first three are by whoever the session
// is of, and each is filed under their tenant. A line says which row and when,
// and nothing of what the row holds: no grant, and nothing of the cookie.
//
// The idle clock's is the one that repeats, and it is there because the row
// is written: the check compares a row with what the newest line about it
// kept, and the deadline is a column of the row. `authsession` moves it once
// in half an idle window at most, and only for a session that is being used.
package session

import (
	"context"
	"crypto/sha256"

	"google.golang.org/protobuf/proto"
	"uuid"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/internal/moment"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/pd"
)

// Store is [authsession.Store] over a table.
type Store struct {
	db *ent.Client
	cs audit.Contents
}

// New makes the store from the client of whichever plane the sessions belong
// to -- the control plane for an operator's, the data plane for a roster
// user's -- and `cs`, what that plane's recorder is handed: what a line of
// the trail about a session keeps is decided where every other line's is.
func New(db *ent.Client, cs audit.Contents) *Store { return &Store{db: db, cs: cs} }

// The acts a session is on the trail as, beside [Expired], which is the
// sweep's: made by a sign-in, its idle clock moved by a call that used it, and
// ended by a sign-out.
const (
	Minted = "roster.session.mint"
	Used   = "roster.session.use"
	Ended  = "roster.session.end"
)

var _ authsession.Store = (*Store)(nil)

// Put writes a session, or moves the one clock that moves.
//
// What it keeps is what payday says a store keeps: who, the grant, both
// clocks, and when its person signed in (`DateAuthenticated`), which is
// written with the row and never by the update.
//
// `authsession` calls this twice for two different reasons: once when a cookie
// is minted, and again -- rarely -- when the idle clock has passed its halfway
// point. So a row that is already there is updated rather than refused, and the
// update touches the one column that can have changed. A session that has
// ended is neither, and is answered [authsession.ErrNoSession].
func (s *Store) Put(ctx context.Context, v authsession.Session) error {
	// Nowhere to put it, and said so rather than dropped: the admin console holds
	// nothing beside a session, and an app that did would find its sign-in
	// refused here instead of finding its every call answered as a session
	// that cannot act.
	if len(v.Held) > 0 {
		return authsession.ErrCannotHold
	}

	sum := Sum(v.Key)

	was, err := s.db.Session.Query().
		Where(session.SecretEQ(sum), session.DateErasedIsNil()).
		Only(ctx)
	switch {
	case err == nil:
		return s.written(ctx, Used, pdid.Id(was.Id), was.HolderId, func(ctx context.Context, db *ent.Client) (bool, error) {
			u := db.Session.UpdateOneId(was.Id).SetDateUpdated(moment.Now())
			if v.Idle.IsZero() {
				u = u.ClearDateIdle()
			} else {
				u = u.SetDateIdle(moment.Of(v.Idle))
			}

			return true, u.Exec(ctx)
		})

	case !ent.IsNotFound(err):
		return err
	}

	// No live row. A cookie being minted, or the clock of a session that has
	// **ended** since the request carrying it read it -- signed out in another
	// tab, voided, collected.
	//
	// This made a row either way, and for the second that was the session
	// back: the same secret, the expiry it had, and *created now*, which is
	// also past the stamp `Holder.Invalidate` voids by. The cookie somebody
	// had just signed out answered again until its expiry (#275).
	//
	// They are told apart by the secret. A cookie's value is thirty-two random
	// bytes and is minted once, so one that has ever been a row here is not
	// one being minted -- and an ended row is still a row, since ending one is
	// an erase and not a delete. The caller is `authsession`'s `used`, which
	// ignores a refusal: the request in flight is served, and nothing is
	// written.
	ended, err := s.db.Session.Query().Where(session.SecretEQ(sum)).Exist(ctx)
	if err != nil {
		return err
	}
	if ended {
		return authsession.ErrNoSession
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

	// An identifier of the session kind, as the servers mint one for every
	// other row (`pd.Minter`). It was `uuid.New()`, an identifier of no kind,
	// and what names a row on the trail is its identifier: the recorder passes
	// over one that says no kind, so nothing could be said of this row there,
	// by the sweep that collects it or by an operator accepting it (#280).
	id := pdid.New(pd.SessionDomain)

	return s.written(ctx, Minted, id, who.Uuid(), func(ctx context.Context, db *ent.Client) (bool, error) {
		// Every time of the row as the database keeps it, on every database
		// (`internal/moment`): what `Get` answers of it is then what was
		// written, on SQLite as on PostgreSQL, and not the sign-in's
		// nanoseconds on one and its microseconds on the other.
		now := moment.Now()
		c := db.Session.Create().
			SetId(id.Uuid()).
			SetHolderId(who.Uuid()).
			SetSecret(sum).
			SetGrant(grant).
			SetDateUpdated(now).
			SetDateCreated(now)
		if !v.DateAuthenticated.IsZero() {
			// When its person proved who they are, as the sign-in said it, and
			// never moved after: the update above writes the idle clock alone.
			c = c.SetDateAuthenticated(moment.Of(v.DateAuthenticated))
		}
		if !v.Expires.IsZero() {
			c = c.SetDateExpired(moment.Of(v.Expires))
		}
		if !v.Idle.IsZero() {
			c = c.SetDateIdle(moment.Of(v.Idle))
		}

		return true, c.Exec(ctx)
	})
}

// written runs `write`, which writes the session `id` of the holder `who`,
// and puts that write on the trail as `act`, in one transaction: a session
// written and not said is the row a check of the rows finds, and one said and
// not written is a line about nothing.
//
// The line is by whoever the session is of, held by their tenant, and `ctx`
// cannot say so: a sign-in is asked by nobody yet, and the idle clock moves
// while the cookie is still being read. So the context the recorder reads is
// framed here, as them, and with nothing granted, since nothing but the
// recorder reads it. The line is filed under the row's own tenant, which is
// theirs (`session.proto`, `tenanted`).
//
// `write` answers whether it wrote. One that did not says nothing. And a row
// from before sessions had a kind is written as it always was and said by
// nothing: the recorder passes over its identifier.
func (s *Store) written(
	ctx context.Context, act string, id pdid.Id, who uuid.UUID,
	write func(context.Context, *ent.Client) (bool, error),
) error {
	// The store's own write, through ent and in a transaction of its own: a
	// session is minted before anybody is anybody and moved by reading its
	// cookie, so it is the deployment's and no hook is asked about it
	// (`cmd/hook.go`).
	ctx = bare.TrustAdmission(ctx)

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	wrote, err := write(ctx, tx.Client())
	if err != nil || !wrote {
		return err
	}

	of, err := tx.Holder.Query().Where(holder.IdEQ(who)).Select(holder.FieldTenantId).Only(ctx)
	if err != nil {
		return err
	}

	as := frame.Into(ctx, frame.New(pdid.Id(who), pdid.Id(of.TenantId), frame.Grant{}))
	if _, err := pd.Record(as, tx.Client(), s.cs, act, []pdid.Id{id}); err != nil {
		return err
	}

	return tx.Commit()
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

	if w := h.DateInvalidated; w != nil && !v.DateCreated.After(*w) {
		// Issued at or before the holder's stamp, so it is void -- and this
		// store is the place that can say so, which is worth spelling out
		// because `MeService.SignOutEverywhere`'s contract sounds like it
		// forbids it. roster answers *invalid since when* and an app answers
		// *what is still alive* -- and for the admin console's cookies,
		// **roster is the app**. This is roster answering as one.
		//
		// Not after, which is `keys.findDelegation`'s reading, a tie included:
		// a session minted in the instant of the stamp is too old, as a
		// delegation, the sign-in a key carries, a link and a sign-in half
		// made are. It read *before*, so that one instant was the one
		// credential a stamp left standing (#277). No call stamps and then
		// mints: a sign-in after a changed password or a recovery is a call
		// of its own, later.
		//
		// Nothing here read the stamp until 2026-08-28, so "sign out
		// everywhere" pressed in the admin console voided delegations a customer's
		// apps held and left every console session alive -- including the one
		// a takeover had opened, which is the session the button is for. The
		// keys a person minted stay, as everywhere: a key is named, listed and
		// revoked one at a time, and D26 says why that is a second act.
		return authsession.Session{}, authsession.ErrNoSession
	}

	out := authsession.Session{
		Key:      key,
		Id:       pdid.Id(h.Id).String(),
		TenantId: tenantOf(h),
		Grant:    decode(v.Grant),
	}
	if v.DateAuthenticated != nil {
		out.DateAuthenticated = *v.DateAuthenticated
	}
	if v.DateExpired != nil {
		out.Expires = *v.DateExpired
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
// signing out immediate everywhere that cookie was used. The trail says so as
// [Ended], by whoever the session was of.
func (s *Store) Del(ctx context.Context, key string) error {
	was, err := s.db.Session.Query().
		Where(session.SecretEQ(Sum(key)), session.DateErasedIsNil()).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}

		return err
	}

	return s.written(ctx, Ended, pdid.Id(was.Id), was.HolderId, func(ctx context.Context, db *ent.Client) (bool, error) {
		// Asked again whether it is live, in the transaction: of two
		// sign-outs at once one ends it, and the other finds it ended and
		// says nothing.
		now := moment.Now()
		n, err := db.Session.Update().
			Where(session.IdEQ(was.Id), session.DateErasedIsNil()).
			SetDateErased(now).
			SetDateUpdated(now).
			Save(ctx)

		return n > 0, err
	})
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
	if h.TenantId == uuid.Nil() {
		return ""
	}

	return pdid.Id(h.TenantId).String()
}
