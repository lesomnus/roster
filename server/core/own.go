package core

import (
	"context"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/quote"
)

// What somebody does to their own account, and what lets them.
//
// # Why it is a role nobody is bound
//
// A role names methods and reaches every row the wall shows its holder. So
// "may add an address to their own account" could only be said as "may add
// an address", across the tenant, to everybody who should manage their own
// -- and a method held across the tenant reaches whoever holds no more than
// the caller, which between two people bound the same role is each other.
// Somebody given their own keys to mint minted one on a colleague's account
// and called as them (#274).
//
// The fix is not a wider rule about whose row a request names: what a grant
// reaches is already said, by where it was made, and a method somebody holds
// **nowhere** reaches what is their own and nothing else (`cmd.People`). So
// what somebody does to their own account is a role they hold nowhere: the
// tenant's role named [Owns], which everybody of the tenant holds over what
// is theirs, with no binding saying so.
//
// # Why it is the tenant's own role
//
// [Manages]'s reasons. It is the name a tenant would give its own, and what
// is there is used as it stands: a tenant whose people do not mint keys for
// themselves takes `ApiKeyService/Issue` out of it, and one that wants none
// of it erases the role. `Tenant.Add` makes it, naming all of [Owning], so
// that somebody given an account can keep it without anybody having thought
// to bind them something.
//
// # And what it may name
//
// Only what is somebody's own to do ([Owning]). It is held by everybody, and
// what confines it to their own rows is the wall narrowing a person's rows --
// which is the whole of the confinement for a verb about a person, and is
// nothing for a verb that reads no such row. So a method that is not on the
// list is not held through this role whatever the role says, and a write
// that would put one there is refused.

// Owns is the alias of the role everybody of a tenant holds over what is
// their own.
const Owns = "self-service"

// Owning is what [Owns] may name: the verbs a person calls about their own
// account, each of which reads or writes a row that is a person's.
//
// `MeService` is not here because it needs no role at all: it takes no
// "whose". What is here takes one, and is the caller's own by the wall.
//
// `EmailService/Claim` is not here, though an address is confirmed from
// somebody's own account page. It answers the link that proves an address
// **to its caller**, and a link is spent by whoever minted it
// (`coreEmail.Confirm`). So a person who may call it holds, of an address they
// wrote onto their own account a moment ago, a proof nobody mailed: all that is
// left between them and an address that is somebody else's is
// `EmailService/Confirm`. It was here for a day, as the switch for the page's
// *confirm*, and somebody bound `Confirm` alone proved the CEO's address onto
// their own account with it (#274). Both verbs are a front door's, which mints
// the link as itself and mails it; the page asks its own app.
var Owning = []string{
	// Who they are, as they say it.
	app.HolderService_Get_FullMethodName,
	app.HolderService_Update_FullMethodName,
	app.HolderService_Portray_FullMethodName,

	// Where they are reached.
	app.EmailService_Get_FullMethodName,
	app.EmailService_List_FullMethodName,
	app.EmailService_Add_FullMethodName,
	app.EmailService_Erase_FullMethodName,
	app.PhoneService_Get_FullMethodName,
	app.PhoneService_List_FullMethodName,
	app.PhoneService_Add_FullMethodName,
	app.PhoneService_Erase_FullMethodName,

	// How they sign in. An account at a provider is `Attest` and not `Add`:
	// `Add` takes no proof, and what proves an account is theirs is the round
	// trip to the provider, which a front door makes and vouches for
	// (`identity.go`, [coreIdentity.Attest]).
	app.IdentityService_Attest_FullMethodName,
	app.IdentityService_Erase_FullMethodName,
	app.CredentialService_Set_FullMethodName,
	app.CredentialService_Enrol_FullMethodName,
	app.CredentialService_Erase_FullMethodName,

	// What calls as them, and where they are signed in.
	app.ApiKeyService_List_FullMethodName,
	app.ApiKeyService_Issue_FullMethodName,
	app.ApiKeyService_Erase_FullMethodName,
	app.DelegationService_List_FullMethodName,
	app.DelegationService_Erase_FullMethodName,

	// And an app handed a delegation of their own, to act as them
	// (`delegation.go`, [coreDelegation.Issue]).
	app.DelegationService_Issue_FullMethodName,
}

// Owned is what of `methods` a role named [Owns] holds: each of [Owning] one
// of them covers. A pattern is read, so `/roster.EmailService/*` is the four
// of that service that are on the list and no fifth.
func Owned(methods []string) []string {
	var out []string
	for _, m := range Owning {
		if slices.ContainsFunc(methods, func(v string) bool { return frame.Covers(v, m) }) {
			out = append(out, m)
		}
	}

	return out
}

// mayBeOwned refuses writing into the role named [Owns] what it may not name,
// or giving it a site.
//
// Said at the write so that it is said at all: read, such a method is simply
// not held ([Owned]), and a tenant that put `HolderService/Erase` there would
// see nothing happen and not know why.
func mayBeOwned(alias string, sited bool, methods []string) error {
	if alias != Owns {
		return nil
	}
	if sited {
		return status.Errorf(codes.FailedPrecondition,
			"site: the role %q is what everybody of the tenant does to their own account, and belongs to no site", Owns)
	}
	for _, m := range methods {
		if !slices.Contains(Owning, m) {
			return status.Errorf(codes.FailedPrecondition,
				"methods: the role %q is held by everybody of the tenant over what is their own, and %s is not one of the things somebody does to their own account (%s)",
				Owns, quote.Sent(m), strings.Join(Owning, ", "))
		}
	}

	return nil
}

// What an organisation says of somebody is not theirs to change.
//
// # Why
//
// `HolderService/Update` replaces the whole profile and is somebody's own to
// call ([Owning]), so a person changed their own department, employee number
// and title with the call that changes their display name (#368). A directory
// that provisions them writes those again when it next sends them, and one
// without provisioning never does: the person's word stood as their
// organisation's, read by every app that grants on a department.
//
// # The rule
//
// Three fields of a profile are what an organisation says of somebody
// ([Organisational]), fixed here and no tenant's setting. On an `Update` of
// the caller's own row, a profile that differs from what is stored in one of
// them is refused, naming it. One that carries all three as they are stored
// passes, so a page that reads a profile, changes the name and sends the
// whole of it back is not refused; one that leaves one out where one is
// stored differs, since the profile is replaced whole and leaving it out
// would clear it. Nothing else of `Update` changes, and no verb, permission
// or field is added: it is a rule about one's own row on the verb there is.
//
// # Who it does not hold
//
// Whoever's hold on `HolderService/Update` is not only over their own row:
// somebody bound a role naming it -- across the tenant, in a site, on a team
// -- writes their own department as they write anybody's, which is an
// administrator of the tenant's people and a directory's provisioner. And a
// write with no caller, or by the deployment: the box, a file.
//
// What is asked is whether the caller holds the verb by anything but [Owns],
// as the gate reads it ([Rules.Held]), and not whether a binding of theirs
// reaches this row: the two differ only for somebody who holds it in a site
// they are not of, who is then not held by this either.
//
// An app acting for somebody through a delegation is that person here: the
// frame's actor is the person, the row is theirs, and the three are kept.

// Organisational is the fields of a profile that are what an organisation
// says of somebody: by the name a refusal gives each, and how each is read.
var Organisational = []struct {
	Field string
	Of    func(*app.Profile) string
}{
	{"department", (*app.Profile).GetDepartment},
	{"employee_no", (*app.Profile).GetEmployeeNo},
	{"title", (*app.Profile).GetTitle},
}

// keepsWhatTheOrganisationSays refuses the caller changing, on their own
// row `whose`, what their organisation says of them: `was` is the profile
// stored, and `now` the one the write replaces it with.
func (s Core) keepsWhatTheOrganisationSays(ctx context.Context, whose pdid.Id, was, now *app.Profile) error {
	f, ok := frame.From(ctx)
	if !ok || s.deployment(ctx) || f.Actor != whose {
		return nil
	}

	changed := ""
	for _, o := range Organisational {
		if o.Of(was) != o.Of(now) {
			changed = o.Field

			break
		}
	}
	if changed == "" {
		return nil
	}

	if s.rules.Held == nil {
		return status.Errorf(codes.PermissionDenied,
			"profile.%s: this server cannot say what you hold, so it will not let you change what your organisation says of you", changed)
	}
	held, _, _, _, err := s.rules.Held(ctx, f.Actor, pdid.Nil)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(held, func(m string) bool { return frame.Covers(m, app.HolderService_Update_FullMethodName) }) {
		return nil
	}

	return status.Errorf(codes.PermissionDenied,
		"profile.%s: it is what your organisation says of you, and is written by an administrator of the tenant's people or by its directory; send it as it is stored",
		changed)
}
