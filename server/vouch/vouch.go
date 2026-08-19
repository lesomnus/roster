// Package vouch is how a secret is used without ever leaving the store.
//
// # Why it exists at all
//
// `Credential.secret` is a column, and the generated `CredentialService.Get`
// will return any column it is asked for. That is right for a row this app
// reads itself and wrong for anything on a wire, and payday has no way to say
// "this field is written and never read" -- `payday.proto` extends
// `MessageOptions` only, so there is no field-level option to put it in.
//
// So it is said where reachability is actually decided: `CredentialService` is
// not registered, and it is closed to the batch. See `cmd.Grpc` and PLAN.md D11.
// What is registered is this, and nothing here answers with a hash.
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
// App, or an admin console, and roster is reached by nothing else. Both RPCs
// therefore need the caller's certificate, like everything else here. See
// `cmd.public`, which says what it cost to get this wrong for an afternoon.
//
// `Verify` is asked before anybody has been resolved to a person, so it reads
// the server the wall was never installed on -- exactly as `cmd.Resolver` does,
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

// Server answers whether a secret is somebody's.
type Server struct {
	app.UnimplementedVouchServiceServer

	open   app.Server
	walled app.Server
}

// New makes the service from the two stacks it needs.
//
// `open` is the one the wall was never installed on and `walled` is the one it
// was; they are separate arguments rather than one server plus a flag, because
// which of them an RPC uses is the whole of what distinguishes these two RPCs
// and it should not be possible to get it wrong by passing a boolean.
func New(open, walled app.Server) *Server {
	return &Server{open: open, walled: walled}
}

// Verify answers whether a secret is the one held for somebody.
//
// Every refusal looks the same from outside: an unknown person, a person with
// no credential of this kind and a wrong secret are one answer, and all three
// cost the same time. The one exception is a lockout, and `VouchVerifyResponse`
// says why that trade was taken.
func (s *Server) Verify(ctx context.Context, req *app.VouchVerifyRequest) (*app.VouchVerifyResponse, error) {
	ref, err := refOf(req.GetWho())
	if err != nil {
		return nil, err
	}

	secret := req.GetSecret()
	v, err := s.credential(ctx, s.open, ref, req.GetKind())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		// Nobody, or nobody with this kind of secret. Do the work anyway, so
		// that the answer takes as long as a wrong password does.
		Burn(secret)

		return no(), nil
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
		Burn(secret)

		return no(), nil
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
		Burn(secret)

		return no(), nil
	}

	if until := v.GetDateLocked(); until != nil && until.AsTime().After(time.Now()) {
		// Not compared, and nothing written. An attempt that was never going to
		// be answered must not move the expiry, or one continuous stream of
		// guesses keeps the account closed for as long as it lasts.
		//
		// It does not make the account un-lockable by somebody else, and
		// nothing here can: ten wrong guesses every [LockFor] will close it
		// again, which is what locking by name costs. See PLAN.md, D14.
		return app.VouchVerifyResponse_builder{LockedUntil: until}.Build(), nil
	}

	ok, err := Compare(v.GetSecret(), secret)
	if err != nil {
		// A row this cannot read is this deployment's problem and not the
		// caller's, and answering "no" would hide it behind somebody being
		// told their own password is wrong.
		return nil, status.Error(codes.Internal, "the stored verifier cannot be read")
	}
	if !ok {
		return s.failed(ctx, v)
	}

	if err := s.passed(ctx, v); err != nil {
		return nil, err
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(v.GetHolder().GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	return app.VouchVerifyResponse_builder{
		Ok:     true,
		Holder: holder.Bytes(),
		Tenant: tenant.Bytes(),
	}.Build(), nil
}

// Set writes a secret, hashing it here.
//
// It runs behind the wall, so who may do it is the ordinary question and gets
// the ordinary answer.
func (s *Server) Set(ctx context.Context, req *app.VouchSetRequest) (*app.VouchSetResponse, error) {
	ref, err := refOf(req.GetWho())
	if err != nil {
		return nil, err
	}
	if len(req.GetSecret()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "secret: must not be empty")
	}

	kind := kindOf(req.GetKind())
	sum, err := Hash(req.GetSecret())
	if err != nil {
		return nil, status.Error(codes.Internal, "the secret cannot be stored just now")
	}

	// Read the person before writing their secret, because the write below may
	// be an Add and an Add resolves its edge by the reference alone -- a
	// reference carrying a key is answered without a query at all, so nothing
	// on that path would notice that they are gone. See [Server.Verify] for the
	// half of this that matters more.
	if _, err := s.walled.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build()); err != nil {
		return nil, err
	}

	now := timestamppb.Now()

	v, err := s.credential(ctx, s.walled, ref, kind)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		_, err = s.walled.Credential().Add(ctx, app.CredentialAddRequest_builder{
			Holder: ref,
			Kind:   kind,
			Secret: sum,
		}.Build())
		if err != nil {
			return nil, err
		}

		return app.VouchSetResponse_builder{}.Build(), nil
	}

	// A new secret clears whatever the old one had accumulated. Somebody who
	// has just proved they can change it is not the person the lockout was
	// protecting against.
	// The version is a precondition here and a conflict is reported, unlike the
	// counter above: two people setting a password at once is one of them
	// finding out, not one of them silently losing.
	_, err = s.walled.Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Secret:         sum,
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateRotated:    now,
		DateUpdated:    v.GetDateUpdated(),
	}.Build())
	if err != nil {
		return nil, err
	}

	return app.VouchSetResponse_builder{}.Build(), nil
}

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
	if n >= MaxFailures {
		until := timestamppb.New(time.Now().Add(LockFor))
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
func (s *Server) passed(ctx context.Context, v *app.Credential) error {
	if v.GetFailures() == 0 && v.GetDateLocked() == nil {
		return nil
	}

	_, err := s.open.Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateUpdated:    v.GetDateUpdated(),
	}.Build())
	if raced(err) {
		// Another request cleared it, which is the same outcome.
		return nil
	}

	return err
}

// credential reads the row, and asks for the columns this needs by name.
//
// `All` is deliberately not used. What comes back includes the hash, and a
// select that names its fields is one a reader can check against what the
// function does with them.
func (s *Server) credential(ctx context.Context, from app.Server, ref *app.HolderRef, kind string) (*app.Credential, error) {
	return from.Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{
				Holder: ref,
				Kind:   z.Ptr(kindOf(kind)),
			}.Build(),
		}.Build(),
		Select: app.CredentialSelect_builder{
			Secret:     z.Ptr(true),
			Failures:   z.Ptr(true),
			DateLocked: z.Ptr(true),

			// The version, because every write below is a compare-and-swap and
			// payday refuses one that did not say what it expected to find.
			DateUpdated: z.Ptr(true),

			Holder: app.HolderSelect_builder{
				Tenant: app.TenantSelect_builder{}.Build(),

				// Whether they are still here, which this has to ask for
				// rather than rely on. See [Server.Verify].
				DateErased: z.Ptr(true),

				// And whether they are allowed to be, which nothing generated
				// reads at all -- a new column is inert until somebody writes
				// the refusal.
				DateDisabled: z.Ptr(true),
			}.Build(),
		}.Build(),
	}.Build())
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
	tenant, alias := w.GetTenant(), w.GetAlias()

	byId := len(id) > 0
	bySlug := tenant != "" || alias != ""

	switch {
	case byId && bySlug:
		return nil, status.Error(codes.InvalidArgument,
			"who: named both an identifier and a name; exactly one of them is meant")

	case byId:
		k, err := pdid.From(id)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "who.id: %s", err)
		}

		return app.HolderRef_builder{Id: k.Bytes()}.Build(), nil

	case bySlug:
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
