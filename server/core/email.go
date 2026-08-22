package core

import (
	"context"

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
// handed. A row written as a provider sent it -- `Someone@Acme.example` -- is a
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

// Patch is the same two rules about the same two fields.
//
// The holder is immutable, so a patch cannot move an address onto somebody
// else's row -- but it can change what the address **is**, which reaches the
// same place: the lowered spelling of the administrator's address, written over
// one of mine, and from then on their address is mine.
func (s coreEmail) Patch(ctx context.Context, req *app.EmailPatchRequest) (*app.Email, error) {
	if req.HasAddress() {
		if err := normalised("address", req.GetAddress(), front.Address); err != nil {
			return nil, err
		}
	}

	return s.EmailServiceServer.Patch(ctx, req)
}
