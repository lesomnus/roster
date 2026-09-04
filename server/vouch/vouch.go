// Package vouch is how a secret is used without ever leaving the store.
//
// # Why it exists at all
//
// `Credential.secret` is a column, and the generated `CredentialService.Get`
// will return any column it is asked for. That is right for a row this app
// reads itself and wrong for anything on a wire. `(payday.field).secret` says
// so beside the column now -- it did not exist when this package was written --
// and the stronger statement is still made where reachability is actually
// decided: `CredentialService` is not registered, and it is closed to the
// batch. See `cmd.Grpc`. What is registered is this, and nothing here answers
// with a hash.
//
// # Why the hashing is here too
//
// `Credential` already argues that comparison belongs to whoever holds the row:
// a hash that has left the store puts timing-safe comparison, attempt counting
// and lockout in two places that will disagree. Hashing is the same argument
// one step earlier. A caller that hashes has chosen the parameters, and a
// caller that chose badly has weakened a store that cannot tell -- what arrives
// is bytes either way.
//
// # Neither half is public, and what they differ in is the frame
//
// The person signing in has no credential -- that is what they are asking for
// -- but they are not who is **calling**. The caller is custody, or a Login
// App, or an admin console, and roster is reached by nothing else. Both Rpcs
// therefore need the caller's certificate, like everything else here. See
// `cmd.public`, which says what it cost to get this wrong for an afternoon.
//
// The line runs between somebody **proving themselves** and somebody **being
// named**. `Verify`, and `Continue`, `Delegate` and `Redeem` behind it, are
// asked before anybody has been resolved to a person, so they read the server
// the wall was never installed on -- exactly as `cmd.Resolver` does,
// and for the same reason: working out who somebody is cannot require already
// knowing.
//
// `Set` is a caller changing somebody's password, which is an ordinary
// authorised write. It goes behind the wall, so an administrator of one tenant
// cannot reach into another, and that narrowing is the generated one.
package vouch

import (
	"context"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// MaxFailures is how many wrong answers in a row close an account, and LockFor
// is how long for.
//
// A lockout and not a refusal: an account that can be locked permanently by
// somebody else typing at it is a denial of service with a login form in front
// of it. What this costs an attacker is that guessing is rate-limited to
// [MaxFailures] per [LockFor], which is what makes an online guess pointless
// without making a helpdesk call the cost of being targeted.
const (
	MaxFailures = 10
	LockFor     = 15 * time.Minute
)

// Lockout is how many wrong answers in a row close an account and for how
// long: a deployment's numbers, where [MaxFailures] and [LockFor] are the ones
// it gets by saying nothing.
//
// A number rather than a switch. There is no "never lock", because an account
// that cannot be locked is one that can be guessed at forever, and a
// deployment that wants that has to write a number large enough to say so.
type Lockout struct {
	Failures int32
	For      time.Duration
}

// DefaultLockout is [MaxFailures] per [LockFor].
var DefaultLockout = Lockout{Failures: MaxFailures, For: LockFor}

// Or is this with the default filled in wherever this says nothing.
func (l Lockout) Or(d Lockout) Lockout {
	if l.Failures <= 0 {
		l.Failures = d.Failures
	}
	if l.For <= 0 {
		l.For = d.For
	}

	return l
}

// Server answers whether a secret is somebody's.
type Server struct {
	app.UnimplementedVouchServiceServer

	open    app.Server
	walled  app.Server
	keys    Keyring
	lockout Lockout
}

// Option is how a deployment says what this service is allowed to assume.
type Option func(*Server)

// WithKeys gives the service what it needs to hold a secret it must read back.
//
// A TOTP seed is not a verifier: computing the code somebody is about to type
// means holding the seed, so the row **is** the secret. Without a keyring this
// service refuses that kind outright rather than storing one in the clear --
// see `server/vouch/seed.go` for what the key buys and what it does not.
func WithKeys(v Keyring) Option { return func(s *Server) { s.keys = v } }

// WithLockout gives the server the deployment's numbers; unset is
// [DefaultLockout].
func WithLockout(v Lockout) Option { return func(s *Server) { s.lockout = v } }

// New makes the service from the two stacks it needs.
//
// `open` is the one the wall was never installed on and `walled` is the one it
// was; they are separate arguments rather than one server plus a flag, because
// which of them an RPC uses is the whole of what distinguishes these two Rpcs
// and it should not be possible to get it wrong by passing a boolean.
func New(open, walled app.Server, opts ...Option) *Server {
	s := &Server{open: open, walled: walled}
	for _, opt := range opts {
		opt(s)
	}
	s.lockout = s.lockout.Or(DefaultLockout)

	return s
}

// Verify answers whether a secret is the one held for somebody.
//
// Every refusal looks the same from outside: an unknown person, a person with
// no credential of this kind and a wrong secret are one answer, and all three
// cost the same time. The one exception is a lockout, and `VouchVerifyResponse`
// says why that trade was taken.
func (s *Server) Verify(ctx context.Context, req *app.VouchVerifyRequest) (*app.VouchVerifyResponse, error) {
	res, _, err := s.verify(ctx, req.GetWho(), req.GetKind(), req.GetName(), req.GetSecret())

	return res, err
}

// verify is the whole of the check, and it answers twice over.
//
// The response is what a caller is told. The credential beside it is what
// [Server.Delegate] needs and nothing else does -- the holder and their tenant,
// already read -- and it is nil for every answer that is not a yes, so there is
// no way to read one off a refusal.
//
// One function rather than two, because the second copy is the one that forgets
// the lockout, or the erasure check, or that every refusal has to cost the
// same.
func (s *Server) verify(ctx context.Context, who *app.VouchWho, kind, name string, secret []byte) (*app.VouchVerifyResponse, *app.Credential, error) {
	ref, err := refOf(who)
	if err != nil {
		return nil, nil, err
	}

	by, err := s.verifierOf(kind)
	if err != nil {
		// A kind this deployment cannot check at all, refused before anybody is
		// looked for: it is a fact about the deployment rather than about the
		// person, so it must not depend on whether they exist.
		//
		// Above the address lookup and not merely above the credential read,
		// which is where this sat and which made the sentence above false for
		// the one form a sign-in form actually collects. `byAddress` answers
		// NotFound for somebody who is not here, and that was turned into the
		// `no()` every stranger gets -- so an unknown kind was InvalidArgument
		// for an address that exists and OK for one that does not, and `totp`
		// on a keyring-less deployment was Unimplemented against the same
		// nothing. That is D14's question answered in a status code: exact
		// rather than statistical, needing no frame to ask, and worse than the
		// clock `delegate.go` already refuses to hand over.
		//
		// `step` has always read this way round. This is now the same shape.
		return nil, nil, err
	}

	if ref == nil {
		// Named by address, which is a lookup rather than a reference. It costs
		// a read, and a read that finds nothing costs the same as a wrong
		// password below -- which is the whole of what D14 asks of every path
		// that ends in a refusal.
		ref, err = s.byAddress(ctx, who.GetTenant(), who.GetAddress())
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return nil, nil, err
			}

			// The **kind's** burn, for the reason the one below gives: this was
			// the package argon2 burn whatever was asked for, so `totp` against
			// an address nobody has cost forty milliseconds and `totp` against
			// somebody real with no second factor cost microseconds. That is
			// the inversion `server/vouch/kind.go` was written to close,
			// reintroduced one branch earlier and pointed at the address rather
			// than at the person.
			by.Burn(secret)

			return no(), nil, nil
		}
	}

	v, err := s.credentialNamed(ctx, s.open, ref, kind, name)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, nil, err
		}

		// Nobody, or nobody with this kind of secret. Do the work anyway, so
		// that the answer takes as long as a real comparison does -- and the
		// **kind's** work, because an argon2 burn against a microsecond TOTP
		// compare inverts the difference rather than closing it. See
		// `server/vouch/kind.go`.
		by.Burn(secret)

		return no(), nil, nil
	}

	if v.GetHolder().GetDateDisabled() != nil {
		// Not to sign in, and their rows stay -- so unlike an erasure this is a
		// person who is still here and still readable. What it costs is the
		// same as everything else that is not somebody proving themselves: one
		// answer, and it takes as long as a wrong password.
		//
		// The other half of this is in `cmd.Resolver`, which is where a
		// credential already issued arrives. Neither covers the other: nothing
		// signed in reaches here, and nothing here has a frame yet.
		by.Burn(secret)

		return no(), nil, nil
	}

	if v.GetHolder().GetDateErased() != nil {
		// Somebody who is gone is nobody, and the answer is the one every
		// stranger gets -- including its cost, which is why this burns.
		//
		// `holder.proto` states this as a guarantee: an erased holder "cannot
		// be read, cannot be changed, and **cannot authenticate**". It states
		// it as a consequence of the wall -- *every read is narrowed by this
		// column* -- and that was true of the read `auth` makes and not of this
		// one. A credential is found by naming its holder, and a reference
		// composed through an edge narrowed nothing, so the row came back and
		// the password verified.
		//
		// Fixed in the generator as well (protoc-gen-orm-ent, "a reference
		// reaches only the rows that are still there"), and asserted here
		// because this is the read the guarantee is about. A sentence that is
		// true only because of how somebody else composes a predicate is a
		// sentence that stops being true without anything here changing.
		by.Burn(secret)

		return no(), nil, nil
	}

	if until := v.GetDateLocked(); until != nil && until.AsTime().After(time.Now()) {
		// Not compared, and nothing written. An attempt that was never going to
		// be answered must not move the expiry, or one continuous stream of
		// guesses keeps the account closed for as long as it lasts.
		//
		// It does not make the account un-lockable by somebody else, and
		// nothing here can: ten wrong guesses every [LockFor] will close it
		// again, which is what locking by name costs; the package comment says
		// why that is accepted.
		return app.VouchVerifyResponse_builder{LockedUntil: until}.Build(), nil, nil
	}

	ok, step, err := by.Compare(v.GetSecret(), secret, v.GetLastStep())
	if err != nil {
		// A row this cannot read is this deployment's problem and not the
		// caller's, and answering "no" would hide it behind somebody being
		// told their own password is wrong.
		return nil, nil, status.Error(codes.Internal, "the stored verifier cannot be read")
	}
	if !ok {
		res, err := s.failed(ctx, v)

		return res, nil, err
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, nil, err
	}
	tenant, err := pdid.From(v.GetHolder().GetTenant().GetId())
	if err != nil {
		return nil, nil, err
	}

	// And now the part that is not a yes or a no: what else this person could
	// prove. `answer` mints a continuation when there is something, and sets
	// `ok` when there is not -- the two are mutually exclusive, which is what
	// keeps a caller that has never heard of second factors failing closed.
	//
	// A caller with no frame -- `init`, the sandbox -- cannot be issued one and
	// does not want one, so it gets the answer this always gave.
	//
	// Which is also why [Begins] is not asked here, and it is worth saying so
	// where somebody will look for it. This branch already answers `ok` to a
	// **first** factor without asking for a second, because it has nowhere to
	// put a continuation -- so what its `ok` means is *the secret matched*
	// rather than *the sign-in is finished*, and a rule about which factor
	// finishes one has nothing to decide here. Nothing in a deployment reaches
	// it either: every caller that gets this far holds a key or a certificate
	// and so carries an actor. It is `init`, and it is the tests and the
	// example confirming a freshly enrolled seed without signing anybody in.
	issuer, err := issuerOf(ctx)
	if err != nil {
		if err := s.passed(ctx, v, step, true); err != nil {
			return nil, nil, err
		}

		return app.VouchVerifyResponse_builder{
			Ok:     true,
			Holder: holder.Bytes(),
			Tenant: tenant.Bytes(),
		}.Build(), v, nil
	}

	res, err := s.answer(ctx, v.GetHolder(), []string{kindOf(kind)}, v.GetId(), issuer)
	if err != nil {
		return nil, nil, err
	}

	if err := s.passed(ctx, v, step, res.GetOk()); err != nil {
		return nil, nil, err
	}
	if !res.GetOk() {
		// Half way. Nothing may be minted from this, which is what the nil
		// credential says to [Server.Delegate].
		return res, nil, nil
	}

	return res, v, nil
}

// Set writes a secret, hashing it here.
//
// It runs behind the wall, so who may do it is the ordinary question and gets
// the ordinary answer.

// failed counts an attempt, and closes the account when there have been enough.
//
// # What this counter is and is not for
//
// It is a compare-and-swap, so two attempts arriving together are one recorded
// failure rather than two: the loser reads the same count the winner did and
// its write is refused. That is a real under-count, and it is left this way
// because the alternative -- forcing past the version -- has the same
// under-count and gives up the locking as well.
//
// What it means is that this defends against **sustained guessing** and not
// against a burst of it. The burst is `grpcx.Limit`'s, which counts calls
// rather than rows and does not have to read anything to do it. Two mechanisms
// because they are two different attacks, and a counter that tried to be both
// would need an atomic increment this schema has no way to ask for.
func (s *Server) failed(ctx context.Context, v *app.Credential) (*app.VouchVerifyResponse, error) {
	n := v.GetFailures() + 1

	patch := app.CredentialPatchRequest_builder{
		Ref:         app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Failures:    z.Ptr(n),
		DateUpdated: v.GetDateUpdated(),
	}

	res := no()
	if n >= s.lockout.Failures {
		until := timestamppb.New(time.Now().Add(s.lockout.For))
		patch.DateLocked = until

		// The count starts again, so that the lock expiring gives a full set of
		// attempts rather than one -- otherwise every later mistake re-locks
		// immediately and the account is effectively gone.
		patch.Failures = z.Ptr(int32(0))

		res = app.VouchVerifyResponse_builder{LockedUntil: until}.Build()
	}

	if _, err := s.open.Credential().Patch(ctx, patch.Build()); err != nil {
		if !raced(err) {
			return nil, err
		}

		// Somebody else counted this instant. Answering "no" is right and
		// answering with an error would be worse than the lost count: it turns
		// two people mistyping at once into a request that failed rather than
		// one that was refused.
		return no(), nil
	}

	return res, nil
}

// raced is a write that lost a compare-and-swap.
func raced(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.FailedPrecondition, codes.NotFound:
		return true
	default:
		return false
	}
}

// passed clears what earlier mistakes left behind, and writes nothing when
// there is nothing to clear.
//
// The check is not an optimisation. Every successful sign-in would otherwise be
// a write -- a row version bumped, an audit entry, a watch event -- for a fact
// that did not change.
// `done` is whether the whole sign-in finished, which is not the same as
// whether this factor did.
//
// It decides whether the counter is cleared, and getting that wrong is a real
// hole rather than an inefficiency. The counter exists so that guessing costs
// something, and D21's fourth condition meters a **second** factor against the
// row the **first** one used -- so if passing the first factor cleared that row,
// every wrong code would be paid for by a fresh first factor that cleared the
// bill. Found by a test that guessed ten codes and watched the password stay
// open.
//
// So: somebody who has proved they can sign in is not the person the lockout
// was protecting against, and somebody who has proved one of two things has not
// proved that yet.
func (s *Server) passed(ctx context.Context, v *app.Credential, step int64, done bool) error {
	clears := done && (v.GetFailures() != 0 || v.GetDateLocked() != nil)
	moves := step > v.GetLastStep()

	// A step that has to be spent is a write whatever else is true, and it is
	// the one write on this path that is not an optimisation to skip: D20 puts
	// replay in roster's half, and the row is the only place a spent code can
	// be recorded.
	if !clears && !moves {
		return nil
	}

	patch := app.CredentialPatchRequest_builder{
		Ref:         app.CredentialRef_builder{Id: v.GetId()}.Build(),
		DateUpdated: v.GetDateUpdated(),
	}
	if clears {
		patch.Failures = z.Ptr(int32(0))
		patch.DateLockedNull = z.Ptr(true)
	}
	if moves {
		patch.LastStep = z.Ptr(step)
	}

	_, err := s.open.Credential().Patch(ctx, patch.Build())
	if raced(err) {
		// Another request got there first. For the counter that is the same
		// outcome; for the step it is **also** the same outcome, because the
		// request that won wrote the same step or a later one -- two attempts
		// with one code cannot both be the winner, which is the property this
		// is for.
		return nil
	}

	return err
}

// no is the answer to everything that is not somebody proving themselves.
func no() *app.VouchVerifyResponse { return app.VouchVerifyResponse_builder{}.Build() }

func kindOf(v string) string {
	if v == "" {
		return KindPassword
	}

	return v
}

// refOf is the holder a request named, and it refuses one that named two ways.
//
// Refused rather than resolved in some order: a caller that filled in both an
// identifier and a name has not decided which it means, and picking one for
// them makes the answer depend on a precedence rule nothing states.
func refOf(w *app.VouchWho) (*app.HolderRef, error) {
	id := w.GetId()
	tenant, alias, address := w.GetTenant(), w.GetAlias(), w.GetAddress()

	byId := len(id) > 0
	bySlug := alias != ""
	byAddress := address != ""

	switch {
	case byId && (bySlug || byAddress || tenant != ""),
		bySlug && byAddress:
		return nil, status.Error(codes.InvalidArgument,
			"who: named more than one way of finding somebody; exactly one of them is meant")

	case byAddress:
		// Not a `HolderRef` at all, which is the shape of the thing: an
		// address names an `Email` row and the person is what that row hangs
		// off. So it is looked up rather than referred to, one step earlier,
		// and this answers nil to say so.
		if tenant == "" {
			return nil, status.Error(codes.InvalidArgument,
				"who: an address is looked up within a tenant, and none was named")
		}

		return nil, nil

	case byId:
		k, err := pdid.From(id)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "who.id: %s", err)
		}

		return app.HolderRef_builder{Id: k.Bytes()}.Build(), nil

	case bySlug || tenant != "":
		if tenant == "" || alias == "" {
			return nil, status.Error(codes.InvalidArgument,
				"who: a name is a tenant and an alias, and one of them is missing")
		}

		return app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: app.TenantRef_builder{Alias: z.Ptr(tenant)}.Build(),
			}.Build(),
		}.Build(), nil

	default:
		return nil, status.Error(codes.InvalidArgument, "who: names nobody")
	}
}

// byAddress is the person an address names, within a tenant.
//
// # Why it is two reads and not one
//
// The tenant arrives as a name -- what a front door read off a hostname, or
// what a form's selector said -- and the index is over the identifier. So the
// tenant is resolved first and the address second, and neither read is one this
// service can skip.
//
// It is worth knowing what that costs on the sign-in path, and worth knowing
// what it buys: `Email` is unique on `(tenant, address)`, so the second read is
// one indexed row rather than a scan, and it can only ever answer with one
// person. F7 was open for exactly as long as that was not true.
//
// # Every refusal here is NotFound
//
// A tenant nobody serves, a domain nobody uses, an address nobody has: one
// answer, and the caller above burns an argon2 comparison over it. Told apart,
// this would answer *is there an account here* faster and more exactly than any
// timing difference could.
func (s *Server) byAddress(ctx context.Context, tenant, address string) (*app.HolderRef, error) {
	t, err := s.open.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Alias: z.Ptr(tenant)}.Build(),
		Select: app.TenantSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	v, err := s.open.Email().Get(ctx, app.EmailGetRequest_builder{
		Ref: app.EmailRef_builder{
			At: app.EmailRefByAt_builder{
				TenantId: t.GetId(),
				// The same normalisation the write is held to, from the same
				// function: a lookup that lowers against a column that does
				// not is an index comparing strings this never compares.
				Address: z.Ptr(front.Address(address)),
			}.Build(),
		}.Build(),
		Select: app.EmailSelect_builder{
			Holder: app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	h := v.GetHolder()
	if h == nil || len(h.GetId()) == 0 {
		return nil, status.Error(codes.NotFound, "no such address")
	}

	return app.HolderRef_builder{Id: h.GetId()}.Build(), nil
}
