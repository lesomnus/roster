package vouch

import (
	"context"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// Delegate is [Server.Verify], and on a yes it mints a credential for the
// person it just proved.
//
// D23 is the gap: nothing let a product app ask roster a question **as**
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
		res, v, err = s.verify(ctx, req.GetWho(), req.GetKind(), req.GetName(), req.GetSecret())
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

	return s.mint(ctx, res, who, issuer, methods, req.GetExpires())
}

// mint is the credential a finished sign-in answers with.
//
// One function rather than two, because [Server.Redeem] finishes a sign-in the
// same way and a second copy is a second set of rules about who it is for and
// how long it lives.
func (s *Server) mint(ctx context.Context, res *app.VouchVerifyResponse, who, issuer pdid.Id, methods []string, until *timestamppb.Timestamp) (*app.VouchDelegateResponse, error) {
	expires := time.Now().Add(keys.DelegateFor)
	if u := until; u != nil {
		expires = u.AsTime()
		if !expires.After(time.Now()) {
			return nil, status.Error(codes.InvalidArgument,
				"expires: a delegation cannot have already expired")
		}
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
// mTls both carry `frame.Whole`, because a header and a certificate have
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

// Accept mints for somebody a front door has already checked, and checks
// nothing itself.
//
// # What it is, in one line
//
// [Server.Delegate] without the proof. Everything after the proof is shared --
// the bound on `methods`, the issuer the delegation is tied to, the expiry, the
// row -- because those were never facts about how somebody was checked.
//
// # Why roster does not do the checking
//
// `connection.proto` decided it: *using it means doing the OIDC exchange, which
// is being the relying party and is what D19 says roster is not.* The front
// door holds the client secret and verifies the signature against an issuer it
// chose; roster holds neither and would have to acquire both. So the claim
// arrives already checked and roster resolves it to a person.
//
// D23 left this open in as many words -- *exchanging an `id_token` for one is
// the obvious route and it is not designed* -- and it is the last thing that
// entry left.
//
// # The refusals it keeps, which are the ones that are not about proof
//
// A claim that reaches nobody, a person who has been disabled, a person who has
// been erased. [Server.verify] refuses all three and this refuses them too --
// not by sharing that function, which is about a secret, but by asking the same
// questions of the row.
//
// What it deliberately does **not** do is burn. D14's equal-cost rule is about
// a caller learning something from how long a refusal took, and it applies to a
// caller **guessing**. This caller proved nothing and is guessing nothing: they
// hold a grant that says roster believes them. Making them wait would be paying
// for a property nobody can use.
func (s *Server) Accept(ctx context.Context, req *app.VouchAcceptRequest) (*app.VouchDelegateResponse, error) {
	methods := req.GetMethods()
	if len(methods) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"methods: a delegation that allows nothing opens no door")
	}
	if err := mayDelegate(ctx, methods); err != nil {
		return nil, err
	}

	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	who, err := s.claimed(ctx, req.GetClaim())
	if err != nil {
		return nil, err
	}

	holder, err := pdid.From(who.GetId())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(who.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	// The same answer a finished sign-in carries, because that is what this is:
	// a caller reading `verified.ok` reads one field whichever way the person
	// was proved.
	res := app.VouchVerifyResponse_builder{
		Ok:     true,
		Holder: holder.Bytes(),
		Tenant: tenant.Bytes(),
	}.Build()

	return s.mint(ctx, res, holder, issuer, methods, req.GetExpires())
}

// claimed is the person a claim reaches, and refuses one that reaches nobody
// who could sign in.
//
// Read through the **walled** server, unlike everything `verify` reads. That is
// not an inconsistency: `verify` is unwalled because a sign-in happens before
// anybody has been resolved, and this caller is resolved before they get here
// -- they hold a grant naming this method. So the ordinary narrowing applies,
// and a front door answering for one tenant cannot present a claim about
// another.
func (s *Server) claimed(ctx context.Context, claim *app.VouchClaim) (*app.Holder, error) {
	tenant, provider, subject := claim.GetTenant(), claim.GetProvider(), claim.GetSubject()
	switch {
	case len(tenant) == 0:
		return nil, pderr.Invalidf("claim.tenant", "which tenant this front door is answering for")
	case provider == "":
		return nil, pderr.Invalidf("claim.provider", "which provider issued this")
	case subject == "":
		return nil, pderr.Invalidf("claim.subject", "who the provider said it was")
	}

	v, err := s.walled.Identity().Get(ctx, app.IdentityGetRequest_builder{
		Ref: app.IdentityRef_builder{
			Subject: app.IdentityRefBySubject_builder{
				TenantId: tenant,
				Provider: z.Ptr(provider),
				Subject:  z.Ptr(subject),
			}.Build(),
		}.Build(),
		// The same three the credential read asks for, and for the same
		// reasons: the tenant because a delegation names one, and the two
		// stamps because a person who is suspended or gone is not somebody a
		// token gets to be.
		Select: app.IdentitySelect_builder{
			Holder: app.HolderSelect_builder{
				Tenant:       app.TenantSelect_builder{}.Build(),
				DateErased:   z.Ptr(true),
				DateDisabled: z.Ptr(true),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Nobody arrives under this claim. Refused rather than provisioned:
			// making a person by presenting a token would turn a front door
			// into something that writes rows in a tenant by receiving one, and
			// that is a different act with a different name.
			return nil, status.Error(codes.NotFound, "no such identity")
		}

		return nil, err
	}

	who := v.GetHolder()
	if who.GetDateDisabled() != nil {
		// The same refusal `verify` makes, and it has to be here too: a
		// suspension that held for a password and not for a token would be a
		// suspension that depends on which door somebody came through.
		return nil, status.Error(codes.PermissionDenied, "not to sign in")
	}
	if who.GetDateErased() != nil {
		// `holder.proto` states this as a guarantee -- an erased holder "cannot
		// authenticate" -- and a reference narrows to the rows still there, so
		// this is belt beside braces rather than the only control.
		return nil, status.Error(codes.NotFound, "no such identity")
	}

	return who, nil
}
