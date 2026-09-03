package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
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
func (s coreEmail) Verify(ctx context.Context, req *app.EmailVerifyRequest) (*app.EmailVerifyResponse, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return nil, status.Error(codes.Unauthenticated, "a link is minted by whoever asked, and nothing here said who that is")
	}

	v, err := s.EmailServiceServer.Get(ctx, app.EmailGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.EmailSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
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
		Holder:      app.HolderRef_builder{Id: v.GetHolder().GetId()}.Build(),
		Email:       app.EmailRef_builder{Id: v.GetId()}.Build(),
		Secret:      sum,
		Issuer:      f.Actor.Bytes(),
		DateExpires: timestamppb.New(expires),
	}.Build()); err != nil {
		return nil, err
	}

	return app.EmailVerifyResponse_builder{Token: token, Expires: timestamppb.New(expires)}.Build(), nil
}

// Confirm spends a verification link and stamps its address.
//
// Every refusal is one `NotFound`, for `Vouch.Redeem`'s reason: a token never
// minted, one spent, one expired, one another caller minted, and one that is a
// recovery link (no `email`) must be told apart by nobody holding a found
// string. What is different from `Redeem` is the whole point -- nothing is
// minted. `date_verified` is written here, through the generated `Patch`, which
// is the one road to it: the gate refuses a request that asserts it.
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
		Ref:    ref,
		Select: app.EmailSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
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
