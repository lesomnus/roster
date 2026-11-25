package core

import (
	"context"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// Badge is what a station's badge and PIN are held to (`vouch.badge`), apart
// from a password's numbers.
//
// # Why its own
//
// A password is a phrase somebody chose, guessed at by anybody who can reach a
// form, and ten wrong answers lock it for a quarter of an hour. A PIN is four
// digits, guessed at by somebody holding the card at a station. Four digits are
// enough with the card in hand only if guessing stops after a few tries and
// somebody has to be asked: a lock that lifts every quarter of an hour is four
// hundred and eighty guesses a day at a found badge. So the numbers are not a
// password's, and nothing about a password is read here.
type Badge struct {
	// MinLength is the fewest digits a PIN is; zero is [MinPinLength].
	MinLength int

	// Failures is how many wrong PINs in a row lock a badge; zero is
	// [BadgeFailures].
	Failures int32

	// For is how long a lock lasts; zero is until somebody unlocks it
	// (`BadgeService/Unlock`). After it, the count starts again from none.
	For time.Duration
}

const (
	// MinPinLength is the fewest digits a PIN may be made to be, and what it is
	// where nothing says: four.
	MinPinLength = 4

	// MaxPinLength is the most digits a PIN is: twelve, past what anybody types
	// at a station, and a bound on what is hashed.
	MaxPinLength = 12

	// BadgeFailures is how many wrong PINs in a row lock a badge where nothing
	// says: five.
	BadgeFailures = 5

	// MaxCodeLength is the most bytes a badge's code is.
	MaxCodeLength = 128
)

// Or is this with the defaults filled in where it says nothing.
func (b Badge) Or() Badge {
	if b.MinLength <= 0 {
		b.MinLength = MinPinLength
	}
	if b.Failures <= 0 {
		b.Failures = BadgeFailures
	}
	if b.For < 0 {
		b.For = 0
	}

	return b
}

// WithBadge gives the layer `vouch.badge`, for `BadgeService`'s verbs.
func WithBadge(v Badge) Option { return func(s *Core) { s.badges = v } }

// locked answers whether a badge's lock holds at `now`: it locked, and either
// nothing lifts it or it has not lifted yet.
func (b Badge) locked(v *app.Badge, now time.Time) bool {
	at := v.GetDateLocked()
	if at == nil {
		return false
	}

	return b.For <= 0 || now.Before(at.AsTime().Add(b.For))
}

// coreBadge is the layer over the generated `BadgeService`: the three verbs
// its overlay declares, and the rule its `Erase` meets.
type coreBadge struct {
	Core
	app.BadgeServiceServer
}

func (s Core) Badge() app.BadgeServiceServer {
	return coreBadge{s, s.Next().Badge()}
}

// errNoBadgeKey is a deployment with no keyring asked to hold a PIN, in the
// sentence TOTP has for a seed: a verifier it cannot seal is one it will not
// store in the clear.
var errNoBadgeKey = status.Error(codes.Unimplemented,
	"this deployment holds no key to seal a PIN with, so it cannot hold a badge")

// errNoBadgeKeyToRead is the same deployment asked to check one.
var errNoBadgeKeyToRead = status.Error(codes.Unimplemented,
	"this deployment holds no key to read a PIN with, so it cannot check a badge")

// badgeCode refuses a code that cannot be what a badge says: nothing, more
// than [MaxCodeLength] bytes, or a control character. Compared as written, so
// nothing is folded here.
func badgeCode(code string) error {
	switch {
	case code == "":
		return pderr.Invalidf("code", "what the badge says; must not be empty")
	case len(code) > MaxCodeLength:
		return pderr.Invalidf("code", "at most %d bytes, and this is %d", MaxCodeLength, len(code))
	case !utf8.ValidString(code):
		return pderr.Invalidf("code", "is not text")
	}
	for _, r := range code {
		if unicode.IsControl(r) {
			return pderr.Invalidf("code", "holds a control character, which no reader puts on a card")
		}
	}

	return nil
}

// pin refuses a PIN that is not the deployment's: decimal digits, at least
// [Badge.MinLength] and at most [MaxPinLength] of them.
func (b Badge) pin(pin string) error {
	for _, r := range pin {
		if r < '0' || r > '9' {
			return pderr.Invalidf("pin", "a PIN is decimal digits, and this one is not")
		}
	}
	switch n := len(pin); {
	case n < b.MinLength:
		return pderr.Invalidf("pin", "at least %d digits", b.MinLength)
	case n > MaxPinLength:
		return pderr.Invalidf("pin", "at most %d digits", MaxPinLength)
	}

	return nil
}

// badgeShown is what of a badge an answer carries: all of it but the
// verifier, which no answer does. Selected rather than cleared, because the
// stack at the box has no layer that clears it.
func badgeShown() *app.BadgeSelect {
	return app.BadgeSelect_builder{
		Holder:      app.HolderSelect_builder{}.Build(),
		Code:        z.Ptr(true),
		Failures:    z.Ptr(true),
		DateLocked:  z.Ptr(true),
		DateRotated: z.Ptr(true),
		TenantId:    z.Ptr(true),
		DateUpdated: z.Ptr(true),
		DateCreated: z.Ptr(true),
	}.Build()
}

// Enrol makes somebody's badge, or replaces all of it.
//
// Held to `mayReach`, as every way in is: a badge is a way in at every
// station that asks, so nobody writes one for somebody who holds more than
// they do. The person is read through `Next()`, so somebody the caller cannot
// see cannot have a badge written for them either.
//
// What it replaces is all of it -- the code, the PIN, the count and the lock --
// because a badge enrolled again is a card handed over again: somebody who was
// given a new PIN is not who the lock was protecting against.
func (s coreBadge) Enrol(ctx context.Context, req *app.BadgeEnrolRequest) (*app.Badge, error) {
	// A fact about the deployment, said before anything about the request.
	if s.keyring.Current == "" {
		return nil, errNoBadgeKey
	}
	if req.GetHolder() == nil {
		return nil, pderr.Invalidf("holder", "whose badge this is")
	}
	if err := badgeCode(req.GetCode()); err != nil {
		return nil, err
	}
	if err := s.badges.pin(req.GetPin()); err != nil {
		return nil, err
	}

	who, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetHolder(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	holder, err := pdid.From(who.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "holder", holder); err != nil {
		return nil, err
	}

	sealed, err := vouch.SealPin(s.keyring, []byte(req.GetPin()))
	if err != nil {
		return nil, status.Error(codes.Internal, "the PIN cannot be stored just now")
	}

	on := app.HolderRef_builder{Id: who.GetId()}.Build()
	mine := app.BadgeRef_builder{Holder: app.BadgeRefByHolder_builder{Holder: on}.Build()}.Build()
	now := timestamppb.Now()

	v, err := s.Next().Badge().Get(ctx, app.BadgeGetRequest_builder{
		Ref:    mine,
		Select: app.BadgeSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	switch {
	case err == nil:
		_, err = s.Next().Badge().Patch(ctx, app.BadgePatchRequest_builder{
			Ref:            app.BadgeRef_builder{Id: v.GetId()}.Build(),
			Code:           z.Ptr(req.GetCode()),
			Secret:         sealed,
			Failures:       z.Ptr(int32(0)),
			DateLockedNull: z.Ptr(true),
			DateRotated:    now,
			DateUpdated:    v.GetDateUpdated(),
		}.Build())

	case status.Code(err) == codes.NotFound:
		_, err = s.Next().Badge().Add(ctx, app.BadgeAddRequest_builder{
			Holder:      on,
			Code:        req.GetCode(),
			Secret:      sealed,
			DateRotated: now,
		}.Build())
	}
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return nil, s.taken(ctx, mine, err)
		}

		return nil, err
	}

	return s.Next().Badge().Get(ctx, app.BadgeGetRequest_builder{Ref: mine, Select: badgeShown()}.Build())
}

// taken is what a write refused as `AlreadyExists` is told: the code is
// somebody else's, unless what is there now is a badge of this person's,
// made by another call in the same instant.
//
// The code is said to be taken and not by whom. The person who has it may be
// somebody the caller cannot see -- of a site they are not bound in -- and
// even where they can, a refusal is no way to find out who a card is.
func (s coreBadge) taken(ctx context.Context, mine *app.BadgeRef, err error) error {
	if _, gerr := s.Next().Badge().Get(ctx, app.BadgeGetRequest_builder{
		Ref:    mine,
		Select: app.BadgeSelect_builder{}.Build(),
	}.Build()); gerr == nil {
		return status.Error(codes.Aborted, "their badge was written by another call just now; read it and enrol again")
	}

	return status.Error(codes.AlreadyExists,
		"code: another person of the tenant has a badge that says this, and a code names one person")
}

// Unlock clears somebody's lock and their count, leaving the PIN.
//
// Held to `mayReach`, as `Credential.Unlock` is: a lock you may clear is one
// you could have caused, so you may clear it for nobody whose permissions are
// not a subset of yours.
func (s coreBadge) Unlock(ctx context.Context, req *app.BadgeUnlockRequest) (*app.BadgeUnlockResponse, error) {
	if req.GetHolder() == nil {
		return nil, pderr.Invalidf("holder", "whose badge this is")
	}

	v, err := s.Next().Badge().Get(ctx, app.BadgeGetRequest_builder{
		Ref: app.BadgeRef_builder{
			Holder: app.BadgeRefByHolder_builder{Holder: req.GetHolder()}.Build(),
		}.Build(),
		Select: app.BadgeSelect_builder{
			DateLocked:  z.Ptr(true),
			DateUpdated: z.Ptr(true),
			Holder:      app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "holder", holder); err != nil {
		return nil, err
	}

	if _, err := s.Next().Badge().Patch(ctx, app.BadgePatchRequest_builder{
		Ref:            app.BadgeRef_builder{Id: v.GetId()}.Build(),
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateUpdated:    v.GetDateUpdated(),
	}.Build()); err != nil {
		return nil, err
	}

	res := app.BadgeUnlockResponse_builder{}
	if at := v.GetDateLocked(); at != nil {
		res.DateLocked = at
	}

	return res.Build(), nil
}

// Erase takes somebody's badge away, held to `mayReach` as a credential's
// erase is: what you may take away of somebody's ways in is what you may
// write of them. Erasing what is not there succeeds, which is `Erase`'s rule.
func (s coreBadge) Erase(ctx context.Context, req *app.BadgeRef) (*app.BadgeEraseResponse, error) {
	v, err := s.BadgeServiceServer.Get(ctx, app.BadgeGetRequest_builder{
		Ref:    req,
		Select: app.BadgeSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return app.BadgeEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return nil, err
	}

	return s.BadgeServiceServer.Erase(ctx, app.BadgeRef_builder{Id: v.GetId()}.Build())
}

// Verify answers whether a PIN is the one held for the badge that says
// `code`, in the caller's tenant.
//
// # One answer, at one cost
//
// `VouchService/Verify`'s habit, so that a station is written against one: a
// code nobody has, a wrong PIN, somebody suspended, erased or of a closed
// tenant, and somebody the caller's grant does not reach are each `ok: false`,
// and each costs an unseal and a hash -- what a wrong PIN costs. A locked
// badge costs the same and says `locked`, which a found badge may learn and a
// person at a station needs to be told.
//
// # Who is read through what
//
// The person, through the wall: this layer's `Next()` on the stack a caller
// reaches is the walled one, narrowed by where the caller holds this method,
// so a station bound in a site checks the badges of that site's people and a
// badge of somebody outside it is answered as one nobody has. The badge is
// read through the same stack and its verifier comes back: the layer that
// clears a verifier is outside this one, which is the reason
// `server/vouch` reads a credential through the open server and is no reason
// here.
//
// # What it leaves
//
// A wrong PIN is counted on the row, a compare-and-swap as a password's is,
// and that write is on the trail as any write is. A verification that answered
// somebody is a read of them, filed under their tenant and naming the caller
// and the badge. A code nobody has leaves nothing.
func (s coreBadge) Verify(ctx context.Context, req *app.BadgeVerifyRequest) (*app.BadgeVerifyResponse, error) {
	if s.keyring.Current == "" {
		return nil, errNoBadgeKeyToRead
	}

	// Which tenant is the caller's, and a caller in none has no badges to ask
	// about: the request names no tenant, so that a station cannot ask about
	// another operator's cards.
	f, ok := frame.From(ctx)
	if !ok || f.Tenant.IsZero() || f.AsAccount() {
		return nil, status.Error(codes.InvalidArgument,
			"a badge is looked for in the caller's tenant, and this caller is in none")
	}

	// Facts about the request, refused before anybody is looked for, so that
	// they cannot depend on whether anybody is there.
	if err := badgeCode(req.GetCode()); err != nil {
		return nil, err
	}
	if len(req.GetPin()) > MaxPinLength {
		return nil, pderr.Invalidf("pin", "at most %d digits", MaxPinLength)
	}

	pin := []byte(req.GetPin())
	no := func() (*app.BadgeVerifyResponse, error) {
		vouch.BurnPin(s.keyring, pin)
		return app.BadgeVerifyResponse_builder{}.Build(), nil
	}

	v, err := s.Next().Badge().Get(ctx, app.BadgeGetRequest_builder{
		Ref: app.BadgeRef_builder{
			Code: app.BadgeRefByCode_builder{TenantId: f.Tenant.Bytes(), Code: z.Ptr(req.GetCode())}.Build(),
		}.Build(),
		Select: app.BadgeSelect_builder{
			Holder:      app.HolderSelect_builder{}.Build(),
			Secret:      z.Ptr(true),
			Failures:    z.Ptr(true),
			DateLocked:  z.Ptr(true),
			TenantId:    z.Ptr(true),
			DateUpdated: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		return no()
	}

	// The person, through the wall, and whether they may be let in at all.
	whose, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: v.GetHolder().GetId()}.Build(),
		Select: app.HolderSelect_builder{
			DateDisabled: z.Ptr(true),
			DateErased:   z.Ptr(true),
			Tenant:       app.TenantSelect_builder{DateClosed: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		return no()
	}
	if whose.GetDateDisabled() != nil || whose.GetDateErased() != nil || whose.GetTenant().GetDateClosed() != nil {
		return no()
	}

	now := time.Now()
	if s.badges.locked(v, now) {
		// Not compared and not counted, and burned as every refusal is: a lock
		// said by the clock is a lock said to a caller the answer is not kept
		// from, but a stream of guesses must not move anything either.
		vouch.BurnPin(s.keyring, pin)
		return app.BadgeVerifyResponse_builder{Locked: true}.Build(), nil
	}
	if v.GetDateLocked() != nil {
		// A lock that lifted by itself: the count starts again from none.
		v.SetFailures(0)
	}

	same, err := vouch.ComparePin(s.keyring, v.GetSecret(), pin)
	if err != nil {
		return nil, status.Error(codes.Internal, "the stored verifier cannot be read")
	}
	if !same {
		return s.failed(ctx, v)
	}

	if v.GetFailures() != 0 || v.GetDateLocked() != nil {
		// Cleared under the version read, and a write that lost is somebody
		// else's write landing this instant: for the count, the same outcome.
		_, err := s.Next().Badge().Patch(ctx, app.BadgePatchRequest_builder{
			Ref:            app.BadgeRef_builder{Id: v.GetId()}.Build(),
			Failures:       z.Ptr(int32(0)),
			DateLockedNull: z.Ptr(true),
			DateUpdated:    v.GetDateUpdated(),
		}.Build())
		if err != nil && !badgeRaced(err) {
			return nil, err
		}
	}

	// A read of who this is, on the access record: the answer carries no
	// field the schema calls somebody's, so it is said here, by the badge that
	// answered, in its tenant.
	id, _ := pdid.From(v.GetId())
	in, _ := pdid.From(v.GetTenantId())
	access.Saw(ctx, access.Subject{Id: id, Tenant: in})

	return app.BadgeVerifyResponse_builder{
		Ok:     true,
		Holder: v.GetHolder().GetId(),
		Tenant: v.GetTenantId(),
	}.Build(), nil
}

// failed counts a wrong PIN, and locks the badge when there have been enough.
//
// A compare-and-swap, as `server/vouch` counts a password's: a write that
// loses reads the row again and counts on what is there now, so a burst of
// guesses sent side by side is counted guess by guess and not as one.
func (s coreBadge) failed(ctx context.Context, v *app.Badge) (*app.BadgeVerifyResponse, error) {
	for range max(int(s.badges.Failures), 1) + 1 {
		now := time.Now()
		n := v.GetFailures() + 1

		patch := app.BadgePatchRequest_builder{
			Ref:         app.BadgeRef_builder{Id: v.GetId()}.Build(),
			Failures:    z.Ptr(n),
			DateUpdated: v.GetDateUpdated(),
		}
		locks := n >= s.badges.Failures
		switch {
		case locks:
			patch.DateLocked = timestamppb.New(now)
		case v.GetDateLocked() != nil:
			// A lock that lifted by itself, cleared as the count starts again.
			patch.DateLockedNull = z.Ptr(true)
		}

		_, err := s.Next().Badge().Patch(ctx, patch.Build())
		if err == nil {
			return app.BadgeVerifyResponse_builder{Locked: locks}.Build(), nil
		}
		if !badgeRaced(err) {
			return nil, err
		}

		// Somebody else wrote this instant. What is there now?
		v, err = s.Next().Badge().Get(ctx, app.BadgeGetRequest_builder{
			Ref: app.BadgeRef_builder{Id: v.GetId()}.Build(),
			Select: app.BadgeSelect_builder{
				Failures:    z.Ptr(true),
				DateLocked:  z.Ptr(true),
				DateUpdated: z.Ptr(true),
			}.Build(),
		}.Build())
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return app.BadgeVerifyResponse_builder{}.Build(), nil
			}

			return nil, err
		}
		if s.badges.locked(v, time.Now()) {
			// Locked by the ones ahead of this. Said, as the answer that locked
			// it was.
			return app.BadgeVerifyResponse_builder{Locked: true}.Build(), nil
		}
		if v.GetDateLocked() != nil {
			v.SetFailures(0)
		}
	}

	// Never landed: a row written more often than this could keep up with.
	// "No" is right, and an error would turn people mistyping at once into a
	// request that failed.
	return app.BadgeVerifyResponse_builder{}.Build(), nil
}

// badgeRaced is a write that lost a compare-and-swap.
func badgeRaced(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.FailedPrecondition, codes.NotFound:
		return true
	default:
		return false
	}
}
