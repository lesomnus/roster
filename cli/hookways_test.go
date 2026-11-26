package cli

import (
	"slices"
	"testing"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestEveryWayInIsOneNoHookIsAskedAbout holds `cmd`'s list of ways in to what
// the apps that let somebody in call: every write the Login App and the
// account app make is either a way in -- which no hook a deployment names is
// asked about unless its file says `hook.ways_in`, so that one that is down
// shuts nobody out -- or is named here
// as a write a hook may refuse, with why. A write either app comes to call
// tomorrow is neither until somebody says, and the one shape that would be
// wrong silently is a sign-in a hook can stop.
func TestEveryWayInIsOneNoHookIsAskedAbout(t *testing.T) {
	// The writes those apps make that are not a way in.
	notAWayIn := map[string]string{
		rstr.MeService_SignOutEverywhere_FullMethodName: "signing out everywhere ends sessions; it lets nobody in",
		rstr.MeService_Leave_FullMethodName:             "leaving erases somebody's account",
		rstr.HolderService_Update_FullMethodName:        "a person's own name and profile",
		rstr.HolderService_Portray_FullMethodName:       "a person's own picture",
		rstr.ApiKeyService_Erase_FullMethodName:         "ending a key takes a way in away",
		rstr.IdentityService_Erase_FullMethodName:       "taking an account at a provider away",
		rstr.EmailService_Add_FullMethodName:            "an address, which is no way in until it is proved (Claim, Confirm)",
		rstr.EmailService_Erase_FullMethodName:          "taking an address away",
		rstr.CredentialService_Erase_FullMethodName:     "taking a second factor away",
		rstr.DelegationService_Erase_FullMethodName:     "ending a delegation by reference",
		rstr.DelegationService_Revoke_FullMethodName:    "a front door signing somebody out of itself",
		rstr.HolderService_Add_FullMethodName:           "making a person: a deployment may want a say in who is made, so a hook that covers it is asked about one a first sign-in makes where a tenant enrols them, and while it is down nobody new is made and everybody who exists still signs in",
	}

	calls := slices.Concat(account.Calls, LoginMethods, policyMethods(nil, "enrolling"))
	for _, m := range calls {
		if cmd.Reads(m) || cmd.WayIn(m) {
			continue
		}
		if _, ok := notAWayIn[m]; ok {
			continue
		}
		t.Errorf("%s: called by an app that lets somebody in, and neither a way in nor said here to be a write "+
			"a hook may refuse. add it to cmd's waysIn, or say here why it lets nobody in", m)
	}

	// And the whole of the service ways in are spent at.
	for _, m := range rstr.VouchService_ServiceDesc.Methods {
		if name := "/" + rstr.VouchService_ServiceDesc.ServiceName + "/" + m.MethodName; !cmd.WayIn(name) {
			t.Errorf("%s is VouchService's and no way in", name)
		}
	}
	for m := range notAWayIn {
		if cmd.WayIn(m) {
			t.Errorf("%s is a way in, and said here not to be", m)
		}
	}
}
