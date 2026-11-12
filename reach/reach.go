// Package reach is the half of an app that asks roster what somebody may call:
// `HolderService/Reaches` for the holder, payday's `frame.Covers` for the
// method, the answer kept for as long as [standing] keeps what it is told, and
// dropped when roster's stream says something of the holder.
//
// # Why it is a package
//
// [standing]'s reason, D22's: the half that is the same in every app, written
// once and shipped as something apps import. An app's permissions are its own
// RPCs and the permission methods it declares, named in roster's roles like
// any other method (`docs/apps.md`, *A person calling the app*), and the
// question it asks is the same in every app: may this holder call this method.
// Asking it well takes the answer read as roster's gate reads it, a cache that
// outlives no change roster says, the stream that says them, and a refusal --
// not a guess -- when roster cannot be asked. Loft was about to write the four
// of them (loft#333), beside the copy of `standing` it had already given up,
// and this is them written once.
//
// # What it is, and what stays the app's
//
//   - [Holders.ReachOf] asks roster what somebody may call; [Holders.Reach]
//     keeps the answer for [standing.HeldTtl], and that roster could not be
//     asked for [standing.HeldRetry]; [Holders.Recheck] drops what is kept of
//     one holder, for a change the app made itself.
//   - [Holders.May] is the question an app's gate asks, answered from
//     [Reach.Everywhere] -- what an app with no sites of its own grants on.
//   - [Holders.Follow] holds roster's sync stream open and drops what is kept
//     of anybody it speaks of; [Holders.Follower] is that as a loop for
//     payday's `spin.Run`.
//
// What a method **means** is the app's, and none of it is here: which of its
// RPCs a permission method stands for, what a refusal looks like to its
// caller, and which methods its default roles start with. So is who the app is
// at roster, which [Config.As] says -- the same `As` an app hands [standing].
// It never asks about a role's name, or which group or team somebody is in:
// a role is a bundle of methods a tenant may split or rename, and a
// membership nothing is bound to grants nothing.
//
// It reaches roster only over the wire, with a `grpc.ClientConnInterface` the
// app dialed: an external roster's data plane, or the one an app runs in its
// own process, served on an in-memory listener. `docs/apps.md` is the page.
package reach

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/standing"
)

// Methods is what an app's role at roster holds for everything here: what
// somebody may call (`HolderService/Reaches`), and the stream that says when
// that may have changed (`SyncService/Watch`).
//
// It is the part of the role this package asks for, beside [standing.Methods]
// and whatever the app asks for itself: the `--role` of `roster app install`,
// or the role an app running roster in its own process writes for its holder.
var Methods = []string{
	rstr.HolderService_Reaches_FullMethodName,
	rstr.SyncService_Watch_FullMethodName,
}

var (
	// ErrDenied is a method roster does not say somebody may call -- across
	// the whole tenant, which is what [Holders.May] asks -- including anybody
	// roster has nobody by, and anybody suspended, who reaches nothing.
	ErrDenied = errors.New("roster does not say they may call that")

	// ErrNoPerson is somebody roster does not have, or does not show the
	// app: [standing]'s, so that one answer reads as itself from both.
	ErrNoPerson = standing.ErrNoPerson

	// ErrUnreachable is a roster that could not be asked, which is a refusal
	// too: what it would have said is not known, and a call is not served on
	// a guess. [standing]'s, so that an app tells its caller the same thing
	// -- this is not your doing -- whichever of the two asked.
	ErrUnreachable = standing.ErrUnreachable
)

// Config is what an app says.
type Config struct {
	// Conn is roster: an external one's data plane, dialed the way the app
	// dials it, or the roster the app runs in its own process, over the
	// in-memory listener (`bufconn`) it serves it on.
	Conn grpc.ClientConnInterface

	// As is a context whose calls roster answers as the app's holder in the
	// tenant its people are in -- [standing.Config.As], and an app hands the
	// same one to both. Its error is a roster that could not be asked, and
	// passes through as it is.
	As func(ctx context.Context) (context.Context, error)

	// Log is where the stream says what became of it. Nil is the logger the
	// context carries.
	Log *slog.Logger

	// Now is the clock what is kept is aged by. Nil is `time.Now`.
	Now func() time.Time

	// Refused is how long a stream roster refused is left before it is asked
	// for again. Zero is [standing.RefusedRetry].
	Refused time.Duration
}

// Reach is what roster says somebody may call: `HolderService/Reaches`, as it
// answered.
type Reach struct {
	Holder pdid.Id

	// Methods is the union roster's gate asks -- *may they ever call this* --
	// as the patterns the roles wrote. roster's wall then narrows the rows a
	// grant bound at one site, or held in a team, reaches; an app that has no
	// such wall of its own cannot, and reading this would hand that grant
	// everywhere. Sites and EverySite are where those grants were made.
	Methods   []string
	Sites     []pdid.Id
	EverySite bool

	// Everywhere is what they hold across the whole tenant, through a binding
	// with no site -- the part that needs no narrowing, and the part an app
	// with no sites of its own grants on ([Reach.Covers]).
	Everywhere []string
}

// Covers says whether somebody may call `method` anywhere in their tenant:
// whether one pattern of [Reach.Everywhere] covers it on its own, which is
// `frame.Covers`'s rule -- one of them has to, and the union never stands in
// for a pattern nobody holds.
func (r Reach) Covers(method string) bool {
	if method == "" {
		return false
	}

	return slices.ContainsFunc(r.Everywhere, func(v string) bool { return frame.Covers(v, method) })
}

// clone is a Reach nobody else holds, so that what the cache keeps is not
// what a caller appends to.
func (r Reach) clone() Reach {
	r.Methods = slices.Clone(r.Methods)
	r.Sites = slices.Clone(r.Sites)
	r.Everywhere = slices.Clone(r.Everywhere)

	return r
}

// Holders is what an app's holders may call, as roster says it: asked when it
// is wanted, kept for as long as the answer is good for, and dropped when
// roster says something of them.
type Holders struct {
	conn    grpc.ClientConnInterface
	as      func(context.Context) (context.Context, error)
	logger  *slog.Logger
	now     func() time.Time
	refused time.Duration

	// What roster answered about each holder [Holders.Reach] was asked
	// about, and when; and how many times anything kept was dropped, which is
	// what tells an answer from one given before a drop.
	mu      sync.Mutex
	known   map[pdid.Id]kept
	dropped uint64
}

type kept struct {
	r   Reach
	err error
	at  time.Time
}

// New checks what an app said and answers with its holders.
func New(c Config) (*Holders, error) {
	switch {
	case c.Conn == nil:
		return nil, errors.New("reach: Conn: which roster to ask")
	case c.As == nil:
		return nil, errors.New("reach: As: who this app is at roster, and in which tenant")
	case c.Refused < 0:
		// Refused rather than read as the default, as `standing` refuses it:
		// a wait whose sign came out wrong asks a refusing roster as fast as
		// it can.
		return nil, errors.New("reach: Refused: a wait cannot be over before it begins")
	}

	h := &Holders{
		conn:    c.Conn,
		as:      c.As,
		logger:  c.Log,
		now:     c.Now,
		refused: c.Refused,
		known:   map[pdid.Id]kept{},
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.refused == 0 {
		h.refused = standing.RefusedRetry
	}

	return h, nil
}

func (h *Holders) log(ctx context.Context) *slog.Logger {
	if h.logger != nil {
		return h.logger
	}

	return log.From(ctx)
}

// ReachOf is what roster says somebody may call now, asked every time and
// kept nowhere -- which is what [Holders.Reach] is for. [ErrNoPerson] is
// somebody roster does not have; anything else is a roster that could not be
// asked, the app's own role not naming `HolderService/Reaches` among them.
//
// It reads nothing declared personal, so asking it at every call is no row of
// roster's access record (`TestAReachIsNoReadOfSomebodysData`).
func (h *Holders) ReachOf(ctx context.Context, holder pdid.Id) (Reach, error) {
	as, err := h.as(ctx)
	if err != nil {
		return Reach{}, err
	}
	v, err := rstr.NewHolderServiceClient(h.conn).Reaches(as, rstr.HolderReachesRequest_builder{
		Ref: rstr.HolderRef_builder{Id: holder.Bytes()}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Reach{}, ErrNoPerson
		}

		return Reach{}, fmt.Errorf("roster: %w", err)
	}

	r := Reach{
		Holder:     holder,
		Methods:    v.GetMethods(),
		EverySite:  v.GetEverySite(),
		Everywhere: v.GetEverywhere(),
	}
	for _, b := range v.GetSites() {
		if k, err := pdid.From(b); err == nil {
			r.Sites = append(r.Sites, k)
		}
	}

	return r, nil
}

// Reach is what somebody may call, by what roster said of them: kept for
// [standing.HeldTtl] -- the same time a session is held to roster for, so
// that an app has one answer to *how late does a change at roster land here*
// -- and that roster could not be asked for [standing.HeldRetry]. Nobody
// there ([ErrNoPerson]) is an answer, and kept as long as one; a roster that
// could not be asked is [ErrUnreachable].
//
// A call that ends while roster is being asked has asked nothing, and nothing
// is kept of it: that is the caller's doing and not roster's ([standing]
// learned it from Loft, as two browser tests failing for no reason they
// shared). And an answer roster gave while something was dropped -- the
// stream spoke of the holder, or [Holders.Recheck] was called -- is not kept
// either, since it may be from before what dropped it; the next call asks.
func (h *Holders) Reach(ctx context.Context, holder pdid.Id) (Reach, error) {
	r, err := h.reach(ctx, holder)

	return r.clone(), err
}

func (h *Holders) reach(ctx context.Context, holder pdid.Id) (Reach, error) {
	now := h.now()
	h.mu.Lock()
	v, ok := h.known[holder]
	dropped := h.dropped
	h.mu.Unlock()
	answered := v.err == nil || errors.Is(v.err, ErrNoPerson)
	fresh := ok && (answered && now.Sub(v.at) < standing.HeldTtl || !answered && now.Sub(v.at) < standing.HeldRetry)
	if !fresh {
		r, err := h.ReachOf(ctx, holder)
		if err != nil && ctx.Err() != nil {
			return Reach{}, fmt.Errorf("the call ended before roster answered: %w", ctx.Err())
		}
		v = kept{r: r, err: err, at: now}
		h.mu.Lock()
		if h.dropped == dropped {
			h.known[holder] = v
		}
		h.mu.Unlock()
	}

	switch {
	case errors.Is(v.err, ErrNoPerson):
		return Reach{}, v.err
	case v.err != nil:
		return Reach{}, fmt.Errorf("%w: %w", ErrUnreachable, v.err)
	}

	return v.r, nil
}

// May answers whether a holder may call `method` -- a full gRPC method name,
// `/loft.AssetService/Get`, or a permission method the app declares,
// `/loft.Permission/BookForOthers` -- anywhere in their tenant: nil when roster
// says so ([Reach.Covers]), [ErrDenied] when it does not, and [ErrUnreachable]
// when roster could not be asked. Both of the last two are refusals; the
// difference is what the app tells its caller.
//
// Somebody roster has nobody by is denied ([ErrDenied], and [ErrNoPerson]
// beside it), and so is anybody suspended: roster answers that they reach
// nothing. It is asked about the holder and never the role, so a tenant may
// split or rename its roles without the app noticing.
func (h *Holders) May(ctx context.Context, holder pdid.Id, method string) error {
	r, err := h.reach(ctx, holder)
	switch {
	case errors.Is(err, ErrNoPerson):
		return fmt.Errorf("%w: %w", ErrDenied, err)
	case err != nil:
		return err
	case !r.Covers(method):
		return fmt.Errorf("%w: %s", ErrDenied, method)
	}

	return nil
}

// Recheck drops what is kept of a holder, so that the next [Holders.Reach]
// asks roster rather than believing what it said up to [standing.HeldTtl]
// ago: for a change the app made itself -- a role it wrote at the roster in
// its own process, which has no stream to say so. The stream does the same
// for what it hears.
func (h *Holders) Recheck(holder pdid.Id) {
	h.mu.Lock()
	delete(h.known, holder)
	h.dropped++
	h.mu.Unlock()
}
