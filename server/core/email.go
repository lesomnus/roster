package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/server/vouch"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/quote"
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
//
// # And it is an address
//
// The spelling was the whole of what was asked, as it was of a hostname
// before `hostnamed`, and it passes whatever lowering and trimming leave
// alone: two words with no `@`, a megabyte, a name and an address in one
// string, the same mailbox in brackets or with a dot after it. So an address
// is held to being one, in the one form [front.NotAddress] says, by every
// road that writes one ([mailboxed]).
type coreEmail struct {
	Core
	app.EmailServiceServer
}

func (s Core) Email() app.EmailServiceServer { return coreEmail{s, s.Next().Email()} }

func (s coreEmail) Add(ctx context.Context, req *app.EmailAddRequest) (*app.Email, error) {
	if err := mailboxed("address", req.GetAddress()); err != nil {
		return nil, err
	}
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
		return nil, err
	}

	return s.EmailServiceServer.Add(ctx, entered(ctx, req))
}

// entered stamps who is writing an address: the frame's actor, which is what
// `Email.entered_by` says and what tells a person's claim about their own
// mailbox from somebody else's word about them.
//
// A request with no frame is the deployment's own work -- `init`, `roster
// holder add` -- and keeps whatever that code said, which is nothing unless it
// had a reason. A caller's own request never gets here saying anything: the
// generated gate refuses an `Add` that asserts a stamp, and this layer is
// inside it.
func entered(ctx context.Context, req *app.EmailAddRequest) *app.EmailAddRequest {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return req
	}

	req = proto.Clone(req).(*app.EmailAddRequest)
	req.SetEnteredBy(f.Actor.Bytes())

	return req
}

// Patch is the normalisation, and **not** the rule about whose row it is.
//
// Which is a gap on purpose rather than the other rule being forgotten, and it
// is only safe because of something one file away: no port serves `/Patch`,
// whatever a configuration says (`closed`, `cmd/serve.go`), so nothing a
// caller reaches gets here. What does is the servers' own writes, which are
// not somebody naming a row.
//
// If that ever changes this wants [Core.mayWriteAWayIn] beside the
// normalisation -- with general writes opened, a proved address was changed
// to another and stayed proved (#278), which is why they are not -- because the holder
// being immutable is not the protection it looks like: a patch cannot move an
// address onto somebody else's row and it can change what the address on
// **their** row is, which reaches the same place. The address is theirs; the
// mailbox would be mine.
//
// The normalisation is here anyway, because it is about the value rather than
// about the caller and those other roads write values too.
func (s coreEmail) Patch(ctx context.Context, req *app.EmailPatchRequest) (*app.Email, error) {
	if req.HasAddress() {
		if err := mailboxed("address", req.GetAddress()); err != nil {
			return nil, err
		}
	}

	return s.EmailServiceServer.Patch(ctx, req)
}

// mailboxed is [normalised] for an address, which is also held to being one:
// [hostnamed], of a mailbox.
//
// The form is asked of what the address would be stored as, and before the
// spelling speaks, for the reason given there: a refusal that says *write it
// this way* names a form the next write would be refused for otherwise.
func mailboxed(field, v string) error {
	if w := front.Address(v); w != "" {
		if why := front.NotAddress(w); why != "" {
			return pderr.Invalidf(field, "%s is not an address: %s", quote.Sent(v), why)
		}
	}

	return normalised(field, v, front.Address)
}

// Erase takes an address away from somebody, and is held to what writing one
// onto them is: [Core.mayTakeAWayIn].
//
// The row is read through the wall first, so a caller who cannot see it is
// answered as the generated `Erase` answers -- nothing erased -- and learns
// nothing of whose it is.
//
// The two erases this file makes itself -- an unproved address giving way to
// whoever proved it (`moved`, `attested`) -- go through the layer below and
// are not asked: there the rule is the proof.
func (s coreEmail) Erase(ctx context.Context, req *app.EmailRef) (*app.EmailEraseResponse, error) {
	v, err := s.EmailServiceServer.Get(ctx, app.EmailGetRequest_builder{
		Ref:    req,
		Select: app.EmailSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return s.EmailServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAWayIn(ctx, "ref", v.GetHolder().GetId()); err != nil {
		return nil, err
	}

	return s.EmailServiceServer.Erase(ctx, req)
}

// Claim mints a link that proves this address, and answers with it once.
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
func (s coreEmail) Claim(ctx context.Context, req *app.EmailClaimRequest) (*app.EmailClaimResponse, error) {
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
			// And the address's tenant, which a claimant has to be of.
			TenantId: z.Ptr(true),
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

		// And somebody of the address's own tenant. A claimant named by
		// identifier crossed the wall unasked: a front door holding a session
		// from one tenant at another tenant's host had a claim link minted --
		// and a message sent to the address -- for somebody the address's
		// tenant never had. The agreement every row naming two holders is
		// held to (`agree.go`).
		//
		// The address's tenant is the row's own (`Email.tenant_id`), and was
		// read off its holder: who, erased, is not there to be read, so a
		// claim of an address they never proved was `NotFound: Holder not
		// found`, and the address was kept from whoever reads its mailbox by
		// a person who had left (#279).
		row, err := pdid.From(v.GetTenantId())
		if err != nil {
			return nil, err
		}
		theirs, err := s.tenantOfHolder(ctx, claim)
		if err != nil {
			return nil, err
		}
		if err := tenantsAgree("holder", theirs, row); err != nil {
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

	// And only for somebody a link would be spent for: [coreEmail.provable].
	if err := s.provable(ctx, whose); err != nil {
		return nil, err
	}

	expires := time.Now().Add(vouch.LinkFor)
	if u := req.GetDateExpired(); u != nil {
		if at := u.AsTime(); at.After(time.Now()) && at.Before(expires) {
			expires = at
		}
	}
	token, sum, err := vouch.MintLink()
	if err != nil {
		return nil, err
	}

	row, err := s.Next().Link().Add(ctx, app.LinkAddRequest_builder{
		// The **claimant**, which for the ordinary shape is the row's own holder
		// and so is what this always wrote. `Confirm` reads it to decide whether
		// there is anything to move, and `Vouch.Redeem` cannot read it at all --
		// a link naming an `email` is refused there, which is the discriminator
		// both doors are held to (`email_svc.ext.proto`).
		Holder:      app.HolderRef_builder{Id: whose}.Build(),
		Email:       app.EmailRef_builder{Id: v.GetId()}.Build(),
		Secret:      sum,
		IssuedTo:    f.Actor.Bytes(),
		DateExpired: timestamppb.New(expires),
	}.Build())
	if err != nil {
		return nil, err
	}

	// The row's expiry and not the one computed above: the store keeps it to
	// the microsecond, and the caller is told what a read of the row gives.
	return app.EmailClaimResponse_builder{Token: token, DateExpired: row.GetDateExpired()}.Build(), nil
}

// provable refuses a link for somebody nothing is proved for: erased or
// suspended, or of a tenant that is closed.
//
// It is what [coreEmail.Confirm] asks before it spends one (#277), asked where
// the link is minted. `Claim` asked none of it: it minted a link for somebody
// suspended and answered it, a front door mailed it, and whoever clicked it
// was told there was no such link -- until they were enabled, when the same
// link confirmed. A refusal belongs where there is somebody to tell, and a
// link nobody may spend is not sent.
//
// `NotFound`, which is what `Confirm` says. Of whoever the link would be
// **for**: the row's own holder, or the claimant where it is a claim, as
// there. Somebody erased is not read by naming them, so they are the read's
// own `NotFound`.
func (s coreEmail) provable(ctx context.Context, whose []byte) error {
	no := status.Error(codes.NotFound,
		"ref: no link proves an address for somebody who may not sign in: erased or suspended, or of a tenant that is closed")

	h, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: whose}.Build(),
		Select: app.HolderSelect_builder{
			Tenant:       app.TenantSelect_builder{DateClosed: z.Ptr(true)}.Build(),
			DateDisabled: z.Ptr(true),
		}.Build(),
	}.Build())
	switch {
	case status.Code(err) == codes.NotFound:
		return no
	case err != nil:
		return err
	case h.GetDateDisabled() != nil, h.GetTenant().GetDateClosed() != nil:
		return no
	}

	return nil
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
//   - **A voucher is required, and is theirs.** An attestation with no
//     identity behind it is an address somebody typed, and that has `Add` and a
//     link. And the identity is one of the person the address is for, read as
//     the caller reads: see [coreEmail.vouching].
//   - **The stamp goes through `Next().Patch`**, which is `Confirm`'s road and
//     for `Confirm`'s reason: `date_verified` is refused to a request, so the
//     one way to it is a server writing it below the gate.
//
// Idempotent, because a person signs in more than once: an address already on
// that row is stamped rather than refused, so the second sign-in after a
// directory started saying `email_verified` writes what the first could not.
//
// # It reads before it writes
//
// It wrote first, and read on `AlreadyExists` -- which is every sign-in after
// the first, since a front door attests on each one. Outside a transaction that
// cost an error in the database's log per sign-in. Inside one it cost the call:
// a write the database refused ends the transaction it was in -- on PostgreSQL
// at once, so the read after it failed too, and on SQLite at the commit, which
// payday refuses after a write that failed (payday#128) -- so a transaction
// could not attest an address somebody already had. So who holds the address is read
// first, by address within the holder's tenant, and the one write that follows
// is the one that answer calls for.
func (s coreEmail) Attest(ctx context.Context, req *app.EmailAttestRequest) (*app.Email, error) {
	if err := mailboxed("address", req.GetAddress()); err != nil {
		return nil, err
	}
	if req.GetVouchedBy() == nil {
		return nil, status.Error(codes.InvalidArgument, "vouched_by: an attestation says whose word it was")
	}
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
		return nil, err
	}

	who, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetHolder(),
		Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	by, err := s.vouching(ctx, req.GetVouchedBy(), who.GetId())
	if err != nil {
		return nil, err
	}
	req.SetVouchedBy(by)

	// Through the wall, by address alone -- which is the read `Email`'s `at`
	// index exists for -- so this finds the row whoever holds it, within the
	// holder's tenant and no further, and only where the caller may see.
	held, err := s.EmailServiceServer.Get(ctx, app.EmailGetRequest_builder{
		Ref: app.EmailRef_builder{
			At: app.EmailRefByAt_builder{
				TenantId: who.GetTenant().GetId(),
				Address:  z.Ptr(front.Address(req.GetAddress())),
			}.Build(),
		}.Build(),
		Select: app.EmailSelect_builder{
			DateUpdated:  z.Ptr(true),
			DateVerified: z.Ptr(true),
			Holder:       app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	switch {
	case status.Code(err) == codes.NotFound:
		held, err = s.EmailServiceServer.Add(ctx, entered(ctx, app.EmailAddRequest_builder{
			Holder:    req.GetHolder(),
			Address:   req.GetAddress(),
			VouchedBy: req.GetVouchedBy(),
		}.Build()))
		if status.Code(err) == codes.AlreadyExists {
			// A row the read did not find: one the wall keeps from this caller,
			// or one written since. Neither is this call's to take, and the
			// answer is the one a stranger's address gets.
			return nil, status.Error(codes.NotFound, "that address is somebody else's")
		}
		if err != nil {
			return nil, err
		}

	case err != nil:
		return nil, err

	case !bytes.Equal(held.GetHolder().GetId(), who.GetId()):
		// Somebody else's. Which used to end here, and that refusal is what
		// made an address squattable: a row nobody proved held the address
		// against the index, so the person a directory vouches for could not be
		// given it by any road.
		//
		// A directory saying `email_verified` is a proof, so it takes an address
		// **nobody proved** the way a link does. Only that, and only on
		// `verified`: a provider's word is its own word, where a link is a
		// round trip to the mailbox -- so this is the narrower of the two roads
		// to the same move and stays that way.
		if !req.GetVerified() {
			return nil, status.Error(codes.NotFound, "that address is somebody else's")
		}

		return s.attested(ctx, req, held)
	}

	if !req.GetVerified() || held.GetDateVerified() != nil {
		// Nothing the provider said to write down, or it is already written --
		// and stays the moment it was first written. A front door attests at
		// every sign-in, and a stamp that moved with each would move which
		// address a token says is theirs ([coreEmail.Confirm]).
		return held, nil
	}

	return s.Next().Email().Patch(ctx, app.EmailPatchRequest_builder{
		Ref:          app.EmailRef_builder{Id: held.GetId()}.Build(),
		DateVerified: timestamppb.Now(),
		DateUpdated:  held.GetDateUpdated(),
	}.Build())
}

// vouching is the identity an attestation names, by identifier, once it is one
// the attestation may name: an identity of `whose`, the person the address is
// for, that the caller can see.
//
// # Why it is read, and whose it has to be
//
// What is written is an edge, `Email.vouched_by`, and the sink writes an edge
// to whatever the reference finds: below the gate nothing asks whether the
// caller may see it. `Attest` handed the reference straight down. So an
// identity of **another tenant**, named by its identifier or by `(tenant,
// provider, subject)`, became the voucher of an address in this one -- and a
// read of that address with a `select` through the edge, which is loaded with
// no wall, answered the other tenant's identity, its holder and their tenant
// (#274). It was the one edge between two people's rows that
// [tenantsAgree] was never asked about.
//
// Read through `Next()`, an identity the caller cannot see is `NotFound` as one
// nobody has is, so the verb no longer says which subjects another tenant has.
//
// And it is theirs, not only their tenant's. An attestation is a provider
// saying *this person's address is this* in the token it signed them in with:
// somebody else's identity says nothing of them, and written here it would
// read as if it had.
func (s coreEmail) vouching(ctx context.Context, ref *app.IdentityRef, whose []byte) (*app.IdentityRef, error) {
	v, err := s.Next().Identity().Get(ctx, app.IdentityGetRequest_builder{
		Ref:    ref,
		Select: app.IdentitySelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(v.GetHolder().GetId(), whose) {
		return nil, status.Error(codes.InvalidArgument,
			"vouched_by: an identity of the person the address is for; somebody else's says nothing of them")
	}

	return app.IdentityRef_builder{Id: v.GetId()}.Build(), nil
}

// Confirm spends a verification link and stamps its address.
//
// Every refusal is one `NotFound`, for `Vouch.Redeem`'s reason: a token never
// minted, one spent, one expired, one another caller minted, one that is a
// recovery link (no `email`), and one for somebody who may no longer sign in
// must be told apart by nobody holding a found string. What is different from
// `Redeem` is the whole point -- nothing is minted. `date_verified` is written
// here, through the generated `Patch`, which is the one road to it: the gate
// refuses a request that asserts it.
//
// # And it is where an address changes hands
//
// A link whose `holder` is not the row's holder is a **claim**, minted by
// [coreEmail.Claim] for somebody who does not hold the address. Spending one is
// three writes in one transaction -- erase the row that held it, write the
// address on the claimant, stamp it -- which is `coreHost.proved`'s shape for
// `coreHost.proved`'s reason: an address that is two rows for a moment is an
// address the unique index refuses, and one that is no rows for a moment is an
// address somebody else can take in between.
//
// What may be taken is an address nobody proved. `Claim` is where that is
// checked, because that is where there is somebody to tell -- and it is checked
// again here rather than trusted, for the reason every hop in this app rechecks:
// minutes pass between the two, and what the row said then is not what it says
// now. Somebody who proved the address in the meantime keeps it.
//
// # A proof of an address already proved moves nothing
//
// The link is spent and the row is answered as it stands, with the moment it
// was first proved. It was stamped again, and `date_verified` is what orders
// somebody's proved addresses: the one proved first is the one a token says is
// theirs (`server/me`), so that proving another never changes what a relying
// party keys an account on. Stamped again, the first address confirmed a
// second time was the newest, and the claim was another address -- changed by
// proving nothing new. [coreEmail.Attest] has always left a proved row as it
// was, which it has to: a directory says the same thing at every sign-in.
//
// An address erased and written again is another row, and its proof is a new
// one, as an address that changed hands is.
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
			IssuedTo:    z.Ptr(true),
			DateExpired: z.Ptr(true),
			Leave:       z.Ptr(true),
			Email:       app.EmailSelect_builder{}.Build(),
			// Whose claim it is, which for an ordinary verification is the row's
			// own holder and for a claim is somebody else. Read here rather than
			// assumed, which is the difference between stamping a row and moving
			// an address -- and with whether they may still sign in, which is
			// asked below.
			Holder: app.HolderSelect_builder{
				Tenant:       app.TenantSelect_builder{DateClosed: z.Ptr(true)}.Build(),
				DateDisabled: z.Ptr(true),
			}.Build(),
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
		l.GetDateExpired() == nil || !time.Now().Before(l.GetDateExpired().AsTime()),
		subtle.ConstantTimeCompare(l.GetIssuedTo(), f.Actor.Bytes()) != 1,
		len(l.GetEmail().GetId()) == 0,
		// A link that confirms somebody leaving names the address it was
		// mailed to as well, and is not a verification of it: spent here, it
		// would be a departure somebody confirmed and nobody made (`link.proto`
		// § `leave`).
		l.GetLeave():
		return nil, no
	}

	// Somebody erased or suspended between the sending and the clicking, or
	// whose tenant closed, is somebody nothing is proved for -- what
	// `vouch.Server.row` asks of the other three kinds of link, and this door
	// did not ask. An address proved is a name that resolves somebody and a
	// mailbox a recovery link goes to: it was stamped for a person who had
	// been stopped, and was theirs, proved, the moment they were enabled or
	// restored (#277).
	//
	// Whoever the link is **for**, which for a claim is the claimant. The
	// row's holder is who loses an address nobody proved, and their standing
	// was never what kept it.
	//
	// Erased is read as a holder that did not come back, as `vouch.Server.row`
	// and `keys.signable` read it: an edge is loaded through the rows still
	// there, so somebody erased is a bare reference with no `date_erased` to
	// read and no tenant, and every holder has a tenant.
	//
	// The epoch is not read, on purpose (`Link.date_created`): proving a
	// mailbox signs nobody in. And before the spend, like every refusal above,
	// so that what is refused is not also used up.
	h := l.GetHolder()
	if gone := len(h.GetTenant().GetId()) == 0; gone || h.GetDateDisabled() != nil || h.GetTenant().GetDateClosed() != nil {
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

	// Proved already: the proof stands as it was made, and the answer is the
	// row -- read whole, which is what the stamp below answers with.
	if e.GetDateVerified() != nil {
		out, err := s.Next().Email().Get(ctx, app.EmailGetRequest_builder{Ref: ref}.Build())
		if err != nil {
			return nil, err
		}

		return app.EmailConfirmResponse_builder{Email: out}.Build(), nil
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
// # What it checks again, having been checked at `Claim`
//
// That the row is still unproved. Fifteen minutes pass between minting a link and
// clicking it (`vouch.LinkFor`), and what the row said then is not what it says
// now: somebody who proved the address in that window keeps it, and the claim is
// the thing that expires. Answered as the refusal `Claim` would have made, rather
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

	// The writes go to the server **below** this one, rebound onto the
	// transaction, as `coreHost.proved`'s do: so `Add` does not arrive back
	// here and ask about a claim that has just been spent.
	var out *app.Email
	if err := s.atomically(ctx, "move an address in three writes", func(next app.Server) error {
		// Let go first. A soft erase, which is what every other row here does,
		// and it is enough: the unique index covers the living only, so an
		// erased row holds no address.
		gone, err := next.Email().Erase(ctx, app.EmailRef_builder{Id: e.GetId()}.Build())
		if err != nil {
			return err
		}
		if !gone.GetErased() {
			// Somebody else took it out from under this call. Which is not a
			// failure worth a special answer: the address is nobody's now, and
			// the claimant may write it with `Email.Add` like anybody else.
			return status.Error(codes.FailedPrecondition,
				"that address is no longer held by the row this link was minted against")
		}

		v, err := next.Email().Add(ctx, entered(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: l.GetHolder().GetId()}.Build(),
			Address: e.GetAddress(),
		}.Build()))
		if err != nil {
			return err
		}

		// And the stamp, which is the whole point of having proved it. Through
		// `Patch` because `date_verified` is `stamped:` and refused to an `Add`.
		out, err = next.Email().Patch(ctx, app.EmailPatchRequest_builder{
			Ref:          app.EmailRef_builder{Id: v.GetId()}.Build(),
			DateVerified: timestamppb.Now(),
			DateUpdated:  v.GetDateUpdated(),
		}.Build())

		return err
	}); err != nil {
		return nil, err
	}

	return app.EmailConfirmResponse_builder{Email: out}.Build(), nil
}

// attested is a directory's word taking an address nobody proved.
//
// [coreEmail.moved]'s three writes with the other kind of evidence in front of
// them, and a separate function rather than a branch because what each one has
// already established is different: `moved` holds a spent link and knows the row
// from it, and this holds a `verified` attestation and the row `Attest` found
// holding the address, through the wall and within the holder's tenant.
//
// The rules it meets are `Attest`'s own, already run by the time this is reached:
// the address is normalised, a voucher is required, and `mayWriteAWayIn` has
// passed for the holder it is writing onto. What is left is the one this adds --
// the incumbent must be **unproved**.
func (s coreEmail) attested(ctx context.Context, req *app.EmailAttestRequest, held *app.Email) (*app.Email, error) {
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

	var out *app.Email
	if err := s.atomically(ctx, "move an address in three writes", func(next app.Server) error {
		gone, err := next.Email().Erase(ctx, app.EmailRef_builder{Id: held.GetId()}.Build())
		if err != nil {
			return err
		}
		if !gone.GetErased() {
			return status.Error(codes.FailedPrecondition, "that address is no longer held by the row this read")
		}

		v, err := next.Email().Add(ctx, entered(ctx, app.EmailAddRequest_builder{
			Holder:    req.GetHolder(),
			Address:   req.GetAddress(),
			VouchedBy: req.GetVouchedBy(),
		}.Build()))
		if err != nil {
			return err
		}

		out, err = next.Email().Patch(ctx, app.EmailPatchRequest_builder{
			Ref:          app.EmailRef_builder{Id: v.GetId()}.Build(),
			DateVerified: timestamppb.Now(),
			DateUpdated:  v.GetDateUpdated(),
		}.Build())

		return err
	}); err != nil {
		return nil, err
	}

	return out, nil
}
