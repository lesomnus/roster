package vouch

import (
	"bytes"
	"context"
	"crypto/subtle"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// A link that confirms somebody leaving, and nothing else: `Link`'s fourth
// kind (`link.proto` § `leave`), `VouchService.LeaveLink`'s and `Leave`'s.
//
// What leaving **is** -- the rules `MeService.Leave` asks and the erase it
// makes -- is `server/me`'s, handed in as [Leaving], so that there is one of
// it and a link only changes how somebody proved who they are.

// LeaveFor is how long a link that confirms somebody leaving lasts, and a
// caller cannot ask for longer or shorter.
//
// [LinkFor], for [LinkFor]'s reason: it goes through the same channel, a
// mailbox, and the window is set by that channel at both ends. Not shorter,
// because mail takes the time it takes -- seconds usually, and a quarter of an
// hour at a server that defers a sender it has not seen before, which is the
// first mail this deployment ever sends somebody. Not longer, because a link
// left in a mailbox deletes the account of whoever is reading the mailbox
// later, and the minutes it lives are what makes it a proof of the mailbox
// *now* -- the moment `MeService.Leave`'s five minutes are asking about.
//
// It is worth strictly less than a recovery link, which hands over a way in;
// and what it does is undone by an operator until the account is forgotten.
// Neither is a reason to let it lie around longer than the mail needs.
const LeaveFor = LinkFor

// Leaving is what leaving is: `server/me`'s `Server`.
type Leaving interface {
	// MayLeave says whether somebody could leave, by every rule that is about
	// them and not about how they proved who they are.
	MayLeave(ctx context.Context, who pdid.Id) error

	// Depart is that person leaving, as `ctx`'s actor, once `proved` -- asked
	// after every rule and before anything is written -- says they proved it.
	Depart(ctx context.Context, confirm string, proved func(context.Context) error) (*app.MeLeaveResponse, error)
}

// WithLeaving gives the service what leaving is. Without it, `LeaveLink` and
// `Leave` answer Unimplemented: the control plane's people are operators, and
// nobody there leaves by a link.
func WithLeaving(v Leaving) Option { return func(s *Server) { s.leaving = v } }

// LeaveLink mints a link that confirms one person leaving, and answers with it
// once, for the caller to mail to the address it answers with. See
// `vouch.proto`.
func (s *Server) LeaveLink(ctx context.Context, req *app.VouchLeaveLinkRequest) (*app.VouchLeaveLinkResponse, error) {
	if s.leaving == nil {
		return nil, status.Error(codes.Unimplemented, "nobody leaves by a link here")
	}
	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	ref, err := refOf(req.GetWho(), tenantOf(req.GetWho(), ctx))
	if err != nil {
		return nil, err
	}
	if ref == nil {
		// An address is where the link goes and never whom it is about: the
		// front door names the person its session holds.
		return nil, status.Error(codes.InvalidArgument,
			"who: the person leaving, by what the session asking holds; an address is only where the link goes")
	}

	// Through the walled server, as `Link` reads and for its reason: the caller
	// is an app with a tenant of its own, and who is elsewhere is not its to
	// reach.
	who, err := s.walled.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: ref,
		Select: app.HolderSelect_builder{
			DateDisabled: z.Ptr(true),
			Tenant:       app.TenantSelect_builder{DateClosed: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if who.GetDateDisabled() != nil || who.GetTenant().GetDateClosed() != nil {
		// Somebody not to sign in is somebody not to send anything to, as
		// `Link` says -- and one who could not spend it either (`row`).
		return nil, status.Error(codes.FailedPrecondition,
			"somebody who may not sign in is sent nothing")
	}
	holder, err := pdid.From(who.GetId())
	if err != nil {
		return nil, err
	}

	// Everything leaving would refuse them that is not about how they proved
	// who they are, refused here so that nothing is mailed that could not be
	// spent.
	if err := s.leaving.MayLeave(ctx, holder); err != nil {
		return nil, err
	}

	// And an address of theirs somebody proved. One nobody checked names
	// nobody (#48), so a link mailed to it would be a proof handed to whoever
	// typed the address; one of somebody else's is somebody else's mailbox.
	e, err := s.walled.Email().Get(ctx, app.EmailGetRequest_builder{
		Ref: app.EmailRef_builder{
			At: app.EmailRefByAt_builder{
				TenantId: who.GetTenant().GetId(),
				Address:  z.Ptr(front.Address(req.GetAddress())),
			}.Build(),
		}.Build(),
		Select: app.EmailSelect_builder{
			Address:      z.Ptr(true),
			DateVerified: z.Ptr(true),
			Holder:       app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	if err != nil || !bytes.Equal(e.GetHolder().GetId(), who.GetId()) || e.GetDateVerified() == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"address: not one of this person's that somebody proved, and a link goes nowhere else")
	}

	token, sum, err := MintLink()
	if err != nil {
		return nil, err
	}
	row, err := s.open.Link().Add(ctx, app.LinkAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: who.GetId()}.Build(),
		Email:       app.EmailRef_builder{Id: e.GetId()}.Build(),
		Leave:       z.Ptr(true),
		Secret:      sum,
		Issuer:      issuer.Bytes(),
		DateExpires: timestamppb.New(time.Now().Add(LeaveFor)),
	}.Build())
	if err != nil {
		return nil, err
	}

	return app.VouchLeaveLinkResponse_builder{
		Token:   token,
		Address: e.GetAddress(),
		// The row's expiry, for `mint`'s reason.
		Expires: row.GetDateExpires(),
	}.Build(), nil
}

// Leave spends a link [Server.LeaveLink] minted, and its person leaves. See
// `vouch.proto` for the order.
func (s *Server) Leave(ctx context.Context, req *app.VouchLeaveRequest) (*app.MeLeaveResponse, error) {
	if s.leaving == nil {
		return nil, status.Error(codes.Unimplemented, "nobody leaves by a link here")
	}
	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	// A link that was never here, spent, expired, somebody else's, of another
	// kind, for somebody who may no longer sign in, or mailed to an address that
	// is no longer theirs or no longer proved: one answer, as every link's --
	// told apart, it says whether a string was ever a real link.
	no := status.Error(codes.NotFound, "no such link")
	v, err := s.row(ctx, req.GetToken())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, no
		}

		return nil, err
	}
	e := v.GetEmail()
	switch {
	case !v.GetLeave(), v.GetAt() != "", len(e.GetId()) == 0,
		subtle.ConstantTimeCompare(v.GetIssuer(), issuer.Bytes()) != 1,
		e.GetDateErased() != nil, e.GetDateVerified() == nil,
		!bytes.Equal(e.GetHolder().GetId(), v.GetHolder().GetId()):
		return nil, no
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(v.GetHolder().GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	// As them, which is who leaves: the erase goes through the stack as the
	// person, as `MeService.Leave`'s does, so it is an erase like an
	// operator's and the trail says whose. Made here because nothing of theirs
	// was presented -- what proves it is the link, and it was theirs to read --
	// and seeing their own tenant and nothing else, which is what the policy
	// would have let them see.
	as := frame.Into(ctx, frame.New(holder, tenant, frame.Whole()).WithScope(frame.Only(tenant)))

	return s.leaving.Depart(as, req.GetConfirm(), func(context.Context) error {
		// Spent by the caller presenting it, after every rule and before
		// anything else is written, so a mistyped alias costs nothing -- and
		// only the caller that spends it goes on: two browsers arriving with
		// one link are one departure.
		spent, err := s.open.Link().Erase(ctx, app.LinkRef_builder{Id: v.GetId()}.Build())
		if err != nil {
			return err
		}
		if !spent.GetErased() {
			return no
		}

		return nil
	})
}
