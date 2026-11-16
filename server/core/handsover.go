package core

import (
	"context"
	"slices"
	"strings"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// What a grant hands over that its name does not say: `docs/usage/wide-grants.md`
// as a list a program can ask.
//
// # Why it is here
//
// Every sentence below is a rule of this package read from the other side --
// what the rule **lets through**. *Nobody writes a way into an account wider
// than their own* (`escalate.go`) is, to whoever is handed
// `ApiKeyService/Issue`, *a way into anybody who holds no more*. The rule and
// what it lets through change together or one of them is wrong, so they are
// kept in one package, and `cmd/baseline_test.go` holds this list and the page
// to each other.
//
// It was three notes in `cmd`, printed by `roster key add`, and a page nothing
// could read (#310).
//
// # By where, because a sentence said of the wrong place is not read
//
// A grant reaches where it was made (`docs/usage/rules.md`), and what it hands
// over is a fact about that place:
//
//   - **Across the tenant**, everybody of it.
//   - **In a site**, the people of that site -- and of what is the tenant's as
//     a whole, nothing: `tenantwide.go` refuses it to somebody who holds it no
//     wider, so beside a site's binding it is a line that does nothing.
//   - **Nowhere**, which is the tenant's `self-service` role, the caller's own
//     rows (`own.go`).
//   - And on **this deployment's own plane**, every tenant: an operator's role
//     is exercised at `admin.addr` and a deployment's key on the data plane,
//     neither behind a wall.
//
// `roster key add` printed a deployment's sentence for a customer's key until
// #314, and an administrator minting one for their own tenant was told it read
// every customer's history.

// handedOver is one grant of the page.
type handedOver struct {
	// grant is its heading's anchor on the page.
	grant string

	// methods answer it when one asked about covers any of them.
	methods []string

	// pattern answers it, instead, for a method that is a pattern.
	pattern bool

	readsAs string

	// What it is, by where it would be held. Empty is nothing: not handed over
	// there, and not answered.
	tenant, site, own, deployment string
}

// A person's verbs: each writes a way into an account and takes a *whose*
// (`escalate.go`, `mayWriteAWayIn` and `mayReach`).
var aPersonsVerbs = []string{
	app.ApiKeyService_Issue_FullMethodName,
	app.CredentialService_Issue_FullMethodName,
	app.CredentialService_Enrol_FullMethodName,
	app.CredentialService_Unlock_FullMethodName,
	app.IdentityService_Add_FullMethodName,
	app.EmailService_Add_FullMethodName,
	app.PhoneService_Add_FullMethodName,
}

// handsOver is the page, in its order.
var handsOver = []handedOver{
	{
		grant:   "a-persons-verb-across-the-tenant",
		methods: aPersonsVerbs,
		readsAs: "may manage somebody's keys, password or addresses",
		tenant: "a way into the account of anybody of the tenant who holds no more than whoever is given it -- " +
			"which, given to two people, is each other",
		site: "a way into the account of anybody of that site who holds no more than whoever is given it -- " +
			"which, given to two people of it, is each other",
	},
	{
		// Neither is held to the rule about a way in, and cannot be: a front
		// door holds nothing. Said of the one that mints, since a link is
		// answered to whoever asked for it.
		grant:   "vouchservicelink-with-redeem",
		methods: []string{app.VouchService_Link_FullMethodName},
		readsAs: "may send recovery links",
		tenant: "every account of the tenant, an administrator's among them: the link is answered to whoever " +
			"asked for it, and `VouchService/Redeem` spends one",
		site: "every account it reaches there, whatever they hold: the link is answered to whoever asked for " +
			"it, and `VouchService/Redeem` spends one",
		deployment: "every account of every tenant, an administrator's among them: the link is answered to " +
			"whoever asked for it, and `VouchService/Redeem` spends one",
	},
	{
		grant:      "vouchserviceaccept",
		methods:    []string{app.VouchService_Accept_FullMethodName},
		readsAs:    "may sign people in",
		tenant:     "a credential for anybody of the tenant, minted on the caller's word that a provider signed them in",
		site:       "a credential for anybody it reaches there, minted on the caller's word that a provider signed them in",
		deployment: "a credential for anybody of any tenant, minted on the caller's word that a provider signed them in",
	},
	{
		// `Verify` answers the link to its caller, as `Link` does (#309).
		grant:   "emailserviceverify-with-confirm",
		methods: []string{app.EmailService_Verify_FullMethodName},
		readsAs: "may confirm addresses",
		tenant: "any address, proved with no mailbox read: the link that proves one is answered to whoever " +
			"asked for it, and `EmailService/Confirm` spends one",
		site: "any address, proved with no mailbox read: the link that proves one is answered to whoever " +
			"asked for it, and `EmailService/Confirm` spends one",
		deployment: "any address, proved with no mailbox read: the link that proves one is answered to whoever " +
			"asked for it, and `EmailService/Confirm` spends one",
	},
	{
		// The tenant's as a whole (`nomination.go`).
		grant:   "nominationserviceadd",
		methods: []string{app.NominationService_Add_FullMethodName},
		readsAs: "may install an app",
		tenant: "who an app's deployment key is answered as here: whoever is named is who the app is, with " +
			"everything they hold",
		deployment: "who an app's deployment key is answered as in a tenant: whoever is named is who the app " +
			"is there, with everything they hold",
	},
	{
		// The rule about a way in reads what the caller is bound in the tenant,
		// and an operator is bound nothing there, so at `admin.addr` it is not
		// asked (`docs/operating.md`, *`admin.addr` is root*).
		grant:   "the-same-verbs-at-adminaddr",
		methods: aPersonsVerbs,
		readsAs: "an operator's helpdesk role",
		deployment: "a way into any account of any tenant, an administrator's included, at `admin.addr` -- " +
			"where whose account it is is not asked -- and into any operator's who holds no more",
	},
	{
		// `mayGrant`: nobody hands out what they do not hold, where they hold it.
		grant: "bindingserviceadd",
		methods: []string{
			app.BindingService_Add_FullMethodName,
			app.RoleService_Add_FullMethodName,
			app.RoleService_Widen_FullMethodName,
		},
		readsAs: "may assign roles",
		tenant:  "everything whoever is given it holds, handed to anybody -- no more than that, and all of it",
		site: "everything whoever is given it holds in that site, handed on there -- no more than that, and " +
			"all of it",
		deployment: "everything whoever is given it holds, handed to anybody of any tenant -- no more than " +
			"that, and all of it",
	},
	{
		// The tenant's as a whole (`group.go`).
		grant:      "groupexposureserviceadd",
		methods:    []string{app.GroupExposureService_Add_FullMethodName},
		readsAs:    "may show a group to an app",
		tenant:     "whatever that app grants on the group's name, which roster cannot see",
		deployment: "whatever that app grants on the group's name, which roster cannot see",
	},
	{
		// The tenant's as a whole (`agree.go`): a site that said who is in it
		// would say who is in its own reach.
		grant:      "sitemembershipserviceadd",
		methods:    []string{app.SiteMembershipService_Add_FullMethodName},
		readsAs:    "may say who works where",
		tenant:     "who is within reach of every grant made in a site: whoever is put into one is in its helpdesk's reach",
		deployment: "who is within reach of every grant made in a site: whoever is put into one is in its helpdesk's reach",
	},
	{
		grant:      "a-pattern",
		pattern:    true,
		readsAs:    "the methods a service has today",
		tenant:     "every method it covers, and every one a later release adds, the day it is added",
		site:       "every method it covers, and every one a later release adds, the day it is added",
		deployment: "every method it covers, and every one a later release adds, the day it is added",
	},
	{
		// Not held to the rule about a way in, on purpose: it makes nobody
		// anybody, and somebody on call stops an account wider than they are.
		grant: "holderservicedisable",
		methods: []string{
			app.HolderService_Disable_FullMethodName,
			app.HolderService_Invalidate_FullMethodName,
		},
		readsAs: "may suspend an account, or sign somebody out everywhere",
		tenant: "anybody's of the tenant, wider than whoever is given it included: an administrator's keys " +
			"and sessions stop at once",
		site:       "anybody's of that site, wider than whoever is given it included",
		deployment: "anybody's of any tenant, an administrator's included",
	},
	{
		// The tenant's as a whole (`cmd/policy.go`, `ofTheWholeTenant`).
		grant: "auditservice",
		methods: []string{
			app.AuditService_List_FullMethodName,
			app.AuditService_Get_FullMethodName,
		},
		readsAs: "may read the audit log",
		tenant: "what every write in the tenant said: the row as each write left it, of rows long since " +
			"erased too",
		deployment: "what every write in every tenant said: the row as each write left it, of rows long " +
			"since erased too",
	},
	{
		// Somebody else's, to whoever holds it across the tenant (`mayReadOf`).
		grant:   "holderservicesignsin",
		methods: []string{app.HolderService_SignsIn_FullMethodName},
		readsAs: "may see how somebody signs in",
		tenant:  "everybody's keys by name, what each key may call, and where each of them is signed in",
		deployment: "everybody's keys by name, in every tenant, what each key may call, and where each of them " +
			"is signed in",
	},
	{
		// `mayGrant` reads what the caller's **holder** is bound, and not the
		// list of the credential that asks: it is how a terminal is signed in
		// (`account`, the device grant). Said of the tenant, where a key is
		// held: a key names no site, so what is held in one is put on none,
		// and a key of the deployment's own hands out nothing.
		grant:   "a-key-that-mints-keys",
		methods: []string{app.ApiKeyService_Issue_FullMethodName},
		readsAs: "on a key, one method",
		tenant: "listed on a key or a delegation, everything whoever it is for holds across the tenant: the key " +
			"it mints lists anything they are bound, and the list it was itself minted with is not asked",
	},
	{
		// The one of what is somebody's own that is worth knowing (`own.go`).
		grant:   "the-self-service-role",
		methods: []string{app.IdentityService_Add_FullMethodName},
		readsAs: "may link an account at a provider to their own",
		own:     "an account at a provider written onto their own row, with no proof that the account is theirs",
	},
}

// HandedOver is what `methods` hand over that they do not read as, where they
// would be `held` -- by the plane `kind` is of, [keys.PrefixDeployment] or
// [keys.PrefixTenant], as [Core] is told its own (`WithPrefix`).
//
// A function beside the method so that whatever else says it -- a note at a
// terminal -- says it from this list.
func HandedOver(kind string, held app.RoleHeld, methods []string) []*app.RoleHandedOver {
	var out []*app.RoleHandedOver

	for _, g := range handsOver {
		is := g.tenant
		switch {
		case strings.HasPrefix(kind, keys.PrefixDeployment):
			// Where it would be held is a customer's question. A role of this
			// plane is exercised in every tenant, wherever it was bound.
			is = g.deployment
		case held == app.RoleHeld_ROLE_HELD_IN_A_SITE:
			is = g.site
		case held == app.RoleHeld_ROLE_HELD_NOWHERE:
			is = g.own
		}
		if is == "" {
			continue
		}

		var by []string
		for _, m := range methods {
			if slices.Contains(by, m) {
				continue
			}

			reaches := slices.ContainsFunc(g.methods, func(v string) bool { return frame.Covers(m, v) })
			if g.pattern {
				reaches = strings.Contains(m, "*")
			}
			if reaches {
				by = append(by, m)
			}
		}
		if len(by) == 0 {
			continue
		}

		out = append(out, app.RoleHandedOver_builder{
			Grant:     g.grant,
			Methods:   by,
			ReadsAs:   g.readsAs,
			HandsOver: is,
		}.Build())
	}

	return out
}

// HandsOver is [HandedOver] for the plane this stack answers for;
// `role_svc.ext.proto` says why it is a role's verb and reads no row.
func (s coreRole) HandsOver(_ context.Context, req *app.RoleHandsOverRequest) (*app.RoleHandsOverResponse, error) {
	if len(req.GetMethods()) == 0 {
		return nil, pderr.Invalidf("methods", "which; a list of nothing hands over nothing, and asking about one is a mistake")
	}

	return app.RoleHandsOverResponse_builder{
		Grants: HandedOver(s.prefix, req.GetHeld(), req.GetMethods()),
	}.Build(), nil
}
