package core

import (
	"context"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

// The narrow write, and the one a caller is given.
//
// # Why `Patch` is not it
//
// `Patch` and `Apply` write anything the schema holds, which is why payday
// closes them at the transport unless a deployment says otherwise: *what a
// caller may change, and under what conditions, is not something a general
// write can be told.*
//
// This can be told, which is what makes it a different method rather than a
// looser setting. Two fields, both of them things a holder carries **about
// itself**, and neither of them anything the wall, the trail or a permission
// reads. Nothing here moves somebody between tenants, renames them into
// somebody else's alias, or changes what they may do.
//
// # Why it is implemented with `Patch`
//
// Because that is what `Patch` is for. payday closes it at the transport and
// not in the stack, and says so: *an RPC written by hand goes on being
// implemented with them.* So this is the whole implementation -- the narrowing
// is the request shape, and the write underneath is the one that already knows
// about versions, the trail and the wall.
//
// # Why it is on `HolderService` and not a service of its own
//
// It is a write on a holder. A second service would be a second name for the
// same rows, and the overlay mechanism exists for exactly this -- nothing had
// used it until now.

type coreHolder struct {
	Core
	app.HolderServiceServer
}

func (s Core) Holder() app.HolderServiceServer { return coreHolder{s, s.Next().Holder()} }

func (s coreHolder) Update(ctx context.Context, req *app.HolderUpdateRequest) (*app.Holder, error) {
	patch := app.HolderPatchRequest_builder{
		Ref: req.GetRef(),

		// Carried through rather than dropped: a write against a row that has
		// moved is refused rather than applied to whatever it became. What
		// `Patch` does with an empty one is `Patch`'s business, and this is not
		// the place to have a second opinion about it.
		DateUpdated: req.GetDateUpdated(),
	}

	// Replaced whole, both of them, which is the shape each was chosen for: one
	// fact from one moment, whatever last told us. Sending neither changes
	// neither -- so a caller updating a profile does not have to know what is
	// in `data`, and cannot erase it by not knowing.
	if req.HasProfile() {
		patch.Profile = req.GetProfile()
	}
	if req.HasData() {
		patch.Data = req.GetData()
	}

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

// Disable, Enable and Invalidate are the two facts an operator writes about
// somebody, and neither is a thing that holder carries about itself.
//
// They are three methods rather than one taking a value, because a role is a
// list of methods: what a deployment can grant is exactly what it can name, and
// "may suspend somebody" and "may reinstate them" are two names. Written as one
// method with a boolean they would be one grant, and a deployment that wanted
// them apart would have nothing to ask for.
//
// # Why they are implemented with `Patch`
//
// [coreHolder.Update]'s reason unchanged: payday closes `Patch` at the
// transport and not in the stack, so the narrowing is the request shape and the
// write underneath is the one that already knows about versions, the trail and
// the wall.
//
// # What is deliberately not here
//
// **Escalation.** Suspending an administrator is a denial of service and
// resetting their credential is a way to become them, and `escalate.go` covers
// the second of the two now -- `Core.mayReach`, chosen in D28 rather than
// assumed here, guards every credential write and every way in beside it.
//
// The first is still nobody's. Somebody who may `Disable` an administrator
// cannot become them; they can only stop them, and D26 says that is a real gap
// and a different one.
//
// **A password change does not invalidate.** *A password reset that leaves old
// sessions alive is not a reset* is true, and it belongs with the recovery
// work that has the reset in it: coupling it here would make somebody changing
// their own password sign themselves out of everything with nothing having said
// so.
func (s coreHolder) Disable(ctx context.Context, req *app.HolderDisableRequest) (*app.Holder, error) {
	patch := app.HolderPatchRequest_builder{
		Ref:          req.GetRef(),
		DateDisabled: timestamppb.Now(),
	}
	lock(&patch, req.GetDateUpdated())

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

func (s coreHolder) Enable(ctx context.Context, req *app.HolderEnableRequest) (*app.Holder, error) {
	patch := app.HolderPatchRequest_builder{
		Ref:              req.GetRef(),
		DateDisabledNull: z.Ptr(true),
	}
	lock(&patch, req.GetDateUpdated())

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

// lock carries the caller's version through, or declines the check when they
// gave none.
//
// `Update` cannot do this and these three cannot do otherwise, and the
// difference is what the write is. `Update` replaces a value the caller read,
// so a version is the only thing standing between two editors and a lost one --
// payday refuses an omitted one rather than assuming, *because an unset field
// cannot be told apart from a caller who never considered locking at all*.
//
// These are not that. They write one column each, to a value that does not
// depend on what was there, and each of them is somebody deciding something
// about a person rather than editing them. Requiring a version would mean a
// suspension or a sign-out-everywhere that **fails because somebody edited a
// profile** -- which makes editing a profile in a loop a way to prevent being
// suspended. A security action that can lose a race is one that can be
// prevented.
//
// A caller who has read the row may still send the version and get the check,
// which is why this is a fallback rather than a rule.
func lock(patch *app.HolderPatchRequest_builder, was *timestamppb.Timestamp) {
	if was != nil {
		patch.DateUpdated = was

		return
	}

	patch.DateUpdatedForce = z.Ptr(true)
}

// Invalidate stamps now, and takes no time from anybody.
//
// Monotonic without reading the row first, which is what the missing argument
// buys: nothing can have written a value in the future, so `now` is never
// behind what is stored. A caller-supplied time would need the read, and would
// still leave un-revoking expressible.
func (s coreHolder) Invalidate(ctx context.Context, req *app.HolderInvalidateRequest) (*app.Holder, error) {
	patch := app.HolderPatchRequest_builder{
		Ref:             req.GetRef(),
		DateInvalidated: timestamppb.Now(),
	}
	lock(&patch, req.GetDateUpdated())

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

// SignsIn answers how somebody signs in, without the verifier.
//
// # Why it is here rather than in `server/me`
//
// `MeService` reads through no wall, and what makes that safe is that it takes
// nothing: it cannot be pointed at anybody else. This takes a subject, so it is
// the ordinary question and gets the ordinary answer -- behind the wall, which
// is what this layer is already inside.
//
// # And why it reads through the stack rather than through ent
//
// `server/me` goes to the ent client because it is answering about the caller
// and there is nothing to narrow. Here there is: the wall has to decide whether
// this holder is one the caller may see at all, and whether their rows are.
// Reading through `Next()` is how that happens without this file knowing what a
// tenant is.
func (s coreHolder) SignsIn(ctx context.Context, req *app.HolderSignsInRequest) (*app.HolderSignsInResponse, error) {
	// The holder first, so that somebody outside the caller's tenant is
	// `NotFound` rather than an empty list -- which would say "here, and with
	// no way in" about a person the caller cannot see.
	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	who := app.HolderRef_builder{Id: v.GetId()}.Build()

	ids, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{
		Filters: []*app.IdentityFilter{
			app.IdentityFilter_builder{Holder: who}.Build(),
		},
	}.Build())
	if err != nil {
		return nil, err
	}

	creds, err := s.Next().Credential().List(ctx, app.CredentialListRequest_builder{
		Filters: []*app.CredentialFilter{
			app.CredentialFilter_builder{Holder: who}.Build(),
		},
	}.Build())
	if err != nil {
		return nil, err
	}

	res := app.HolderSignsInResponse_builder{}
	for _, i := range ids.GetItems() {
		res.Identities = append(res.Identities, app.SignInIdentity_builder{
			Id:          i.GetId(),
			Provider:    i.GetProvider(),
			Subject:     i.GetSubject(),
			DateCreated: i.GetDateCreated(),
		}.Build())
	}
	for _, c := range creds.GetItems() {
		// The fields are written out, so the verifier is absent rather than
		// deselected -- there is no `Select` here to get wrong, and the shape
		// itself is the statement D13 makes by not registering the service.
		f := app.SignInCredential_builder{Kind: c.GetKind(), Name: c.GetName()}
		if u := c.GetDateRotated(); u != nil {
			f.DateRotated = u
		}
		if u := c.GetDateLocked(); u != nil && u.AsTime().After(time.Now()) {
			f.DateLocked = u
		}

		res.Credentials = append(res.Credentials, f.Build())
	}

	keys, err := s.Next().ApiKey().List(ctx, app.ApiKeyListRequest_builder{
		Filters: []*app.ApiKeyFilter{
			app.ApiKeyFilter_builder{Holder: who}.Build(),
		},
	}.Build())
	if err != nil {
		return nil, err
	}
	for _, k := range keys.GetItems() {
		// Written out like the credentials above, and for the identical
		// reason: the verifier is **absent** rather than deselected. There is
		// no `Select` here to get wrong, and the shape is the statement.
		f := app.SignInKey_builder{
			Id:      k.GetId(),
			Alias:   k.GetAlias(),
			Methods: k.GetMethods(),
		}
		if u := k.GetDateExpires(); u != nil {
			f.DateExpires = u
		}
		if u := k.GetDateUsed(); u != nil {
			f.DateUsed = u
		}

		res.Keys = append(res.Keys, f.Build())
	}

	return res.Build(), nil
}

// RevokeKey ends one key of one person's, and refuses one that is not theirs.
//
// The read is the whole of the rule. `ApiKey` narrows by its holder's tenant,
// so an identifier alone would be an argument that reaches every key in it --
// and the reference is what makes this a *which* within a *whose*: the holder
// is resolved through the wall first, exactly as `SignsIn` resolves it, so
// somebody outside the caller's tenant is `NotFound` before any key is named.
//
// The erase goes through `Next()` and not a client, so it is recorded and
// narrowed like every other write. There is no soft/hard question here: an
// `ApiKey` is soft-erased like everything else, and what stops a revoked key
// working is that `keys.findKey` reads through a reference that reaches only
// the rows still there.
func (s coreHolder) RevokeKey(ctx context.Context, req *app.HolderRevokeKeyRequest) (*app.HolderRevokeKeyResponse, error) {
	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if len(req.GetId()) == 0 {
		return nil, pderr.Invalidf("id", "which key")
	}

	// Theirs, or nothing. The holder is the first predicate and the identifier
	// the second, which is the order that makes the answer about the person --
	// the same order `MeService.Unlink` reads in, for the same reason.
	vs, err := s.Next().ApiKey().List(ctx, app.ApiKeyListRequest_builder{
		Filters: []*app.ApiKeyFilter{
			app.ApiKeyFilter_builder{
				Holder: app.HolderRef_builder{Id: v.GetId()}.Build(),
			}.Build(),
		},
	}.Build())
	if err != nil {
		return nil, err
	}

	for _, k := range vs.GetItems() {
		if !bytesEq(k.GetId(), req.GetId()) {
			continue
		}

		if _, err := s.Next().ApiKey().Erase(ctx,
			app.ApiKeyRef_builder{Id: k.GetId()}.Build()); err != nil {
			return nil, err
		}

		return app.HolderRevokeKeyResponse_builder{}.Build(), nil
	}

	return nil, status.Error(codes.NotFound, "no such key")
}
