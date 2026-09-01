package cmd_test

import (
	"context"
	"google.golang.org/protobuf/reflect/protoreflect"
	"strconv"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// Becoming somebody else by naming them.
//
// roster answers to four names: an identifier, `@tenant/alias`, an address,
// and a subject at a provider. Each of them is the same sentence said at a
// sign-in screen -- *this is me* -- and each of them is therefore a way to be
// somebody else, if what it names can be made to point at another person's
// row.
//
// So what every test in this file pins is one sentence, in the shape the name
// it is about takes it: **a name answers with the person it belongs to, or
// with nobody.** Never with whoever wrote it down most recently, never with
// somebody in another tenant, and never with a row that is not a person at
// all.
//
// The failure is always the same and is worth saying once. There is no error
// at the end of it: the sign-in works, the token is issued, the trail records
// the person whose name it was, and what a product app sees is that person
// doing whatever the caller went on to do. Nothing anywhere says a name was
// answered wrongly, because from inside the request it was not.

const (
	addIdentity = "/roster.IdentityService/Add"
	addAddress  = "/roster.EmailService/Add"
)

// TestNobodyWritesAWayInForSomebodyWiderThanThey is item 11's rule at the two
// other doors it has to hold at, and does not.
//
// `escalate.go` says who may write whose **credential**: only somebody whose
// permissions are a subset of yours, because setting a password is a way to
// *become* the person whose password it is. `operate_test.go` pins that rule,
// and the first subtest below is it again so that what follows is the same
// rule at another door rather than one this file invented.
//
// A password is not the only thing that becomes somebody. `server/core`'s own
// unlink rule counts what a person can sign in with and counts **an Identity
// or a Credential** -- *the two things a Login App and `VouchService` between
// them can turn into a signed-in person*. And `Vouch.Link` makes an address a
// third: it mints a way in for whoever an address names and lets somebody else
// deliver it, so whoever reads that mailbox is who arrives.
//
// Writing either onto another person's row is therefore the same act the rule
// already refuses, through a door nobody put a lock on:
//
//	Alice may call IdentityService.Add and EmailService.Add and nothing else.
//	Alice links her own Google account to the administrator's Holder.
//	Alice signs in at Google and is the administrator.
//
// Or, with no provider at all and nothing to log in to:
//
//	Alice adds her own mailbox as an address of the administrator's Holder.
//	Alice asks for a magic link at that address.
//	Alice redeems it and roster answers that she is the administrator.
//
// Two Rpcs each, from "Alice keeps people's contact details up to date" --
// which is a smaller sentence than "Alice may reset passwords", and the
// permission an organisation hands out with less thought than any other.
//
// The refusal is `mayReach`'s and it is already written: what is missing is
// the two calls to it. `Reaching` exists as a function precisely because
// `VouchService` is not a layer; these two are layers, so `coreIdentity.Add`
// and an `Email` layer that does not exist yet would ask `s.mayReach` the way
// `agree.go` asks `s.mayGrant`.
func TestNobodyWritesAWayInForSomebodyWiderThanThey(t *testing.T) {
	b, ctx := build(t)

	// The administrator, who may erase anybody.
	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.mayCall(t, ctx, boss, "admin", eraseHold, listHolders)

	// And the desk, who may write down who somebody is and nothing else.
	desk := b.holder(t, ctx, b.Contoso, "desk")
	asDesk := b.mayCall(t, ctx, desk, "desk", addIdentity, addAddress)

	// A mailbox the desk reads, which is the whole of the attack: whatever
	// arrives there is a way into whichever Holder the row hangs off.
	const mailbox = "desk@desk.example"

	t.Run("the credential door is shut", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Credential().Set(asDesk, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Secret: []byte("a new one"),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the desk reset the administrator's password")
	})

	t.Run("and so is the one at the provider", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Identity().Add(asDesk, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Provider: "github",
			Subject:  "1074321",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the desk linked an account of their own to the administrator")
	})

	t.Run("and so is the one at the mailbox", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Email().Add(asDesk, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Address: mailbox,
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the desk put their own mailbox on the administrator's row")
	})

	// And the same write on the port a caller actually reaches, so that what
	// refused it -- or did not -- cannot be said to be the harness.
	//
	// The desk holds `IdentityService.Add`, so the gate interceptor lets them
	// through: that is the whole shape of this, and it is why the refusal has
	// to be a layer rather than a permission. Another provider than the one
	// above, because `core.oneAccountPerProvider` would refuse a second GitHub
	// account on the same row and this would then pass for a reason that has
	// nothing to do with who may write it.
	t.Run("and it is not the port that refuses it either", func(t *testing.T) {
		x := require.New(t)

		conn := served(t, b.Server)

		_, err := app.NewIdentityServiceClient(conn).Add(asOverTheWire(ctx, desk),
			app.IdentityAddRequest_builder{
				Holder:   app.HolderRef_builder{Id: boss.Bytes()}.Build(),
				Provider: "entra",
				Subject:  "8f14e45f-ea1e-4f0e-9a1b-2c3d4e5f6a7b",
			}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the desk linked an account of their own to the administrator, over the wire")
	})

	// And the end of it, which is the reason the two above are worth refusing.
	//
	// Written as what must be true rather than as what happens today: an
	// address the desk owns names **nobody**, so a link minted at it resolves
	// to nothing -- which is the answer `link.go` gives for a stranger and is
	// what makes asking say nothing about who is here.
	t.Run("and a link at the desk's own address is nobody's", func(t *testing.T) {
		x := require.New(t)

		v := b.operated()

		made, err := v.Link(asDesk, app.VouchLinkRequest_builder{
			Who: app.VouchWho_builder{Tenant: "contoso", Address: mailbox}.Build(),
		}.Build())
		x.NoError(err)

		res, err := v.Redeem(asDesk, app.VouchRedeemRequest_builder{
			Token:   made.GetToken(),
			Methods: []string{listHolders},
		}.Build())
		x.NoError(err)
		x.False(res.GetVerified().GetOk(),
			"a mailbox the desk reads signed somebody in")
		x.NotEqual(boss.Bytes(), res.GetVerified().GetHolder(),
			"roster answered that the desk is the administrator")
	})
}

// TestAnAddressNamesOnePersonInATenant is the constraint F7 closed on, and
// the half of it a constraint cannot state.
//
// `Email` is unique on `(tenant_id, address)` so that `VouchWho` can take an
// address at all: with a tenant -- which a front door now has, off its own
// hostname -- there is exactly one row to find. `front_test.go` pins the
// refusal itself.
//
// What is here is what happens **after** the refusal, which no index says
// anything about. A write that refused and moved the row, or that refused and
// left the address resolving to whoever asked last, would be a takeover with
// an error message on it -- the caller is told no and the sign-in answers with
// them anyway. So the assertion is not that the second `Add` fails; it is that
// the person who has the address is still who the address answers with.
func TestAnAddressNamesOnePersonInATenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const theirs = "someone@contoso.example"

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err)
	b.sets(t, ctx, b.ContosoUser, "the password they chose")

	// Somebody else in the same tenant, with a password of their own so that
	// which row answered is visible in the answer rather than inferred.
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.sets(t, ctx, alice, "the one alice chose")

	_, err = b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err))

	res := b.verifiesAt(t, ctx, "contoso", theirs, "the password they chose")
	x.True(res.GetOk(), "their own password stopped opening their own address")
	x.Equal(b.ContosoUser.Bytes(), res.GetHolder())

	// And the direction the refusal exists for. Alice's password at an address
	// that is not hers is the same nothing anybody else gets, and the answer
	// carries no holder -- told apart, a refusal would say whose address it is.
	res = b.verifiesAt(t, ctx, "contoso", theirs, "the one alice chose")
	x.False(res.GetOk(), "alice's password opened somebody else's address")
	x.Empty(res.GetHolder())
}

// TestAnAddressIsStoredAsItIsLookedUp is `TestAHostIsStoredAsItIsCompared`,
// one entity along, and it costs more here.
//
// The index compares the column. `vouch.byAddress` looks an address up
// **lowered and trimmed**, because what somebody types at a sign-in screen is
// not canonical anything. Nothing normalises the write, so those two do not
// agree, and everything between them is a second row for one address:
//
//	The victim's address arrived from Entra as `Someone@Contoso.example`,
//	which is how it is stored, because nothing lowered it.
//	Alice adds `someone@contoso.example` to her own Holder. The index compares
//	two different strings and agrees.
//	The victim types their own address at the sign-in screen. It is lowered,
//	it finds Alice's row, and roster answers that this address is Alice.
//
// After which the victim's own password does not open their own address,
// Alice's does, and a magic link posted to the victim's mailbox signs whoever
// clicks it in as Alice -- which is the account their work lands in for as
// long as nobody notices. Who got there first does not come into it: the only
// row the lookup can reach is the **lowered** one, so the address answers with
// whoever wrote that form, and the person whose address it is cannot take it
// back by writing it again -- their own form is what is already there.
//
// And in a deployment nobody has attacked it is the quieter half that shows
// up: an address stored as a provider sent it cannot sign in at all, because
// the lookup lowers what it is given and the column never was. F7 is closed
// only for the addresses that happen to be lowercase already.
//
// The refusal belongs at the **write**, where `host.go` puts it, saying what
// the address should have been. Fixing it quietly at the write is the thing
// `front_test.go` refused for a hostname -- it hands back a row that differs
// from what the caller wrote and then disagrees with itself -- and fixing it
// in the lookup leaves the duplicate rows in the table.
func TestAnAddressIsStoredAsItIsLookedUp(t *testing.T) {
	b, ctx := build(t)

	// As a provider's claims carry it, which is where most addresses here come
	// from and is why this is the write that has to be refused rather than a
	// spelling nobody would produce.
	const asSent = "Someone@Contoso.example"
	const stored = "someone@contoso.example"

	t.Run("the write says what it should have been", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: asSent,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.Contains(status.Convert(err).Message(), strconv.Quote(stored),
			"the refusal did not name the form it should have been stored as")
	})

	// So this is what is there, and it is the only form there can be.
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: stored,
	}.Build())
	require.NoError(t, err)
	b.sets(t, ctx, b.ContosoUser, "the password they chose")

	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.sets(t, ctx, alice, "the one alice chose")

	// Every one of these is the same address, and each is a way the second row
	// used to be written. A mail server is free to care about the case of a
	// local part; no organisation's does, `byAddress` does not, and neither
	// does the person typing it in.
	t.Run("and no way of writing it is a second row", func(t *testing.T) {
		for _, tc := range []struct{ desc, address string }{
			{"the local part in capitals", "SOMEONE@contoso.example"},
			{"the domain in capitals", "someone@CONTOSO.EXAMPLE"},
			{"as the provider sent it", asSent},
			{"exactly as it is stored", stored},
			{"a space in front", " someone@contoso.example"},
			{"a space behind", "someone@contoso.example "},
		} {
			t.Run(tc.desc, func(t *testing.T) {
				x := require.New(t)

				_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
					Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
					Address: tc.address,
				}.Build())
				x.Error(err, "a second row in this tenant now holds one address")

				// Either answer is a refusal a console can act on, and which
				// one it is says which rule caught it: the write saying what
				// the address should have been written as, or the index saying
				// that address is taken.
				x.Contains([]codes.Code{codes.AlreadyExists, codes.InvalidArgument},
					status.Code(err))
			})
		}
	})

	// And the end of it, which is what the refusals above are for. The address
	// is the victim's, typed exactly as roster holds it.
	t.Run("and the address answers with the person whose it is", func(t *testing.T) {
		x := require.New(t)

		res := b.verifiesAt(t, ctx, "contoso", stored, "the password they chose")
		x.True(res.GetOk(), "their own password no longer opens their own address")
		x.Equal(b.ContosoUser.Bytes(), res.GetHolder())

		res = b.verifiesAt(t, ctx, "contoso", stored, "the one alice chose")
		x.False(res.GetOk(), "alice's password opened the victim's address")
		x.NotEqual(alice.Bytes(), res.GetHolder(),
			"roster answered that the victim's address is alice")
	})

	// And the address as a provider sends it reaches the row anyway, which is
	// the quieter half of the same disagreement: before, an address stored the
	// way it arrived could not sign in at all, because the lookup lowered what
	// it was given and the column never was. F7 was closed only for the
	// addresses that happened to be lowercase already.
	t.Run("and it is found however it is typed", func(t *testing.T) {
		x := require.New(t)

		for _, typed := range []string{asSent, " " + stored + " ", "SOMEONE@CONTOSO.EXAMPLE"} {
			res := b.verifiesAt(t, ctx, "contoso", typed, "the password they chose")
			x.True(res.GetOk(), "%q did not reach the row it names", typed)
		}
	})
}

// verifiesAt is a sign-in by tenant and address, which is what a front door
// that has read its own hostname has to work with.
func (b *built) verifiesAt(t *testing.T, ctx context.Context, tenant, address, secret string) *app.VouchVerifyResponse {
	t.Helper()

	v, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Tenant: tenant, Address: address}.Build(),
		Secret: []byte(secret),
	}.Build())
	require.NoError(t, err)

	return v
}

// TestNamingSomebodyInAnotherTenantNamesNobody, in every direction a name
// travels.
//
// A tenant is the same service under another operator, so a name that crossed
// one would be one operator writing into another's people -- and the write
// that matters is not `Patch`, it is a **way in**: an identity or an address
// attached to somebody the caller cannot see is a sign-in that lands in
// another organisation.
//
// The wall is a predicate and an `Add` has no row to narrow, so what refuses
// these is the generated `Gate`: it reads the `Holder` the request names
// through the walled stack first, and a row outside the wall is `NotFound`
// rather than a refusal -- that such a person exists is itself not to be said.
//
// The slug is the same claim asked as a read, and it is here because it is the
// one shape where being answered *something* is plausible: `@fabrikam/someone`
// and `@contoso/someone` are the same alias, so a resolution that fell back to
// the caller's own tenant when it could not see the named one would answer
// with somebody real, whose row reads correctly, in a tenant the caller may
// see -- and every read after it would be about the wrong person.
func TestNamingSomebodyInAnotherTenantNamesNobody(t *testing.T) {
	b, ctx := build(t)

	// The same alias in both tenants, which is what makes an alias a name
	// rather than an identifier -- and is the case a wrong answer hides in.
	theirs := b.holder(t, ctx, b.Fabrikam, "someone")

	asContoso := b.as(ctx, b.ContosoUser, b.Contoso)
	asFabrikam := b.as(ctx, theirs, b.Fabrikam)

	t.Run("an identity written onto their person", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Identity().Add(asContoso, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
			Provider: "github",
			Subject:  "1074321",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"contoso linked an account to somebody in fabrikam")

		// And back the other way, because a wall that holds in one direction
		// and not the other is a wall that was written for the test.
		_, err = b.Walled.Identity().Add(asFabrikam, app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Provider: "github",
			Subject:  "2200002",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"fabrikam linked an account to somebody in contoso")
	})

	t.Run("an address written onto their person", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Email().Add(asContoso, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
			Address: "contoso@contoso.example",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"contoso put a mailbox of its own on somebody in fabrikam")

		_, err = b.Walled.Email().Add(asFabrikam, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Address: "fabrikam@fabrikam.example",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"fabrikam put a mailbox of its own on somebody in contoso")
	})

	t.Run("and a slug that names their tenant", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Walled.Holder().Get(asContoso, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{
				Slug: app.HolderRefBySlug_builder{
					Tenant: app.TenantRef_builder{Alias: z.Ptr("fabrikam")}.Build(),
					Alias:  z.Ptr("someone"),
				}.Build(),
			}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))

		// Neither of the two people it could have been. The second is the one
		// worth asserting: answered with contoso's own `someone`, every read
		// after it is about the right tenant and the wrong person, and nothing
		// in the answer says so.
		x.NotEqual(theirs.Bytes(), v.GetId(), "contoso read fabrikam's person")
		x.NotEqual(b.ContosoUser.Bytes(), v.GetId(),
			"a slug naming another tenant was answered from the caller's own")
	})
}

// TestASubjectIsNotANameWithoutItsTenant is what the tenant in `Identity`'s
// key buys, asked from the outside.
//
// The pair `(provider, subject)` is what an OIDC callback holds and it is
// **not** a name here: `identity.proto` puts the tenant in the unique index
// deliberately, so that the same human signing up to two operators' services
// with one Google account is two people with two histories. A reference that
// left the tenant out would undo that at the read -- one account at a provider
// would answer with whichever operator's row happened to be found, and the
// front door that asked has no way to tell that it got somebody else's person.
//
// So `IdentityRefBySubject` carries `tenant_id` and a request that omits it is
// refused rather than widened. Which is the one refusal in this file a caller
// meets by accident: a Login App holds the pair from the callback and has to
// go and get the tenant from `FrontService.WhoseHost` to ask at all.
func TestASubjectIsNotANameWithoutItsTenant(t *testing.T) {
	b, ctx := build(t)

	// One Google account, at two operators. Two people, by design.
	theirs := b.holder(t, ctx, b.Fabrikam, "someone")
	b.identity(t, ctx, theirs, "github", "1074321")
	b.identity(t, ctx, b.ContosoUser, "github", "1074321")

	at := func(tenant pdid.Id) *app.IdentityGetRequest {
		return app.IdentityGetRequest_builder{
			Ref: app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					TenantId: tenant.Bytes(),
					Provider: z.Ptr("github"),
					Subject:  z.Ptr("1074321"),
				}.Build(),
			}.Build(),
			Select: app.IdentitySelect_builder{
				Holder: app.HolderSelect_builder{}.Build(),
			}.Build(),
		}.Build()
	}

	t.Run("the pair alone names nobody", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Identity().Get(ctx, app.IdentityGetRequest_builder{
			Ref: app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					Provider: z.Ptr("github"),
					Subject:  z.Ptr("1074321"),
				}.Build(),
			}.Build(),
		}.Build())

		// InvalidArgument and not NotFound: the request is the thing that is
		// wrong, and a deployment where this answered *nothing* would answer
		// *somebody* the moment one of the two rows was erased.
		x.Equal(codes.InvalidArgument, status.Code(err),
			"one Google account answered across the whole deployment")
	})

	// Asked with the tenant, it is two rows and each names its own person --
	// through the server the wall was never installed on, so what is being
	// read here is the reference and not the scope.
	t.Run("and with a tenant it is that tenant's person", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ungated.Identity().Get(ctx, at(b.Contoso))
		x.NoError(err)
		x.Equal(b.ContosoUser.Bytes(), v.GetHolder().GetId())

		v, err = b.Ungated.Identity().Get(ctx, at(b.Fabrikam))
		x.NoError(err)
		x.Equal(theirs.Bytes(), v.GetHolder().GetId())
	})

	// And naming somebody else's tenant is not a way to read it, which is the
	// wall doing what the reference already said.
	t.Run("and naming another tenant answers with nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Identity().Get(b.as(ctx, b.ContosoUser, b.Contoso), at(b.Fabrikam))
		x.Equal(codes.NotFound, status.Code(err),
			"contoso read who signs in to fabrikam with that account")
	})
}

// TestAnIdentifierNamesOnlyItsOwnKind, and the byte that says which kind is
// the caller's word rather than the answer.
//
// A `pdid.Id` carries a domain so that a reference can be refused before the
// database is asked -- and it is sixteen bytes in a request, so what it says
// about itself is whatever the caller wrote. [pdid.WithDomain] is that in one
// line. So the kind is checked where it can be checked, at a write, and what
// makes a reference safe is that it is answered by a **row**: naming a Site
// where a person belongs finds no person, whatever the ninth byte says.
//
// What it would cost is a way in hanging off something that is not a person.
// A `Site` is inside a tenant and is not somebody; an `Email` or an `Identity`
// that named one would be a row whose `holder.tenant` reaches nowhere, and
// whose address or subject nothing can be resolved to a person by -- except by
// whatever read it next, which is the thing that would then be answered
// wrongly.
//
// `InvalidArgument` and not merely *an error*, because where it is refused is
// half of the claim. What refuses it is the stamp being resolved -- the
// `Holder` the edge names is read, and *it does not name one row* -- which
// happens before anything is written. There is a foreign key underneath as
// well and it is not what this pins: a constraint answers after the statement
// is built, says whatever the dialect says, and is the second line of a
// defence rather than the first.
func TestAnIdentifierNamesOnlyItsOwnKind(t *testing.T) {
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")

	// The same sixteen bytes with the domain rewritten to say `holder`. It is
	// one line to write, which is the point: the byte is part of the request
	// and so it is part of what a caller may lie about.
	forged := pdid.WithDomain(seoul, pd.HolderDomain)

	for _, tc := range []struct {
		desc string
		who  pdid.Id
	}{
		{"a site where a person belongs", seoul},
		{"and one that claims to be a person", forged},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
				Holder:  app.HolderRef_builder{Id: tc.who.Bytes()}.Build(),
				Address: "seoul@contoso.example",
			}.Build())
			x.Equal(codes.InvalidArgument, status.Code(err),
				"an address was written for something that is not a person")

			_, err = b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
				Holder:   app.HolderRef_builder{Id: tc.who.Bytes()}.Build(),
				Provider: "github",
				Subject:  "1074321",
			}.Build())
			x.Equal(codes.InvalidArgument, status.Code(err),
				"a way in was written for something that is not a person")

			// Read off the tables rather than off the two errors: what matters
			// is that nothing was written, and an `Add` that refused after
			// writing would say the same thing to a caller.
			n, err := b.Ent.Email.Query().Count(ctx)
			x.NoError(err)
			x.Zero(n)

			n, err = b.Ent.Identity.Query().Count(ctx)
			x.NoError(err)
			x.Zero(n)
		})
	}

	// And asked to sign one in, it is the answer a stranger gets -- not an
	// error, which would tell whoever sent it that they had named something
	// real. `vouch.refOf` reads the shape of the identifier and not its
	// domain, so this is the row being missing rather than the byte.
	t.Run("and signing one in is the answer nobody gets", func(t *testing.T) {
		x := require.New(t)

		v := b.verifies(t, ctx, forged, "whatever they sent")
		x.False(v.GetOk())
		x.Empty(v.GetHolder())
	})
}

// TestAStampIsNotAFieldACallerWrites.
//
// The tenant stamp is the whole of what a read is narrowed by: `pd.Wall`
// compares it and nothing else, so a row whose stamp says another tenant is a
// row that tenant reads and this one does not. Which makes it the shortest way
// across the wall there could be -- not a reference to follow or a permission
// to acquire, one column.
//
// `Identity.tenant_id` was declared without `immutable: true` while
// `Email.tenant_id`, the other stamp in this schema and written the same way,
// carried it. So it landed in the generated patch request as an ordinary field,
// and a write could move the row.
//
// It was closed at the transport the whole time -- `/Patch` is a general write
// and roster opens none -- which is why nothing had noticed, and is not a
// reason to leave a column a batch or the servers' own writes reach by another
// road. The schema is where it is said, and the assertion is that it is not a
// field at all rather than that some layer refuses it.
func TestAStampIsNotAFieldACallerWrites(t *testing.T) {
	x := require.New(t)

	// Read off the descriptor rather than by trying a write, because what is
	// being pinned is that there is nothing to try: a caller cannot name the
	// column, in any request, on any road.
	for _, tc := range []struct {
		what string
		m    protoreflect.MessageDescriptor
	}{
		{"an identity", (&app.IdentityPatchRequest{}).ProtoReflect().Descriptor()},
		{"an email", (&app.EmailPatchRequest{}).ProtoReflect().Descriptor()},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			x.Nil(tc.m.Fields().ByName("tenant_id"),
				"a patch can move the row across the wall")
		})
	}

	// And the stamp is still answered, so what went is the write and not the
	// column: a caller may read which tenant a row is filed under.
	x.NotNil((&app.IdentitySelect{}).ProtoReflect().Descriptor().Fields().ByName("tenant_id"))
}
