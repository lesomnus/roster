package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// Becoming somebody else by acquiring their permissions.
//
// `escalate.go` states the rule -- what you grant must be a subset of what you
// hold -- and every write that names a `Role`, a list of methods, or **who** a
// grant reaches is a place that rule has to be asked. Three of those were found
// missing this week: `TeamMembership.Add`, `GroupMembership.Add`, and a binding
// arriving through a group being invisible to `Granted`.
//
// What these pin is the claim rather than the three fixes: **nothing a person
// may write makes roster answer as somebody wider than they are.** Written from
// the surface inwards -- every write that names a role, every write that moves
// who a grant reaches, and every door those writes can arrive through -- so
// that the fourth one of this shape is red before anybody argues about it.
//
// Four of the seven below are red today, and they are **three** holes -- the
// first two are one hole reached through two doors. Each is marked, and each
// says what it costs and what the attacker walks away with. They are written
// as the claim and not as the current behaviour, so the day the source is
// fixed they go green without being touched.

const (
	// The general write of a membership, which names a role exactly as `Add`
	// does. Closed at the transport unless a deployment sets
	// `allow_general_writes`, which is a supported setting and not a mistake.
	patchMembership = "/roster.TeamMembershipService/Patch"

	// Writing somebody's password, which is what a helpdesk holds.
	writeSecret = "/roster.CredentialService/Set"

	mintKey   = "/roster.ApiKeyService/Add"
	listTeams = "/roster.TeamService/List"
)

// TestNobodyAttachesARoleByPatchingAMembership.
//
// # RED: this is a hole, and it is the third of the shape `escalate.go` opens
//
// `TeamMembership.Add` asks `mayGrant` because attaching a role **is** granting
// its methods -- `policy.of` unions the methods of a role somebody holds in a
// team into the set the gate answers from, deliberately, because the gate is
// outermost and never sees which team a call is about. That went in this week.
//
// `TeamMembership.Patch` names the same edge and asks nothing. So the write
// that was closed has a general-purpose twin one field along:
//
//	Alice manages who is in what team, and holds nothing else.
//	Alice puts herself in a team with no role, which is refused by nothing
//	and should not be.
//	Alice patches that membership to name the tenant's admin role.
//	Alice may now erase anybody.
//
// Two Rpcs, no method she did not already hold, and the second one is the one
// `Add` was taught to refuse. It is the same mistake `Role.Patch` was found to
// have -- a rule wired to `Add` and not to the write that can change the same
// column afterwards -- and that one was closed in the layer rather than left to
// the transport. This one was not, and the asymmetry is the finding: a
// deployment that opens general writes gets `Role.Patch` and `ApiKey.Patch`
// refused and this one served.
func TestNobodyAttachesARoleByPatchingAMembership(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	mine := b.team(t, ctx, seoul, "mine")

	// Alice manages memberships across the tenant, and holds nothing else.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "manager", addMember, patchMembership), nil)

	as := framed(ctx, b.ContosoUser, b.Contoso)

	// A membership of her own naming no role. Allowed, and it has to be: it is
	// how somebody is put in a team without being given anything, and
	// `TestAMembershipWithNoRoleIsStillAMembership` says so.
	v, err := b.Walled.TeamMembership().Add(as, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// And the role she is not entitled to, patched into it.
	admin := b.role(t, ctx, "admin", eraseHold)

	_, err = b.Walled.TeamMembership().Patch(as, app.TeamMembershipPatchRequest_builder{
		Ref:         app.TeamMembershipRef_builder{Id: v.GetId()}.Build(),
		Role:        app.RoleRef_builder{Id: admin.Bytes()}.Build(),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she attached herself a role holding what she does not, by patching the membership she had just been allowed to write")
	x.Contains(status.Convert(err).Message(), eraseHold,
		"the refusal did not say which permission was the problem")

	// And what the refusal is for. The gate reads a role held in a team as
	// something this person may ever call, so the membership above is not a
	// row about a team -- it is her permissions.
	conn := served(t, b.Server)
	_, err = app.NewHolderServiceClient(conn).Erase(asOverTheWire(ctx, b.ContosoUser),
		app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "victim").Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she erased somebody, so the patch was written after all")
}

// TestAGeneralWriteIsNotAWayRoundTheEscalationRule is the same hole from the
// wire, in the deployment that has to live with it.
//
// `escalate.go` says `Patch` and `Apply` are closed at the transport and that a
// deployment which opens them opens this with them. That reading is what makes
// the missing check above look like somebody else's problem -- and it is not
// the reading the rest of the file was written to: `Role.Patch` and
// `ApiKey.Patch` both ask `mayGrant`, in the layer, precisely so that the
// setting is a decision about the Api rather than about who may become the
// administrator.
//
// So this is the same two Rpcs against a server built the way that deployment
// builds one, and it shows what they buy: a caller who manages memberships
// erases a person she was never allowed to read.
func TestAGeneralWriteIsNotAWayRoundTheEscalationRule(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	mine := b.team(t, ctx, seoul, "mine")

	b.binds(t, b.ContosoUser, b.role(t, ctx, "manager", addMember, patchMembership), nil)

	// A deployment that turned general writes on, which is one line of
	// configuration and nothing else.
	g, err := b.Grpc(ctx, cmd.Config{Server: config.ServerConfig{AllowGeneralWrites: true}})
	x.NoError(err)

	conn := pdtest.Serve(t, g)
	wire := asOverTheWire(ctx, b.ContosoUser)
	c := app.NewTeamMembershipServiceClient(conn)

	v, err := c.Add(wire, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	_, err = c.Patch(wire, app.TeamMembershipPatchRequest_builder{
		Ref:         app.TeamMembershipRef_builder{Id: v.GetId()}.Build(),
		Role:        app.RoleRef_builder{Id: b.role(t, ctx, "admin", eraseHold).Bytes()}.Build(),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a general write attached a role its caller does not hold")

	// The escalation itself, which is what this is about rather than which
	// error code the patch answered with.
	_, err = app.NewHolderServiceClient(conn).Erase(wire,
		app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "victim").Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"two Rpcs from 'she manages who is in what team' and she erased somebody")
}

// TestNobodyMintsAKeyOnSomebodyElsesHolder.
//
// # RED: this is a hole, and it is `mayReach` undone by the door beside it
//
// A tenant key resolves to the **holder** it hangs on -- that is what makes it
// a person calling rather than a second kind of caller, and `keys.go` says so
// in as many words. So the row `ApiKey.holder` names is not a detail of
// bookkeeping: it is who the bearer of that string **is**.
//
// `apikey.go` asks `mayGrant` about the key's `methods`, which is the right
// question about what the key may do and is not a question about who it is.
// Nothing asks the other one. So:
//
//	Alice is the helpdesk: she may write a password and mint a key.
//	D28 refuses her writing the boss's password, because he holds more.
//	Alice mints a key on the **boss's** holder, allowing only what she holds.
//	Alice presents it, and is the boss -- so `mayReach` reads the write as
//	somebody changing their own password, and lets it through.
//	Alice signs in as the boss.
//
// The rule she walked round is the one that went in *before* the surface it
// protects, because the list of twelve insisted the order there was a
// correctness question rather than a convenience. This is that surface arriving anyway, one table
// along: a key is a credential for a holder, so minting one is writing their
// credential, and the same sentence `mayReach` is written in covers it --
// **you may only write the credential of somebody whose permissions are a
// subset of yours.**
//
// # What it is not
//
// It is not about what the key allows. Every method on it is one Alice holds;
// `mayGrant` saw to that and would have refused anything else. What she gains
// is not a permission but an **identity**, and every rule that reads
// `frame.Actor` -- `mayReach`'s own "changing your own credential is not
// becoming somebody else", `MeService`, anything a product app introspects --
// answers about the boss from then on.
//
// # Reachability, said plainly rather than left as comfort
//
// `ApiKeyService/Add` is closed on every port today, so there is no wire door
// on the data plane and nothing is exploiting this now. That is exactly the
// position `apikey.go` describes for the hole it closed: *nothing exploited it,
// and the reason is not a check -- it is that minting a key needed a shell.* A
// console is what removes the shell, and roster has one. So this is written at
// the layer, which is where the console's write lands, and the consequence is
// then shown over the wire because the token it mints works there today.
func TestNobodyMintsAKeyOnSomebodyElsesHolder(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	alice := b.Who
	boss := addHolder(t, ctx, b.Server, b.Contoso, "boss")

	// The boss administers the tenant.
	permitting(t, ctx, b, boss, "admin", "/roster.*/*")

	// Alice is the helpdesk: she may write a password, and mint a key.
	permitting(t, ctx, b, alice, "helpdesk", writeSecret, mintKey)

	// And the boss has a password, which is the thing that must still be his
	// at the end of this.
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: boss.Bytes()}.Build(),
		Secret: []byte("the one he chose"),
	}.Build())
	require.NoError(t, err)

	c := app.NewVouchServiceClient(b.Conn)
	set := func(as string, secret string) error {
		_, err := app.NewCredentialServiceClient(b.Conn).Set(bearing(ctx, as), app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())

		return err
	}

	// The rule as it stands, over a key of her own so that the caller is
	// unambiguously Alice: she may write a password and not **his**.
	hers := mintFor(t, ctx, b, alice, "hers", []string{writeSecret}, time.Time{})

	err = set(hers, "the one alice chose")
	x.Equal(codes.PermissionDenied, status.Code(err),
		"the helpdesk wrote the administrator's password directly")

	// So she mints a key on his holder instead. Same methods -- hers -- and a
	// different person at the other end of them.
	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	// Through the walled stack as Alice, which is what a console does; see
	// `TestNobodyMintsAKeyForWhatTheyDoNotHold`, which mints the same way.
	as := frame.Into(ctx, frame.New(alice, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))

	_, err = b.Walled.ApiKey().Add(as, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: boss.Bytes()}.Build(),
		Alias:   "hers-on-his",
		Secret:  sum,
		Methods: []string{writeSecret},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she minted a credential that answers as somebody who holds more than she does")

	// And the whole of what that key is worth, which is the reason the refusal
	// above is not a formality: presenting it, she is him.
	x.Error(set(token, "the one alice chose"),
		"she wrote the administrator's password through a key on his holder")

	// Whose password is still his own -- asked through the deployment's key,
	// because this is the one question the whole attack was about.
	is := func(secret string) bool {
		v, err := c.Verify(bearing(ctx, b.Token), app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: boss.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err)

		return v.GetOk()
	}

	x.False(is("the one alice chose"), "the administrator's password is one the helpdesk chose")
	x.True(is("the one he chose"), "the administrator can no longer sign in as himself")
}

// permitting binds a role to somebody on the data plane, through the door the
// harness does its own setting up through.
//
// `keyFor`'s deployment has a control plane, so `auth.Plain` is not wired and
// `asOverTheWire` says nothing there -- a caller is a key. Which is why the
// tests above call as somebody by minting them one.
func permitting(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, alias string, methods ...string) {
	t.Helper()
	x := require.New(t)

	v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: v.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// TestARoleHeldThroughATeamIsStillHeld.
//
// # RED: this is a hole, and it is the group hole one edge along
//
// `mayReach` reads what the **target** holds and allows the write when that is
// nothing. `Granted` answers it, and `Granted` reads bindings -- deliberately
// leaving out a role held in a team, on the reasoning that *its scope is a team
// and the scopes here are the tenant and a site, so there is nothing to compare
// it against.*
//
// That reasoning is false for every method a team is not about. `policy.of`
// unions the methods of a team role into what the person may **ever** call, and
// the only thing that narrows one back down to a team is
// `core.mayChangeTeam`, which guards `TeamMembership.Add` and `Erase` and
// nothing else. So a role naming `Holder.Erase`, attached in one team, is
// `Holder.Erase` over the whole tenant -- this test asserts that first, because
// otherwise the refusal it wants would be about a permission nobody has.
//
// Which leaves the same silence the group binding left, for the same reason
// `escalate.go` gives about direction:
//
//	Ops may look at people, and nothing else.
//	Ops resets the password of an administrator provisioned through a team.
//	Ops signs in as them.
//
// A missing path in `mayGrant` only ever refuses a grant somebody could have
// made, which is a conversation. Here it **allows**, and the person it allows
// writing is the administrator the rule exists to protect. That asymmetry is
// why the group version got a test of its own -- `TestAPermissionHeldThroughAGroupIsStillHeld`
// -- and this is the same test with the last edge in the schema that carries a
// role.
func TestARoleHeldThroughATeamIsStillHeld(t *testing.T) {
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	mine := b.team(t, ctx, seoul, "mine")

	// An administrator whose permission arrives only through a team.
	boss := b.holder(t, ctx, b.Contoso, "boss")
	_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: boss.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: mine.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: b.role(t, ctx, "admin", eraseHold).Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	// And an operator who may only look.
	ops := b.holder(t, ctx, b.Contoso, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", getHolder)

	set := func(who pdid.Id) error {
		_, err := b.Walled.Credential().Set(asOps, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Secret: []byte("a new one"),
		}.Build())

		return err
	}

	// The premise, asserted rather than assumed: what he holds in one team is
	// not confined to it. He erases somebody who is in no team of his, in no
	// site of his, through the wall and the gate exactly as they are wired.
	t.Run("a team role reaches past the team", func(t *testing.T) {
		x := require.New(t)

		conn := served(t, b.Server)
		_, err := app.NewHolderServiceClient(conn).Erase(asOverTheWire(ctx, boss),
			app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "stranger").Bytes()}.Build())
		x.NoError(err,
			"the premise is gone: a team role no longer reaches the tenant, and this test needs rewriting rather than deleting")
	})

	t.Run("so it is not nothing when somebody narrower writes his credential", func(t *testing.T) {
		x := require.New(t)

		err := set(boss)
		x.Equal(codes.PermissionDenied, status.Code(err),
			"an operator became a team-provisioned administrator in two operations")
		x.Contains(status.Convert(err).Message(), eraseHold)
	})

	// And the fast path, which is the common case and is what makes the rule
	// affordable: somebody who really holds nothing is still writable.
	t.Run("while somebody who really holds nothing still may be", func(t *testing.T) {
		x := require.New(t)

		x.NoError(set(b.holder(t, ctx, b.Contoso, "joe")))
	})
}

// TestABatchIsNoWayRoundTheEscalationRule.
//
// A batch arrives as one method carrying many, and `batch.Guard` applies the
// gate per operation -- `TestABatchIsTheSameKey` pins that half. This is the
// other half, and it fails differently: a batch opens a **transaction**, and
// `enttx.Rebind` builds the whole stack again on it. A layer that cannot make
// itself again is simply missing from the rebuilt stack, and the operations run
// straight into the sink.
//
// Nothing about that is visible from a unit test of the layer, from the wiring,
// or from any single-Rpc call: `core` is asked on every ordinary write and the
// suite is green. It comes apart only inside a transaction, which is a batch or
// a multi-write Rpc -- and what comes apart is not a crash but the escalation
// rule not being asked. Alice writes the role she may not write, binds it to
// herself, and both operations commit together.
//
// So this is the escalation of `TestNobodyGrantsWhatTheyDoNotHold`, posted
// through the one door where the rule is reassembled rather than merely called.
func TestABatchIsNoWayRoundTheEscalationRule(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Alice manages who is in what, and holds nothing else.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "manager",
		addRole, addBinding, pdpb.BatchService_Do_FullMethodName), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	op := func(method string, m proto.Message) *pdpb.Op {
		v, err := anypb.New(m)
		x.NoError(err)

		return pdpb.Op_builder{Method: method, Request: v}.Build()
	}

	// The two Rpcs `escalate.go` opens with, in one transaction.
	_, err := pdpb.NewBatchServiceClient(conn).Do(wire, pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{op(addRole, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:   "sneaky",
			Methods: []string{eraseHold},
		}.Build())},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a batch wrote a role holding what its caller does not -- the layer is missing from the stack the transaction runs on")

	// Nothing was written, which is the transaction doing its half.
	n, err := b.Ent.Role.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "the refused batch left a role behind")

	// And what she may write, she may write in a batch -- so the refusal above
	// is the escalation and not the batch.
	_, err = pdpb.NewBatchServiceClient(conn).Do(wire, pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{op(addRole, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:   "fine",
			Methods: []string{addRole},
		}.Build())},
	}.Build())
	x.NoError(err)
}

// TestNobodyGrantsAPatternWiderThanAnythingTheyHold.
//
// A role names **patterns**, and that is what makes the rule harder than a set
// comparison: `/roster.HolderService/*` is not in anybody's list of methods and
// is still something somebody can be holding, and something they can hand out.
//
// `mayGrant` therefore asks whether one grant of the caller's covers each
// pattern **on its own**, and never whether the union does. The difference is
// invisible on the day it is written and is the whole point:
//
//	Alice holds Holder.Get and Holder.Erase, which today is every method
//	HolderService has that she has any use for.
//	Alice writes a role naming /roster.HolderService/*, which allows nothing
//	she cannot already do.
//	A method is added to HolderService next quarter.
//	Everybody that role was granted to gains it, at once, and nobody reviews
//	a change to a role that was not touched.
//
// So the refusal is not about what the pattern reaches today; it is that a
// pattern is a claim about methods that do not exist yet, and only somebody who
// already holds that claim may make it. `List` stands in for the method added
// next quarter -- it exists, she does not hold it, and it is inside the pattern
// she is asking to hand out.
func TestNobodyGrantsAPatternWiderThanAnythingTheyHold(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const listHolders = "/roster.HolderService/List"

	// Alice manages who is in what, and holds two methods of one service.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "manager",
		addRole, addBinding, getHolder, eraseHold), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	_, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "holders",
		Methods: []string{"/roster.HolderService/*"},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she wrote a role naming a pattern no grant of hers covers, and every method that service grows is now hers to hand out")

	// The same refusal one service along: a role she may not write is a role
	// somebody else's copy of is not hers to bind either.
	theirs := b.role(t, ctx, "holders-theirs", "/roster.HolderService/*")
	_, err = app.NewBindingServiceClient(conn).Add(wire, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: theirs.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she bound herself a pattern no grant of hers covers")

	// And what the pattern would have bought her, which is the cost rather
	// than the mechanism: a method of that service she never held.
	_, err = app.NewHolderServiceClient(conn).List(wire, app.HolderListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she holds a method she was never granted, so one of the two refusals above did not happen")

	// While a pattern she does hold is hers to pass on, so what refused above
	// is the widening and not the wildcard.
	t.Run("and a pattern she holds is hers to pass on", func(t *testing.T) {
		x := require.New(t)

		other := b.holder(t, ctx, b.Contoso, "wide")
		b.binds(t, other, b.role(t, ctx, "wide-manager", addRole, "/roster.HolderService/*"), nil)

		_, err := app.NewRoleServiceClient(conn).Add(asOverTheWire(ctx, other),
			app.RoleAddRequest_builder{
				Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
				Alias:   "holders-again",
				Methods: []string{"/roster.HolderService/*"},
			}.Build())
		x.NoError(err)
	})
}

// TestARoleWithNoMethodsIsStillAGrantOfScope.
//
// `mayGrant` answers immediately for a role that names no method: there is
// nothing to compare, so there is nothing to refuse. Which is right about
// **methods** and says nothing about the other axis, and the other axis is
// where a site administrator's whole confinement lives:
//
//	Alice administers Seoul: her binding names it, so `pd.Grouped` narrows
//	every read she makes to Seoul's rows.
//	Alice writes a role naming no method at all -- refused by nothing, and
//	correctly so, since it allows nothing.
//	Alice binds that role to herself with **no site**, which is the whole
//	tenant.
//	`policy.of` reads a binding with no site as `anySite`, and Alice now
//	reads Frankfurt -- with the methods her Seoul role gave her, because the
//	gate's method check never looks at a site.
//
// That is `TestASiteAdministratorStaysInTheirSite`'s escalation exactly, with
// the one ingredient removed that `mayGrant` inspects. Nothing about it is
// hypothetical: an empty role is a real thing to write, it is what a deployment
// makes when it wants a team to exist before it decides what the team may do.
//
// It is refused today, and by two different things depending on which way she
// writes it -- which is worth pinning precisely because neither of them is the
// rule anybody would think to check.
func TestARoleWithNoMethodsIsStillAGrantOfScope(t *testing.T) {
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	frankfurt := b.site(t, ctx, b.Contoso, "frankfurt")
	b.team(t, ctx, seoul, "ours")
	b.team(t, ctx, frankfurt, "theirs")

	// She administers Seoul: the role is Seoul's and so is her binding.
	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
		Alias:   "seoul-admin",
		Methods: []string{listTeams, addRole, addBinding},
	}.Build())
	require.NoError(t, err)
	b.binds(t, b.ContosoUser, mustId(t, r.GetId()), &seoul)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	// What she reaches, which is the measure everything below is against.
	reaches := func(t *testing.T) int {
		t.Helper()

		vs, err := app.NewTeamServiceClient(conn).List(wire, app.TeamListRequest_builder{}.Build())
		require.NoError(t, err)

		return len(vs.GetItems())
	}

	require.Equal(t, 1, reaches(t), "she is not narrowed to her own site to begin with")

	bind := func(role []byte) error {
		_, err := app.NewBindingServiceClient(conn).Add(wire, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: role}.Build(),
			Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			// No site, which is the whole tenant.
		}.Build())

		return err
	}

	// Written for her own site, where she may write one, and then bound wider
	// than the site it belongs to. `bindableIn` is what stands here, and it
	// reads the role rather than its methods -- which is why an empty one does
	// not slip past it.
	t.Run("one written for her site is not bindable across the tenant", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Site:   app.SiteRef_builder{Id: seoul.Bytes()}.Build(),
			Alias:  "nothing-in-seoul",
		}.Build())
		x.NoError(err, "an empty role is a real thing to write and she may write one in her own site")

		err = bind(v.GetId())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a role of one site was bound across the tenant because it named no method to argue about")
		x.Equal(1, reaches(t), "she widened her own second axis with a role that allows nothing")
	})

	// The two share one caller, deliberately -- they are two ways of writing
	// the same row and the claim is about her reach, which is one thing. It
	// means a failure above shows here as well: the second refusal is only
	// meaningful while the first one holds.
	//
	// And written for no site, which is this schema's ClusterRole and is the
	// tenant operator's to write. `mayGrant` lets her, since there is nothing
	// to hand out -- and the row she has written is one the wall does not show
	// her, so the binding names a role she cannot see and the gate answers
	// NotFound. A refusal by narrowing rather than by rule, which is worth
	// knowing: it is the only thing standing here.
	t.Run("and one written for no site is not hers to bind", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "nothing-at-all",
		}.Build())
		x.NoError(err)

		err = bind(v.GetId())
		x.Error(err, "a site administrator bound herself a tenant-wide row")
		x.Equal(1, reaches(t),
			"she reads every site in the tenant, with the methods her one site gave her")
	})
}
