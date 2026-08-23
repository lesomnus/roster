package core

import (
	"context"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"

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
	if err := s.notVouchedForByTheCaller(ctx, req); err != nil {
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

// notVouchedForByTheCaller refuses a caller asserting that an address has been
// checked.
//
// # Why the schema cannot say this
//
// `05792f6` closed the same class one entity over -- *a stamp is not a field a
// caller writes* -- and it closed it with `immutable: true`, which is the right
// word for `Identity.tenant_id` and the wrong one here. `immutable` takes a
// field out of the **patch** request and leaves it in `Add`, and what this
// wants is the other way round: nobody asserts a verification when the row is
// created, and something here stamps it when a verification actually happens.
//
// `orm.field` has `immutable` and no opposite. `payday.field` **now** has one --
// `stamped:`, added for this, `lesomnus/payday@1c2b63e` -- and roster cannot
// take it yet: the option lives in payday's **buf module**, which roster
// depends on as `buf.build/payday/payday:dev`, and a schema using it fails to
// compile until that module is published again. `docs/MIGRATING.md` carries the
// same ordering note about `secret:`.
//
// The push has not happened -- there is no `buf push` in payday's CI, so the
// label moves when somebody runs it:
//
//	cd <payday>; buf push --label dev ./proto     # needs BUF_TOKEN
//
// After which this is three lines. `(payday.field) = {stamped: true}` beside
// the `orm.field` on `Email.date_verified`, this method deleted with its call,
// and `cmd/foreignedge_test.go` left exactly as it is -- it asserts the refusal
// and not where the refusal lives, including the *no frame is the deployment's
// own work* case, which the generated refusal keeps by living in the **gate**
// and the gate being installed on `Walled` alone.
//
// # What it is protecting, given nothing reads the column yet
//
// Nothing does, today: `MeService` echoes it back to the person it is about and
// no rule consults it. That is what makes this cheap to close and worth closing
// now rather than later, because the field's whole stated job is to decide
// whether an address may be **trusted** -- `email.proto` on `vouched_by` says
// an unverified provider address must not be trusted to link accounts -- and
// the day something reads it is the day every value already in the column was
// put there by whoever could write the row.
//
// And that is wider than it sounds. [Core.mayReach] passes for the caller's own
// row and for any target holding nothing the caller lacks, so a support desk
// whose whole job is contact details can write an address for nearly everybody
// in the tenant. A desk asserting *this one is verified* is the escalation this
// closes before it exists.
//
// # Who is allowed to
//
// Nobody, through a request. A frame means somebody asked; no frame is the
// deployment's own work -- `init`, a seed, a server writing through the
// unwalled stack -- which is the same opt-out `mayGrant` and `mayJoin` take,
// and for the same reason. `Patch` is left alone: that is the road a
// verification would be stamped by, and it is closed at the transport anyway.
func (s coreEmail) notVouchedForByTheCaller(ctx context.Context, req *app.EmailAddRequest) error {
	if _, ok := frame.From(ctx); !ok {
		return nil
	}
	if !req.HasDateVerified() {
		return nil
	}

	return pderr.Invalidf("date_verified",
		"an address is verified by something checking it, not by the request that creates it")
}
