package core

import (
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/roster/rstr"
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
// `EmailService/Verify` is not here, though an address is confirmed from
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

	// How they sign in.
	app.IdentityService_Add_FullMethodName,
	app.CredentialService_Set_FullMethodName,
	app.CredentialService_Enrol_FullMethodName,
	app.CredentialService_Erase_FullMethodName,

	// What calls as them, and where they are signed in.
	app.ApiKeyService_List_FullMethodName,
	app.ApiKeyService_Issue_FullMethodName,
	app.ApiKeyService_Erase_FullMethodName,
	app.DelegationService_List_FullMethodName,
	app.DelegationService_Erase_FullMethodName,
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
				Owns, m, strings.Join(Owning, ", "))
		}
	}

	return nil
}
