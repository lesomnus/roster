package vouch

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// Delegate is [Server.Verify], and on a yes it mints a credential for the
// person it just proved.
//
// PLAN.md D23 is the gap: nothing let a product app ask roster a question **as**
// somebody it had signed in, and the two obvious ways -- the app's own key,
// which sees every tenant, and the app filtering its own reads, which is what
// D17 called *the kind of thing that leaks by being forgotten* -- are both
// refused there.
//
// # The order of what happens here is the design
//
// What the caller may mint is checked **first**, before the person is even
// looked up. Run after the comparison, a caller that asked for more than it
// holds would get one answer for a wrong password and another for a right one:
// D14 spent real effort making every refusal cost the same, and a status code
// that differs is worse than a clock that does, because it is exact rather than
// statistical.
//
// So: the request is refused or it is not, identically for a stranger and for
// somebody real; then the secret is compared exactly as `Verify` compares it,
// through the same function, with the same lockout count; then, and only on a
// yes, a row is written.
//
// # It writes on the sign-in path, and that is a cost
//
// `passed` goes out of its way not to write on a successful verify -- *every
// successful sign-in would otherwise be a write, for a fact that did not
// change*. This puts one back: a row, a version, an audit entry and a watch
// event per delegated sign-in. It is the price of the credential existing at
// all, it is why `keys.Sweep` is not optional, and it is a reason to call
// `Verify` instead where no delegation is wanted.
func (s *Server) Delegate(ctx context.Context, req *app.VouchDelegateRequest) (*app.VouchDelegateResponse, error) {
	// The shape of the request first, because it is not about anybody: a
	// caller that named two ways of proving somebody has not decided, and
	// telling them so cannot say anything about who exists.
	if req.GetContinuation() != "" && req.GetWho() != nil {
		return nil, status.Error(codes.InvalidArgument,
			"named both somebody and a continuation; exactly one of them is meant")
	}

	methods := req.GetMethods()
	if len(methods) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"methods: a delegation that allows nothing opens no door")
	}
	if err := mayDelegate(ctx, methods); err != nil {
		return nil, err
	}

	expires := time.Now().Add(keys.DelegateFor)
	if u := req.GetExpires(); u != nil {
		expires = u.AsTime()
		if !expires.After(time.Now()) {
			return nil, status.Error(codes.InvalidArgument,
				"expires: a delegation cannot have already expired")
		}
	}

	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	// Either way of proving somebody, and exactly one of them.
	//
	// The continuation form is what makes a two-step sign-in end in a token
	// without a second argon2 comparison and without minting for somebody
	// nobody just proved -- the two things this method's own comment refuses.
	// It is safe here for the reason it is not safe in general: a continuation
	// is single-use, alive for minutes, and belongs to the caller spending it.
	var (
		res *app.VouchVerifyResponse
		v   *app.Credential
	)
	switch handle := req.GetContinuation(); {
	case handle != "":
		res, v, err = s.step(ctx, handle, req.GetKind(), req.GetName(), req.GetSecret())

	default:
		res, v, err = s.verify(ctx, req.GetWho(), req.GetKind(), req.GetSecret())
	}
	if err != nil {
		return nil, err
	}
	if v == nil {
		// Every refusal, unchanged -- and every answer that is only **half** a
		// sign-in, which carries its continuation and no token. A caller
		// reading `verified.ok` reads the same field either way, and one that
		// gates on the token gates on the thing that is actually a credential.
		return app.VouchDelegateResponse_builder{Verified: res}.Build(), nil
	}

	who, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}

	token, row, err := keys.Delegate(ctx, s.open, keys.Delegated{
		Holder:  who,
		Issuer:  issuer.Bytes(),
		Methods: methods,
		For:     time.Until(expires),
	})
	if err != nil {
		return nil, err
	}

	return app.VouchDelegateResponse_builder{
		Verified: res,

		// The row's expiry and not the one computed above. They differ by the
		// microseconds it took to write, and answering with the number that was
		// actually stored is the difference between a caller knowing when this
		// stops working and a caller knowing what somebody meant.
		Token:   token,
		Expires: row.GetDateExpires(),
	}.Build(), nil
}

// Revoke ends a delegation before its expiry.
//
// D23 says *revoking it is a delete*, and for a while nothing could: the
// generated service is unregistered and closed, so signing out of an app left
// that app holding a credential which went on working. This is the delete.
//
// # Everything answers the same
//
// A token that was never here, one that has expired, one somebody else was
// issued: all of them succeed and remove nothing. That is `Erase`'s rule --
// *erasing what is not there succeeds*, and out of reach is not there -- and it
// is also what keeps this from telling whoever holds a found string whether it
// is real, whose it is, or whether it is still alive.
//
// The consequence worth knowing: a caller cannot learn from the answer that it
// revoked anything. What it can rely on is that a delegation **it** was issued
// is gone afterwards.
func (s *Server) Revoke(ctx context.Context, req *app.VouchRevokeRequest) (*app.VouchRevokeResponse, error) {
	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	if err := keys.Undelegate(ctx, s.open, req.GetToken(), issuer); err != nil {
		return nil, err
	}

	return app.VouchRevokeResponse_builder{}.Build(), nil
}

// mayDelegate refuses a caller minting wider than itself.
//
// The escalation is real and is not about the person: a delegation is bounded
// by what its holder may do, so it can never reach past them -- but an app
// allowed only to check passwords could mint one carrying
// `/roster.HolderService/Erase` and then use it through somebody who may erase.
// It would have gained, through a person, a method its own credential does not
// carry.
//
// # What it is not
//
// It is **not** `server/core/escalate.go`'s rule, and the difference matters
// enough to say. `mayGrant` reads what the caller holds through *bindings*,
// because what it is guarding is a row that hands permissions to somebody else.
// Here the caller is a machine with a key, its bindings are in another database
// entirely, and what bounds it is the attenuation on its own credential. So
// this reads `f.Grant`, which is the key's `methods` column.
//
// # How hard it bites
//
// Exactly as hard as the caller's own credential is narrow. `auth.Plain` and
// mTLS both carry `frame.Whole`, because a header and a certificate have
// nowhere to put an attenuation -- so in those deployments this refuses
// nothing, and correctly: there is nothing there to be wider than. It is a
// rule about keys, and `roster key add --allow` is where a deployment writes
// it down.
//
// A request with no frame at all is the deployment's own work in this process,
// which is `mayGrant`'s reading of the same case and the same answer.
func mayDelegate(ctx context.Context, methods []string) error {
	f, ok := frame.From(ctx)
	if !ok {
		return nil
	}

	for _, m := range methods {
		if !f.Grant.Allows(m) {
			return status.Errorf(codes.PermissionDenied,
				"methods: you may not call %s, so you may not hand it out", m)
		}
	}

	return nil
}

// issuerOf is who the delegation is being minted for, or revoked by.
//
// The frame's actor, which for a deployment key is the **key row** rather than
// the service holding it -- `cmd.Resolver` reads one with an empty select and
// never learns the holder. D25 took that deliberately: rotating an app's key
// invalidates the delegations it issued, and a caller whose credential has been
// replaced is not obviously the same caller.
//
// A call with no frame cannot mint one. `keys.Delegate` refuses an empty issuer
// for a reason that applies here first: two empty slices compare equal, so a
// delegation bound to nobody would match a caller who could not be named. In
// this process there is `keys.Delegate` itself, which is a Go call and says who
// it is for.
func issuerOf(ctx context.Context) (pdid.Id, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor == pdid.Nil {
		return pdid.Nil, status.Error(codes.Unauthenticated,
			"a delegation is minted for whoever asked, and nothing here said who that is")
	}

	return f.Actor, nil
}
