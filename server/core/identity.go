package core

import (
	"context"
	"slices"
	"strings"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/quote"
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
//
// And it refuses the one somebody writes themselves where it is not theirs to
// say: at the connection their tenant's directory provisions through
// ([coreIdentity.directorys]).
func (s coreIdentity) Add(ctx context.Context, req *app.IdentityAddRequest) (*app.Identity, error) {
	if !s.deployment(ctx) {
		tenant, err := s.tenantOfHolder(ctx, req.GetHolder())
		switch status.Code(err) {
		case codes.OK:
			if err := s.directorys(ctx, "provider", tenant, req.GetProvider(), accountWritten); err != nil {
				return nil, err
			}
		case codes.NotFound, codes.InvalidArgument:
			// Not a person this caller can see, which `add` refuses as what
			// it is.
		default:
			return nil, err
		}
	}

	return s.add(ctx, req)
}

// What is done to an account at the connection a directory provisions
// through, each with the verb that does it and who holds that verb across a
// tenant ([coreIdentity.directorys]).
var (
	accountWritten = directorysWrite{
		method: app.IdentityService_Add_FullMethodName,
		done:   "written for somebody already here",
		by:     "a front door they signed in through, or an administrator",
	}
	accountTakenAway = directorysWrite{
		method: app.IdentityService_Erase_FullMethodName,
		done:   "taken away",
		by:     "an administrator; a directory that means somebody has left suspends them",
	}
	accountMoved = directorysWrite{
		method: app.IdentityService_Resubject_FullMethodName,
		done:   "moved to another subject",
		by:     "a front door at their sign-in, or an administrator",
	}
)

type directorysWrite struct {
	method   string
	done, by string
}

// directorys refuses a write of an account at the connection a tenant's
// directory provisions through -- one added, one taken away, one moved to
// another subject -- to anybody who does not hold that verb across the tenant.
//
// # What a person's own write did there
//
// `IdentityService/Add` is somebody's own to call about their own row
// (`self-service`), and roster checks nothing about the subject: being the
// relying party is what it is not. Anywhere else that is a second way into the
// account of whoever wrote it, and nobody else's business.
//
// At the connection a directory provisions through, the row is also **who the
// directory means**. It finds its user by `externalId` through that identity
// (`scim`), and speaks for whoever has one ([Core.speaksFor]). So somebody who
// wrote a colleague's id onto their own row was that colleague to the
// directory: found by a lookup for them, the reason creating them was refused
// as taken, and the row every later word about the colleague was written
// onto -- a name, a suspension, a place in a group the directory grants
// through (#278).
//
// # And what taking one away did
//
// The same thing from the other side. *Unlink*, on somebody's own page, needs
// no role (`MeService/Unlink`), and asked only that another way in be left.
// Somebody the directory made, who also had a password, unlinked the account
// it made them by, and was from then on nobody the directory spoke for: its
// `active: false` on the day they left was refused, and they went on signing
// in. And an account moved to another subject (`Resubject`) is another of the
// directory's people, which a grant made in one site could do.
//
// # Whose it is
//
// The directory's to say. It writes these as it makes people
// (`HolderService.Provision`, which does not come through here: [coreIdentity.add]),
// and says somebody has left by suspending them, which removes no account.
// Any other write of one is made on a grant, as `Provision` is called on one:
// by whoever holds the verb across the tenant by a binding -- which what
// everybody may do to their own account is not (`cmd/policy.go`,
// `policy.own`), and neither is what needs no role. That is a front door,
// which links somebody it saw the provider sign in and moves them to the claim
// their token carries; an administrator; an operator's `roster scim adopt`.
// Across the tenant because the directory finds its people across it: a grant
// made in a site does not say who somebody is to everybody.
//
// The deployment's own work is not asked, as everywhere in this package.
func (s coreIdentity) directorys(ctx context.Context, field string, tenant pdid.Id, provider string, what directorysWrite) error {
	if s.deployment(ctx) {
		return nil
	}

	c, err := s.connectionOf(ctx, tenant, provider)
	if err != nil || !c.GetProvisions() {
		return err
	}

	f, _ := frame.From(ctx)
	if s.rules.Everywhere == nil {
		return status.Error(codes.PermissionDenied,
			"this server cannot say what you hold across the tenant, so it will not let you say who has an account at the connection its directory provisions through")
	}
	held, err := s.rules.Everywhere(ctx, f.Actor)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(held, func(m string) bool { return frame.Covers(m, what.method) }) {
		return nil
	}

	return status.Errorf(codes.PermissionDenied,
		"%s: this tenant's directory provisions through %s, so who has an account there is the directory's to say. "+
			"It writes these as it makes people, and one is %s by whoever holds %s across the tenant -- %s -- "+
			"and not by the person, nor on a grant made in part of the tenant",
		field, provider, what.done, what.method, what.by)
}

// add is [coreIdentity.Add] for a verb of this layer's own that writes an
// account as it makes the person -- a directory's `Provision` -- which is
// asked about itself where it arrives, and not about `Add`.
func (s coreIdentity) add(ctx context.Context, req *app.IdentityAddRequest) (*app.Identity, error) {
	// The provider is a connection's name wherever it is one, and is held to
	// what a connection's name is held to ([named]): `Entra` beside the
	// connection `entra` was a second account for one person, which nothing
	// the connection says of its subjects reached.
	if err := named("provider", req.GetProvider()); err != nil {
		return nil, err
	}
	if err := subjectIsStable(req.GetSubject()); err != nil {
		return nil, err
	}

	// Asked here, where it always was, so that a caller refused for this is
	// told so before being told whose account it is -- and asked again below,
	// where the answer is one two writes cannot both be given.
	if err := s.oneAccountPerProvider(ctx, req); err != nil {
		return nil, err
	}

	// And whose account this is a way into. Linking a provider account to
	// somebody is handing whoever holds that account their sign-in; see
	// `escalate.go`, [Core.mayWriteAWayIn].
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
		return nil, err
	}

	holder, tenant, err := s.whose(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}
	subject, err := s.subjectFits(ctx, tenant, req.GetProvider(), req.GetSubject())
	if err != nil {
		return nil, err
	}
	if subject != req.GetSubject() {
		// Kept in the one spelling it is looked for in ([front.Subject]). The
		// request is the caller's, so it is a copy that is changed.
		req = proto.CloneOf(req)
		req.SetSubject(subject)
	}

	// The count and the write together, or neither.
	//
	// What [coreIdentity.oneAccountPerProvider] asks is how many accounts the
	// person has at this provider, which is a count of other rows, and the
	// write after it adds to that count: [Core.only]'s case, from the other
	// side of [coreIdentity.Erase]. Two accounts added at once each counted
	// none and both landed. So both make one write on the person's own row
	// first, and whichever is second counts what the first wrote.
	var out *app.Identity
	err = s.only(ctx, holder, func(next app.Server, _ dialect.Driver) error {
		if err := s.on(next).oneAccountPerProvider(ctx, req); err != nil {
			return err
		}

		v, err := next.Identity().Add(ctx, req)
		if err != nil {
			return err
		}

		out = v

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// whose is the person an account is written for, as this caller reads them:
// their identifier, which is the row two writes of their accounts contend on
// ([Core.only]), and their tenant, whose connection says what shape a subject
// has ([coreIdentity.subjectFits]).
//
// Neither, for a reference that names no person this caller can see. `Add`
// refuses that as what it is -- a reference naming no person is an argument
// and not a missing row -- and there is then nobody whose accounts two writes
// could both count.
func (s coreIdentity) whose(ctx context.Context, ref *app.HolderRef) ([]byte, pdid.Id, error) {
	if ref == nil {
		return nil, pdid.Nil, nil
	}

	v, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	switch status.Code(err) {
	case codes.OK:
	case codes.NotFound, codes.InvalidArgument:
		return nil, pdid.Nil, nil
	default:
		return nil, pdid.Nil, err
	}

	tenant, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return nil, pdid.Nil, err
	}

	return v.GetId(), tenant, nil
}

// Get finds an identity, and one named by its subject in the one spelling the
// row has ([Core.spelled]).
func (s coreIdentity) Get(ctx context.Context, req *app.IdentityGetRequest) (*app.Identity, error) {
	ref, err := s.spelled(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if ref != req.GetRef() {
		req = proto.CloneOf(req)
		req.SetRef(ref)
	}

	return s.IdentityServiceServer.Get(ctx, req)
}

// spelled is a reference to an identity with its subject as the row holds it:
// a GUID in small letters, where the connection it names says its people are
// named by `oid` ([front.Subject]). Any other reference is answered as it is.
//
// # Why it is here and not at whoever asks
//
// A lookup that spells a subject another way than the row does finds nobody,
// and what comes of that is not a refusal: a sign-in that is a stranger's, a
// directory that makes its person a second time. Who asks is a front door with
// a token, a directory with an `externalId`, an operator at a terminal, and
// whatever is written against this API next; each would have to know which
// claim the connection names and fold the same way, and the one that did not
// would be the one nobody tested. The row's spelling is this layer's doing
// ([coreIdentity.Add]), so finding it by another is too.
//
// The connection is read only for a subject that is a GUID with a capital in
// it, so a lookup as Entra spells one -- which is nearly every one -- costs
// nothing it did not cost before.
func (s Core) spelled(ctx context.Context, ref *app.IdentityRef) (*app.IdentityRef, error) {
	by := ref.GetSubject()
	if by == nil {
		return ref, nil
	}
	subject := by.GetSubject()
	if !front.GUID(subject) || subject == strings.ToLower(subject) {
		return ref, nil
	}
	tenant, err := pdid.From(by.GetTenantId())
	if err != nil {
		// Not a tenant, so not a row; what answers that is below this.
		return ref, nil
	}
	claim, err := s.claimOf(ctx, tenant, by.GetProvider())
	if err != nil {
		return nil, err
	}
	as := front.Subject(claim, subject)
	if as == subject {
		return ref, nil
	}

	return app.IdentityRef_builder{Subject: app.IdentityRefBySubject_builder{
		TenantId: by.GetTenantId(),
		Provider: z.Ptr(by.GetProvider()),
		Subject:  z.Ptr(as),
	}.Build()}.Build(), nil
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
//
// And a subject with a space before or after it, which is nobody's identifier
// and somebody's paste. It is refused and not trimmed: a subject is found by
// what was written, and a row that holds something else than its writer wrote
// is one they do not find.
//
// What it asks is [front.Unstable], which a front door asks too, before it
// makes somebody whose account this would then refuse.
func subjectIsStable(v string) error {
	if why := front.Unstable(v); why != "" {
		return pderr.Invalidf("subject", "%s", why)
	}

	return nil
}

// subjectFits holds a subject to the claim its connection names, where that
// claim has a shape.
//
// `oid` is a GUID. A connection that names people by it and an identity whose
// subject is not one is the mistake the claim exists to end: Entra's pairwise
// `sub`, written where the person's own identifier belongs, names them to one
// front door and to nothing else -- and a directory provisioning them, which
// says `oid`, would find nobody and make a second person.
//
// What it answers is the subject as it is kept: a GUID in the one spelling it
// has ([front.Subject]), and anything else as it was written.
//
// `tenant` is the person's ([coreIdentity.whose]), and none where they are
// nobody this caller can see: there is then no connection to hold it to, and
// it is answered as it was written.
func (s coreIdentity) subjectFits(ctx context.Context, tenant pdid.Id, provider, subject string) (string, error) {
	claim, err := s.claimOf(ctx, tenant, provider)
	if err != nil {
		return "", err
	}
	if claim == claimOid && !front.GUID(subject) {
		return "", pderr.Invalidf("subject",
			"the %s connection names people by `oid`, which is a GUID, and %s is not one", provider, quote.Sent(subject))
	}

	return front.Subject(claim, subject), nil
}

// claimOf is the claim a tenant's connection of that name reads as a subject.
//
// `sub` where there is no such connection: a provider no front door signs
// anybody in through -- `local`, `ldap`, a name a script chose -- has no
// claim, and its subjects are whatever it says they are. Read through this
// stack, so a connection the caller cannot see is one they are not held to.
func (s Core) claimOf(ctx context.Context, tenant pdid.Id, provider string) (string, error) {
	v, err := s.connectionOf(ctx, tenant, provider)
	if err != nil {
		return "", err
	}

	return subjectClaimOf(v.GetSubjectClaim()), nil
}

// connectionOf is a tenant's connection of that name, with what this layer
// asks of one about an identity at it -- the claim it names its people by, and
// whether a directory provisions through it -- and nil where there is none.
func (s Core) connectionOf(ctx context.Context, tenant pdid.Id, name string) (*app.Connection, error) {
	if tenant == pdid.Nil {
		return nil, nil
	}

	v, err := s.Next().Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref: app.ConnectionRef_builder{
			At: app.ConnectionRefByAt_builder{
				Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
				Name:   z.Ptr(name),
			}.Build(),
		}.Build(),
		Select: app.ConnectionSelect_builder{SubjectClaim: z.Ptr(true), Provisions: z.Ptr(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return v, nil
}

// Resubject moves an identity to the subject its connection now names.
//
// `identity_svc.ext.proto` says why this is a verb and not a patch. What it
// holds the move to is what makes it narrower than `Add` rather than wider:
//
//   - **The same row.** The holder edge is immutable, so whoever this is a way
//     into, it stays a way into them -- the move cannot point an account at
//     somebody else.
//   - **A connection that names a claim other than `sub`**, which is the one
//     reason a person's identifier at a provider changes at all.
//   - **One way, once.** The subject held must not already be of the claim's
//     shape and the new one must be, so a moved identity cannot be moved again
//     -- a second move is a second person, and is `Add`'s refusal to make.
//   - **And the caller may write a way into this person**, as for `Add`: the
//     new subject is a way in like any other, and a front door is narrower than
//     an administrator ([Core.mayWriteAWayIn]).
//
// The write goes through [Core.only] beside the identity's other ways in being
// counted, as `Erase` does, so a move and an unlink of the same person are one
// after the other rather than at once.
func (s coreIdentity) Resubject(ctx context.Context, req *app.IdentityResubjectRequest) (*app.Identity, error) {
	ref, err := s.spelled(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	v, err := s.IdentityServiceServer.Get(ctx, app.IdentityGetRequest_builder{
		Ref: ref,
		Select: app.IdentitySelect_builder{
			Holder:   app.HolderSelect_builder{}.Build(),
			Provider: z.Ptr(true),
			Subject:  z.Ptr(true),
			TenantId: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	tenant, err := pdid.From(v.GetTenantId())
	if err != nil {
		return nil, err
	}
	claim, err := s.claimOf(ctx, tenant, v.GetProvider())
	if err != nil {
		return nil, err
	}

	to := req.GetSubject()
	switch claim {
	case claimSub:
		return nil, status.Errorf(codes.FailedPrecondition,
			"subject: the %s connection names people by `sub`, which is what this identity holds; "+
				"a connection names its claim before anybody moves to it", v.GetProvider())
	case claimOid:
		if front.GUID(v.GetSubject()) {
			return nil, status.Error(codes.FailedPrecondition,
				"subject: this identity already holds an `oid`, and a person's `oid` does not change; another one is another person")
		}
		if !front.GUID(to) {
			return nil, pderr.Invalidf("subject", "an `oid` is a GUID, and %s is not one", quote.Sent(to))
		}
	}
	if err := subjectIsStable(to); err != nil {
		return nil, err
	}
	// Moved to the one spelling an `oid` is kept in, as `Add` keeps one: in
	// another it would be a second row for whoever already holds it.
	to = front.Subject(claim, to)

	holder := v.GetHolder().GetId()
	if err := s.mayWriteAWayIn(ctx, "ref", app.HolderRef_builder{Id: holder}.Build()); err != nil {
		return nil, err
	}
	// And at the connection their directory provisions through, the subject
	// is who the directory finds them by: moved, they are another of its
	// people ([coreIdentity.directorys]).
	if err := s.directorys(ctx, "ref", tenant, v.GetProvider(), accountMoved); err != nil {
		return nil, err
	}

	var out *app.Identity
	err = s.only(ctx, holder, func(next app.Server, _ dialect.Driver) error {
		patch := app.IdentityPatchRequest_builder{
			Ref:     app.IdentityRef_builder{Id: v.GetId()}.Build(),
			Subject: z.Ptr(to),
		}
		if req.HasDateUpdated() {
			patch.DateUpdated = req.GetDateUpdated()
		} else {
			patch.DateUpdatedForce = z.Ptr(true)
		}

		w, err := versioned(next.Identity().Patch(ctx, patch.Build()))
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
// # And what asking the database did not decide
//
// Two `Add`s at one provider with different subjects, at the same time. Both
// listed, neither saw the other, and both were written -- the same
// read-then-write gap [coreIdentity.Erase] had, and the same person
// accumulating two accounts this refuses when it is asked once. Forty people,
// each given two at once: thirty-eight held both (#279).
//
// So it is asked where [coreIdentity.Erase] counts: in one transaction with
// the write, after a write on the person's own row that both callers make
// first ([Core.only], which [coreIdentity.add] takes). Whichever is second
// lists what the first wrote, and is told what this says.
//
// # Why not an index
//
// The uniqueness that exists is `(tenant, provider, subject)` where nothing
// is erased, and what this means is `(holder, provider)` on the same terms,
// which a schema can say. It is not said there because a unique index is a
// key, and a key is a way to name the row: `IdentityRef` would gain a third
// on the wire, for a rule that is this layer's to state. This function would
// stay either way: a constraint says "already exists", and what a caller
// needs to be told is which link went to the wrong row.
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

			// `AlreadyExists`, as every other row that exists already is: a
			// caller that offers *already linked* on 409 is told so here too.
			return status.Errorf(codes.AlreadyExists,
				"provider: this person already has a %s identity; a second one is a link that found the wrong row, "+
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
// # And a way in is one that works
//
// The same mistake a third time, and found the same way: the count was of
// rows. Any other identity was a way in, whatever its provider -- a connection
// erased since, a name no front door was ever connected to -- and so was a
// password row in a tenant that takes no passwords, which `Verify` refuses.
// Either erase left somebody holding a row and no way to sign in by it.
//
// So an identity counts where its provider is a connection the tenant has,
// which is where a front door of roster's sends somebody to sign in; and a
// credential counts where the tenant lets its kind be a way in
// ([vouch.Offers], the sentence `Verify` asks). An address is not counted
// though a link mailed to it ends in a password: it is a way in only where a
// front door mails, and that is nothing this layer can see. Each of these errs
// the one way -- towards refusing an erase that would have been safe -- and an
// operator who knows better erases the person's row or gives them another way
// in first.
//
// # What it does not stop
//
// Erasing the **person**, which is deliberate and is a different act with a
// different name. And an operator resetting a password, which is D28's -- this
// is about somebody removing their own last way in, not about anybody being
// locked out on purpose.
//
// # Somebody erased has no last way in
//
// An erase stamps the person and leaves their accounts at providers theirs,
// for a restore to give back. Until they are forgotten an administrator may
// decide one is not -- the account at the directory is somebody else's now --
// and could not: the count asked the tenant of a holder who is not there, and
// the erase was `NotFound: Holder not found`, about a person the caller never
// named (#279). Their address and their number had no such count and were
// erased all along.
//
// They are erased, and nothing signs them in, so no account of theirs is the
// last way anybody has. It is taken away by whoever could have taken it while
// they were there: the two rules above the count read what the person holds
// and where the account is, and neither needs them to be there to be read.
// Brought back afterwards, they are who they were without it.
//
// # And what a count on its own cannot say
//
// A count is a read, and the erase after it is a write. Two callers unlinking a
// person's last two identities each count before either writes, each see the
// other's still live, and both go through -- so the state this exists to refuse
// is reached by asking for it twice at the same time. Forty people, each
// unlinked twice at once, lost 39 on PostgreSQL and four on SQLite. It was not
// a narrow window and it was not one dialect's.
//
// The count and the erase share a transaction now, with one write both callers
// make before either counts. [Core.only] is that, and it is also where the
// first attempt went wrong -- a version precondition with no write beside it is
// a read with an opinion.
func (s coreIdentity) Erase(ctx context.Context, req *app.IdentityRef) (*app.IdentityEraseResponse, error) {
	// By the spelling the row has, where the reference is a subject.
	req, err := s.spelled(ctx, req)
	if err != nil {
		return nil, err
	}

	// The row first, because the reference may be a subject and the count is
	// about the person. Through `Next()` rather than a client, so the wall
	// narrows it exactly as the erase below will: a caller who cannot see it
	// gets the same answer either way.
	v, err := s.IdentityServiceServer.Get(ctx, app.IdentityGetRequest_builder{
		Ref: req,
		Select: app.IdentitySelect_builder{
			Holder:   app.HolderSelect_builder{}.Build(),
			Provider: z.Ptr(true),
			TenantId: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Erasing what is not there succeeds, which is the rule the
			// generated `Erase` already states and this must not change.
			//
			// And `erased` is false, which is the same answer the generated one
			// gives for a row that was already gone: the RPC does not fail, and
			// it does not pretend either.
			return app.IdentityEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	// Whose it is, before how many they have left: an identity is taken from
	// somebody by whoever may write one onto them, and by them.
	if err := s.mayTakeAWayIn(ctx, "ref", v.GetHolder().GetId()); err != nil {
		return nil, err
	}

	// And not by them, nor on a grant made in a site, at the connection their
	// directory provisions through: the account is what makes them somebody
	// it speaks for, and without it its word that they have left is refused
	// ([coreIdentity.directorys]).
	tenant, err := pdid.From(v.GetTenantId())
	if err != nil {
		return nil, err
	}
	if err := s.directorys(ctx, "ref", tenant, v.GetProvider(), accountTakenAway); err != nil {
		return nil, err
	}

	// The count and the erase together, or neither.
	//
	// See [Core.only] for why a count on its own decides nothing, and what the
	// two callers contend for.
	var out *app.IdentityEraseResponse
	err = s.only(ctx, v.GetHolder().GetId(), func(next app.Server, _ dialect.Driver) error {
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

// notTheirLastWayIn counts what would be left, and counts what somebody could
// sign in by ([coreIdentity.Erase] says which rows those are).
func (s coreIdentity) notTheirLastWayIn(ctx context.Context, holder, erasing []byte) error {
	if len(holder) == 0 {
		return nil
	}

	ref := app.HolderRef_builder{Id: holder}.Build()

	// Their tenant, for its word on both halves: which providers it has a
	// connection to, and whether it takes a password.
	who, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: ref,
		Select: app.HolderSelect_builder{
			Tenant: app.TenantSelect_builder{Config: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		// Nobody there to be left with nothing. Where that is somebody
		// erased, it is not their last way in, and the erase goes on
		// ([coreIdentity.Erase], *Somebody erased has no last way in*); any
		// other reading of it is the refusal it was.
		gone, failed := s.gone(ctx, erasing)
		if failed != nil {
			return failed
		}
		if gone {
			return nil
		}
	}
	if err != nil {
		return err
	}
	tenant := who.GetTenant()

	doors, err := s.connected(ctx, tenant.GetId())
	if err != nil {
		return err
	}

	// Every page: one more identity than a page holds is somebody whose way
	// in was not looked at.
	after := ""
	for {
		ids, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{Holder: ref}.Build(),
			},
			After: after,
		}.Build())
		if err != nil {
			return err
		}
		for _, v := range ids.GetItems() {
			if bytesEq(v.GetId(), erasing) {
				continue
			}
			if _, ok := doors[v.GetProvider()]; ok {
				return nil
			}
		}

		after = ids.GetNext()
		if after == "" {
			break
		}
	}

	// No other identity anybody signs in by, so a password is the only thing
	// that could be left. Read through the unwalled half of nothing -- this
	// layer is on both stacks, and the credential is the person's own row
	// behind the same wall the identity was.
	after = ""
	for {
		creds, err := s.Next().Credential().List(ctx, app.CredentialListRequest_builder{
			Filters: []*app.CredentialFilter{
				app.CredentialFilter_builder{Holder: ref}.Build(),
			},
			After: after,
		}.Build())
		if err != nil {
			return err
		}
		for _, v := range creds.GetItems() {
			// The kind decides, not the row: a seed is a credential and is
			// not a way in ([vouch.Begins]). And the tenant decides about the
			// kind: a password it does not take opens nothing
			// ([vouch.Offers]).
			if vouch.Begins(v.GetKind()) && vouch.Offers(tenant, v.GetKind()) {
				return nil
			}
		}

		after = creds.GetNext()
		if after == "" {
			break
		}
	}

	return status.Error(codes.FailedPrecondition,
		"this is the only way they can sign in; give them another before taking it away")
}

// gone is whether the person an identity is a way into has been erased.
//
// Read as every reader of an edge reads it (`vouch.Server.row`,
// `keys.signable`): an edge is loaded through the rows still there, so
// somebody erased comes back as a bare reference, and every holder has a
// tenant, so one that comes back with none was not found. Asked of the
// identity and not of the holder, because a holder who cannot be read is one
// of two things -- erased, or not this caller's to see -- and only the first
// is nobody.
func (s coreIdentity) gone(ctx context.Context, identity []byte) (bool, error) {
	v, err := s.IdentityServiceServer.Get(ctx, app.IdentityGetRequest_builder{
		Ref: app.IdentityRef_builder{Id: identity}.Build(),
		Select: app.IdentitySelect_builder{
			Holder: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return false, err
	}

	return len(v.GetHolder().GetTenant().GetId()) == 0, nil
}

// connected is the names of a tenant's connections: the providers a front
// door of roster's signs its people in through, and so the ones an identity
// at is a way in.
func (s Core) connected(ctx context.Context, tenant []byte) (map[string]struct{}, error) {
	out := map[string]struct{}{}

	after := ""
	for {
		vs, err := s.Next().Connection().List(ctx, app.ConnectionListRequest_builder{
			Filters: []*app.ConnectionFilter{app.ConnectionFilter_builder{
				Tenant: app.TenantRef_builder{Id: tenant}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			out[v.GetName()] = struct{}{}
		}

		after = vs.GetNext()
		if after == "" {
			return out, nil
		}
	}
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
