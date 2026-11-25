package core

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	sqlpage "github.com/protobuf-orm/ent/dialect/sql/sqlpage"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/portrait"
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
// somebody else's alias, or changes what they may do. And of the profile, what
// an organisation says of somebody is not theirs to change on their own row
// ([Organisational]).
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
	got, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref: req.GetRef(), Select: app.HolderSelect_builder{Labels: z.Ptr(true), Profile: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}
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
		// What their organisation says of them is kept, where the row is the
		// caller's own (`own.go`).
		whose, err := pdid.From(got.GetId())
		if err != nil {
			return nil, err
		}
		if err := s.keepsWhatTheOrganisationSays(ctx, whose, got.GetProfile(), req.GetProfile()); err != nil {
			return nil, err
		}
		patch.Profile = req.GetProfile()

		// One picture in two forms (`holder_svc.ext.proto`): a profile naming
		// a different one leaves the portrait a copy of the old, so it goes.
		// The URL is checked only when it changes: a save that does not touch
		// the picture is not refused for it.
		if now := req.GetProfile().GetPicture(); now != got.GetProfile().GetPicture() {
			if err := portrait.CheckURL(now); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "profile.picture: %s", err)
			}
			patch.Portrait = none()
		}
	}
	if req.HasData() {
		patch.Data = req.GetData()
	}

	return versioned(s.HolderServiceServer.Patch(ctx, patch.Build()))
}

// Realias writes the index, which is what makes it a method of its own and not
// a field on `Update`: `(alias, tenant)` is what every reference resolves
// through, so a caller who may change it can make a script, a bookmark and
// somebody else's runbook stop finding a person.
//
// The version is carried through and not made optional. Unlike `Disable` and
// the two beside it, this **replaces a value the caller read** -- somebody else
// may have renamed the same row in between, and two editors reaching two names
// is exactly what a lost update is.
//
// Nothing else is checked of the name here, and that is the point of it being
// a narrow write: an alias that is not one is refused by the grammar payday already
// holds every alias to, one written otherwise than it is read by the rule
// every write of an alias meets ([spelled]), and one somebody in this tenant
// already has by the unique index that was there before this method was.
//
// **No `mayReach`, and that is the same answer `Disable` gives.** Renaming an
// administrator does not let anybody become them; it only makes them harder to
// find, which is D26's shape -- a denial of service that is a real gap and a
// different one. What it is *not* is escalation, and `escalate.go` guards the
// writes that are: a credential, a way in. If this grew one it would have to
// grow one on `Disable` first, and that decision is written down over there
// rather than taken here by a method that arrived later.
//
// # And not a row a file declares
//
// `Update` and `Disable` of one are refused, and this asked nothing: a declared
// holder was renamed, OK. What applies the file finds its holder by the name
// the file says (`cli/provision.go`), so the next start found nobody by it and
// made a second, and the first kept the role, the binding and the label of a
// row the configuration no longer knew. Refused as they are
// ([Core.mayWriteDeclared]). `Invalidate` of one is not: it edits nothing a
// file wrote.
func (s coreHolder) Realias(ctx context.Context, req *app.HolderRealiasRequest) (*app.Holder, error) {
	if err := s.declaredHolder(ctx, req.GetRef()); err != nil {
		return nil, err
	}

	return versioned(s.HolderServiceServer.Patch(ctx, app.HolderPatchRequest_builder{
		Ref:         req.GetRef(),
		Alias:       z.Ptr(req.GetAlias()),
		DateUpdated: req.GetDateUpdated(),
	}.Build()))
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
// The first is still nobody's: somebody who may `Disable` an administrator
// cannot become them; they can only stop them, and D26 says that is a real gap
// and a different one. Except for the last of them -- a suspension that leaves
// the tenant with nobody administering it is refused (`administered.go`),
// since nobody in the tenant could undo it.
//
// **A password change invalidates, where it is somebody's own.** *A password
// reset that leaves old sessions alive is not a reset* was held to be true of a
// reset alone, and somebody changing their own was left signed in everywhere,
// so as not to sign them out with nothing having said so. But a password is
// changed because somebody else may have it, and that somebody was signed in
// already (`coreCredential.Set`, #275). A password the box writes for somebody
// ends nothing, as before: `reset` is the one that does.
func (s coreHolder) Disable(ctx context.Context, req *app.HolderDisableRequest) (*app.Holder, error) {
	for try := 0; ; try++ {
		// The row's labels, for [coreHolder.declaredHolder]'s question; and
		// whether they are suspended already, with the version that says so.
		got, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
			Ref: req.GetRef(),
			Select: app.HolderSelect_builder{
				Labels: z.Ptr(true), DateDisabled: z.Ptr(true), DateUpdated: z.Ptr(true),
			}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}
		if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
			return nil, err
		}

		patch := app.HolderPatchRequest_builder{
			Ref: req.GetRef(),

			// The suspension is the operator's now, whoever made it: a
			// directory lifts only what it put there
			// (`Holder.directory_standing`).
			DirectoryStanding: z.Ptr(app.DirectoryStanding_DIRECTORY_STANDING_UNSPECIFIED),
		}

		// Since when is when they were first suspended, so somebody suspended
		// already keeps the moment they have. Written again each time, it
		// moved: somebody stopped in March and suspended again in June had
		// been suspended since June. The moment an address was first proved
		// stays for the same reason ([coreEmail.Confirm]), and a directory's
		// `Deactivate` has always left it.
		//
		// Leaving the moment out is the one thing here that depends on what
		// was read, so that write -- and no other -- is made on the version
		// read: were the suspension lifted in between, by the directory that
		// made it, a write that only said whose it is would answer OK about
		// somebody who is not suspended. Refused for that, it is decided
		// again; and a row that will not hold still three times over is
		// suspended as it always was, by a write that asks nothing.
		was, kept := req.GetDateUpdated(), got.GetDateDisabled() != nil && try < 3
		switch {
		case !kept:
			patch.DateDisabled = timestamppb.Now()
		case was == nil:
			was = got.GetDateUpdated()
		}
		lock(&patch, was)

		// The write's own refusal is told from the rule's around it here,
		// where it is still the write's: a tenant left with nobody
		// administering it is `FailedPrecondition` too, and is no version.
		var out *app.Holder
		err = s.keepsTheirs(ctx, req.GetRef(), "suspending them", func(next app.Server) error {
			var err error
			out, err = versioned(next.Holder().Patch(ctx, patch.Build()))

			return err
		})
		if kept && req.GetDateUpdated() == nil && status.Code(err) == codes.Aborted {
			continue
		}

		return out, err
	}
}

func (s coreHolder) Enable(ctx context.Context, req *app.HolderEnableRequest) (*app.Holder, error) {
	if err := s.declaredHolder(ctx, req.GetRef()); err != nil {
		return nil, err
	}
	patch := app.HolderPatchRequest_builder{
		Ref:               req.GetRef(),
		DateDisabledNull:  z.Ptr(true),
		DirectoryStanding: z.Ptr(app.DirectoryStanding_DIRECTORY_STANDING_UNSPECIFIED),
	}
	lock(&patch, req.GetDateUpdated())

	return versioned(s.HolderServiceServer.Patch(ctx, patch.Build()))
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

// errMoved is what every verb that takes a version answers for one that has
// moved since the caller read it.
//
// `Aborted`, and not the `FailedPrecondition` the generated `Patch` answers
// it with: that is the code of every rule's refusal, which sending again does
// not mend, and this is the one refusal a caller mends by reading the row
// again and sending the write again. Over HTTP it is 409, and not the 400 of
// a request that cannot be taken as it is written. And one sentence, which
// names the field, wherever it is answered from.
var errMoved = status.Error(codes.Aborted,
	"date_updated: it has changed since the version you sent; read it again and send the write again")

// versioned is a verb's answer for a write it made with nothing but the
// caller's version as its condition: a condition that did not hold is that
// version having moved, and is answered [errMoved]; anything else is answered
// as it is.
//
// Each verb turns it here, at its own edge, because the generated `Patch`
// goes on answering what it answers in process, where a write with tests of
// its own reads `FailedPrecondition` as one of them failing (`Widen`,
// `Narrow`). A verb whose write has another condition beside the version
// tells the two apart itself ([coreTenant.written]); and a refusal of a rule
// made around the write is no version, so a verb that holds its write to one
// turns the write's answer inside it ([coreHolder.Disable]).
func versioned[T any](v T, err error) (T, error) {
	if status.Code(err) == codes.FailedPrecondition {
		return v, errMoved
	}

	return v, err
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

	return versioned(s.HolderServiceServer.Patch(ctx, patch.Build()))
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
	// no way in" about a person the caller cannot see. With the tenant they
	// are in, which is where the record of this read is filed.
	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// How somebody signs in is theirs to read, and whoever was told to read
	// everybody's ([Core.mayReadOf]): it is their keys and their factors by
	// name, which a list of keys is refused for the same.
	whose, err := pdid.From(v.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReadOf(ctx, "ref", whose); err != nil {
		return nil, err
	}

	who := app.HolderRef_builder{Id: v.GetId()}.Build()

	// On the access record, said here because nothing else can see it (#280).
	//
	// The record reads whose data a call carried off the messages that declare
	// a personal field, and this answers in messages of its own: the subject
	// `Identity.subject` declares personal comes back in a `SignInIdentity`,
	// which declares nothing. So the call answered somebody's accounts at
	// their providers and what their keys and factors are called, to an
	// administrator or an operator, and left no row. It names the person
	// asked about, in their tenant, whatever they turn out to have -- and
	// below, each account whose subject it answers, by the row that holds it,
	// as a list of them is recorded.
	in, _ := pdid.From(v.GetTenant().GetId())
	access.Saw(ctx, access.Subject{Id: whose, Tenant: in})

	// Every page of each. The answer has no cursor, so what it says is all
	// there is: read once with no size, each was a list's first page --
	// twenty -- and somebody with twenty-five keys was answered twenty, with
	// nothing saying there were more, to whoever reads this to end the keys
	// of an account (#279). In the order a list is in, which is the order
	// they were made in.
	res := app.HolderSignsInResponse_builder{}
	for after := ""; ; {
		page, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{Holder: who}.Build(),
			},
			Size:  signsInPage,
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, i := range page.GetItems() {
			res.Identities = append(res.Identities, app.SignInIdentity_builder{
				Id:          i.GetId(),
				Provider:    i.GetProvider(),
				Subject:     i.GetSubject(),
				DateCreated: i.GetDateCreated(),
			}.Build())

			if k, err := pdid.From(i.GetId()); err == nil {
				access.Saw(ctx, access.Subject{Id: k, Tenant: in})
			}
		}

		if after = page.GetNext(); after == "" {
			break
		}
	}

	for after := ""; ; {
		page, err := s.Next().Credential().List(ctx, app.CredentialListRequest_builder{
			Filters: []*app.CredentialFilter{
				app.CredentialFilter_builder{Holder: who}.Build(),
			},
			Size:  signsInPage,
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, c := range page.GetItems() {
			// The fields are written out, so the verifier is absent rather
			// than deselected -- there is no `Select` here to get wrong, and
			// the shape itself is the statement D13 makes by not registering
			// the service.
			f := app.SignInCredential_builder{Kind: c.GetKind(), Name: c.GetName()}
			if u := c.GetDateRotated(); u != nil {
				f.DateRotated = u
			}
			if u := c.GetDateUnlocked(); u != nil && u.AsTime().After(time.Now()) {
				f.DateUnlocked = u
			}

			res.Credentials = append(res.Credentials, f.Build())
		}

		if after = page.GetNext(); after == "" {
			break
		}
	}

	// Their badge, beside the rest and not among them: it opens a station,
	// and nothing that signs anybody in reads it. Written out as the
	// credentials are, so the code and the verifier are absent rather than
	// deselected.
	b, err := s.Next().Badge().Get(ctx, app.BadgeGetRequest_builder{
		Ref: app.BadgeRef_builder{Holder: app.BadgeRefByHolder_builder{Holder: who}.Build()}.Build(),
		Select: app.BadgeSelect_builder{
			DateRotated: z.Ptr(true),
			DateLocked:  z.Ptr(true),
		}.Build(),
	}.Build())
	switch {
	case err == nil:
		f := app.SignInBadge_builder{}
		if u := b.GetDateRotated(); u != nil {
			f.DateRotated = u
		}
		if s.badges.locked(b, time.Now()) {
			f.DateLocked = b.GetDateLocked()
		}
		res.Badge = f.Build()
	case status.Code(err) != codes.NotFound:
		return nil, err
	}

	// The alias of each app a key is for, read once each, as the caller
	// reads a holder: what a screen draws beside the identifier.
	aliases := map[string]string{}
	aliasOf := func(of []byte) (string, error) {
		if len(of) == 0 {
			return "", nil
		}
		if v, ok := aliases[string(of)]; ok {
			return v, nil
		}
		h, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: of}.Build(),
			Select: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil && status.Code(err) != codes.NotFound {
			return "", err
		}
		aliases[string(of)] = h.GetAlias()

		return h.GetAlias(), nil
	}

	for after := ""; ; {
		page, err := s.Next().ApiKey().List(ctx, app.ApiKeyListRequest_builder{
			Filters: []*app.ApiKeyFilter{
				app.ApiKeyFilter_builder{Holder: who}.Build(),
			},
			Size:  signsInPage,
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, k := range page.GetItems() {
			// Written out like the credentials above, and for the identical
			// reason: the verifier is **absent** rather than deselected.
			// There is no `Select` here to get wrong, and the shape is the
			// statement.
			f := app.SignInKey_builder{
				Id:      k.GetId(),
				Alias:   k.GetAlias(),
				Methods: k.GetMethods(),
				App:     k.GetApp().GetId(),
			}
			alias, err := aliasOf(k.GetApp().GetId())
			if err != nil {
				return nil, err
			}
			f.AppAlias = alias
			if u := k.GetDateExpired(); u != nil {
				f.DateExpired = u
			}
			if u := k.GetDateUsed(); u != nil {
				f.DateUsed = u
			}

			res.Keys = append(res.Keys, f.Build())
		}

		if after = page.GetNext(); after == "" {
			break
		}
	}

	return res.Build(), nil
}

// signsInPage is how many rows [coreHolder.SignsIn] asks a list for at a
// time: the most one answers with, so that somebody's ways in are a read or
// two of each.
const signsInPage = 100

func (s coreHolder) Reaches(ctx context.Context, req *app.HolderReachesRequest) (*app.HolderReachesResponse, error) {
	// Which app's roles, asked before anything about the person: a caller
	// who may not be answered about that app is told so whoever they asked
	// about.
	at, err := s.reachedAt(ctx, req)
	if err != nil {
		return nil, err
	}

	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.HolderSelect_builder{
			DateDisabled: z.Ptr(true),
			Tenant:       app.TenantSelect_builder{DateClosed: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if v.GetDateDisabled() != nil || v.GetTenant().GetDateClosed() != nil {
		// Suspended, or in a tenant that is closed: every credential of
		// theirs is refused before the gate is asked, so the gate's answer is
		// nothing. Answering their bindings instead told an app checking an
		// SSO token roster cannot see -- one minted before the suspension,
		// good for its hour -- that they still held everything they held.
		//
		// The tenant's closing is read here and not left to whoever asks: a
		// caller inside the tenant is refused already, and one outside it --
		// a deployment key as itself -- was answered their bindings for as
		// long as the tenant was closed, while `standing` asked by the same
		// key refused them.
		return app.HolderReachesResponse_builder{EverySite: false}.Build(), nil
	}
	if s.rules.Held == nil {
		return nil, status.Error(codes.Unimplemented,
			"this server cannot say what anybody holds")
	}

	who, err := pdid.From(v.GetId())
	if err != nil {
		return nil, err
	}

	ms, sites, apps, every, err := s.rules.Held(ctx, who, at)
	if err != nil {
		return nil, err
	}

	res := app.HolderReachesResponse_builder{Methods: ms, EverySite: every}
	for _, k := range sites {
		res.Sites = append(res.Sites, k.Bytes())
	}
	for _, k := range apps {
		res.Apps = append(res.Apps, k.Bytes())
	}
	if s.rules.Everywhere != nil {
		if res.Everywhere, err = s.rules.Everywhere(ctx, who, at); err != nil {
			return nil, err
		}
	}

	return res.Build(), nil
}

// reachedAt is the app a `Reaches` is answered at: the one the request names,
// roster's own ([pdid.Nil]) where it says `roster`, and the caller itself
// where it says neither -- an app asking what somebody may call at it.
//
// # Who is answered about which app
//
// The caller about itself, as anybody was answered before a role said whose
// it is: the wall, and a role naming the method. About another app, whoever
// holds `BindingService/List` across the tenant or for that app; about
// roster, across the tenant -- since that is what reads the parts this adds
// up, the bindings and through them the roles, and an app is answered nothing
// it could not read. An app installed with `reach.Methods` holds no list, and
// is answered about itself alone. And somebody who holds `Reaches` itself for
// one app alone (`Binding.app`) is answered at that app and refused
// everywhere else, about themselves as an app among them.
//
// The deployment's own work is not asked, as nowhere is: a command at the box,
// what applies a file, the operators' port. With no frame there is nobody to
// be the caller, and neither is roster's.
func (s coreHolder) reachedAt(ctx context.Context, req *app.HolderReachesRequest) (pdid.Id, error) {
	f, framed := frame.From(ctx)
	self := pdid.Nil
	if framed {
		self = f.Actor
	}

	var (
		at    pdid.Id
		asked string
	)
	switch {
	case req.GetRoster():
		at, asked = pdid.Nil, "roster"
	case req.GetApp() != nil:
		k, err := s.holderOf(ctx, req.GetApp())
		if err != nil {
			return pdid.Nil, err
		}
		at, asked = k, "app"
		if k == self {
			asked = "self"
		}
	default:
		at, asked = self, "self"
	}
	if s.deployment(ctx) {
		return at, nil
	}

	// Held for one app alone (`Binding.app`), `Reaches` is answered at that
	// app and nowhere else: not at roster, not at another app, and not about
	// the caller as an app, which is another.
	apps, sites, whole, err := s.heldFor(ctx, app.HolderService_Reaches_FullMethodName)
	if err != nil {
		return pdid.Nil, err
	}
	if !whole && len(sites) == 0 && len(apps) > 0 {
		if at != pdid.Nil && slices.Contains(apps, at) {
			return at, nil
		}
		if asked == "self" {
			asked = "app"
		}

		return pdid.Nil, s.reachedNowhere(asked)
	}
	if asked == "self" {
		return at, nil
	}

	if s.rules.Everywhere == nil {
		return pdid.Nil, status.Error(codes.PermissionDenied,
			"this server cannot say what you hold across the tenant, so it will not answer about anybody but yourself")
	}
	held, err := s.rules.Everywhere(ctx, f.Actor, pdid.Nil)
	if err != nil {
		return pdid.Nil, err
	}
	if slices.ContainsFunc(held, func(m string) bool { return frame.Covers(m, app.BindingService_List_FullMethodName) }) {
		return at, nil
	}
	if asked == "app" {
		// Or for that app (`Binding.app`): its administrator reads the
		// bindings of its roles, which this adds up. Not at roster, which is
		// the tenant's whole.
		mine, err := s.heldForApp(ctx, app.BindingService_List_FullMethodName, at)
		if err != nil {
			return pdid.Nil, err
		}
		if mine {
			return at, nil
		}
	}

	return pdid.Nil, s.reachedNowhere(asked)
}

// reachedNowhere is the refusal of a `Reaches` asked at roster or at another
// app by somebody who may not be answered there.
func (s coreHolder) reachedNowhere(asked string) error {
	where, wide := "at roster", "across the tenant"
	if asked == "app" {
		where, wide = "at another app", "across the tenant or for that app"
	}

	return status.Errorf(codes.PermissionDenied,
		"%s: what somebody may call %s is answered to whoever holds %s %s, whose lists it adds up; "+
			"an app asks about itself by naming neither", asked, where, app.BindingService_List_FullMethodName, wide)
}

// SearchPageSize is what a search answers with when it was not told, and
// SearchPageLimit the most it will.
const (
	SearchPageSize  = 20
	SearchPageLimit = 100
)

// Search reads through `List` -- the walled one, page by page -- and keeps the
// rows that match. In memory rather than in SQL, on purpose for now: a
// customer is an organisation and not the internet, the wall has already cut
// the rows to one, and a predicate over three columns of a few thousand rows
// costs less than the round trip. `holder_svc.ext.proto` says what is matched
// and why an address is not.
//
// The cursor is `List`'s: the last row of the page, encoded the way `List`
// encodes the last row of one, so the next call carries on after it and no
// row is answered twice or skipped.
//
// # And it is empty when the page was the last
//
// It stopped at the row that filled the page and answered that row as where
// to carry on from, whether or not anything after it matched: ten matches
// asked for ten at a time were a full page and a cursor, and the page after
// was empty (#279). A full page is not the end of the reading. It goes on
// until one more row matches, which is what there being more means, or until
// there are no more rows -- so the call that answers the last page has read
// to the end of the people its caller may see, as a call that finds nothing
// always did.
func (s coreHolder) Search(ctx context.Context, req *app.HolderSearchRequest) (*app.HolderSearchResponse, error) {
	q := strings.ToLower(strings.TrimSpace(req.GetQ()))
	department := strings.TrimSpace(req.GetDepartment())
	employeeNo := strings.TrimSpace(req.GetEmployeeNo())
	if q == "" && department == "" && employeeNo == "" {
		return nil, status.Error(codes.InvalidArgument, "q, department or employee_no: what to look for; nothing is a List")
	}

	matches := func(v *app.Holder) bool {
		p := v.GetProfile()
		if department != "" && !strings.EqualFold(p.GetDepartment(), department) {
			return false
		}
		if employeeNo != "" && p.GetEmployeeNo() != employeeNo {
			return false
		}
		if q == "" {
			return true
		}
		for _, field := range []string{v.GetAlias(), v.GetName(), p.GetDisplayName()} {
			if strings.Contains(strings.ToLower(field), q) {
				return true
			}
		}

		return false
	}

	size := int(req.GetSize())
	switch {
	case size <= 0:
		size = SearchPageSize
	case size > SearchPageLimit:
		size = SearchPageLimit
	}

	res := app.HolderSearchResponse_builder{Items: make([]*app.Holder, 0, size)}
	after := req.GetAfter()

	// Where the page ends, once it is full: said only when a row after it
	// matches.
	last := ""
	for {
		page, err := s.Next().Holder().List(ctx, app.HolderListRequest_builder{
			Filters: req.GetFilters(),
			Size:    SearchPageLimit,
			After:   after,
		}.Build())
		if err != nil {
			return nil, err
		}

		for _, v := range page.GetItems() {
			if !matches(v) {
				continue
			}
			if len(res.Items) == size {
				// One more than the page holds: there is more.
				res.Next = last

				return res.Build(), nil
			}

			// Read through `Next()`, so the page arrives as it is stored;
			// what this answers is a page, and carries what `List` does.
			res.Items = append(res.Items, paged(v))
			if len(res.Items) < size {
				continue
			}

			id, err := pdid.From(v.GetId())
			if err != nil {
				return nil, err
			}
			if last, err = sqlpage.Encode(v.GetDateCreated().AsTime(), id.Uuid()); err != nil {
				// The log's, and not the caller's: nothing they sent is wrong.
				log.From(ctx).ErrorContext(ctx, "holder: could not say where the next page begins", slog.Any("err", err))

				return nil, status.Error(codes.Internal, "could not say where the next page begins")
			}
		}

		if page.GetNext() == "" {
			return res.Build(), nil
		}
		after = page.GetNext()
	}
}
