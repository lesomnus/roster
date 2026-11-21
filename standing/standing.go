// Package standing is the half of an app that keeps asking roster about the
// people it signed in: whether each is still in good standing, what roster has
// said of them since, and the addresses somebody checked for them.
//
// # Why it is a package
//
// `frontdoor`'s reason, D22's: the half that is the same in every app, written
// once and shipped as something apps import. An app that signs somebody in
// holds a copy of a decision -- *this is that person, and they may do these
// things* -- and roster is where it stops being true. Keeping the copy honest
// takes a cache with two lifetimes, a stream that replays nothing, and a rule
// for what the stream cannot carry, and each of the three is easy to get
// subtly wrong. Loft wrote them first, in its `internal/identity`, and found
// one of them by its defect -- a call cut short while roster was asked, kept as
// roster failing, refused every call the account made for five seconds. This
// is that code, so that the next app -- levyan, keeping billing accounts that
// roster accounts own -- starts from what Loft learned rather than finding it
// again.
//
// # What it is, and what stays the app's
//
//   - [Accounts.StandingOf] asks roster about an account, and [Accounts.Held]
//     holds something issued at a moment -- a session -- to the answer, kept
//     for [HeldTtl] and, when roster could not be asked, for [HeldRetry];
//     [Accounts.Recheck] drops what is kept, for a change the app made itself.
//   - [Accounts.Follow] holds roster's sync stream open and hands the app each
//     account it speaks of; [Accounts.Follower] is that as a loop for payday's
//     `spin.Run`.
//   - [Accounts.Reconcile] finds what the stream missed, and [Accounts.Settle]
//     asks roster again before the app acts on what it marked.
//   - [Accounts.Addresses] is an account's addresses that somebody checked.
//
// What a change **means** is the app's, and none of it is here: Loft ends
// sessions and marks memberships as leaving; levyan ends a payer's links at the
// end of their period. So is anything that writes at roster -- an app running
// roster in its own process makes and suspends its people through that roster's
// own server -- and so is who the app is at roster, which [Config.As] says.
//
// It reaches roster only over the wire, with a `grpc.ClientConnInterface` the
// app dialed: an external roster's data plane, or the one an app runs in its own
// process, served on an in-memory listener. `docs/relying-app.md` is the page.
package standing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// Standing is what roster says of an account now: when everything issued to
// them before stopped being good, when they were suspended, when they were
// erased, and when the directory they are in was closed; zero for not.
//
// It is state and not a movement -- the four facts as they are, which is what
// roster's stream sends (`proto/app/sync.proto`) -- so the same one twice
// changes nothing, and two that arrive out of order still say what is true.
type Standing struct {
	Holder, Tenant                        pdid.Id
	Invalidated, Disabled, Erased, Closed time.Time
}

// Since is the latest of the four: what was issued before it, or at all when
// they are suspended, erased or their directory is closed, is not good. Zero
// is good standing.
func (v Standing) Since() time.Time {
	t := v.Invalidated
	for _, u := range []time.Time{v.Disabled, v.Erased, v.Closed} {
		if u.After(t) {
			t = u
		}
	}

	return t
}

// Good says whether something issued at `at` is still good: a session that
// began then, say.
func (v Standing) Good(at time.Time) bool {
	return v.Disabled.IsZero() && v.Erased.IsZero() && v.Closed.IsZero() && !at.Before(v.Invalidated)
}

var (
	// ErrNoPerson is somebody roster does not have: never there, erased or
	// forgotten -- or in a directory the app's holder cannot see, which is the
	// same answer from where the app asks. It is an answer, and [Accounts.Held]
	// keeps it as long as one. [Accounts.StandingOf] answers it as a status of
	// `NotFound`, and [Accounts.Held] as [ErrRefused] with this beside it.
	ErrNoPerson = errors.New("no such person")

	// ErrRefused is something issued that roster's word does not hold any
	// more: suspended, erased, signed out everywhere since, a directory closed,
	// or nobody there at all -- which is [ErrNoPerson] beside it. It is
	// [Accounts.Held]'s, and a caller handed it is answered `Unauthenticated`
	// with this sentence alone.
	ErrRefused = errors.New("refused")

	// ErrUnreachable is a roster that could not be asked: it refused the app,
	// it was not there, it did not answer within [HeldWait], or the app's own
	// [Config.As] failed, whose error is found beside it. Every call here that
	// asks roster answers with it then -- [Accounts.StandingOf],
	// [Accounts.Held], [Accounts.Addresses], [Accounts.Reconcile],
	// [Accounts.Settle] -- and none does where the caller's own context ended
	// first, which is the caller's doing. [Accounts.Held] refuses while it
	// lasts -- what roster would have said is not known -- and an app that
	// tells its caller so tells them it is not their doing: a caller handed it
	// is answered `Unavailable` with this sentence alone, and nothing of
	// roster's or the transport's, which name where roster is dialed.
	ErrUnreachable = errors.New("roster cannot be asked just now")
)

// answer is an error this package returns: a gRPC status of the package's
// choosing, whose message is the sentence of the package's value `says` and
// nothing else, around `err`, which says everything -- roster's words and the
// transport's among them -- for the app's log, and holds the package's values,
// roster's error and the app's own for [errors.Is] and [errors.As].
//
// The returned error is itself the status, so that gRPC reads the package's
// and not the one roster answered with, which it would find through wrapping:
// an app may hand what it is answered to its own caller as it is, as payday's
// gate takes an error for the caller's answer, and the caller reads neither
// roster's refusal nor where roster is dialed. An app that wraps it before it
// returns it has chosen its own words, and gRPC then reads those.
type answer struct {
	code codes.Code
	says error
	err  error
}

func (e *answer) Error() string { return e.err.Error() }
func (e *answer) Unwrap() error { return e.err }

func (e *answer) GRPCStatus() *status.Status { return status.New(e.code, e.says.Error()) }

// nobody is roster saying it has nobody by that identifier.
var nobody error = &answer{codes.NotFound, ErrNoPerson, ErrNoPerson}

// unreachable is a roster that could not be asked, for whatever reason `err`
// says; and left is the caller's own context ending first, which is answered
// as gRPC answers that context.
func unreachable(err error) error {
	return &answer{codes.Unavailable, ErrUnreachable, fmt.Errorf("%w: %w", ErrUnreachable, err)}
}

func left(ctx context.Context) error {
	return &answer{status.FromContextError(ctx.Err()).Code(), ctx.Err(), fmt.Errorf("the call ended before roster answered: %w", ctx.Err())}
}

// worded is `err`, said in more words, as the status `was` is.
func worded(was error, err error) error {
	var a *answer
	if errors.As(was, &a) {
		return &answer{a.code, a.says, err}
	}

	return err
}

// HeldTtl is how long what roster says of an account is believed before it is
// asked again: how late a suspension, an erasure or signing out everywhere
// reaches a session already open, when the stream did not carry it first.
const HeldTtl = 30 * time.Second

// HeldRetry is how long a roster that could not be asked is not asked again,
// so that one that is down, or one that takes calls and says nothing, is not
// asked once per call.
const HeldRetry = 5 * time.Second

// HeldWait is how long roster has to answer one asking, within whatever time
// the caller has: a roster that takes the call and says nothing -- a process
// frozen, a path that drops what it carries -- is one that could not be asked
// ([ErrUnreachable]) once it is over, rather than every call waiting out its
// caller's whole deadline, or for ever where the caller has none. `reach`
// uses it as it uses the other clocks here.
const HeldWait = 5 * time.Second

// heldWait is [HeldWait], which a test makes short.
var heldWait = HeldWait

// RefusedRetry is how long a stream roster refused is left before it is asked
// for again ([Config.Refused]): long enough not to fill a log, short enough
// that the role change which fixes it is noticed without a restart.
const RefusedRetry = 10 * time.Minute

// Methods is what an app's role at roster holds for everything here: an
// account's standing (`HolderService/Get`), the stream (`SyncService/Watch`),
// and the addresses somebody checked (`EmailService/List` and `Get`).
//
// It is the part of the role this package asks for, and an app's role is that
// and whatever the app asks for itself: the `--role` of `roster app install`,
// or the role an app running roster in its own process writes for its holder.
var Methods = []string{
	rstr.HolderService_Get_FullMethodName,
	rstr.SyncService_Watch_FullMethodName,
	rstr.EmailService_List_FullMethodName,
	rstr.EmailService_Get_FullMethodName,
}

// Config is what an app says.
type Config struct {
	// Conn is roster: an external one's data plane, dialed the way the app
	// dials it, or the roster the app runs in its own process, over the
	// in-memory listener (`bufconn`) it serves it on. Both are the same calls.
	Conn grpc.ClientConnInterface

	// As is a context whose calls roster answers as the app's holder in the
	// directory its accounts are in, and every call here is made with one.
	//
	// It is the app's, because how an app is somebody at roster is its shape
	// (`docs/apps.md`): a deployment key with `roster-at` naming the
	// directory, a tenant key alone, or -- on a roster in the app's own process
	// -- payday's Plain naming the app's holder there. Nothing here holds a key.
	//
	// What it cannot answer is roster not answering: every call here answers
	// [ErrUnreachable] beside its error, which passes through as it is, so an
	// app's own kinds of error still read as themselves.
	As func(ctx context.Context) (context.Context, error)

	// Log is where the stream says what became of it. Nil is the logger the
	// context carries -- payday's, which `spin.Run` names each loop on.
	Log *slog.Logger

	// Now is the clock what is known is aged by. Nil is `time.Now`.
	Now func() time.Time

	// Refused is how long a stream roster refused is left before it is asked
	// for again. Zero is [RefusedRetry].
	Refused time.Duration
}

// Accounts is an app's accounts as roster has them: asked about when they are
// wanted, kept for as long as the answer is good for, and dropped when roster
// says something new.
type Accounts struct {
	conn    grpc.ClientConnInterface
	as      func(context.Context) (context.Context, error)
	logger  *slog.Logger
	now     func() time.Time
	refused time.Duration
	wait    time.Duration

	// What roster said of each account [Accounts.Held] was asked about, and
	// when; and how many times anything known was dropped, which is what
	// tells an answer roster gave from one it gave before a drop.
	mu      sync.Mutex
	held    map[pdid.Id]held
	dropped uint64
}

type held struct {
	st  Standing
	err error
	at  time.Time
}

// New checks what an app said and answers with its accounts.
func New(c Config) (*Accounts, error) {
	switch {
	case c.Conn == nil:
		return nil, errors.New("standing: Conn: which roster to ask")
	case c.As == nil:
		return nil, errors.New("standing: As: who this app is at roster, and in which directory")
	case c.Refused < 0:
		// Refused rather than read as the default: a duration whose sign came
		// out wrong would otherwise ask a refusing roster as fast as it could.
		return nil, errors.New("standing: Refused: a wait cannot be over before it begins")
	}

	a := &Accounts{
		conn:    c.Conn,
		as:      c.As,
		logger:  c.Log,
		now:     c.Now,
		refused: c.Refused,
		wait:    heldWait,
		held:    map[pdid.Id]held{},
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.refused == 0 {
		a.refused = RefusedRetry
	}

	return a, nil
}

func (a *Accounts) log(ctx context.Context) *slog.Logger {
	if a.logger != nil {
		return a.logger
	}

	return log.From(ctx)
}

// failed is what an asking of roster that failed answers: the caller's doing
// where the caller's own context ended, and a roster that could not be asked
// otherwise -- whatever ended it, [HeldWait] among them.
func failed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return left(ctx)
	}

	return unreachable(err)
}

// StandingOf is what roster says of an account now, asked every time and kept
// nowhere -- which is what [Accounts.Held] is for. [ErrNoPerson] is somebody
// roster does not have; [ErrUnreachable] a roster that could not be asked; and
// a caller whose own context ended is told that, and neither.
//
// It reads the four facts and nothing else. Not the person's name or anything
// else declared personal, because none of it is used, and a read that carries
// it is a read of somebody's data in roster's access record: a session held to
// roster is asked about every [HeldTtl], and every one was a row.
//
// The directory's closing is read with them, as the stream reads it. Asked as
// a holder **in** that directory, a closed one answers nothing at all -- every
// credential that resolves into it is refused -- so the app hears a roster
// that will not answer, which is no session either; asked from outside it, as
// a deployment key on its own may, it is the fourth fact.
func (a *Accounts) StandingOf(ctx context.Context, account pdid.Id) (Standing, error) {
	as, err := a.as(ctx)
	if err != nil {
		return Standing{}, failed(ctx, err)
	}
	as, cancel := context.WithTimeout(as, a.wait)
	defer cancel()
	v, err := rstr.NewHolderServiceClient(a.conn).Get(as, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
		Select: rstr.HolderSelect_builder{
			Tenant:          rstr.TenantSelect_builder{DateClosed: proto.Bool(true)}.Build(),
			DateInvalidated: proto.Bool(true),
			DateDisabled:    proto.Bool(true),
			DateErased:      proto.Bool(true),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Standing{}, nobody
		}

		return Standing{}, failed(ctx, err)
	}
	// Whose they are is on the event and not what decides anything, so a
	// tenant the answer did not carry is left zero rather than refused.
	tenant, _ := pdid.From(v.GetTenant().GetId())

	return Standing{
		Holder:      account,
		Tenant:      tenant,
		Invalidated: at(v.GetDateInvalidated()),
		Disabled:    at(v.GetDateDisabled()),
		Erased:      at(v.GetDateErased()),
		Closed:      at(v.GetTenant().GetDateClosed()),
	}, nil
}

// Held says whether something issued to an account at `began` -- a session
// that began then -- is still good, by what roster says of the account:
// [ErrRefused] when they were suspended, erased or signed out everywhere since,
// their directory closed, or nobody is there at all, with [ErrNoPerson]
// beside it then; [ErrUnreachable] when roster could not be asked, which is a
// refusal too.
//
// What it answers may be handed to the app's own caller as it is: a refusal
// is `Unauthenticated`, a roster that could not be asked `Unavailable`, and a
// call that ended what gRPC says of its context, each with the package's
// sentence and nothing else, while `Error` says everything for the app's log
// (`answer`). An app that wraps it first has chosen its own words.
//
// What roster said is believed for [HeldTtl], and that it could not be asked
// for [HeldRetry]; nobody there is an answer, and kept as long as one.
//
// A call that ends while roster is being asked -- a page left half loaded, a
// call that ran out of its own time -- is told so, and nothing is kept of it:
// that is the caller's doing and not roster's. Kept as roster's failing, it was
// a refusal of every call the account made for [HeldRetry], and two of Loft's
// browser tests that had nothing to do with each other failed on it.
func (a *Accounts) Held(ctx context.Context, account pdid.Id, began time.Time) error {
	now := a.now()
	a.mu.Lock()
	v, ok := a.held[account]
	dropped := a.dropped
	a.mu.Unlock()
	// Somebody roster does not have is an answer, and kept as long as one.
	known := v.err == nil || errors.Is(v.err, ErrNoPerson)
	fresh := ok && (known && now.Sub(v.at) < HeldTtl || !known && now.Sub(v.at) < HeldRetry)
	if !fresh {
		st, err := a.StandingOf(ctx, account)
		if err != nil && !errors.Is(err, ErrNoPerson) && !errors.Is(err, ErrUnreachable) {
			// The caller's doing, and nothing is kept of it.
			return err
		}
		v = held{st: st, err: err, at: now}
		a.mu.Lock()
		// Not kept when something was dropped while roster was asked: what
		// it answered may be from before what dropped it, and kept, it was
		// believed for HeldTtl over the stream's word. This call has it all
		// the same; the next one asks.
		if a.dropped == dropped {
			a.held[account] = v
		}
		a.mu.Unlock()
	}

	switch {
	case errors.Is(v.err, ErrNoPerson):
		return &answer{codes.Unauthenticated, ErrRefused, fmt.Errorf("%w: roster has %w", ErrRefused, v.err)}
	case v.err != nil:
		return v.err
	case !v.st.Good(began):
		return &answer{codes.Unauthenticated, ErrRefused, fmt.Errorf("%w: suspended, erased, signed out everywhere or their directory closed since", ErrRefused)}
	}

	return nil
}

// Recheck drops what is known of an account, so that the next [Accounts.Held]
// asks roster rather than believing what it said up to [HeldTtl] ago: for a
// change the app made itself -- an account it suspended at the roster in its
// own process -- to be seen at once. The stream does the same for what it
// hears.
func (a *Accounts) Recheck(account pdid.Id) {
	a.mu.Lock()
	delete(a.held, account)
	a.dropped++
	a.mu.Unlock()
}

// Addresses is an account's addresses that somebody checked -- roster holds
// them as verified, by a link it sent or by the provider the account signed in
// through -- as roster stores them, folded.
//
// An address nobody checked is not one of them, and that is the whole of the
// method. Any directory's administrator may put any address on a person of
// their own, and a provider passes such an address on (nOAuth); roster keeps
// it, with nothing claimed, and an invitation found by it would go to whoever
// typed it.
func (a *Accounts) Addresses(ctx context.Context, account pdid.Id) ([]string, error) {
	as, err := a.as(ctx)
	if err != nil {
		return nil, failed(ctx, err)
	}
	c := rstr.NewEmailServiceClient(a.conn)
	// Each asking has HeldWait of its own.
	list := func(req *rstr.EmailListRequest) (*rstr.EmailListResponse, error) {
		ctx, cancel := context.WithTimeout(as, a.wait)
		defer cancel()
		return c.List(ctx, req)
	}
	get := func(req *rstr.EmailGetRequest) (*rstr.Email, error) {
		ctx, cancel := context.WithTimeout(as, a.wait)
		defer cancel()
		return c.Get(ctx, req)
	}

	var out []string
	after := ""
	for {
		vs, err := list(rstr.EmailListRequest_builder{
			Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{
				Holder: rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil, nobody
			}

			return nil, failed(ctx, err)
		}
		for _, v := range vs.GetItems() {
			// Listed narrow and read whole, as Loft read them: the stamp is
			// read off the row by itself.
			e, err := get(rstr.EmailGetRequest_builder{
				Ref:    rstr.EmailRef_builder{Id: v.GetId()}.Build(),
				Select: rstr.EmailSelect_builder{Address: proto.Bool(true), DateVerified: proto.Bool(true)}.Build(),
			}.Build())
			if status.Code(err) == codes.NotFound {
				// Gone since it was listed: not one of theirs any more.
				continue
			}
			if err != nil {
				return nil, failed(ctx, err)
			}
			if e.HasDateVerified() {
				out = append(out, strings.ToLower(strings.TrimSpace(e.GetAddress())))
			}
		}
		if after = vs.GetNext(); after == "" || len(vs.GetItems()) == 0 {
			return out, nil
		}
	}
}

// at is a timestamp as a time, and an unset one as zero: an unset `Timestamp`
// reads back as the zero instant of 1970, which `IsZero` is false for.
func at(v *timestamppb.Timestamp) time.Time {
	if v == nil {
		return time.Time{}
	}

	return v.AsTime()
}
