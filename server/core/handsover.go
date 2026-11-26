package core

import (
	"context"
	"fmt"
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
	// A delegation handed to an app acts as whoever it is of.
	app.DelegationService_Issue_FullMethodName,
	app.CredentialService_Issue_FullMethodName,
	app.CredentialService_Enrol_FullMethodName,
	app.CredentialService_Unlock_FullMethodName,
	// A badge is a way in at every station that asks, as whoever it is
	// written for; a lock cleared is guesses given back.
	app.BadgeService_Enrol_FullMethodName,
	app.BadgeService_Unlock_FullMethodName,
	app.IdentityService_Add_FullMethodName,
	// The subject of an account somebody already has, moved to one the caller
	// names (`identity.go`, `Resubject`): whoever the directory says that is
	// signs in as them from then on.
	app.IdentityService_Resubject_FullMethodName,
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
		// The tenant's as a whole (`tenantwide.go`), and held to nothing about
		// who arrives through the connection: an account at a provider is kept
		// by the connection's **name**, `VouchService/Accept` asks no reach,
		// and what a write here asks is that the deployment's secret is not
		// sent somewhere a tenant chose (`connection.go`, `maySend`). So it is
		// said rather than ruled: whoever says where a tenant's people are
		// asked who they are is who answers for them, in any system that asks
		// somewhere else.
		grant:   "connectionserviceadd-and-update",
		methods: []string{app.ConnectionService_Add_FullMethodName, app.ConnectionService_Update_FullMethodName},
		readsAs: "may set up sign-in through a directory",
		tenant: "the account of anybody of the tenant who signs in through that connection, an administrator's " +
			"among them: an account at a provider is kept by the connection's name, and a connection moved to " +
			"another issuer, or added again under a name, is where its people are asked from then on",
		deployment: "the account of anybody of any tenant who signs in through a connection, an administrator's " +
			"among them: an account at a provider is kept by the connection's name, and a connection moved to " +
			"another issuer, or added again under a name, is where its people are asked from then on",
	},
	{
		// `Claim` answers the link to its caller, as `Link` does (#309).
		grant:   "emailserviceclaim-with-confirm",
		methods: []string{app.EmailService_Claim_FullMethodName},
		readsAs: "may confirm addresses",
		tenant: "any address, proved with no mailbox read: the link that proves one is answered to whoever " +
			"asked for it, and `EmailService/Confirm` spends one",
		site: "any address, proved with no mailbox read: the link that proves one is answered to whoever " +
			"asked for it, and `EmailService/Confirm` spends one",
		deployment: "any address, proved with no mailbox read: the link that proves one is answered to whoever " +
			"asked for it, and `EmailService/Confirm` spends one",
	},
	{
		// Held to the rule about a way in and to the person having an identity
		// to be the word's source (`email.go`, `Attest`); what proves the
		// address is the caller saying a directory did.
		grant:   "emailserviceattest",
		methods: []string{app.EmailService_Attest_FullMethodName},
		readsAs: "may record the address a directory handed over",
		tenant: "an address proved on the caller's word, with no mailbox read, for anybody of the tenant who signs " +
			"in through a directory and holds no more than whoever is given it -- taken from whoever had written " +
			"it unproved",
		site: "an address proved on the caller's word, with no mailbox read, for anybody it reaches there who " +
			"signs in through a directory and holds no more than whoever is given it",
		deployment: "an address proved on the caller's word, with no mailbox read, for anybody of any tenant who " +
			"signs in through a directory",
	},
	{
		// Taken only through a delegation of an app that holds it across the
		// tenant, and only for the person's own row (`identity.go`, `Attest`):
		// held anywhere smaller, or by a deployment key as itself, it does
		// nothing.
		grant:   "identityserviceattest",
		methods: []string{app.IdentityService_Attest_FullMethodName},
		readsAs: "may connect the accounts people sign in with",
		tenant:  "whoever holds it vouches for any account a person of the tenant connects through it",
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
		// Putting somebody in a group asks whether the one doing it holds
		// what the group hands out, and a group's manager is in it and holds
		// it (`agree.go`, `mayJoin`): so whoever binds a role to a group has
		// chosen its manager as who gives it out. The heading is about the
		// binding; the verb it hands over by is this one.
		grant:   "a-role-bound-to-a-group",
		methods: []string{app.GroupMembershipService_Add_FullMethodName},
		readsAs: "may put people in groups",
		tenant: "the role bound to a group of the tenant, handed to whoever it puts in the group, by whoever " +
			"holds that role -- which a group's manager does, being in it",
		site: "the role bound to a group of that site, handed to whoever it puts in the group, by whoever " +
			"holds that role -- which a group's manager does, being in it",
		deployment: "the role bound to a group of any tenant, handed to whoever it puts in the group",
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
			// And erasing somebody, which asks what suspending them asks: the
			// verb across the tenant, a row no file declares, and a tenant left
			// with somebody administering it (`takeaway.go`). It was the one of
			// the three the page did not say (#279).
			app.HolderService_Erase_FullMethodName,
		},
		readsAs: "may suspend an account, sign somebody out everywhere, or erase them",
		tenant: "anybody's of the tenant, wider than whoever is given it included: an administrator's keys " +
			"and sessions stop at once",
		site:       "anybody's of that site, wider than whoever is given it included",
		deployment: "anybody's of any tenant, an administrator's included",
	},
	{
		// `LeaveLink` answers the link to its caller, as `Link` does, and
		// `Leave` spends one (`server/vouch/leave.go`). Said of the one that
		// mints.
		grant:   "vouchserviceleavelink-with-leave",
		methods: []string{app.VouchService_LeaveLink_FullMethodName},
		readsAs: "may mail a link that deletes an account",
		tenant: "anybody's account of the tenant, deleted, where the tenant lets its people leave: the link is " +
			"answered to whoever asked for it, and `VouchService/Leave` spends one",
		site: "the account of anybody it reaches there, deleted, where the tenant lets its people leave: the link " +
			"is answered to whoever asked for it, and `VouchService/Leave` spends one",
		deployment: "anybody's account of any tenant that lets its people leave, deleted: the link is answered to " +
			"whoever asked for it, and `VouchService/Leave` spends one",
	},
	{
		// The tenant's as a whole (`cmd/policy.go`, `ofTheWholeTenant`).
		grant: "auditservice",
		methods: []string{
			app.AuditService_List_FullMethodName,
			app.AuditService_Get_FullMethodName,
		},
		readsAs: "may read the audit log",
		// What a write set, which is what a line keeps at the default. The row
		// the write left is on a line only for a kind the deployment keeps at
		// `audit.content: value`, and this said every line carries it (#280).
		tenant: "what every write in the tenant said: what each write set, and the row it left where the " +
			"deployment keeps rows on its trail, of rows long since erased too",
		deployment: "what every write in every tenant said: what each write set, and the row it left where " +
			"the deployment keeps rows on its trail, of rows long since erased too",
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
		// The four facts and no row (`sync.proto`), of everybody the wall
		// shows its caller; and a stream that carries no field the schema
		// calls somebody's leaves no row on the access record.
		grant:   "syncservicewatch",
		methods: []string{app.SyncService_Watch_FullMethodName},
		readsAs: "may hear when a sign-in stops being good",
		tenant: "the identifier, the tenant and four moments -- suspended, erased, signed out everywhere, their " +
			"tenant closed -- of everybody of the tenant, and no row of the access record is written for it",
		site: "the identifier, the tenant and four moments -- suspended, erased, signed out everywhere, their " +
			"tenant closed -- of everybody of that site, and no row of the access record is written for it",
		deployment: "the identifier, the tenant and four moments -- suspended, erased, signed out everywhere, " +
			"their tenant closed -- of everybody of every tenant, and no row of the access record is written for it",
	},
	{
		// `mayGrant` reads what the caller's **holder** is bound, and not the
		// list of the credential that asks: it is how a terminal is signed in
		// (`account`, the device grant). Said of the tenant, where a key is
		// held: a key names no site, so what is held in one is put on none,
		// and a key of the deployment's own hands out nothing.
		//
		// And `DelegationService/Issue`, which #567 added, by the same rule
		// (`delegation.go`, `Issue`): what a delegation carries is asked of
		// what the person may call, and not of the credential that asks, so
		// one listed alone hands an app of the tenant a delegation of anything
		// they may call.
		grant: "a-key-that-mints-keys",
		methods: []string{
			app.ApiKeyService_Issue_FullMethodName,
			app.DelegationService_Issue_FullMethodName,
		},
		readsAs: "on a key, one method",
		tenant: "listed on a key or a delegation, everything whoever it is for holds across the tenant: the key " +
			"or the delegation it mints lists anything they are bound, and the list it was itself minted with is not asked",
	},
	{
		// The directory is whoever holds its verbs (`directory.go`): nothing
		// ties them to the key `roster scim provision` mints. A person is no
		// site's, so making one is the tenant's as a whole.
		grant: "a-directorys-key",
		methods: []string{
			app.HolderService_Provision_FullMethodName,
			app.HolderService_Deactivate_FullMethodName,
			app.HolderService_Tell_FullMethodName,
		},
		readsAs: "a sync job",
		tenant: "people made in the tenant, and whoever signs in through its connection suspended -- the " +
			"tenant's last administrator among them",
		deployment: "people made in any tenant, and whoever signs in through the connection that tenant " +
			"provisions through suspended -- its last administrator among them",
	},
	{
		// The one of what is somebody's own that is worth knowing (`own.go`).
		grant:   "the-self-service-role",
		methods: []string{app.IdentityService_Attest_FullMethodName},
		readsAs: "may link an account at a provider to their own",
		own:     "an account at a provider written onto their own row on the word of any app the tenant bound `IdentityService/Attest` across the tenant",
	},
}

// allOfRoster is what a method covering every grant of the page is answered
// instead of them: `/roster.*/*`, which is the tenant. Answered each grant, it
// was twenty notes for the commonest binding there is, each saying the pattern
// reads as something nobody reads it as; what it is, the page's own entry for
// a pattern says, and the rest are parts of what whoever binds it knows they
// gave. `%d` is how many others the page has where it would be held.
var allOfRoster = handedOver{
	grant:   "a-pattern",
	pattern: true,
	readsAs: "every method roster serves",
	tenant: "every one a later release adds too, the day it is added: all %d other grants on the page, across " +
		"the tenant, which is the tenant itself, as `everything` holds it",
	site: "every one a later release adds too, the day it is added: all %d other grants on the page that a " +
		"site's binding hands over, of the people of that site",
	deployment: "every one a later release adds too, the day it is added: all %d other grants on the page, in " +
		"every tenant",
}

// CoversThePage is a method that covers every method of every grant of the
// page, which is answered as [allOfRoster].
func CoversThePage(m string) bool {
	for _, g := range handsOver {
		for _, v := range g.methods {
			if !frame.Covers(m, v) {
				return false
			}
		}
	}

	return true
}

// GrantsOfThePage is how many grants of the page are answered where they
// would be `held`, on the plane `kind` is of, the one for a pattern aside.
func GrantsOfThePage(kind string, held app.RoleHeld) int {
	n := 0
	for _, g := range handsOver {
		if !g.pattern && g.at(kind, held) != "" {
			n++
		}
	}

	return n
}

// at is what g is where it would be held, on the plane `kind` is of.
func (g handedOver) at(kind string, held app.RoleHeld) string {
	switch {
	case strings.HasPrefix(kind, keys.PrefixDeployment):
		// Where it would be held is a customer's question. A role of this
		// plane is exercised in every tenant, wherever it was bound.
		return g.deployment
	case held == app.RoleHeld_ROLE_HELD_IN_A_SITE:
		return g.site
	case held == app.RoleHeld_ROLE_HELD_NOWHERE:
		return g.own
	}

	return g.tenant
}

// HandedOver is what `methods` hand over that they do not read as, where they
// would be `held` -- by the plane `kind` is of, [keys.PrefixDeployment] or
// [keys.PrefixTenant], as [Core] is told its own (`WithPrefix`).
//
// A grant is answered by its own methods that those asked about cover, and
// not by a pattern that covers them: `/roster.CredentialService/*` does not
// read as *may manage somebody's keys*, `CredentialService/Issue` does. A
// method that covers every grant is answered one, [allOfRoster], wherever a
// pattern is a grant at all.
//
// A function beside the method so that whatever else says it -- a note at a
// terminal -- says it from this list.
func HandedOver(kind string, held app.RoleHeld, methods []string) []*app.RoleHandedOver {
	if is := allOfRoster.at(kind, held); is != "" {
		var all []string
		for _, m := range methods {
			if CoversThePage(m) && !slices.Contains(all, m) {
				all = append(all, m)
			}
		}
		if len(all) > 0 {
			return []*app.RoleHandedOver{app.RoleHandedOver_builder{
				Grant:     allOfRoster.grant,
				Methods:   all,
				ReadsAs:   allOfRoster.readsAs,
				HandsOver: fmt.Sprintf(is, GrantsOfThePage(kind, held)),
			}.Build()}
		}
	}

	var out []*app.RoleHandedOver

	for _, g := range handsOver {
		is := g.at(kind, held)
		if is == "" {
			continue
		}

		var by []string
		if g.pattern {
			for _, m := range methods {
				if strings.Contains(m, "*") && !slices.Contains(by, m) {
					by = append(by, m)
				}
			}
		}
		for _, v := range g.methods {
			if !slices.Contains(by, v) && slices.ContainsFunc(methods, func(m string) bool { return frame.Covers(m, v) }) {
				by = append(by, v)
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
