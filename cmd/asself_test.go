package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/credential"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// Becoming somebody else through a call that is supposed to be about you.
//
// D23 gave `MeService` its shape and stated the property that makes it safe:
// **it takes nothing, so it cannot be pointed at anybody else.** The caller is
// the subject by construction, and `cmd.Policy` waives a binding for it on
// exactly that ground -- so the absence of an argument is not a convenience
// here, it is the whole of the authorisation.
//
// Every other self-service act is the same shape wearing different clothes.
// Signing yourself out, removing your own way in, changing your own password,
// enrolling your own phone: each of them is a write that is safe only while
// *whose row* is decided by the frame rather than by the request. The moment
// one of them takes a subject -- a ref, a filter, a field a builder happens to
// carry -- it stops being self-service and becomes an operator's call with no
// operator's rule on it.
//
// The failure is worth saying once, because it looks like nothing. There is no
// error: the page loads, the button works, the trail records the person whose
// row it was, and what every product app downstream sees is that person doing
// it. A self-service screen is the one surface every account holder can reach
// without being given anything at all, so the population that can try is the
// whole customer base rather than the handful of people with a role.
//
// What each test here pins is one sentence in the shape its own door takes it:
// **a self-service call is about whoever is calling, and about nobody else.**

const getMe = "/roster.MeService/Get"

// namesNobody is a set of field names that are somebody rather than something.
//
// A request may carry an identifier without being about a person -- `Unlink`
// takes the identifier of an `Identity` row, which is a *which* and never a
// *whose*, because the query that finds it is narrowed by the caller first. So
// the test below cannot simply refuse every field; what it refuses is a field
// that names a **subject**, and these are the names this schema gives one.
var namesNobody = map[string]bool{
	"actor":    true,
	"email":    true,
	"group":    true,
	"holder":   true,
	"identity": true,
	"ref":      true,
	"site":     true,
	"sub":      true,
	"subject":  true,
	"team":     true,
	"tenant":   true,
	"who":      true,
}

// TestNothingTheWallIsWaivedForCanNameAnybody is D23's property, asked of the
// list rather than of the handler.
//
// `cmd.Policy` denies by default and waives that for a named few methods, and
// the reason it may is stated in `aboutYourself`: *there is no subject
// argument, so it cannot be pointed at anybody else, and the absence of that
// argument is what makes this safe rather than a judgement about the handler.*
// That is a claim about a **message**, and nothing was checking it.
//
// So this reads the waiver the way an attacker would: it asks the policy about
// every method this deployment serves, as somebody who holds nothing, and for
// each one it is let through it goes and looks at what that method's request
// can carry. A method that is waived *and* takes a subject is a call anybody
// with an account may make about anybody they can name -- which is not a
// missing permission, it is a permission granted to the entire customer base
// at once.
//
// It is written this way round on purpose. A test naming `MeService`'s three
// methods would pass forever while the fourth thing somebody adds to the list
// -- `Holder.Invalidate` reads like "sign somebody out", `Holder.Get` reads
// like "my profile" -- walks straight through it. The list is what is
// dangerous, so the list is what is read.
//
// What this does not cover is the other door with no permission on it: a
// method served to callers who have no credential at all. That is `cmd.public`
// and `AuthService.SignIn` is deliberately on it, subject and all -- somebody
// signing in has nothing to be narrowed by yet. See `cmd.public`, and
// `vouch_test.go` for what keeps it honest.
func TestNothingTheWallIsWaivedForCanNameAnybody(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Somebody real, holding nothing -- which is every account the moment it is
	// created, and is the population this is about.
	nobody := b.holder(t, ctx, b.Contoso, "nobody")

	p := cmd.Policy(b.Ent)
	waived := []string{}

	// Off the server's own descriptors rather than written out, for
	// `mayAnything`'s reason: a list written by hand is right on the day it is
	// written, and the method somebody adds tomorrow is the one nobody looks
	// at.
	for svc, info := range b.grpc(t).GetServiceInfo() {
		for _, m := range info.Methods {
			method := "/" + svc + "/" + m.Name

			err := p.May(ctx, gate.Call{Actor: nobody, Tenant: b.Contoso, Action: method})
			if err != nil {
				x.Equal(codes.PermissionDenied, status.Code(err),
					"%s: refused for a reason that is not the wall", method)

				continue
			}

			waived = append(waived, method)

			d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
			x.NoError(err, "%s is waived and its schema cannot be read", method)

			sd, ok := d.(protoreflect.ServiceDescriptor)
			x.True(ok)

			md := sd.Methods().ByName(protoreflect.Name(m.Name))
			x.NotNil(md)

			where, named := subjectIn(md.Input(), map[protoreflect.FullName]bool{})
			x.False(named,
				"%s needs no role and takes %s: anybody with an account may make it about anybody",
				method, where)
		}
	}

	// And the waiver is not empty, so that a policy which had quietly stopped
	// waiving anything -- or a descriptor walk that found no methods at all --
	// could not pass the loop above by having nothing to say.
	x.NotEmpty(waived, "nothing is waived, so the loop above proved nothing")
	x.Contains(waived, getMe, "the one call a person with no role has was not waived")
}

// subjectIn is the first field of this message that names a person, looked for
// the way somebody adding a field would arrive at one.
//
// Two shapes count. A **reference** -- any generated `*Ref` -- is the argument
// every walled service takes and is the whole of what "pointed at somebody
// else" means here. And a field **named** for a subject catches the other way
// it arrives: raw bytes called `holder`, a string called `subject`, an address
// that is looked up rather than referred to.
//
// Recursive, because a subject nested one message deep is still a subject and
// is how one would actually appear: nobody adds `holder` to `MeGetRequest`,
// they add a `filter` that has one in it.
func subjectIn(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) (string, bool) {
	if seen[md.FullName()] {
		return "", false
	}
	seen[md.FullName()] = true

	fs := md.Fields()
	for i := range fs.Len() {
		f := fs.Get(i)

		if namesNobody[string(f.Name())] {
			return string(md.Name()) + "." + string(f.Name()), true
		}
		if f.Kind() != protoreflect.MessageKind && f.Kind() != protoreflect.GroupKind {
			continue
		}

		in := f.Message()
		if strings.HasSuffix(string(in.Name()), "Ref") {
			return string(md.Name()) + "." + string(f.Name()) + " (" + string(in.Name()) + ")", true
		}

		if where, ok := subjectIn(in, seen); ok {
			return string(md.Name()) + "." + string(f.Name()) + " -> " + where, true
		}
	}

	return "", false
}

// TestSigningYourselfOutIsNotSigningAnybodyElseOut is D26's split, pinned from
// both ends.
//
// An epoch is the strongest thing a person can be handed about themselves:
// everything issued before it is void, so moving somebody's is ending every
// session they have, on every app, at once. `HolderService.Invalidate` takes a
// subject and is therefore an operator's; `MeService.SignOutEverywhere` takes
// nothing and is therefore everybody's, waived from the wall so that somebody
// who has just been given an account can end a session they no longer trust.
//
// Those two facts are only compatible while the second cannot name anybody. If
// it ever could -- or if the waiver ever grew to cover the first -- then a
// permission deliberately granted to every account holder becomes a way to
// throw the administrator out of every session they hold, from a page with a
// single button on it, with nothing refused and nothing to look at afterwards
// except a person who cannot stay signed in.
//
// So: the caller here holds **nothing at all**, which is what makes the pair
// of answers below mean something. The unwalled one works and moves one row;
// the one that takes a subject is refused.
func TestSigningYourselfOutIsNotSigningAnybodyElseOut(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// The administrator, who has sessions worth ending.
	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.mayCall(t, ctx, boss, "admin", eraseHold, listHolders)

	// And somebody with an account and no role, which is the population that
	// reaches a self-service page.
	mallory := b.holder(t, ctx, b.Contoso, "mallory")

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, mallory)

	res, err := app.NewMeServiceClient(conn).SignOutEverywhere(wire,
		app.MeSignOutEverywhereRequest_builder{}.Build())
	x.NoError(err, "somebody with an account could not sign themselves out")
	x.NotNil(res.GetDateInvalidated())

	// One row moved and it was hers. The subject came from the frame, so there
	// was nowhere for another name to come from -- which is the whole of what
	// the request having no field for one buys.
	x.Nil(b.epochOf(t, ctx, boss),
		"a person with no role ended every session the administrator held")
	x.NotNil(b.epochOf(t, ctx, mallory),
		"the row that moved was not the caller's, so it was somebody else's")

	// And the call that *does* take a subject is the operator's, refused to
	// her by the wall the one above is waived from. Both halves are needed:
	// waived-and-narrow is safe, permissioned-and-wide is safe, and it is the
	// diagonal that is the hole.
	t.Run("and the one that names somebody is an operator's", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewHolderServiceClient(conn).Invalidate(wire,
			app.HolderInvalidateRequest_builder{
				Ref: app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the subject-taking half of D26 was open to somebody holding nothing")

		x.Nil(b.epochOf(t, ctx, boss))
	})

	// Nor by naming herself and meaning somebody else: `Invalidate` is refused
	// for the method, before anything reads which row it is about, so there is
	// no version of this call she reaches by pointing it at a row she may see.
	t.Run("and not even about her own row", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewHolderServiceClient(conn).Invalidate(wire,
			app.HolderInvalidateRequest_builder{
				Ref: app.HolderRef_builder{Id: mallory.Bytes()}.Build(),
			}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}

// epochOf is when everything issued to somebody became void, or nil.
func (b *built) epochOf(t *testing.T, ctx context.Context, who pdid.Id) *timestamppb.Timestamp {
	t.Helper()

	v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Select: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	require.NoError(t, err)

	return v.GetDateInvalidated()
}

// TestMeAnswersWithNothingOfAnybodyElseS is the read half of D23, asked of
// every list on the message rather than of the one that was interesting at the
// time.
//
// `MeGetResponse` is a join: addresses, teams, identities, credentials and the
// union the gate enforces, each its own query, each narrowed by the frame's
// actor and by nothing else. There is no wall in front of any of them -- the
// package says so outright, *the rows it returns are the caller's own, selected
// by the frame's actor* -- so a query that forgets its holder predicate does
// not get refused somewhere further in. It answers, with the tenant's rows on
// a page that says "you".
//
// What that would cost is not equal across the five, and that is why the test
// is all five. An address is somebody's mailbox, and a mailbox is where a magic
// link arrives. An identity is the account they sign in with, named exactly
// enough to go and ask the provider for it. A credential says which factors
// somebody holds and when each was last changed, which is how an attacker picks
// whom to phone. Teams and methods say who is worth attacking at all.
//
// So the shape of this is one colleague who has one of **everything**, and a
// caller who has one of everything too -- because a leak that showed nothing
// but the caller's own rows and a leak that showed the whole tenant look
// identical when the caller's list is empty.
func TestMeAnswersWithNothingOfAnybodyElseS(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	mine := b.team(t, ctx, seoul, "mine")
	yours := b.team(t, ctx, seoul, "yours")

	// The caller, with one of each.
	me := b.holder(t, ctx, b.Contoso, "me")
	b.addressOf(t, ctx, me, "me@contoso.example")
	b.identity(t, ctx, me, "github", "1078")
	b.sets(t, ctx, me, "correct horse battery staple")
	b.inTeam(t, ctx, me, mine, b.role(t, ctx, "reader", getHolder))
	b.binds(t, me, b.role(t, ctx, "mine-only", getMe), nil)

	// And a colleague in the same tenant with one of each of their own, every
	// one of them distinguishable from the caller's.
	them := b.holder(t, ctx, b.Contoso, "them")
	b.addressOf(t, ctx, them, "them@contoso.example")
	b.identity(t, ctx, them, "entra", "8bf1e0a2")
	b.sets(t, ctx, them, "hunter2hunter2")
	b.inTeam(t, ctx, them, yours, b.role(t, ctx, "eraser", eraseHold))

	conn := served(t, b.Server)

	v, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, me),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err)

	// The person it is about, which every list below hangs off.
	x.Equal(me.Bytes(), v.GetId())
	x.Equal(b.Contoso.Bytes(), v.GetTenant())

	x.Len(v.GetEmails(), 1,
		"a colleague's mailbox was on a page that says 'you' -- and a mailbox is where a link arrives")
	x.Equal("me@contoso.example", v.GetEmails()[0].GetAddress())

	x.Len(v.GetTeams(), 1, "a colleague's team was answered as the caller's")
	x.Equal("mine", v.GetTeams()[0].GetAlias())

	x.Len(v.GetIdentities(), 1,
		"a colleague's account at a provider, named exactly enough to go and ask for it")
	x.Equal("github", v.GetIdentities()[0].GetProvider())
	x.NotEqual("8bf1e0a2", v.GetIdentities()[0].GetSubject())

	// Two rows of the same kind in the tenant, so a query that answered with
	// both would be answering with a colleague's rather than with a second one
	// of the caller's.
	x.Len(v.GetCredentials(), 1,
		"which factors a colleague holds, and when each was last changed")
	x.Equal(vouch.KindPassword, v.GetCredentials()[0].GetKind())

	// And the union, which is the one field on this message that is not a row
	// at all. It is what a page draws buttons from, so a colleague's methods
	// arriving here is a page offering somebody an operation the server will
	// refuse -- the drift `MeService` exists to prevent, and a list of what to
	// go and look for besides.
	x.Contains(v.GetMethods(), getMe)
	x.Contains(v.GetMethods(), getHolder, "the role held in their own team is missing")
	x.NotContains(v.GetMethods(), eraseHold,
		"a colleague's permissions were answered as the caller's")

	// The counts above are the whole assertion, so this is what makes them
	// mean something: there really were two of each in the tenant.
	t.Run("and there was something to leak", func(t *testing.T) {
		x := require.New(t)

		counts := map[string]func() (int, error){
			"emails":      func() (int, error) { return b.Ent.Email.Query().Count(ctx) },
			"identities":  func() (int, error) { return b.Ent.Identity.Query().Count(ctx) },
			"credentials": func() (int, error) { return b.Ent.Credential.Query().Count(ctx) },
			"teams":       func() (int, error) { return b.Ent.TeamMembership.Query().Count(ctx) },
		}
		for what, count := range counts {
			n, err := count()
			x.NoError(err)
			x.Equal(2, n,
				"%s: the tenant held one row, so a leak would have looked like a narrowing", what)
		}
	})
}

// addressOf puts a mailbox on somebody's row.
func (b *built) addressOf(t *testing.T, ctx context.Context, who pdid.Id, address string) {
	t.Helper()

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Address: address,
	}.Build())
	require.NoError(t, err)
}

// inTeam puts somebody in a team, holding a role there.
func (b *built) inTeam(t *testing.T, ctx context.Context, who, team, role pdid.Id) {
	t.Helper()

	_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: team.Bytes()}.Build(),
		Role:   app.RoleRef_builder{Id: role.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
}

// factoring is `VouchService` as a deployment that holds second factors wires
// it: a keyring to wrap a seed with, and the rule about whose credential a
// caller may write.
//

// TestNobodyEnrolsASecondFactorOnSomebodyWiderThanThey is item 11's rule at the
// door `Enrol` opened, and `operate.go` says why it belongs there: *adding a
// way in for somebody is not quite writing their credential, and it is close
// enough.*
//
// It is closer than that. A second factor is half of what the person signs in
// with, and it is the half a password reset does not touch -- so somebody who
// enrols a phone of their own onto an administrator's account has not taken the
// account today, they have taken the part that will still be theirs after the
// password is changed, after the reset that comes with a takeover, and after
// everybody has agreed the incident is closed. Nothing about the account looks
// wrong: it has 2FA, which is what an audit is checking for.
//
// The rule that refuses it is `mayReach`, and `Enrol` is one line away from not
// asking it -- the sibling that generates a seed asks nothing until it has one,
// and the read above it answers for anybody in the tenant. Every existing test
// of `Enrol` calls it with no frame at all, which is the deployment's own work
// and is the one case the rule deliberately allows.
func TestNobodyEnrolsASecondFactorOnSomebodyWiderThanThey(t *testing.T) {
	b, ctx := build(t)

	// The administrator, who may erase anybody.
	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.mayCall(t, ctx, boss, "admin", eraseHold, listHolders)

	// And the desk, whose whole job is helping people who cannot sign in --
	// which is the permission an organisation hands to its newest employee.
	desk := b.holder(t, ctx, b.Contoso, "desk")
	asDesk := b.mayCall(t, ctx, desk, "desk", listHolders)

	enrol := func(c context.Context, who pdid.Id, name string) error {
		_, err := b.Walled.Credential().Enrol(c, app.CredentialEnrolRequest_builder{
			Ref:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Kind: vouch.KindTotp,
			Name: name,
		}.Build())

		return err
	}

	// Somebody who holds nothing may be given one, which is what the desk is
	// for -- so the refusal below is the rule and not the door being shut.
	t.Run("the desk enrols a factor for somebody who holds nothing", func(t *testing.T) {
		x := require.New(t)

		joe := b.holder(t, ctx, b.Contoso, "joe")
		x.NoError(enrol(asDesk, joe, "the phone"),
			"the desk could not help somebody with no permissions at all")
	})

	t.Run("and not for somebody who holds more than they do", func(t *testing.T) {
		x := require.New(t)

		err := enrol(asDesk, boss, "the phone in my drawer")
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the desk put their own authenticator on the administrator's account")
		x.Contains(status.Convert(err).Message(), eraseHold,
			"the refusal did not say which permission was the problem")
	})

	// And nothing was written on the way to refusing. A row that went in and a
	// call that came back refused is the worst of both: the administrator now
	// holds a factor somebody else scanned, and the caller was told no, so
	// nobody goes looking.
	t.Run("and nothing of theirs was written on the way to refusing", func(t *testing.T) {
		x := require.New(t)

		n, err := b.Ent.Credential.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "a refused enrolment left a way in behind it")
	})

	// And the tenant next door, which is the case the rule above would have
	// **allowed** -- which is why it is pinned separately rather than assumed.
	//
	// Somebody in another customer's tenant holds nothing *here*: `Granted`
	// reads this deployment's bindings and none of theirs reach the caller.
	// `mayReach` sees an empty list, reads that as somebody with nothing to
	// escalate to, and says yes. What refuses this is the wall, twice over --
	// the read that resolves the person and the write that adds the row both go
	// through the walled stack -- and neither of those is about permissions at
	// all.
	//
	// Both are load-bearing, and that is the finding worth keeping: with either
	// one moved to the unwalled stack this is still refused, and with both moved
	// one customer enrols an authenticator on another customer's person. A
	// service that is not a layer holds its own wall by choosing which stack to
	// call, one call site at a time, and the rule beside it will not catch a
	// mistake there.
	t.Run("and not for somebody in another tenant at all", func(t *testing.T) {
		x := require.New(t)

		them := b.holder(t, ctx, b.Fabrikam, "erlich")

		err := enrol(asDesk, them, "the phone")
		x.Equal(codes.NotFound, status.Code(err),
			"one customer put an authenticator on another customer's person")

		n, err := b.Ent.Credential.Query().
			Where(credential.HasHolderWith(entholder.IdEQ(them.Uuid()))).
			Count(ctx)
		x.NoError(err)
		x.Zero(n, "a way in was written for somebody the caller cannot even read")
	})

	// Their own is not becoming somebody else, and this is the case that makes
	// the rule usable: an administrator enrolling their own phone holds
	// everything they hold, which is true and is a strange way to write it.
	t.Run("and anybody may enrol their own", func(t *testing.T) {
		x := require.New(t)

		asBoss := b.asNobody(ctx, boss, b.Contoso)
		x.NoError(enrol(asBoss, boss, "my own phone"),
			"an administrator could not add a second factor to their own account")
	})
}

// TestAResetIsMadeByWhoeverIsAskingAndNotByTheDeployment is the door around the
// rule rather than the rule itself.
//
// `Reset` does not write a credential. It generates a passphrase and calls
// `Set`, *which is where the hashing, the wall and the escalation rule already
// are* -- so the refusal that stops an operator becoming an administrator is
// one function call away, in code that has every reason to look like plumbing.
// `operate_test.go` pins the rule on `Set`; what nothing pins is that `Reset`
// is still standing behind it.
//
// The way that stops being true is an edit somebody would make without feeling
// they had changed anything: write the row here rather than call `Set`, because
// a reset wants one field `Set` does not take, or because two round trips
// looked like one too many. `Reset` already reaches for `s.walled.Credential()`
// on the line below, so the parts are in scope and the diff is short. What goes
// with them is the refusal, and then every operator holds every permission in
// their tenant, two calls at a time, with a generated password to show for it.
//
// The other edit of that family is passing a context that is not the caller's
// -- `Reset` already makes one best-effort call whose failure is deliberately
// ignored. That one is caught by the wall rather than by the rule, since the
// read `Set` makes has no frame to narrow by either; it is worth knowing that
// the two refusals are not the same refusal, and that only one of them says
// anything about who the caller is.
func TestAResetIsMadeByWhoeverIsAskingAndNotByTheDeployment(t *testing.T) {
	b, ctx := build(t)

	const listSites = "/roster.SiteService/List"

	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.mayCall(t, ctx, boss, "admin", eraseHold, listHolders, listSites)

	ops := b.holder(t, ctx, b.Contoso, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", listHolders, listSites)

	v := b.operated()
	reset := func(c context.Context, who pdid.Id) (string, error) {
		res, err := v.Reset(c, app.VouchResetRequest_builder{
			Who: app.VouchWho_builder{Id: who.Bytes()}.Build(),
		}.Build())

		return res.GetSecret(), err
	}

	// Narrower first, so that the refusal below is about who the person is and
	// not about the call being shut.
	t.Run("an operator resets somebody narrower than they are", func(t *testing.T) {
		x := require.New(t)

		mate := b.holder(t, ctx, b.Contoso, "mate")
		b.mayCall(t, ctx, mate, "narrower", listHolders)

		secret, err := reset(asOps, mate)
		x.NoError(err, "an operator could not reset somebody holding less than they do")
		x.NotEmpty(secret)
	})

	t.Run("and not somebody wider", func(t *testing.T) {
		x := require.New(t)

		_, err := reset(asOps, boss)
		x.Equal(codes.PermissionDenied, status.Code(err),
			"an operator became the administrator, with a password to sign in as them")
		x.Contains(status.Convert(err).Message(), eraseHold)
	})

	// The password they already had still works, which is the assertion that
	// says the refusal happened **before** the write rather than after it.
	//
	// A `Reset` that hashed first and refused afterwards would leave the
	// administrator with a secret nobody knows -- locked out by a call that was
	// refused, which is a denial of service delivered through the permission
	// check that was supposed to stop one.
	t.Run("and what the administrator had is untouched", func(t *testing.T) {
		x := require.New(t)

		b.sets(t, ctx, boss, "correct horse battery staple")

		_, err := reset(asOps, boss)
		x.Equal(codes.PermissionDenied, status.Code(err))

		got := b.verifies(t, ctx, boss, "correct horse battery staple")
		x.True(got.GetOk(), "a refused reset changed the password anyway")
	})

	// And the sessions the reset would have ended are still there. `Reset`
	// invalidates because *a password reset that leaves old sessions alive is
	// not a reset*; a refused one that invalidated anyway would be an operator
	// throwing the administrator out of every app they are signed into, on
	// demand, from a call they are not allowed to make.
	t.Run("and so are the sessions it would have ended", func(t *testing.T) {
		x := require.New(t)

		x.Nil(b.epochOf(t, ctx, boss),
			"a refused reset signed the administrator out of everything")
	})
}
