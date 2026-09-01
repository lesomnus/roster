package core

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

type coreIdentity struct {
	Core
	app.IdentityServiceServer
}

func (s Core) Identity() app.IdentityServiceServer {
	return coreIdentity{s, s.Next().Identity()}
}

// Add links an external identity to somebody, and refuses the two ways that go
// wrong quietly.
//
// Neither is something the schema can state. Uniqueness of the pair is a column
// constraint; these are judgements about what a good pair looks like and about
// what a second one means.
func (s coreIdentity) Add(ctx context.Context, req *app.IdentityAddRequest) (*app.Identity, error) {
	if err := subjectIsStable(req.GetSubject()); err != nil {
		return nil, err
	}

	if err := s.oneAccountPerProvider(ctx, req); err != nil {
		return nil, err
	}

	// And whose account this is a way into. Linking a provider account to
	// somebody is handing whoever holds that account their sign-in; see
	// `escalate.go`, [Core.mayWriteAWayIn].
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
		return nil, err
	}

	return s.IdentityServiceServer.Add(ctx, req)
}

// subjectIsStable refuses a subject that is obviously the wrong value.
//
// A provider's subject has to be the identifier that provider treats as
// immutable -- a numeric Id for GitHub, `objectGuid` or `entryUuid` for LDAP,
// `oid` for Entra. What gets written by mistake is the username or the email
// address, because both are visible in the same claims and both read like a
// name.
//
// Getting it wrong is not noticed at link time. It is noticed months later,
// when somebody changes their address and cannot sign in -- or, far worse, when
// their old address is reassigned to a new joiner, who signs in and is served
// as them. That is the case this refuses.
//
// It cannot catch everything: a username with no "@" in it looks like anything
// else. What it catches is the mistake that has an unrecoverable failure at the
// end of it.
func subjectIsStable(v string) error {
	if v == "" {
		return pderr.Invalidf("subject", "must not be empty")
	}
	if strings.Contains(v, "@") {
		return pderr.Invalidf("subject",
			"looks like an address; it must be the identifier the provider treats as immutable, "+
				"because an address is reassigned and whoever gets it next would be served as this person")
	}

	return nil
}

// oneAccountPerProvider refuses a second identity at a provider this person
// already has one at.
//
// The pair is unique, so this is not about two people sharing a subject -- the
// column handles that. It is about **one** person accumulating two accounts at
// one provider, which is what a link that went to the wrong row looks like from
// here.
//
// A person genuinely has one account at each provider. If they have two, one of
// them belongs to somebody else, and the design is explicit that a failed link
// must not quietly become a new record: an explicit failure is better.
//
// # Why it asks the database and does not sift here
//
// It used to list every identity the caller could see and compare in Go, and
// that was inert for almost everybody in two separate ways.
//
// A page is 20 rows ordered by `date_created` ascending and the loop ignored
// the cursor, so what it actually read was the twenty **oldest** identities of
// the whole tenant. Whether somebody was checked depended on when they joined,
// and the twenty-first person onwards was never checked at all. The failure was
// silent and got worse as a deployment grew.
//
// And the comparison was against `holder.id` bytes, so a request naming the
// holder any other way -- [app.HolderRef] carries a slug and an IdP subject as
// well, and enrolment uses those -- matched nothing and the check passed.
//
// The filter is the fix for both: the ref goes to the server as it arrived, so
// every shape of it resolves, and what comes back is one person's identities
// rather than a page of the tenant's. The pages are still followed, because a
// person may have more of them than a page holds and a check that is right only
// for the first twenty is the bug this is replacing.
//
// # And what asking the database still does not decide
//
// Two `Add`s at one provider with different subjects, at the same time. Both
// list, neither sees the other, and both are written -- the same read-then-write
// gap [coreIdentity.Erase] has, and the same person accumulating two accounts
// this refuses when it is asked once.
//
// This half a database can decide on its own, and should: the uniqueness that
// exists is `(tenant, provider, subject)` where nothing is erased, and what
// this function means is `(holder, provider)` on the same terms. That is an
// index in the schema rather than a judgement in a layer, so it is not written
// here -- and this function stays either way, because a constraint violation
// says "already exists" and what a caller needs to be told is which link went
// to the wrong row.
func (s coreIdentity) oneAccountPerProvider(ctx context.Context, req *app.IdentityAddRequest) error {
	ref := req.GetHolder()
	if ref == nil {
		return nil
	}

	// Read through this stack rather than around it, so the wall applies: an
	// identity of somebody the caller cannot see is not a reason they can
	// discover.
	after := ""
	for {
		vs, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{Holder: ref}.Build(),
			},
			After: after,
		}.Build())
		if err != nil {
			return err
		}

		for _, v := range vs.GetItems() {
			if v.GetProvider() != req.GetProvider() {
				continue
			}

			return pderr.Invalidf("provider",
				"this person already has a %s identity; a second one is a link that found the wrong row, "+
					"and linking it would join two people into one", req.GetProvider())
		}

		// Empty when the last page was the last one, which the generated List
		// answers without a second query: it asks for one row more than the
		// page and drops it.
		after = vs.GetNext()
		if after == "" {
			return nil
		}
	}
}

// Erase unlinks an external identity, and refuses the one that locks somebody
// out of their own account.
//
// # Why it is a layer and not a policy
//
// No deployment would want this configured differently, and D17 already decided
// what that means: *a configurable invariant is one that every deployment
// configures identically until one of them gets it wrong.* So it is roster's
// rule, in roster's layer, tested once -- the same place the team rules went.
//
// # What counts as a way in
//
// An `Identity`, or a `Credential` **of a kind that can begin a sign-in**. They
// are what `VouchService` and a Login App between them can turn into a
// signed-in person, and the count is over both: somebody with a password may
// unlink their last provider, and somebody with two providers and no password
// may unlink one of them.
//
// A second factor is not one of them, and counting it was the same mistake this
// file makes about counts generally -- the number was right and the question
// was wrong. Six digits are what somebody is asked for *after* they have said
// who they are; a person left holding nothing else cannot start. [vouch.Begins]
// is the one sentence both sides of it now ask, and the other side of it is
// `Verify`, which used to let that same person in.
//
// # What it does not stop
//
// Erasing the **person**, which is deliberate and is a different act with a
// different name. And an operator resetting a password, which is D28's -- this
// is about somebody removing their own last way in, not about anybody being
// locked out on purpose.
//
// # And what a count on its own cannot say
//
// A count is a read, and the erase after it is a write. Two callers unlinking a
// person's last two identities each count before either writes, each see the
// other's still live, and both go through -- so the state this exists to refuse
// is reached by asking for it twice at the same time. Forty people, each
// unlinked twice at once, lost 39 on PostgreSql and four on SQLite. It was not
// a narrow window and it was not one dialect's.
//
// The count and the erase share a transaction now, with one write both callers
// make before either counts. [Core.only] is that, and it is also where the
// first attempt went wrong -- a version precondition with no write beside it is
// a read with an opinion.
func (s coreIdentity) Erase(ctx context.Context, req *app.IdentityRef) (*app.IdentityEraseResponse, error) {
	// The row first, because the reference may be a subject and the count is
	// about the person. Through `Next()` rather than a client, so the wall
	// narrows it exactly as the erase below will: a caller who cannot see it
	// gets the same answer either way.
	v, err := s.IdentityServiceServer.Get(ctx, app.IdentityGetRequest_builder{
		Ref: req,
		Select: app.IdentitySelect_builder{
			Holder: app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Erasing what is not there succeeds, which is the rule the
			// generated `Erase` already states and this must not change.
			//
			// And `erased` is false, which is the same answer the generated one
			// gives for a row that was already gone: the Rpc does not fail, and
			// it does not pretend either.
			return app.IdentityEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	// The count and the erase together, or neither.
	//
	// See [Core.only] for why a count on its own decides nothing, and what the
	// two callers contend for.
	var out *app.IdentityEraseResponse
	err = s.only(ctx, v.GetHolder().GetId(), func(next app.Server) error {
		if err := s.on(next).notTheirLastWayIn(ctx, v.GetHolder().GetId(), v.GetId()); err != nil {
			return err
		}

		w, err := next.Identity().Erase(ctx, req)
		if err != nil {
			return err
		}

		out = w

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// on is this layer reading through `next` rather than through the stack it was
// built on -- which inside [Core.only] is the same stack, bound to a
// transaction.
func (s coreIdentity) on(next app.Server) coreIdentity {
	return coreIdentity{Core: New(next, s.rules), IdentityServiceServer: next.Identity()}
}

// notTheirLastWayIn counts what would be left.
func (s coreIdentity) notTheirLastWayIn(ctx context.Context, holder, erasing []byte) error {
	if len(holder) == 0 {
		return nil
	}

	ref := app.HolderRef_builder{Id: holder}.Build()

	ids, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{
		Filters: []*app.IdentityFilter{
			app.IdentityFilter_builder{Holder: ref}.Build(),
		},
	}.Build())
	if err != nil {
		return err
	}

	left := 0
	for _, v := range ids.GetItems() {
		if !bytesEq(v.GetId(), erasing) {
			left++
		}
	}
	if left > 0 {
		return nil
	}

	// No other identity, so a password is the only thing that could be left.
	// Read through the unwalled half of nothing -- this layer is on both
	// stacks, and the credential is the person's own row behind the same wall
	// the identity was.
	creds, err := s.Next().Credential().List(ctx, app.CredentialListRequest_builder{
		Filters: []*app.CredentialFilter{
			app.CredentialFilter_builder{Holder: ref}.Build(),
		},
	}.Build())
	if err != nil {
		return err
	}
	for _, v := range creds.GetItems() {
		// The kind decides, not the row: a seed is a credential and is not a
		// way in. See [vouch.Begins].
		if vouch.Begins(v.GetKind()) {
			return nil
		}
	}

	return status.Error(codes.FailedPrecondition,
		"this is the only way they can sign in; give them another before taking it away")
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
