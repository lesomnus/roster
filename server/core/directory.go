package core

import (
	"context"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/quote"
)

// A directory's say over the people it provisions: `Provision`, `Deactivate`
// and `Activate`, which `holder_svc.ext.proto` argues for one at a time, and
// `Holder.directory`, which says whose a suspension is. And `Tell`, which is
// what it says about anybody and is believed about nobody.
const (
	directoryInactive = "inactive"
	directoryDeleted  = "deleted"
)

// provisioning is the connection a tenant's directory provisions its people
// through (`Connection.provisions`). FailedPrecondition when there is none:
// a directory with nowhere to link the people it makes has nothing to do.
func (s Core) provisioning(ctx context.Context, tenant pdid.Id) (*app.Connection, error) {
	after := ""
	for {
		vs, err := s.Next().Connection().List(ctx, app.ConnectionListRequest_builder{
			Filters: []*app.ConnectionFilter{app.ConnectionFilter_builder{
				Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if v.GetProvisions() {
				return v, nil
			}
		}

		after = vs.GetNext()
		if after == "" {
			return nil, status.Error(codes.FailedPrecondition,
				"no connection of this tenant provisions its people; `Connection.provisions` names the one a directory does")
		}
	}
}

// speaksFor refuses somebody their directory does not sign in: a person with
// no identity at the connection it provisions through. So what a directory may
// suspend is the people who arrive through it -- not an app's holder, not the
// front door's, not somebody who signs in another way.
func (s Core) speaksFor(ctx context.Context, holder []byte, tenant pdid.Id) error {
	c, err := s.provisioning(ctx, tenant)
	if err != nil {
		return err
	}

	after := ""
	for {
		vs, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{
				Holder: app.HolderRef_builder{Id: holder}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return err
		}
		for _, v := range vs.GetItems() {
			if v.GetProvider() == c.GetName() {
				return nil
			}
		}

		after = vs.GetNext()
		if after == "" {
			return status.Errorf(codes.PermissionDenied,
				"the directory speaks for the people who sign in through %s, and this is not one of them", c.GetName())
		}
	}
}

// Provision makes somebody a directory says exists, with their identity and
// their address, in one write or none.
//
// Every write goes through this layer's own rules -- the identity is held to
// its connection's claim, the address to its normal form, both to
// [Core.mayWriteAWayIn] -- over a stack bound to one transaction, as
// `Tenant.Add` writes its five. The way-in rules pass because the person is
// new and holds nothing; that they are new is what makes this safe to hold
// where `Identity.Add` is not.
//
// The identity is written by [coreIdentity.add] and not by `Add`: who may say
// somebody has an account at this connection is what `Add` asks there
// ([coreIdentity.directorys]), and this verb is the directory saying so.
func (s coreHolder) Provision(ctx context.Context, req *app.HolderProvisionRequest) (*app.Holder, error) {
	t, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    req.GetTenant(),
		Select: app.TenantSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(t.GetId())
	if err != nil {
		return nil, err
	}

	c, err := s.provisioning(ctx, tenant)
	if err != nil {
		return nil, err
	}
	// Asked again where the way in is written, of a row the connection's own
	// writes never let through: one from before the rule, or written past it.
	// Suspending is left to work there -- a leaver stopped is never the danger.
	if who := openTo(c.GetIssuer()); who != "" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%s, the connection this tenant provisions through, signs in %s; a directory provisions through its own organisation's door",
			c.GetName(), who)
	}
	if req.GetProvider() != c.GetName() {
		return nil, pderr.Invalidf("provider",
			"%s is not the connection this tenant provisions through, which is %q", quote.Sent(req.GetProvider()), c.GetName())
	}
	if err := subjectIsStable(req.GetSubject()); err != nil {
		return nil, err
	}
	if subjectClaimOf(c.GetSubjectClaim()) == claimOid && !front.GUID(req.GetSubject()) {
		return nil, pderr.Invalidf("subject",
			"the %s connection names people by `oid`, which is a GUID, and %s is not one", c.GetName(), quote.Sent(req.GetSubject()))
	}
	if a := req.GetAddress(); a != "" {
		if err := mailboxed("address", a); err != nil {
			return nil, err
		}
	}
	if req.GetAlias() == "" {
		return nil, pderr.Invalidf("alias", "what roster calls them; a directory names one from their address")
	}
	alias, err := s.freeAlias(ctx, t.GetId(), req.GetAlias())
	if err != nil {
		return nil, err
	}

	var out *app.Holder
	run := func(at Core) error {
		h, err := coreHolder{at, at.Next().Holder()}.add(ctx, app.HolderAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:   alias,
			Name:    req.GetName(),
			Profile: req.GetProfile(),
		}.Build())
		if err != nil {
			return err
		}
		ref := app.HolderRef_builder{Id: h.GetId()}.Build()

		if _, err := (coreIdentity{at, at.Next().Identity()}).add(ctx, app.IdentityAddRequest_builder{
			Holder:   ref,
			Provider: req.GetProvider(),
			Subject:  req.GetSubject(),
		}.Build()); err != nil {
			return err
		}
		if a := req.GetAddress(); a != "" {
			// Unverified, on purpose: see `HolderService.Provision`.
			if _, err := at.Email().Add(ctx, app.EmailAddRequest_builder{
				Holder:  ref,
				Address: a,
			}.Build()); err != nil {
				return err
			}
		}

		out = h

		return nil
	}

	if s.drv == nil {
		if err := run(s.Core); err != nil {
			return nil, err
		}

		return out, nil
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
	if err := run(s.over(next, drv)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return out, nil
}

// freeAlias is `want`, or `want` with a suffix when somebody has it -- the
// rule a sign-in that enrols somebody follows, so two people named alike are
// two people rather than a refusal the directory retries forever.
//
// A read and not a guarantee: two at once may pick the same, and the second
// `Add` is refused as taken, which the directory retries.
func (s coreHolder) freeAlias(ctx context.Context, tenant []byte, want string) (string, error) {
	try := want
	for range 8 {
		_, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(try),
				Tenant: app.TenantRef_builder{Id: tenant}.Build(),
			}.Build()}.Build(),
			Select: app.HolderSelect_builder{}.Build(),
		}.Build())
		switch status.Code(err) {
		case codes.NotFound:
			return try, nil
		case codes.OK:
		default:
			return "", err
		}

		try = want + "-" + slug.RandomAliasN(4)
	}

	return "", status.Errorf(codes.AlreadyExists, "alias: %s and every suffix tried are taken", quote.Sent(want))
}

// Deactivate suspends somebody on their directory's word.
//
// An operator's suspension stays the operator's: the directory saying
// `inactive` about somebody already suspended changes nothing, because what
// it may lift afterwards is only its own. `deleted` is written over it all the
// same -- the directory will not speak of them again, and that is no power to
// lift anything (`Activate` refuses the deleted).
func (s coreHolder) Deactivate(ctx context.Context, req *app.HolderDeactivateRequest) (*app.Holder, error) {
	return s.said(ctx, req.GetRef(), req.GetDateUpdated(), func(v *app.Holder) (*app.HolderPatchRequest_builder, error) {
		patch := &app.HolderPatchRequest_builder{}
		switch {
		case req.GetDeleted():
			patch.Directory = z.Ptr(directoryDeleted)
		case v.GetDirectory() == directoryDeleted:
			return nil, nil
		case v.GetDateDisabled() != nil && v.GetDirectory() == "":
			return nil, nil
		default:
			patch.Directory = z.Ptr(directoryInactive)
		}
		if v.GetDateDisabled() == nil {
			patch.DateDisabled = timestamppb.Now()
		}

		return patch, nil
	})
}

// Activate lifts the suspension the directory made, and refuses the rest.
func (s coreHolder) Activate(ctx context.Context, req *app.HolderActivateRequest) (*app.Holder, error) {
	return s.said(ctx, req.GetRef(), req.GetDateUpdated(), func(v *app.Holder) (*app.HolderPatchRequest_builder, error) {
		switch v.GetDirectory() {
		case directoryInactive:
		case directoryDeleted:
			return nil, status.Error(codes.FailedPrecondition,
				"the directory deleted them; bringing them back is an operator's, with `holder enable`")
		default:
			if v.GetDateDisabled() != nil {
				return nil, status.Error(codes.PermissionDenied,
					"an operator suspended them, and that is not the directory's to lift")
			}

			return nil, nil
		}

		return &app.HolderPatchRequest_builder{
			DateDisabledNull: z.Ptr(true),
			Directory:        z.Ptr(""),
		}, nil
	})
}

// said is what a directory says of somebody's standing, decided and written
// as one act: `decide` is handed the row as it is behind a lock on it, and
// answers the write to make, or none.
//
// # Why it is one act
//
// Both verbs decide on what the row says -- whose suspension it is -- and
// they read it, decided, and then wrote, with no version and behind nothing.
// So what they decided on was a moment old by the time they wrote, and an
// operator's `Disable` landing in that moment answered OK and was written
// over: `Activate` lifted a suspension the operator had just made, and
// `Deactivate` called it the directory's, to be lifted the next time the
// directory said `active`. Seen on a run, with the operator's call beginning
// within two milliseconds after the directory's: 111 of 200 people left not
// suspended, and 119 of 200 suspensions left the directory's.
//
// # Why a lock, and not the version that was read
//
// A write made on the version read would be refused when anything moved the
// row in between, and anything includes the person: [lock] says why a
// suspension takes no version -- editing a profile in a loop would be a way
// not to be suspended -- and a directory's word that somebody has left is a
// suspension. So the row is locked first ([Core.only], the write the count of
// somebody's ways in contends on), read behind the lock, and written in the
// same transaction. An operator's write is one statement on that row: it
// lands before the lock, and is what `decide` sees, or it waits for the
// commit, and is last. Either way it stands.
//
// # The caller's version
//
// The lock moves the row's version, so a version the caller sent is compared
// with the row as it was read before the lock, and the decision is made on
// the row as it is behind it. The directory's endpoint sends none.
//
// Who they are and whether the directory speaks for them is asked before the
// lock ([coreHolder.directed]): none of it is what these two write. And a
// call that would write nothing, or be refused, by the row as it was read
// takes no lock at all: the directory says the same thing about everybody
// whenever it is set going again, and the lock is a write.
func (s coreHolder) said(ctx context.Context, ref *app.HolderRef, was *timestamppb.Timestamp,
	decide func(v *app.Holder) (*app.HolderPatchRequest_builder, error),
) (*app.Holder, error) {
	v, err := s.directed(ctx, ref)
	if err != nil {
		return nil, err
	}
	if was != nil && !was.AsTime().Equal(v.GetDateUpdated().AsTime()) {
		return nil, status.Error(codes.FailedPrecondition,
			"date_updated: they have changed since the version you sent; read them again")
	}

	// Nothing to write, or a refusal, by what the row says now: answered as it
	// is, with no lock taken. Neither writes anything to be wrong about.
	switch patch, err := decide(v); {
	case err != nil:
		return nil, err
	case patch == nil:
		return v, nil
	}
	who := app.HolderRef_builder{Id: v.GetId()}.Build()

	var out *app.Holder
	err = s.only(ctx, v.GetId(), func(next app.Server, _ dialect.Driver) error {
		now, err := next.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: who,
			Select: app.HolderSelect_builder{
				Tenant:       app.TenantSelect_builder{}.Build(),
				DateDisabled: z.Ptr(true),
				Directory:    z.Ptr(true),
				DateUpdated:  z.Ptr(true),
			}.Build(),
		}.Build())
		if err != nil {
			return err
		}

		patch, err := decide(now)
		if err != nil {
			return err
		}
		if patch == nil {
			out = now

			return nil
		}
		patch.Ref = who
		patch.DateUpdatedForce = z.Ptr(true)

		out, err = next.Holder().Patch(ctx, patch.Build())

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// Tell keeps what a directory calls somebody -- its id for them, their address
// -- beside them, and changes nothing else: no identity, no address, nothing a
// rule reads (`Holder.directory_subject`, `Holder.directory_address`).
//
// Not held to [Core.speaksFor]. The people a directory does not speak for yet
// -- matched by their address, never linked -- are the ones whose `externalId`
// it has nowhere else to put. Held to what makes the word a directory's at
// all: the tenant provisions through a connection, and the person is not a
// row a file declares.
//
// What is said is held to the form the real thing would be -- a subject to
// the connection's claim, an address to its normal form -- so that an operator
// who believes it can write it as it stands. Saying what is already kept
// writes nothing.
//
// Never of the caller's own row, which is refused as `Deactivate` of it is. A
// directory says what it calls the people it speaks of, and it is not one of
// them: what its key said of its own holder, `roster scim adopt` listed and
// linked, and the directory's holder then had an account at the connection.
// It is whoever holds the verb, and not the endpoint's key alone.
func (s coreHolder) Tell(ctx context.Context, req *app.HolderTellRequest) (*app.Holder, error) {
	if err := s.declaredHolder(ctx, req.GetRef()); err != nil {
		return nil, err
	}

	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.HolderSelect_builder{
			Tenant:           app.TenantSelect_builder{}.Build(),
			DirectorySubject: z.Ptr(true),
			DirectoryAddress: z.Ptr(true),
			DirectoryName:    z.Ptr(true),
			DateUpdated:      z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if f, ok := frame.From(ctx); ok && bytesEq(v.GetId(), f.Actor.Bytes()) {
		return nil, status.Error(codes.PermissionDenied,
			"a directory says what it calls the people it speaks of, and it is not one of them: this is the caller's own row")
	}
	tenant, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return nil, err
	}
	c, err := s.provisioning(ctx, tenant)
	if err != nil {
		return nil, err
	}

	patch := app.HolderPatchRequest_builder{
		Ref: app.HolderRef_builder{Id: v.GetId()}.Build(),
	}
	said := false

	// In the spelling the real thing would be kept in ([front.Subject]), which
	// is the one it is looked for in: said in another, it is the same word.
	claim := subjectClaimOf(c.GetSubjectClaim())
	if subject := front.Subject(claim, req.GetSubject()); req.HasSubject() && subject != v.GetDirectorySubject() {
		switch subject {
		case "":
			patch.DirectorySubjectNull = z.Ptr(true)
		default:
			if err := subjectIsStable(subject); err != nil {
				return nil, err
			}
			if claim == claimOid && !front.GUID(subject) {
				return nil, pderr.Invalidf("subject",
					"the %s connection names people by `oid`, which is a GUID, and %s is not one", c.GetName(), quote.Sent(subject))
			}
			patch.DirectorySubject = z.Ptr(subject)
		}
		said = true
	}
	if req.HasAddress() && req.GetAddress() != v.GetDirectoryAddress() {
		switch address := req.GetAddress(); address {
		case "":
			patch.DirectoryAddressNull = z.Ptr(true)
		default:
			if err := mailboxed("address", address); err != nil {
				return nil, err
			}
			patch.DirectoryAddress = z.Ptr(address)
		}
		said = true
	}
	if req.HasName() && req.GetName() != v.GetDirectoryName() {
		switch name := req.GetName(); name {
		case "":
			patch.DirectoryNameNull = z.Ptr(true)
		default:
			// Compared without regard to case, so kept in the one form a
			// lookup is made in: an address's, which most of them are.
			if err := normalised("name", name, front.Address); err != nil {
				return nil, err
			}
			patch.DirectoryName = z.Ptr(name)
		}
		said = true
	}
	if !said {
		return v, nil
	}
	lock(&patch, req.GetDateUpdated())

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

// directed is somebody a directory may speak for, read with what deciding
// about their suspension needs: never a row a file declared, and only a
// person who signs in through the connection the directory provisions.
func (s coreHolder) directed(ctx context.Context, ref *app.HolderRef) (*app.Holder, error) {
	if err := s.declaredHolder(ctx, ref); err != nil {
		return nil, err
	}

	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref: ref,
		Select: app.HolderSelect_builder{
			Tenant:       app.TenantSelect_builder{}.Build(),
			DateDisabled: z.Ptr(true),
			Directory:    z.Ptr(true),
			DateUpdated:  z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	tenant, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.speaksFor(ctx, v.GetId(), tenant); err != nil {
		return nil, err
	}

	return v, nil
}
