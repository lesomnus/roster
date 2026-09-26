package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/server/vouch"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// An address is a way in, and it is stored as it will be looked up.
//
// Two rules, and `Email` had neither because it had no layer at all. Both are
// rules this app states elsewhere and had not applied here.
//
// # An address is a way to sign in
//
// Not by itself -- nothing resolves anybody by address alone. But `Vouch.Link`
// mints a way in **at an address**, and `Vouch.Verify` and `Vouch.Reset` take
// one in place of a name. So a row on somebody's account naming a mailbox
// somebody else reads is that person's account, one mail away:
//
//	Alice may call Email.Add, and nothing else.
//	Alice adds a mailbox of hers to the administrator's row.
//	Alice asks for a link at that address and clicks it.
//
// That is [Core.mayWriteAWayIn], which is [Core.mayReach] said about the other
// half of a sign-in. Her own address is untouched, because that rule passes for
// the caller's own row.
//
// # And it is stored as it is compared
//
// `coreHost` says this about a hostname and gives the reason at length; the
// same argument holds here and the consequence is worse. The uniqueness that
// makes an address name one person within a tenant is an index on
// `(tenant_id, address)`, and `vouch.byAddress` lowers and trims what it is
// handed. A row written as a provider sent it -- `Someone@Contoso.example` -- is a
// row the lookup never reaches, and the lowered spelling of it is a row the
// index thinks is a different address.
//
// So the index was decorative: two people in one tenant could hold one address
// between them, spelled two ways, and the one holding the lowered spelling is
// the one an address resolves to. Refused rather than fixed up, for the reason
// `coreHost` gives -- a caller whose row comes back different from what it
// wrote is a console that cannot find the address somebody just typed --
// and `front.Address` is exported so there is nothing to reimplement.
type coreEmail struct {
	Core
	app.EmailServiceServer
}

func (s Core) Email() app.EmailServiceServer { return coreEmail{s, s.Next().Email()} }

func (s coreEmail) Add(ctx context.Context, req *app.EmailAddRequest) (*app.Email, error) {
	if err := normalised("address", req.GetAddress(), front.Address); err != nil {
		return nil, err
	}
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
		return nil, err
	}

	return s.EmailServiceServer.Add(ctx, req)
}

// Patch is the normalisation, and **not** the rule about whose row it is.
//
// Which is a gap on purpose rather than the other rule being forgotten, and it
// is only safe because of something one file away: `/Patch` is closed at the
// transport by `grpcx.GeneralWrite` and roster sets no `AllowGeneralWrites`, so
// nothing a caller reaches gets here. What does is a batch and the servers' own
// writes, neither of which is somebody naming a row.
//
// If that changes -- a deployment with a reason to open general writes -- this
// wants [Core.mayWriteAWayIn] beside the normalisation, because the holder
// being immutable is not the protection it looks like: a patch cannot move an
// address onto somebody else's row and it can change what the address on
// **their** row is, which reaches the same place. The address is theirs; the
// mailbox would be mine.
//
// The normalisation is here anyway, because it is about the value rather than
// about the caller and those other roads write values too.
func (s coreEmail) Patch(ctx context.Context, req *app.EmailPatchRequest) (*app.Email, error) {
	if req.HasAddress() {
		if err := normalised("address", req.GetAddress(), front.Address); err != nil {
			return nil, err
		}
	}

	return s.EmailServiceServer.Patch(ctx, req)
}

// Verify mints a link that proves this address, and answers with it once.
//
// Through the wall first (`Get`), so a caller who cannot see the row cannot
// mint for it; then `mayReach` on the row's holder, because an address is where
// a recovery link goes and proving one is one step from holding one. The link
// goes in the same table `Vouch.Link` writes, naming its `email`, which is what
// tells `Confirm` this is a verification and not a way in. See
// `email_svc.ext.proto`.
//
// # And the other shape, which is a claim rather than a proof of your own
//
// `holder` set is somebody claiming an address **another row holds unproved**.
// The rules swap: `mayReach` on the row's holder is not asked -- it guards a row
// somebody proved and this is one nobody did -- and `mayWriteAWayIn` is asked
// about the **claimant** instead, which is the rule `Email.Add` already meets on
// the other road to the same row.
//
// What makes it safe is not a permission. It is that the link goes to the
// **mailbox**, and roster does not deliver: the caller here is a front door,
// which reads the address off the row and puts the token in a message to it
// (`account/account.go`). Whoever reads that mailbox is who ends up holding the
// address, which is the whole of what a proof of an address can mean.
//
// See [coreEmail.Confirm] for where the address actually moves, and
// `email_svc.ext.proto` for why a **proved** address does not.
func (s coreEmail) Verify(ctx context.Context, req *app.EmailVerifyRequest) (*app.EmailVerifyResponse, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return nil, status.Error(codes.Unauthenticated, "a link is minted by whoever asked, and nothing here said who that is")
	}

	v, err := s.EmailServiceServer.Get(ctx, app.EmailGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.EmailSelect_builder{
			// The stamp, which decides which of the two shapes below this is.
			DateVerified: z.Ptr(true),
			Holder:       app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// Whose the link is, which is whose the address becomes. The row's own
	// holder unless a claimant was named.
	whose := v.GetHolder().GetId()

	if claim := req.GetHolder(); claim != nil {
		if v.GetDateVerified() != nil {
			// A proved address does not move. Said rather than made one answer
			// with the rest, because the caller is a front door and this is a
			// fact about the row it is looking at: a page that drew *claim this
			// address* over somebody's confirmed one should stop drawing it.
			return nil, status.Error(codes.FailedPrecondition,
				"ref: somebody has proved that address, and a proved address does not change hands here")
		}
		if err := s.mayWriteAWayIn(ctx, "holder", claim); err != nil {
			return nil, err
		}

		k, err := s.holderOf(ctx, claim)
		if err != nil {
			return nil, err
		}
		whose = k.Bytes()
	} else if holder, err := pdid.From(v.GetHolder().GetId()); err != nil {
		return nil, err
	} else if err := s.mayReach(ctx, "ref", holder); err != nil {
		return nil, err
	}

	expires := time.Now().Add(vouch.LinkFor)
	if u := req.GetExpires(); u != nil {
		if at := u.AsTime(); at.After(time.Now()) && at.Before(expires) {
			expires = at
		}
	}
	token, sum, err := vouch.MintLink()
	if err != nil {
		return nil, err
	}

	if _, err := s.Next().Link().Add(ctx, app.LinkAddRequest_builder{
		// The **claimant**, which for the ordinary shape is the row's own holder
		// and so is what this always wrote. `Confirm` reads it to decide whether
		// there is anything to move, and `Vouch.Redeem` cannot read it at all --
		// a link naming an `email` is refused there, which is the discriminator
		// both doors are held to (`email_svc.ext.proto`).
		Holder:      app.HolderRef_builder{Id: whose}.Build(),
		Email:       app.EmailRef_builder{Id: v.GetId()}.Build(),
		Secret:      sum,
		Issuer:      f.Actor.Bytes(),
		DateExpires: timestamppb.New(expires),
	}.Build()); err != nil {
		return nil, err
	}

	return app.EmailVerifyResponse_builder{Token: token, Expires: timestamppb.New(expires)}.Build(), nil
}

// Attest writes an address a provider vouched for, and stamps it when that
// provider said it had checked.
//
// `email_svc.ext.proto` argues for it. What is here is the two rules it meets
// and one it is held to:
//
//   - **`mayWriteAWayIn`**, exactly as `Add` is. An address is a way into an
//     account and this writes one; that it arrived in somebody's token changes
//     nothing about whose row it lands on.
//   - **A voucher is required.** An attestation with no identity behind it is
//     an address somebody typed, and that has `Add` and a link.
//   - **The stamp goes through `Next().Patch`**, which is `Confirm`'s road and
//     for `Confirm`'s reason: `date_verified` is refused to a request, so the
//     one way to it is a server writing it below the gate.
//
// Idempotent, because a person signs in more than once: an address already on
// that row is stamped rather than refused, so the second sign-in after a
// directory started saying `email_verified` writes what the first could not.
func (s coreEmail) Attest(ctx context.Context, req *app.EmailAttestRequest) (*app.Email, error) {
	if err := normalised("address", req.GetAddress(), front.Address); err != nil {
		return nil, err
	}
	if req.GetVouchedBy() == nil {
		return nil, status.Error(codes.InvalidArgument, "vouched_by: an attestation says whose word it was")
	}
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
		return nil, err
	}

	v, err := s.EmailServiceServer.Add(ctx, app.EmailAddRequest_builder{
		Holder:    req.GetHolder(),
		Address:   req.GetAddress(),
		VouchedBy: req.GetVouchedBy(),
	}.Build())
	switch {
	case err == nil:
	case status.Code(err) == codes.AlreadyExists:
		// Theirs already, or somebody else's -- and the read below is narrowed
		// by the wall, so an address held in another tenant is not found here
		// and this answers what a stranger's does.
		v, err = s.EmailServiceServer.Get(ctx, app.EmailGetRequest_builder{
			Ref: app.EmailRef_builder{
				Address: app.EmailRefByAddress_builder{
					Holder:  req.GetHolder(),
					Address: z.Ptr(front.Address(req.GetAddress())),
				}.Build(),
			}.Build(),
			Select: app.EmailSelect_builder{
				DateUpdated:  z.Ptr(true),
				DateVerified: z.Ptr(true),
			}.Build(),
		}.Build())
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return nil, err
			}

			// Looked up on **this holder's** row, so a NotFound here is the
			// address being somebody else's. Which used to end here, and that
			// refusal is what made an address squattable: a row nobody proved
			// held the address against the index, so the person a directory
			// vouches for could not be given it by any road.
			//
			// A directory saying `email_verified` is a proof, so it takes an
			// address **nobody proved** the way a link does. Only that, and only
			// on `verified`: a provider's word is its own word, where a link is a
			// round trip to the mailbox -- so this is the narrower of the two
			// roads to the same move and stays that way.
			if !req.GetVerified() {
				return nil, err
			}

			return s.attested(ctx, req)
		}

	default:
		return nil, err
	}

	if !req.GetVerified() || v.GetDateVerified() != nil {
		// Nothing the provider said to write down, or it is already written.
		return v, nil
	}

	return s.Next().Email().Patch(ctx, app.EmailPatchRequest_builder{
		Ref:          app.EmailRef_builder{Id: v.GetId()}.Build(),
		DateVerified: timestamppb.Now(),
		DateUpdated:  v.GetDateUpdated(),
	}.Build())
}

// Confirm spends a verification link and stamps its address.
//
// Every refusal is one `NotFound`, for `Vouch.Redeem`'s reason: a token never
// minted, one spent, one expired, one another caller minted, and one that is a
// recovery link (no `email`) must be told apart by nobody holding a found
// string. What is different from `Redeem` is the whole point -- nothing is
// minted. `date_verified` is written here, through the generated `Patch`, which
// is the one road to it: the gate refuses a request that asserts it.
//
// # And it is where an address changes hands
//
// A link whose `holder` is not the row's holder is a **claim**, minted by
// [coreEmail.Verify] for somebody who does not hold the address. Spending one is
// three writes in one transaction -- erase the row that held it, write the
// address on the claimant, stamp it -- which is `coreHost.proved`'s shape for
// `coreHost.proved`'s reason: an address that is two rows for a moment is an
// address the unique index refuses, and one that is no rows for a moment is an
// address somebody else can take in between.
//
// What may be taken is an address nobody proved. `Verify` is where that is
// checked, because that is where there is somebody to tell -- and it is checked
// again here rather than trusted, for the reason every hop in this app rechecks:
// minutes pass between the two, and what the row said then is not what it says
// now. Somebody who proved the address in the meantime keeps it.
func (s coreEmail) Confirm(ctx context.Context, req *app.EmailConfirmRequest) (*app.EmailConfirmResponse, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	}
	no := status.Error(codes.NotFound, "no such link")

	token := req.GetToken()
	if !strings.HasPrefix(token, vouch.PrefixLink) {
		return nil, no
	}
	sum := sha256.Sum256([]byte(token))

	l, err := s.Next().Link().Get(ctx, app.LinkGetRequest_builder{
		Ref: app.LinkRef_builder{Secret: sum[:]}.Build(),
		Select: app.LinkSelect_builder{
			Secret:      z.Ptr(true),
			Issuer:      z.Ptr(true),
			DateExpires: z.Ptr(true),
			Email:       app.EmailSelect_builder{}.Build(),
			// Whose claim it is, which for an ordinary verification is the row's
			// own holder and for a claim is somebody else. Read here rather than
			// assumed, which is the difference between stamping a row and moving
			// an address.
			Holder: app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, no
		}

		return nil, err
	}
	switch {
	case subtle.ConstantTimeCompare(l.GetSecret(), sum[:]) != 1,
		l.GetDateExpires() == nil || !time.Now().Before(l.GetDateExpires().AsTime()),
		subtle.ConstantTimeCompare(l.GetIssuer(), f.Actor.Bytes()) != 1,
		len(l.GetEmail().GetId()) == 0:
		return nil, no
	}

	// Spent first, so two confirmations of one link are one: the second finds
	// nothing to erase and is told nothing.
	spent, err := s.Next().Link().Erase(ctx, app.LinkRef_builder{Id: l.GetId()}.Build())
	if err != nil {
		return nil, err
	}
	if !spent.GetErased() {
		return nil, no
	}

	ref := app.EmailRef_builder{Id: l.GetEmail().GetId()}.Build()
	e, err := s.Next().Email().Get(ctx, app.EmailGetRequest_builder{
		Ref: ref,
		Select: app.EmailSelect_builder{
			Address:      z.Ptr(true),
			DateVerified: z.Ptr(true),
			DateUpdated:  z.Ptr(true),
			Holder:       app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// A claim, which is a link for somebody who does not hold the row.
	if !bytes.Equal(l.GetHolder().GetId(), e.GetHolder().GetId()) {
		return s.moved(ctx, l, e)
	}

	out, err := s.Next().Email().Patch(ctx, app.EmailPatchRequest_builder{
		Ref:          ref,
		DateVerified: timestamppb.Now(),
		DateUpdated:  e.GetDateUpdated(),
	}.Build())
	if err != nil {
		return nil, err
	}

	return app.EmailConfirmResponse_builder{Email: out}.Build(), nil
}

// moved is a claim being spent: the address leaves the row that held it and
// arrives on the claimant's, proved.
//
// `coreHost.proved`'s three writes, in one transaction and for the same reason --
// `Email` is unique on `(tenant, address)` among the living, so the erase has to
// land before the add or the index refuses it, and neither may be visible alone.
// A crash between them would otherwise leave an address that is nobody's with a
// link already spent.
//
// # What it checks again, having been checked at `Verify`
//
// That the row is still unproved. Fifteen minutes pass between minting a link and
// clicking it (`vouch.LinkFor`), and what the row said then is not what it says
// now: somebody who proved the address in that window keeps it, and the claim is
// the thing that expires. Answered as the refusal `Verify` would have made, rather
// than `NotFound`, because the caller is a front door holding a spent link and the
// honest thing to tell it is why.
//
// And that both rows are one tenant's, which the wall has already decided --
// `Confirm` reads both through `Next()`, which is through it. Said here because it
// is the one property that makes the write narrow: an address crosses rows and
// never a tenant.
func (s coreEmail) moved(ctx context.Context, l *app.Link, e *app.Email) (*app.EmailConfirmResponse, error) {
	if e.GetDateVerified() != nil {
		return nil, status.Error(codes.FailedPrecondition,
			"somebody proved that address while this link was out, and a proved address does not change hands here")
	}
	if s.drv == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this server cannot open a transaction, so it will not move an address in three writes")
	}

	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// This layer again over a rebound one below it, as `coreHost.proved` does:
	// the writes go to the server **below** this one, so `Add` does not arrive
	// back here and ask about a claim that has just been spent.
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	// Let go first. A soft erase, which is what every other row here does, and it
	// is enough: the unique index covers the living only, so an erased row holds
	// no address.
	gone, err := next.Email().Erase(ctx, app.EmailRef_builder{Id: e.GetId()}.Build())
	if err != nil {
		return nil, err
	}
	if !gone.GetErased() {
		// Somebody else took it out from under this call. Which is not a failure
		// worth a special answer: the address is nobody's now, and the claimant
		// may write it with `Email.Add` like anybody else.
		return nil, status.Error(codes.FailedPrecondition,
			"that address is no longer held by the row this link was minted against")
	}

	v, err := next.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: l.GetHolder().GetId()}.Build(),
		Address: e.GetAddress(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// And the stamp, which is the whole point of having proved it. Through
	// `Patch` because `date_verified` is `stamped:` and refused to an `Add`.
	out, err := next.Email().Patch(ctx, app.EmailPatchRequest_builder{
		Ref:          app.EmailRef_builder{Id: v.GetId()}.Build(),
		DateVerified: timestamppb.Now(),
		DateUpdated:  v.GetDateUpdated(),
	}.Build())
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return app.EmailConfirmResponse_builder{Email: out}.Build(), nil
}

// attested is a directory's word taking an address nobody proved.
//
// [coreEmail.moved]'s three writes with the other kind of evidence in front of
// them, and a separate function rather than a branch because what each one has
// already established is different: `moved` holds a spent link and knows the row
// from it, and this holds a `verified` attestation and has to go and find the row.
//
// The rules it meets are `Attest`'s own, already run by the time this is reached:
// the address is normalised, a voucher is required, and `mayWriteAWayIn` has
// passed for the holder it is writing onto. What is left is the one this adds --
// the incumbent must be **unproved** -- and it is read here rather than taken on
// trust, because the caller named an address and not a row.
func (s coreEmail) attested(ctx context.Context, req *app.EmailAttestRequest) (*app.Email, error) {
	if s.drv == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this server cannot open a transaction, so it will not move an address in three writes")
	}

	address := front.Address(req.GetAddress())

	// Through the wall, by address alone -- which is the read `Email`'s `at` index
	// exists for and the one `Attest` above could not make, because that one is
	// narrowed to a holder. So this finds the row whoever holds it, within the
	// caller's tenant and no further.
	tenant, err := s.tenantOfHolder(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}

	held, err := s.EmailServiceServer.Get(ctx, app.EmailGetRequest_builder{
		Ref: app.EmailRef_builder{
			At: app.EmailRefByAt_builder{TenantId: tenant.Bytes(), Address: z.Ptr(address)}.Build(),
		}.Build(),
		Select: app.EmailSelect_builder{DateVerified: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if held.GetDateVerified() != nil {
		// A proved address does not move, and this is the road where that matters
		// most: a directory's claim about an address is cheaper to come by than a
		// mailbox. `NotFound` rather than a reason, uniquely here, because the
		// caller is an app in the middle of signing somebody in and the answer it
		// needs is the one it already handles -- `Attest`'s own note says a
		// failure is not a failure, since the sign-in worked and the address is a
		// convenience.
		return nil, status.Error(codes.NotFound, "that address is somebody else's")
	}

	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	gone, err := next.Email().Erase(ctx, app.EmailRef_builder{Id: held.GetId()}.Build())
	if err != nil {
		return nil, err
	}
	if !gone.GetErased() {
		return nil, status.Error(codes.FailedPrecondition, "that address is no longer held by the row this read")
	}

	v, err := next.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:    req.GetHolder(),
		Address:   req.GetAddress(),
		VouchedBy: req.GetVouchedBy(),
	}.Build())
	if err != nil {
		return nil, err
	}

	out, err := next.Email().Patch(ctx, app.EmailPatchRequest_builder{
		Ref:          app.EmailRef_builder{Id: v.GetId()}.Build(),
		DateVerified: timestamppb.Now(),
		DateUpdated:  v.GetDateUpdated(),
	}.Build())
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return out, nil
}
