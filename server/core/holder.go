package core

import (
	"context"

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
