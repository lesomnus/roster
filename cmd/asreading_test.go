package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/vouch"
)

// Learning enough about somebody else to become them, or to know they exist.
//
// The wall is a **predicate**, which `pd.Wall` says in as many words: *narrowing
// what a caller may see is a predicate, and a predicate belongs in the query*.
// That is what makes it cheap and it is also what makes it fragile -- there is
// no second check behind it. A row a query matched is a row that is answered,
// so any read that composes its own query is a read that can be the one that
// forgot, and nothing anywhere will say so. `bare.<Entity>Narrow` exists for
// exactly that reason and names the failure itself: *a List is the one read
// nothing generates, and so the one that would otherwise answer with rows
// nobody should be given.*
//
// So the first three of these walk the reads this app serves -- a `Select` that
// loads an edge, a stream, a list carrying a filter -- and ask each one the
// same question: does it narrow the way `Get` narrows? The comparison is deliberate.
// Not "is this refused", which passes for any number of accidental reasons, but
// *does this answer what `Get` answers*, for one caller and one filter, since
// `Get` is the read whose narrowing is generated and reviewed.
//
// The last two are the other half of the same question, and D14 is why they
// belong beside it. An oracle does not leak a row; it leaks the **existence**
// of one, which is what somebody looking for an account to take over is
// shopping for. A verifier a caller can read is worse: an argon2 hash is a
// password given enough hardware, and a wrapped TOTP seed is every code that
// person will ever be asked for. Neither is a row anybody has to cross the wall
// to reach -- they sit on rows their own owner may read.

// TestNothingOfAnErasedHolderIsReadableThroughARowThatOutlivedThem
//
// `holder.proto` states the guarantee: an erased holder "cannot be read, cannot
// be changed, and **cannot authenticate**", and gives the wall as the reason --
// every read is narrowed by that column. F9 found two ways that sentence was
// false and closed both: `Verify` through `CredentialRefByKind`, and `Email.Get`
// through `(erased holder, address)`. Both were a **reference** composed over an
// edge, and both were fixed in `protoc-gen-orm-ent` by making `<Entity>Pick`
// answer among the live rows. `TestNothingOfAnErasedHolderIsReadableByNamingThem`
// and `TestAnErasedHolderCannotAuthenticate` are what keeps them closed.
//
// This is a third way, and it is not a reference at all -- which is why the fix
// to references did not reach it. It is a **`Select`**. `bare.EmailSelect` loads
// the edge with
//
//	q.WithHolder(func(q *ent.HolderQuery) { HolderSelect(q, m.GetHolder()) })
//
// and that inner query goes through no `HolderNarrow`. Not the wall, and not
// `holder.DateErasedIsNil()`. So the parent of any row a caller may read is
// readable whole, whatever state it is in.
//
// What it costs: erase somebody, and their `Email` row survives them -- nothing
// cascades, which is deliberate. `Email.List` with no filters answers it to
// anybody who may read that tenant's mail, and `EmailRefByAt{tenant, address}`
// names it without naming them. Ask for `select.holder.all` on the way past and
// the person comes back entire: alias, name, `profile`, `idp_subject`,
// `date_disabled`, `date_invalidated` -- everything `HolderService.Get` answers
// NotFound to, one call later, for the same caller.
//
// That is the whole of what a directory holds about a person, for every person
// somebody thought they had removed, reachable with `EmailService/Get` and no
// permission anybody would hesitate over.
func TestNothingOfAnErasedHolderIsReadableThroughARowThatOutlivedThem(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const address = "joe@contoso.example"

	joe := b.holder(t, ctx, b.Contoso, "joe")
	_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: joe.Bytes()}.Build(),
		Name:             z.Ptr("Joseph Bloggs"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: joe.Bytes()}.Build(),
		Address: address,
	}.Build())
	x.NoError(err)

	// One of their identities too, so that this is about the shape rather than
	// about `Email` -- every entity that reaches its tenant through the holder
	// generates the same `Select`.
	sub := b.identity(t, ctx, joe, "entra", "joes-object-guid")

	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: joe.Bytes()}.Build())
	x.NoError(err)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	wire := asOverTheWire(ctx, b.ContosoUser)

	// The guarantee, from the front. This is the half that works, and without
	// it the failures below could be a caller who may read nothing at all.
	_, err = app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: joe.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "the control: naming them finds nobody")

	// And the row that outlived them, named without naming them.
	t.Run("their address is not a way back to them", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewEmailServiceClient(conn).Get(wire, app.EmailGetRequest_builder{
			Ref: app.EmailRef_builder{
				At: app.EmailRefByAt_builder{
					TenantId: b.Contoso.Bytes(),
					Address:  z.Ptr(address),
				}.Build(),
			}.Build(),
			Select: app.EmailSelect_builder{
				All:    z.Ptr(true),
				Holder: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err, "the address row itself is nobody's secret and is not what this is about")

		x.Empty(v.GetHolder().GetAlias(),
			"an erased holder was read through a row that outlived them")
		x.Empty(v.GetHolder().GetName(),
			"and their profile came with it")
	})

	t.Run("nor is their identity", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewIdentityServiceClient(conn).Get(wire, app.IdentityGetRequest_builder{
			Ref: app.IdentityRef_builder{Id: sub.GetId()}.Build(),
			Select: app.IdentitySelect_builder{
				All:    z.Ptr(true),
				Holder: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)

		x.Empty(v.GetHolder().GetAlias(),
			"an erased holder was read through the identity that outlived them")
	})

	// A list needs no name at all, which is what made the two above worth
	// having rather than curiosities: an attacker does not have to know an
	// address to start. The row is still listed -- nothing cascades, which is
	// the next section -- and what a list of them is worth is what the two
	// subtests above just took away.
	t.Run("and a list is no better a place to start", func(t *testing.T) {
		x := require.New(t)

		vs, err := app.NewEmailServiceClient(conn).List(wire,
			app.EmailListRequest_builder{}.Build())
		x.NoError(err)

		seen := false
		for _, v := range vs.GetItems() {
			if v.GetAddress() != address {
				continue
			}

			seen = true

			// A list takes no `Select` -- it answers the row and the keys of
			// its edges, and that is the whole of what an enumerator gets.
			// The key is not nothing, so it is followed: it answers what
			// naming them answers, which is nobody.
			x.Empty(v.GetHolder().GetAlias(),
				"a list answered an erased holder through the row that outlived them")

			_, err := app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
				Ref: app.HolderRef_builder{Id: v.GetHolder().GetId()}.Build(),
			}.Build())
			x.Equal(codes.NotFound, status.Code(err),
				"the key a list hands out is a way back to them after all")
		}
		x.True(seen, "the row was not listed at all, so this asserted nothing")
	})
}

// TestARowOutlivesThePersonItWasAbout, which is what a soft erase means and is
// the half the test above deliberately does not close.
//
// Erasing a `Holder` cascades to nothing. Their `Email` and their `Identity`
// are rows of the tenant and they stay, which is not an oversight: the trail
// outlives what it names for the same reason, and a directory that destroyed
// the evidence of a person on the way out would have no answer to *what was
// this address, before*.
//
// What that costs is one row saying an address was once somebody's, to a caller
// who may already read every address in that tenant. What it no longer costs --
// and this is the line between the two tests -- is the person: the parent edge
// answers nothing now, so the row is an address and not a way back to who had
// it.
//
// If a deployment has to destroy rather than to forget, that is an erase that
// cascades, and it is a decision about the schema rather than about a read.
func TestARowOutlivesThePersonItWasAbout(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const address = "gone@contoso.example"

	who := b.holder(t, ctx, b.Contoso, "gone")
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Address: address,
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)

	vs, err := b.Ungated.Email().List(ctx, app.EmailListRequest_builder{}.Build())
	x.NoError(err)

	found := false
	for _, v := range vs.GetItems() {
		if v.GetAddress() == address {
			found = true
		}
	}
	x.True(found, "the row went with the person, which is a schema decision nobody recorded")
}

// TestAWatchAnswersNothingAGetWouldRefuse.
//
// A stream is the one read that is not a query a caller wrote. It is opened
// once and answered for as long as it stays open, by a loop that runs for every
// write anybody makes to the entity -- so it sees rows from every tenant in the
// deployment go past, and what keeps them from being sent is one `Get` per row
// through the caller's own context. That is one line, in generated code, with
// nothing above it that would refuse a second time.
//
// Which is the shape worth pinning rather than the code. `sinkHolder.watchRead`
// says it outright -- *the Get is what keeps the wall out of this file* -- and a
// sentence like that is true until somebody makes the read cheaper. So this
// compares: same caller, same filter, `Get` against `Watch`, and the two must
// agree. Not "the watch refuses", because a watch refuses for a dozen reasons a
// change could take away one at a time; **the same answer as the read whose
// narrowing is generated and reviewed**.
//
// Both entity shapes, because the wall reaches them by different SQL and only
// one of them is a column on the row. `Holder` is `tenant_id IN (...)`.
// `TeamMembership` is `HasHolderWith(TenantIdIn(...))` -- a correlated subquery
// through a second table, which is the shape somebody optimises away.
func TestAWatchAnswersNothingAGetWouldRefuse(t *testing.T) {
	b, ctx := build(t)

	// Cancelled with the test rather than left to the harness, so a stream that
	// does open ends with it.
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	// Somebody else's tenant, with somebody in it and a team they are on.
	theirs := b.holder(t, ctx, b.Fabrikam, "theirs")
	theirTeam := b.team(t, ctx, b.site(t, ctx, b.Fabrikam, "their-site"), "their-team")

	_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: theirTeam.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	wire := asOverTheWire(ctx, b.ContosoUser)

	t.Run("a row of another tenant", func(t *testing.T) {
		x := require.New(t)

		// Bounded, because the failure this is looking for does not answer: a
		// watch that resolved its filters past the wall opens, finds nothing to
		// send, and sits there. A test that waited for that would hang rather
		// than say what it found, which is the worst way for an assertion about
		// a leak to fail.
		wire, cancel := context.WithTimeout(wire, 3*time.Second)
		defer cancel()

		_, get := app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
		}.Build())

		out, err := app.NewHolderServiceClient(conn).Watch(wire, app.HolderWatchRequest_builder{
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{
					Ref: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
				}.Build(),
			},
		}.Build())
		x.NoError(err, "the stream is refused on its first Recv, not on the call")

		_, watch := out.Recv()

		x.Equal(codes.NotFound, status.Code(get), "the control")
		x.Equal(status.Code(get), status.Code(watch),
			"a watch and a get disagreed about somebody in another tenant")
	})

	// The same, one table further out. What narrows this one is a subquery
	// through `Holder`, so a `Watch` that resolved its filters against the
	// membership table alone would answer here and refuse above -- and the half
	// that leaked would be the quiet one.
	t.Run("and a row that reaches its tenant through another row", func(t *testing.T) {
		x := require.New(t)

		wire, cancel := context.WithTimeout(wire, 3*time.Second)
		defer cancel()

		ref := app.TeamMembershipRef_builder{
			Member: app.TeamMembershipRefByMember_builder{
				Holder: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
				Team:   app.TeamRef_builder{Id: theirTeam.Bytes()}.Build(),
			}.Build(),
		}.Build()

		c := app.NewTeamMembershipServiceClient(conn)

		_, get := c.Get(wire, app.TeamMembershipGetRequest_builder{Ref: ref}.Build())

		out, err := c.Watch(wire, app.TeamMembershipWatchRequest_builder{
			Filters: []*app.TeamMembershipFilter{
				app.TeamMembershipFilter_builder{Ref: ref}.Build(),
			},
		}.Build())
		x.NoError(err)

		_, watch := out.Recv()

		x.Equal(codes.NotFound, status.Code(get), "the control")
		x.Equal(status.Code(get), status.Code(watch),
			"a watch and a get disagreed about a membership in another tenant")
	})

	// And the other direction in time, which is the half a snapshot cannot
	// cover: a row that was this caller's when the stream opened and is not any
	// more. `HolderWatchItem.value` is documented as **absent** for exactly
	// that, and the absence is the whole of how it is said -- there is no flag,
	// deliberately, because a flag would tell a caller which rows stopped being
	// theirs.
	//
	// If the per-event read ever stops going through the walled server, this is
	// where it shows: the row goes on being sent, with its contents, after it
	// has stopped being answerable by `Get`.
	t.Run("and a row that leaves reach leaves the stream", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "watched")
		me := app.HolderRef_builder{Id: who.Bytes()}.Build()

		c := watching(t, wire, conn, app.HolderWatchRequest_builder{
			Filters: []*app.HolderFilter{app.HolderFilter_builder{Ref: me}.Build()},
		}.Build())

		x.Equal("watched", arrives(t, c).GetAlias(), "the control: the snapshot carries them")

		_, err := app.NewHolderServiceClient(conn).Erase(wire, me)
		x.NoError(err)

		// Read the item rather than the value, since the value is what must not
		// be there. `arrives` insists on one, and one is what a removal is.
		select {
		case v, ok := <-c:
			x.True(ok, "the stream ended instead of saying so")
			x.Len(v.GetItems(), 1)
			x.Equal(who.Bytes(), v.GetItems()[0].GetId(),
				"a removal names the row, or a client cannot forget it")
			x.Nil(v.GetItems()[0].GetValue(),
				"a row that stopped being readable went on being streamed")

		case <-time.After(5 * time.Second):
			t.Fatal("nothing arrived, so the stream said nothing about a row it was carrying")
		}

		// And `Get` agrees, which is the comparison this whole test is.
		_, err = app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{Ref: me}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})
}

// TestNoFilterCarriesARowFromAnotherTenant.
//
// A filter is a caller's own predicate, ANDed into the same query the wall
// narrows -- so on paper it cannot widen anything, and that is precisely why it
// is worth a test. `identity.proto` writes the argument out where a filter was
// added: *it grants nothing. The wall reaches this entity through
// `holder.tenant`, so the list is already narrowed to the tenants the caller
// may see, and naming a holder outside that answers with nothing.* That
// sentence is true only for as long as `sinkIdentity.List` keeps calling
// `bare.IdentityNarrow` before it applies the filters, and a `List` is the one
// read nothing generates for it.
//
// The filters chosen here are the ones that reach **through an edge**, because
// a filter on this row's own column cannot reach past the wall by construction
// and proves nothing. Each of these names a parent -- a holder, a tenant, a
// team -- that belongs to somebody else, and each is a shape a console
// legitimately sends every day.
func TestNoFilterCarriesARowFromAnotherTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const address = "theirs@fabrikam.example"

	theirs := b.holder(t, ctx, b.Fabrikam, "theirs")
	b.identity(t, ctx, theirs, "entra", "their-object-guid")

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
		Address: address,
	}.Build())
	x.NoError(err)

	theirTeam := b.team(t, ctx, b.site(t, ctx, b.Fabrikam, "their-site"), "their-team")
	_, err = b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
		Team:   app.TeamRef_builder{Id: theirTeam.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	wire := asOverTheWire(ctx, b.ContosoUser)

	// The control. Every list below has to come back empty, and an empty list
	// is what a broken query answers too -- so first, the same calls with the
	// wall out of the way, to prove the rows are there to be found.
	t.Run("the rows exist to be leaked", func(t *testing.T) {
		x := require.New(t)

		vs, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{
					Holder: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
				}.Build(),
			},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
	})

	t.Run("an identity filtered by a holder in another tenant", func(t *testing.T) {
		x := require.New(t)

		vs, err := app.NewIdentityServiceClient(conn).List(wire, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{
					Holder: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
				}.Build(),
			},
		}.Build())
		x.NoError(err, "a filter naming somebody else is not an error, it is an empty answer")
		x.Empty(vs.GetItems(), "a list carried another tenant's identity through its holder")
	})

	// The same entity, named by the tenant column payday stamps on it. It is
	// there so that "every identity in this tenant" can be asked for at all --
	// which means the one filter that names a tenant outright is on the entity
	// whose tenancy is two hops away.
	t.Run("and one filtered by another tenant outright", func(t *testing.T) {
		x := require.New(t)

		vs, err := app.NewIdentityServiceClient(conn).List(wire, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{TenantId: b.Fabrikam.Bytes()}.Build(),
			},
		}.Build())
		x.NoError(err)
		x.Empty(vs.GetItems(), "a list answered another tenant by name")
	})

	// `EmailRefByAt` is a tenant and an address, which is the pair a front door
	// has and the reason the reference exists. Handed another operator's
	// tenant, it is a lookup of somebody else's mail by address.
	t.Run("and an address in another tenant", func(t *testing.T) {
		x := require.New(t)

		c := app.NewEmailServiceClient(conn)
		ref := app.EmailRef_builder{
			At: app.EmailRefByAt_builder{
				TenantId: b.Fabrikam.Bytes(),
				Address:  z.Ptr(address),
			}.Build(),
		}.Build()

		vs, err := c.List(wire, app.EmailListRequest_builder{
			Filters: []*app.EmailFilter{app.EmailFilter_builder{Ref: ref}.Build()},
		}.Build())
		x.NoError(err)
		x.Empty(vs.GetItems(), "a list read another tenant's mail by address")

		// And `Get`, which is the read the list is being compared against.
		_, err = c.Get(wire, app.EmailGetRequest_builder{Ref: ref}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	// And a membership, whose reference is two edges at once -- a holder and a
	// team, both somebody else's.
	t.Run("and a membership named by two of their rows", func(t *testing.T) {
		x := require.New(t)

		vs, err := app.NewTeamMembershipServiceClient(conn).List(wire,
			app.TeamMembershipListRequest_builder{
				Filters: []*app.TeamMembershipFilter{
					app.TeamMembershipFilter_builder{
						Ref: app.TeamMembershipRef_builder{
							Member: app.TeamMembershipRefByMember_builder{
								Holder: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
								Team:   app.TeamRef_builder{Id: theirTeam.Bytes()}.Build(),
							}.Build(),
						}.Build(),
					}.Build(),
				},
			}.Build())
		x.NoError(err)
		x.Empty(vs.GetItems(), "a list carried another tenant's membership through its edges")
	})
}

// TestNoVerifierIsAnsweredByThePortThatServesItsRow.
//
// `CredentialService`, `ApiKeyService` and `DelegationService` are kept off the
// wire, and there are tests for each of those doors. This is about the doors
// that are **open**: `pd.Secret`, the layer that clears the column on the way
// out, on the stacks that really do answer with these rows.
//
// It matters most for `ApiKey`, because that entity has one port whose whole
// reason for existing is serving it. `cmd.GrpcControl` registers
// `s.Control.Walled.ApiKey()` and the comment beside it says *`Get` still
// answers with the verifier column if it is asked for* -- which is what makes
// this worth running rather than reading. If that were true, the console an
// operator signs in to would hand out the digest of every key in the
// deployment, and the wall would not have been crossed to get it: they are that
// tenant's own rows.
//
// The other two are on the data plane's walled stack, which nothing serves on
// the wire today -- and *today* is the word. The layer is the only control that
// survives somebody adding a registration for tidiness, which `cmd.register`
// warns about by name for `Delegation`.
//
// A TOTP seed is the one that would cost the most, and D14 says why: everything
// else here is a verifier, so a copy of the rows is a copy of things nobody can
// use. A seed is not. It is wrapped, and a caller who can read the ciphertext
// has the half of the problem that keeps -- which is why it goes through this
// same column and is checked here beside the hash rather than trusted to be.
func TestNoVerifierIsAnsweredByThePortThatServesItsRow(t *testing.T) {
	// Both deployments are stood up **here** rather than in the subtests that
	// use them, and that is not tidiness. `pdtest.DB` names a PostgreSql schema
	// after the running test and truncates it to sixty-three characters, and
	// the counter that gives a second database in one test its own name is a
	// suffix -- so inside a subtest, where the name is already long, the
	// counter is what gets cut off. Both planes then land in one schema and the
	// second `DROP SCHEMA` takes the first one's tables with it. It passes on
	// SQLite, where every call is its own file, which is the direction that
	// hides a mistake.
	b := keyFor(t, verify)
	ctx := t.Context()

	d, dctx := build(t)

	t.Run("the port that serves keys", func(t *testing.T) {
		x := require.New(t)

		as := atTheConsole(t, ctx, b)

		row, err := b.Control.Ent.ApiKey.Query().Only(ctx)
		x.NoError(err)
		x.NotEmpty(row.Secret, "nothing is stored, so this would prove nothing")

		// The server object `cmd.GrpcControl` registers, and not a stack
		// assembled here: what is being asked is whether *that* one clears the
		// column.
		c := b.Control.Walled.ApiKey()

		v, err := c.Get(as, app.ApiKeyGetRequest_builder{
			Ref:    app.ApiKeyRef_builder{Id: row.Id[:]}.Build(),
			Select: app.ApiKeySelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetAlias(), "the rest of the row is answered, which is the point of the port")
		x.Empty(v.GetSecret(), "the console port answered with a key's verifier")

		vs, err := c.List(as, app.ApiKeyListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems())
		for _, w := range vs.GetItems() {
			x.Empty(w.GetSecret(), "a list of keys carried their verifiers")
		}

		// And the column is untouched, so what was cleared was the answer and
		// not the row -- `keys.Store` compares it in process on every call.
		again, err := b.Control.Ent.ApiKey.Query().Only(ctx)
		x.NoError(err)
		x.Equal(row.Secret, again.Secret, "the layer cleared the row rather than the answer")
	})

	t.Run("nor the stack holding passwords and seeds", func(t *testing.T) {
		x := require.New(t)
		b, ctx := d, dctx

		b.sets(t, ctx, b.ContosoUser, "correct horse battery staple")

		// A second factor beside it, because the two are one column and only
		// one of them is a verifier. `Enrol` wraps the seed with the
		// deployment's key; what must not leave is the ciphertext, since the
		// process that would read it is the process that holds the key.
		seed, err := b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{
			Ref:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Kind: vouch.KindTotp,
		}.Build())
		x.NoError(err)
		x.NotEmpty(seed.GetSeed(), "nothing was enrolled, so this would prove nothing")

		rows, err := b.Ent.Credential.Query().All(ctx)
		x.NoError(err)
		x.Len(rows, 2, "a password and a second factor")

		as := b.as(ctx, b.ContosoUser, b.Contoso)
		c := b.Walled.Credential()

		for _, row := range rows {
			v, err := c.Get(as, app.CredentialGetRequest_builder{
				Ref:    app.CredentialRef_builder{Id: row.Id[:]}.Build(),
				Select: app.CredentialSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build())
			x.NoError(err)
			x.NotEmpty(v.GetKind(), "the rest of the row travels")
			x.Empty(v.GetSecret(), "a %s credential answered with its secret", row.Kind)
		}

		vs, err := c.List(as, app.CredentialListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 2)
		for _, v := range vs.GetItems() {
			x.Empty(v.GetSecret(), "a list of credentials carried the verifiers")
		}

		// Still in the database, which is the half that was never the problem.
		for _, row := range rows {
			got, err := b.Ent.Credential.Get(ctx, row.Id)
			x.NoError(err)
			x.Equal(row.Secret, got.Secret)
		}
	})

	// A delegation is a sign-in in miniature, and `cmd.register` says the
	// missing registration is *the only control that closes it* -- so the layer
	// behind that decision is worth having said out loud too.
	t.Run("nor a delegation", func(t *testing.T) {
		x := require.New(t)

		_, v, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
			Holder:  b.Who,
			Issuer:  issuerOf(t, ctx, b),
			Methods: []string{verify},
		})
		x.NoError(err)

		as := atTheDataPlane(t, ctx, b)
		c := b.Walled.Delegation()

		got, err := c.Get(as, app.DelegationGetRequest_builder{
			Ref:    app.DelegationRef_builder{Id: v.GetId()}.Build(),
			Select: app.DelegationSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Empty(got.GetSecret(), "a delegation answered with the digest of the token")

		vs, err := c.List(as, app.DelegationListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems())
		for _, w := range vs.GetItems() {
			x.Empty(w.GetSecret(), "a list of delegations carried their digests")
		}
	})
}

// TestNoRefusalSaysWhoIsHere.
//
// D14's rule, asserted as an **equality of answers** rather than as a list of
// empty fields. The difference is not pedantry: every existing test of this
// checks `ok`, `holder` and `locked_until` by name, so a field added to
// `VouchVerifyResponse` tomorrow -- a reason, a hint, a retry-after -- would be
// filled in for somebody real and left empty for a stranger, and every one of
// those tests would still pass. `proto.Equal` cannot miss it.
//
// The cases are chosen to be the ways of not being here that a store has to
// keep apart internally and must not tell apart outside:
//
//   - a real person with the wrong secret, which is the baseline;
//   - a name nobody has, and a tenant nobody serves;
//   - somebody real with no secret of that kind at all;
//   - somebody **erased**, which is F9's symptom and has a test of its own;
//   - somebody **disabled**, which has not been asked this question. It is the
//     newest of the five and the one whose branch is a separate `if` in
//     `vouch.verify` -- a suspension that answered differently would say *this
//     is a real account, and it is one nobody is watching*, which is the best
//     answer an attacker could get short of a password.
//
// And by both names, because a sign-in form collects an address and `byAddress`
// is an extra read on the way in -- the branch D14's comment says was got wrong
// once already, an inversion pointed *at the address rather than at the person*.
func TestNoRefusalSaysWhoIsHere(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const (
		right = "correct horse battery staple"
		wrong = "hunter2"
	)

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)
	b.sets(t, ctx, b.ContosoUser, right)

	// Somebody suspended, with a password that is still correct. The right
	// secret and not the wrong one, because the branch under test is the one
	// that runs **before** the comparison: a version of it that fell through
	// would answer `ok`.
	off := b.holder(t, ctx, b.Contoso, "suspended")
	b.sets(t, ctx, off, right)
	_, err = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
		Ref: app.HolderRef_builder{Id: off.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	gone := b.holder(t, ctx, b.Contoso, "leaver")
	b.sets(t, ctx, gone, right)
	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone.Bytes()}.Build())
	x.NoError(err)

	b.holder(t, ctx, b.Contoso, "passwordless")

	v := b.vouched()
	answer := func(t *testing.T, who *app.VouchWho, secret string) *app.VouchVerifyResponse {
		t.Helper()

		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: who, Secret: []byte(secret),
		}.Build())
		require.NoError(t, err, "a refusal that is an error is a refusal that differs")

		return res
	}

	// The baseline every other answer is compared against: somebody who is
	// certainly here, getting it wrong.
	baseline := answer(t, app.VouchWho_builder{Tenant: "contoso", Alias: "someone"}.Build(), wrong)
	x.False(baseline.GetOk(), "the control")

	for _, tc := range []struct {
		desc   string
		who    *app.VouchWho
		secret string
	}{
		{"a name nobody has", app.VouchWho_builder{Tenant: "contoso", Alias: "nobody"}.Build(), wrong},
		{"a tenant nobody serves", app.VouchWho_builder{Tenant: "nowhere", Alias: "someone"}.Build(), wrong},
		{"an identifier nobody has", app.VouchWho_builder{Id: pdid.New(pd.HolderDomain).Bytes()}.Build(), wrong},
		{"somebody with no secret of that kind", app.VouchWho_builder{Tenant: "contoso", Alias: "passwordless"}.Build(), wrong},
		{"somebody suspended, right secret", app.VouchWho_builder{Tenant: "contoso", Alias: "suspended"}.Build(), right},
		{"somebody erased, right secret", app.VouchWho_builder{Tenant: "contoso", Alias: "leaver"}.Build(), right},

		{"an address nobody has", app.VouchWho_builder{Tenant: "contoso", Address: "nobody@contoso.example"}.Build(), wrong},
		{"an address in a tenant nobody serves", app.VouchWho_builder{Tenant: "nowhere", Address: "someone@contoso.example"}.Build(), wrong},
		{"an address, wrong secret", app.VouchWho_builder{Tenant: "contoso", Address: "someone@contoso.example"}.Build(), wrong},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			got := answer(t, tc.who, tc.secret)
			x.True(proto.Equal(baseline, got),
				"the answer differs from a wrong password, which says %s is a distinguishable state:\n%v",
				tc.desc, got)
		})
	}

	// The one difference this app admits to, and the boundary it has to keep.
	//
	// `VouchVerifyResponse.locked_until` is documented as a deliberate trade --
	// *it tells a caller that this person exists, which every other refusal here
	// is careful not to* -- taken because somebody locked out and told nothing
	// tries forever. The trade is only payable if it is confined to people who
	// are actually here and actually signing in. Somebody who is gone, somebody
	// who is suspended and somebody who was never here must go on looking
	// identical however long an attacker types at them, or the oracle the trade
	// bought back is handed over anyway, one account at a time.
	t.Run("and typing at somebody who is not here never closes an account", func(t *testing.T) {
		x := require.New(t)

		for _, tc := range []struct {
			desc string
			who  *app.VouchWho
		}{
			{"a name nobody has", app.VouchWho_builder{Tenant: "contoso", Alias: "nobody"}.Build()},
			{"an address nobody has", app.VouchWho_builder{Tenant: "contoso", Address: "nobody@contoso.example"}.Build()},
			{"somebody suspended", app.VouchWho_builder{Tenant: "contoso", Alias: "suspended"}.Build()},
			{"somebody erased", app.VouchWho_builder{Tenant: "contoso", Alias: "leaver"}.Build()},
		} {
			for range vouch.MaxFailures + 1 {
				got := answer(t, tc.who, wrong)
				x.Nil(got.GetLockedUntil(), "a lockout said %s is a real account", tc.desc)
				x.True(proto.Equal(baseline, got), tc.desc)
			}
		}

		// And it really does appear for somebody who is here, so what was
		// asserted above is the branch rather than a lockout nothing reaches.
		var last *app.VouchVerifyResponse
		for range vouch.MaxFailures {
			last = answer(t, app.VouchWho_builder{Tenant: "contoso", Alias: "someone"}.Build(), wrong)
		}
		x.NotNil(last.GetLockedUntil(), "nothing locks, so the checks above looked at nothing")
	})
}

// atTheConsole is a deployment operator on the control plane, holding
// everything.
//
// The control plane is roster again on its own database, so being somebody
// there is the same three rows it is anywhere else -- and `pd.Gate` refuses a
// call with no frame before any of this is reached, which is what makes the
// helper necessary rather than decorative.
func atTheConsole(t *testing.T, ctx context.Context, b *keyedBuilt) context.Context {
	t.Helper()
	x := require.New(t)

	// Off the key's own holder, rather than "the first tenant there is". The
	// two planes are two databases and the operator belongs to the control
	// one, so asking the row that is under test which tenant it is in is both
	// shorter and impossible to get wrong.
	v, err := b.Control.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref: app.ApiKeyRef_builder{Secret: keys.Sum(b.Token)}.Build(),
		Select: app.ApiKeySelect_builder{
			Holder: app.HolderSelect_builder{
				Tenant: app.TenantSelect_builder{}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)

	tenant := mustId(t, v.GetHolder().GetTenant().GetId())
	who := addHolder(t, ctx, b.Control, tenant, "ops")

	r, err := b.Control.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		Alias:   "everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	_, err = b.Control.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	return frame.Into(ctx, frame.New(who, tenant, frame.Whole()).WithScope(frame.Only(tenant)))
}

// atTheDataPlane is the same on the data plane of a keyed deployment, for
// whichever person the harness put there.
func atTheDataPlane(t *testing.T, ctx context.Context, b *keyedBuilt) context.Context {
	t.Helper()
	x := require.New(t)

	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	return frame.Into(ctx, frame.New(b.Who, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))
}
