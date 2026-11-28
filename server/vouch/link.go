package vouch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/moment"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// A way in that roster mints and somebody else delivers.
//
// `link.proto` is the row and roadmap.md's item 3 is the subject. What is here is
// the pair of RPCs and the one property that is easy to lose: **minting says
// nothing about whether anybody is there.**

// PrefixLink is what a link looks like, so that one in a log is recognisable.
const PrefixLink = "rl_"

// KindLink is what a link answers as, in `satisfied`.
//
// It is not a `Credential` kind -- nothing is stored under it and
// `verifierOf` does not know it -- and it is in the same vocabulary because
// that vocabulary is *how somebody got in*, which is the question `satisfied`
// asks.
const KindLink = "link"

// LinkFor is how long a link lasts when nothing says otherwise.
//
// Long enough for mail to arrive and somebody to notice it, and short enough
// that one left in a mailbox is not a way in next week. A caller may ask for
// less and not for more.
const LinkFor = 15 * time.Minute

// LinkAtFor is how long a link minted for the user console lasts.
//
// Shorter than [LinkFor] by an order of magnitude, because the channel is a
// browser redirect and not a mailbox: it is spent in the seconds between the
// front door answering and the user console asking, and one that is not is one
// somebody is looking at in an address bar.
const LinkAtFor = 2 * time.Minute

// Link mints a way in for somebody and answers with it once.
func (s *Server) Link(ctx context.Context, req *app.VouchLinkRequest) (*app.VouchLinkResponse, error) {
	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	expires := time.Now().Add(LinkFor)
	if u := req.GetDateExpired(); u != nil {
		if at := u.AsTime(); at.After(time.Now()) && at.Before(expires) {
			// Less, and not more. How long the channel takes is the caller's to
			// know; how long a way into somebody's account may lie around is
			// not.
			expires = at
		}
	}
	// As the store keeps it (`internal/moment`), and here rather than at the
	// one answer that writes a row: the store cuts what it writes to the
	// microsecond, and a link for somebody answered with that and one for
	// nobody with the clock's nanoseconds would say which of the two it was.
	expires = moment.Of(expires)

	token, sum, err := MintLink()
	if err != nil {
		return nil, err
	}

	// The same tenant rule `Verify` has, and for the same reason: a front door
	// asking for a link about somebody of its own operator has already said
	// which one, in the key it is calling with. The walled read below is what
	// makes that safe here too.
	in := tenantOf(req.GetWho(), ctx)

	ref, err := refOf(req.GetWho(), in)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		ref, err = s.byAddress(ctx, in, req.GetWho().GetAddress())
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return nil, err
			}

			// Nobody. **Answered anyway**, with a token that resolves to
			// nothing, because the alternative is that asking for a link
			// answers *is this address here* -- through a form that is meant to
			// be filled in by strangers, and about the one question every other
			// refusal here is careful not to answer.
			//
			// The cost is a token nobody can spend, which is what the caller
			// was going to send to a mailbox that does not belong to anybody
			// here anyway.
			return app.VouchLinkResponse_builder{
				Token:       token,
				DateExpired: timestamppb.New(expires),
			}.Build(), nil
		}
	}

	// Through the **walled** server, as `CredentialService`'s `Issue`, `Unlock`
	// and `Enrol` read.
	//
	// `Verify` reads the unwalled one because the person signing in has no
	// credential yet and there is no frame to narrow by. This is not that call:
	// it is made by an app that holds one, about somebody that app names, so
	// the caller's tenant is known and the wall applies to it like any other
	// read.
	//
	// Read unwalled, it minted a **spendable** way into another tenant. A
	// holder in contoso with `Vouch.Link` and `Vouch.Redeem` -- an ordinary
	// permission for an app that mails recovery links -- could name
	// `@fabrikam/erlich`, be handed a real token, redeem it, and act as somebody
	// in an organisation contoso cannot otherwise see a row of.
	//
	// Narrowed, that request answers what a request for a stranger answers:
	// NotFound below, and the token that resolves to nobody. Which is the same
	// answer for the same reason -- who is in another tenant is not this
	// caller's to learn, any more than who is here at all is.
	who, err := s.walled.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: ref,
		Select: app.HolderSelect_builder{
			DateErased:   z.Ptr(true),
			DateDisabled: z.Ptr(true),
			Tenant:       app.TenantSelect_builder{Config: z.Ptr(true), DateClosed: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		return app.VouchLinkResponse_builder{
			Token:       token,
			DateExpired: timestamppb.New(expires),
		}.Build(), nil
	}
	if !Offers(who.GetTenant(), KindLink) {
		// A tenant with no passwords has nothing for a link to hand over:
		// `Redeem` ends by writing one (`Credential.Issue`), and a link that
		// mints a credential nobody can sign in with is a mailbox full of dead
		// ends. Refused where it is minted rather than where it is spent, so
		// that nothing is sent at all.
		//
		// The same answer as a stranger, which is the answer this whole method
		// gives to everything: a token that resolves to nothing. Distinguishing
		// it would make the recovery form -- the one form meant to be filled in
		// by people who are not signed in -- a way to read another operator's
		// configuration.
		return app.VouchLinkResponse_builder{
			Token:       token,
			DateExpired: timestamppb.New(expires),
		}.Build(), nil
	}
	if who.GetDateDisabled() != nil || who.GetTenant().GetDateClosed() != nil {
		// Somebody not to sign in is somebody not to send a way in to -- and
		// so is everybody of a tenant that closed -- and the answer is the same
		// one a stranger gets.
		return app.VouchLinkResponse_builder{
			Token:       token,
			DateExpired: timestamppb.New(expires),
		}.Build(), nil
	}

	if _, err := s.open.Link().Add(ctx, app.LinkAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: who.GetId()}.Build(),
		Secret:      sum,
		IssuedTo:    issuer.Bytes(),
		DateExpired: timestamppb.New(expires),
	}.Build()); err != nil {
		return nil, err
	}

	return app.VouchLinkResponse_builder{
		Token:       token,
		DateExpired: timestamppb.New(expires),
	}.Build(), nil
}

// Redeem spends a link, which proves the person and nothing more.
func (s *Server) Redeem(ctx context.Context, req *app.VouchRedeemRequest) (*app.VouchDelegateResponse, error) {
	methods := req.GetMethods()
	if len(methods) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"methods: a delegation that allows nothing opens no door")
	}
	if err := s.mayDelegate(ctx, methods); err != nil {
		return nil, err
	}

	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	v, err := s.link(ctx, req.GetToken(), issuer)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		// A link that was never here, one that was spent, one that expired, one
		// somebody else was issued, one minted for a stranger, and one out
		// since before everything of its holder's was ended: one answer.
		return app.VouchDelegateResponse_builder{Verified: no()}.Build(), nil
	}

	// Spent whatever happens next, which is the whole of what single-use means:
	// *used* is *not there*. And **only the caller that spent it** goes on --
	// the erase answers who did, because a link is a first factor with no
	// second one behind it, so two winners here are two credentials from one
	// mail.
	//
	// A mail client that fetches a link to preview it, and a person who clicks
	// it while that fetch is in flight, is not an exotic case. It is the shape
	// this is most likely to meet.
	//
	// The same `no()` every other unusable token gets, for the same reason:
	// told apart, this says whether a string was ever a real link.
	spent, err := s.open.Link().Erase(ctx, app.LinkRef_builder{Id: v.GetId()}.Build())
	if err != nil {
		return nil, err
	}
	if !spent.GetErased() {
		return app.VouchDelegateResponse_builder{Verified: no()}.Build(), nil
	}

	// And then it is a first factor like any other. If they have a second one
	// it is asked for exactly as it would have been after a password -- a link
	// that skipped it would be a way to turn a mailbox into an account.
	//
	// `metered_by` is empty because there is no first-factor row to meter
	// against: a link is not a `Credential` and guessing one is guessing 256
	// bits. `step` falls back to the row being tried, and the note there says
	// why that is enough here -- restarting costs a fresh link, which costs the
	// channel.
	res, err := s.answer(ctx, v.GetHolder(), []string{KindLink}, nil, issuer)
	if err != nil {
		return nil, err
	}
	if !res.GetOk() {
		return app.VouchDelegateResponse_builder{Verified: res}.Build(), nil
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}

	// Proved now: the link was in the mailbox, and it has just been spent.
	return s.mint(ctx, res, holder, issuer, methods, req.GetDateExpired(), time.Now())
}

// link finds the row a token names, if it is the caller's and still open.
func (s *Server) link(ctx context.Context, token string, by pdid.Id) (*app.Link, error) {
	v, err := s.row(ctx, token)
	if err != nil {
		return nil, err
	}

	if len(v.GetIssuedTo()) == 0 || by == pdid.Nil ||
		subtle.ConstantTimeCompare(v.GetIssuedTo(), by.Bytes()) != 1 {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	// A link for the user console names where it is spent, and this is not
	// there: it is bound to a name rather than to a caller, and it ends in a
	// session rather than a delegation. Refused here as the user console refuses a
	// link that names no name -- the discriminator is read on both doors, which
	// is the lesson the `email` guard below was learned from.
	if v.GetAt() != "" {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	// A verification link names its `email`, and this is the door it must not
	// open. `Email.Claim` mints into this table with this shape on purpose --
	// one table, one sweep -- and the `email` edge is what tells the two apart;
	// `Email.Confirm` refuses a link that names none, and until this the other
	// direction was not refused at all. So a token mailed to prove an address
	// was redeemable for a delegation naming its holder, which is a mailbox
	// read once being an account held: the one thing a verify link is worth
	// less than a recovery link *for*. See `email_svc.ext.proto`, § "A verify
	// link is worth strictly less than a recovery link".
	//
	// A discriminator written on one side is not one, and the pair is now read
	// the same way in both places.
	if len(v.GetEmail().GetId()) != 0 {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	// And a link that confirms somebody leaving is a way in to nothing: it
	// ends in the account's erase and signs nobody in (`leave.go`). It names
	// an `email` too, so the guard above refuses it already; this one says
	// what it is refusing, so that the two do not stand or fall together.
	if v.GetLeave() {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	return v, nil
}

// row is the link a token names, if it is real, still open, for somebody who
// may still sign in, and minted since everything of theirs was last ended --
// which is what every kind of link spent here has in common. What each kind
// is bound to is the caller's to check.
//
// # A link ends with what ends a sign-in
//
// `Holder.date_invalidated` says everything issued before a moment is void. A
// password issued for somebody stamps it, a password they change does, and so
// does *sign out everywhere*: each is somebody saying that what was out before
// is not to be trusted, and a link in a mailbox is out. Nothing read the stamp
// against one. So of two recovery links asked for, following the second -- a
// new password, everything signed out -- left the first a way in for the rest
// of its quarter hour, and following that replaced the password the second
// had just handed over (#277).
//
// It is read here, once, because this is the read all three kinds spent in
// this package pass through: a recovery (`Redeem`), a departure (`Leave`) and
// a hand into the user console (`SpendAt`). And it can only be read where the
// row is: when a link was minted is the link's to say, so the resolver, which
// sees the holder and not the link, cannot cover it -- `keys.findDelegation`'s
// arrangement.
//
// None of the three stamps the epoch before it reads. A recovery's stamp is
// the password issued once `Redeem` has answered, leaving signs out after the
// link is spent (`me.Server.Depart`), and a hand into the user console stamps
// nothing.
//
// A link that confirms an address is not read here and is not ended by the
// epoch (`Email.Confirm`, in `server/core`): it signs nobody in and hands
// nothing over, and somebody who changed their password a minute ago still
// reads their mailbox. The rest of what this asks -- that whoever the link is
// for may still sign in -- that read asks for itself.
func (s *Server) row(ctx context.Context, token string) (*app.Link, error) {
	if !strings.HasPrefix(token, PrefixLink) {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	sum := sha256.Sum256([]byte(token))

	v, err := s.open.Link().Get(ctx, app.LinkGetRequest_builder{
		Ref: app.LinkRef_builder{Secret: sum[:]}.Build(),
		Select: app.LinkSelect_builder{
			Secret:      z.Ptr(true),
			IssuedTo:    z.Ptr(true),
			At:          z.Ptr(true),
			DateExpired: z.Ptr(true),
			// When it was minted, to read against when everything of its
			// holder's was last ended (`date_invalidated`, below).
			DateCreated: z.Ptr(true),
			Leave:       z.Ptr(true),
			// The address a link was mailed to, for the kind that is spent on
			// whether it is still theirs and still proved (`leave.go`).
			Email: app.EmailSelect_builder{
				DateVerified: z.Ptr(true),
				DateErased:   z.Ptr(true),
				Holder:       app.HolderSelect_builder{}.Build(),
			}.Build(),

			// With their tenant, which is also how somebody erased is read:
			// see below.
			Holder: app.HolderSelect_builder{
				Tenant:          app.TenantSelect_builder{Alias: z.Ptr(true), DateClosed: z.Ptr(true)}.Build(),
				DateDisabled:    z.Ptr(true),
				DateInvalidated: z.Ptr(true),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	if subtle.ConstantTimeCompare(v.GetSecret(), sum[:]) != 1 {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	u := v.GetDateExpired()
	if u == nil || !time.Now().Before(u.AsTime()) {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	// Somebody erased or suspended between the sending and the clicking, or
	// whose tenant closed, is somebody this must not let in.
	//
	// Erased is read as **a holder that did not come back**, which is
	// `keys.signable`'s reading of a token. An edge is loaded through the rows
	// still there, so somebody erased is answered as a bare reference: the
	// identifier, and nothing of them. Every holder has a tenant and this read
	// selects it, so one with none was not found.
	//
	// It asked for `date_erased` on that edge, which is a column of a row
	// that was not loaded: the check never fired. What stopped somebody
	// erased was the next thing to need their tenant -- after `Redeem` had
	// spent the link, and as an identifier with no bytes, which a caller saw
	// as `Unknown` where every other dead link is one answer (#277). Refused
	// here, it is that answer, and before anything is spent.
	h := v.GetHolder()
	if gone := len(h.GetTenant().GetId()) == 0; gone || h.GetDateDisabled() != nil || h.GetTenant().GetDateClosed() != nil {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	// And one minted at or before the holder's epoch is void, which is the
	// reading `keys.findDelegation` gives a delegation, a tie included. A link
	// and a stamp in one instant are a link handed to the sign-in the stamp
	// was ending, so the safe reading of a tie is that the link is too old.
	// That is meant: somebody handed toward the user console in the instant
	// their password changed signs in again.
	//
	// The same `NotFound` as every dead link above, so that spending one says
	// nothing about why.
	if w := h.GetDateInvalidated(); w != nil && !v.GetDateCreated().AsTime().After(w.AsTime()) {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	return v, nil
}

// SpendAt spends a link at the name it was minted for, and answers who.
//
// The user console's half of `VouchAcceptRequest.at`, and a Go call rather
// than an RPC: the caller is `AuthService.SignIn`, in this process, answering
// a browser that holds no credential -- so there is no frame to bind a spend
// to and no method for a role to name. What binds it instead is `at`, which
// the user console takes from the name the request arrived at and never from
// anything the browser said, and the tenant that name resolved to.
//
// It answers in `Verify`'s shape: `ok` and the person, or `ok` false for every
// way of being wrong -- a link that was never here, one already spent, one
// expired, one minted for another name or another tenant, a recovery link,
// somebody erased or suspended since, or who has ended every sign-in of theirs
// since ([Server.row]). One answer, because told apart it says whether a
// string was ever a real link.
//
// No continuation, deliberately. The person was checked by the front door
// against the tenant's own directory, which is the check `Accept` takes as
// finished and mints on without a second factor; a second factor asked here
// would be one the account page did not ask, for the same person through the
// same directory.
func (s *Server) SpendAt(ctx context.Context, token, at, tenant string) (*app.VouchVerifyResponse, error) {
	at = front.Hostname(at)
	if at == "" || tenant == "" {
		return no(), nil
	}

	v, err := s.row(ctx, token)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		return no(), nil
	}

	// Bound to **where**, and this is the whole of the binding: a recovery link
	// names no name and is refused; a link for another of this tenant's names is
	// another door's; and a name that has changed hands since the mint is
	// caught by the tenant behind it, which is belt beside braces.
	if v.GetAt() == "" || v.GetAt() != at || v.GetHolder().GetTenant().GetAlias() != tenant ||
		len(v.GetEmail().GetId()) != 0 || v.GetLeave() {
		return no(), nil
	}

	// Spent whatever happens next, and only the caller that spent it goes on:
	// the erase answers who did, which is `Redeem`'s arrangement and the reason
	// two browsers arriving with one link are one session and not two.
	spent, err := s.open.Link().Erase(ctx, app.LinkRef_builder{Id: v.GetId()}.Build())
	if err != nil {
		return nil, err
	}
	if !spent.GetErased() {
		return no(), nil
	}

	return app.VouchVerifyResponse_builder{
		Ok:     true,
		Holder: v.GetHolder().GetId(),
		Tenant: v.GetHolder().GetTenant().GetId(),
	}.Build(), nil
}

// linkAt mints the link `Accept` hands a front door for one name, and answers
// with it once. The row is what `link.proto` § *The third kind* describes.
func (s *Server) linkAt(ctx context.Context, holder, issuer pdid.Id, at string) (string, *timestamppb.Timestamp, error) {
	token, sum, err := MintLink()
	if err != nil {
		return "", nil, err
	}

	row, err := s.open.Link().Add(ctx, app.LinkAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: holder.Bytes()}.Build(),
		Secret:      sum,
		IssuedTo:    issuer.Bytes(),
		At:          at,
		DateExpired: timestamppb.New(time.Now().Add(LinkAtFor)),
	}.Build())
	if err != nil {
		return "", nil, err
	}

	// The row's expiry and not the one computed above, for `mint`'s reason.
	return token, row.GetDateExpired(), nil
}

// mintLink is the token and the verifier stored for it.
// MintLink is a link token and the hash that is stored for it -- `PrefixLink`
// and thirty-two random bytes. Exported for `Email.Claim`, which mints the
// same shape for a different purpose and must not mint a different one.
func MintLink() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, status.Error(codes.Internal, "a link cannot be made just now")
	}

	token := PrefixLink + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))

	return token, sum[:], nil
}
