package cmd_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	entemail "github.com/lesomnus/roster/internal/ent/email"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
)

// afterTheNextReadOfAPerson makes `act` once, when the next read of a person
// has answered: the moment between what an act reads of somebody and what it
// writes about them, which is where a second act arrives when two are made at
// once. Every act here reads the person before it opens the transaction it
// writes in, so `act` is made in no transaction of the first's, on either
// store.
func (b *built) afterTheNextReadOfAPerson(act func()) {
	afterTheNextRead(b.Ent.Holder.Intercept, act)
}

// afterTheNextRead is [built.afterTheNextReadOfAPerson] for the kind whose
// reads `of` hears.
func afterTheNextRead(of func(...ent.Interceptor), act func()) {
	armed := true
	of(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			if armed {
				armed = false
				act()
			}

			return v, err
		})
	}))
}

// leaver is somebody of Contoso with an address, erased: what a pass finds due.
func (b *built) leaver(t *testing.T, ctx context.Context, alias string) pdid.Id {
	t.Helper()

	who := b.holder(t, ctx, b.Contoso, alias)
	b.addressOf(t, ctx, who, alias+"@contoso.example")
	_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
	require.NoError(t, err)

	return who
}

// whole is what is left of somebody a restore and a forget were both asked
// about: forgotten, and then all of them is -- erased, no name, no address --
// or not, and then none of them is. Anything between the two fails here: it
// is the person nobody can find again.
func (b *built) whole(t *testing.T, ctx context.Context, who pdid.Id, alias string) (forgotten bool) {
	t.Helper()
	x := require.New(t)

	row, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	addresses, err := b.Ent.Email.Query().Where(entemail.HasHolderWith(entholder.IdEQ(who.Uuid()))).Count(ctx)
	x.NoError(err)

	if row.Alias == "" {
		x.NotNil(row.DateErased, "somebody with no name and no rows is not erased: no pass finds them, and a restore says they were never erased")
		x.Zero(addresses, "somebody forgotten has an address")

		return true
	}

	x.Equal(alias, row.Alias)
	x.Equal(1, addresses, "somebody who was not forgotten lost their address")

	return false
}

// namingTheForgotten is how many rows name somebody forgotten, by table: every
// column the schema keys to a holder, asked of the store. A consent is left
// out, since it is kept past its account on purpose (`server/forget/consent.go`).
func (b *built) namingTheForgotten(t *testing.T, ctx context.Context) map[string]int {
	t.Helper()

	out := map[string]int{}
	asked := 0
	for _, table := range entmigrate.Tables {
		if table.Name == "consent" {
			continue
		}
		for _, fk := range table.ForeignKeys {
			if fk.RefTable == nil || fk.RefTable.Name != entholder.Table {
				continue
			}

			asked++
			var n int
			require.NoError(t, b.Db.QueryRowContext(ctx, fmt.Sprintf(
				`SELECT COUNT(*) FROM %q c JOIN %q h ON c.%q = h."id" WHERE h."alias" = '' AND h."date_erased" IS NOT NULL`,
				table.Name, entholder.Table, fk.Columns[0].Name,
			)).Scan(&n))
			if n > 0 {
				out[table.Name+"."+fk.Columns[0].Name] = n
			}
		}
	}
	require.NotZero(t, asked, "no table is keyed to a holder, so this proves nothing")

	return out
}

// TestAPassLeavesWhoeverWasRestoredAtOnce: the people's pass read who was due
// once and then forgot each, and `forget.Forget` asked nothing again. So
// somebody an operator restored after the pass had read its list -- a
// mistaken deletion undone, and `roster restore` answering that they are back
// -- was forgotten by that pass a moment later: erased, no name, their
// address gone; right is the person as the restore left them. A tenant's
// destruction asks again before each person (`forget.DestroyTenant`); a
// person's had no such question.
//
// A forget made on the clock asks again, inside the transaction that
// destroys, that the person is still due: still erased, and for as long. Who
// is not is left, and the pass says so of them.
func TestAPassLeavesWhoeverWasRestoredAtOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	erin, frank, grace := b.leaver(t, ctx, "erin"), b.leaver(t, ctx, "frank"), b.leaver(t, ctx, "grace")

	b.afterTheNextReadOfAPerson(func() {
		x.NoError(forget.Restore(ctx, b.Ent, grace, audit.Contents{}, nil), "the restore was refused, so this proves nothing")
	})
	did, err := forget.Pass(ctx, b.Ent, time.Now().Add(time.Hour), forget.Held(trail.Policy{}, b.Ent), audit.Contents{}, nil)
	x.NoError(err)
	x.Len(did, 3, "the pass read its list after the restore, so this proves nothing")

	x.False(b.whole(t, ctx, grace, "grace"), "somebody restored after the pass began was forgotten by it, having been told they were back")
	x.True(b.whole(t, ctx, erin, "erin"))
	x.True(b.whole(t, ctx, frank, "frank"))

	for _, v := range did {
		if v.Who == grace {
			x.ErrorIs(v.Err, forget.ErrNotDue, "the pass does not say it left them")

			continue
		}
		x.NoError(v.Err)
	}
}

// TestAPassForgetsNobodyOfATenantHeldAtOnce: `forget.Due` leaves out the
// people of a tenant an operator holds, and the manual says they are not
// forgotten on the clock. The pass asked that once, when it read its list: a
// hold placed after that stopped nobody of the pass, and all three of a held
// tenant's leavers were forgotten under the hold; right is none of them. The
// hold is asked again before each person, as a tenant's destruction asks it.
func TestAPassForgetsNobodyOfATenantHeldAtOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	leavers := map[pdid.Id]string{}
	for _, alias := range []string{"erin", "frank", "grace"} {
		leavers[b.leaver(t, ctx, alias)] = alias
	}

	b.afterTheNextReadOfAPerson(func() {
		x.NoError(forget.Hold(ctx, b.Ungated, b.Contoso, "a claim that arrived while the pass ran"))
	})
	did, err := forget.Pass(ctx, b.Ent, time.Now().Add(time.Hour), forget.Held(trail.Policy{}, b.Ent), audit.Contents{}, nil)
	x.NoError(err)
	x.Len(did, 3, "the pass read its list after the hold, so this proves nothing")

	for who, alias := range leavers {
		x.False(b.whole(t, ctx, who, alias), "%s was forgotten on the clock under a hold", alias)
	}
	for _, v := range did {
		x.ErrorIs(v.Err, forget.ErrNotDue, "the pass does not say it left %s", leavers[v.Who])
	}

	// And they are due again once it lifts, which is what the hold put off.
	x.NoError(forget.Lift(ctx, b.Ungated, b.Contoso))
	did, err = forget.Pass(ctx, b.Ent, time.Now().Add(time.Hour), forget.Held(trail.Policy{}, b.Ent), audit.Contents{}, nil)
	x.NoError(err)
	x.Len(did, 3)
	for who, alias := range leavers {
		x.True(b.whole(t, ctx, who, alias), "%s was not forgotten once the hold lifted", alias)
	}
}

// TestAPassOfConsentsLeavesThoseOfSomebodyRestoredAtOnce: a consent is kept
// past its account for `consent.keep` and then destroyed, and "a restore
// inside that window makes them somebody again, and their consents are theirs
// as they were" (`server/forget/consent.go`). The pass read which consents
// were due once and destroyed them by identifier, asking nothing again: the
// consent of somebody restored after it had read its list was destroyed, and
// so was one of a tenant held after that; right is both kept. It is the
// people's pass's defect in the other loop, and is held the same way: what is
// destroyed is what is still due inside the transaction that destroys it.
func TestAPassOfConsentsLeavesThoseOfSomebodyRestoredAtOnce(t *testing.T) {
	for name, between := range map[string]func(c *consenting) error{
		"restored": func(c *consenting) error { return forget.Restore(c.ctx, c.Ent, c.account, audit.Contents{}, nil) },
		"of a tenant held": func(c *consenting) error {
			return forget.Hold(c.ctx, c.Ungated, c.Contoso, "a claim that arrived while the pass ran")
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			c := consentingAt(t)
			cl, as, _ := c.as(t, "rove")

			_, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Documents: []*app.ConsentDocument{doc("terms", "1", false)}}.Build())
			x.NoError(err)
			_, err = c.Ungated.Holder().Erase(c.ctx, c.erin())
			x.NoError(err)

			due, err := forget.DueConsents(c.ctx, c.Ent, time.Now().Add(time.Hour))
			x.NoError(err)
			x.Len(due, 1, "nothing is due, so this proves nothing")

			afterTheNextRead(c.Ent.Consent.Intercept, func() { x.NoError(between(c), "so this proves nothing") })
			n, err := forget.PassConsents(c.ctx, c.Ent, time.Now().Add(time.Hour), audit.Contents{})
			x.NoError(err)

			left, err := c.Ent.Consent.Query().Count(c.ctx)
			x.NoError(err)
			x.Equal(1, left, "the consent of somebody %s after the pass began was destroyed by it", name)
			x.Zero(n, "and the pass says it destroyed one")
		})
	}
}

// TestARestoreAndAForgetOfOnePersonAtOnceLeaveThemWhole: a restore read the
// person and cleared `date_erased` with no condition, and a forget read the
// person and stamped `date_erased` only if its own earlier read had found
// them live. Made at once, in either order, both answered OK and left
// somebody who is not erased, has no name and no rows: `HolderService/Get`
// answers them, no pass finds them due, and `roster restore` says they were
// never erased. Right is one of the two, whole: forgotten, or back.
//
// A restore is written on the condition it was decided on -- erased, and not
// forgotten -- and is no write at all when that has moved, as a tenant's is
// (`server/core/close.go`). A forget reads the person inside the transaction
// that destroys them, and one made by name refuses somebody it found erased
// who has been restored since: it cannot be taken back, so it is not made on
// what was true a moment ago.
func TestARestoreAndAForgetOfOnePersonAtOnceLeaveThemWhole(t *testing.T) {
	t.Run("a forget made between what a restore reads and what it writes", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		erin := b.leaver(t, ctx, "erin")

		b.afterTheNextReadOfAPerson(func() {
			_, err := forget.Forget(ctx, b.Ent, erin, trail.Policy{}, audit.Contents{}, nil)
			x.NoError(err, "the forget was refused, so this proves nothing")
		})
		err := forget.Restore(ctx, b.Ent, erin, audit.Contents{}, nil)

		x.True(b.whole(t, ctx, erin, "erin"))
		x.Error(err, "a restore of somebody forgotten in the meantime answered that they are back")
		x.ErrorContains(err, "forgotten")
	})

	t.Run("a restore made between what a forget by name reads and what it writes", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		erin := b.leaver(t, ctx, "erin")

		b.afterTheNextReadOfAPerson(func() {
			x.NoError(forget.Restore(ctx, b.Ent, erin, audit.Contents{}, nil), "the restore was refused, so this proves nothing")
		})
		_, err := forget.Forget(ctx, b.Ent, erin, trail.Policy{}, audit.Contents{}, nil)

		x.False(b.whole(t, ctx, erin, "erin"))
		x.Error(err, "a forget of somebody restored in the meantime answered that they are forgotten")
		x.ErrorContains(err, "restored")

		// Asked again, of the person as they are now, it is made: somebody
		// who is not erased is forgotten by name as anybody is.
		_, err = forget.Forget(ctx, b.Ent, erin, trail.Policy{}, audit.Contents{}, nil)
		x.NoError(err)
		x.True(b.whole(t, ctx, erin, "erin"))
	})
}

// written is the kinds of row an administrator writes naming somebody, for
// the test of a write and a forget at once: each makes one for `who`.
func (b *built) written(t *testing.T, ctx context.Context) map[string]func(ctx context.Context, who *app.HolderRef) error {
	t.Helper()
	x := require.New(t)
	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	reader := roleRef(b.role(t, ctx, "reader", getHolder))
	seoul := app.SiteRef_builder{Id: b.site(t, ctx, b.Contoso, "seoul").Bytes()}.Build()
	team, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{Tenant: contoso, Alias: "night"}.Build())
	x.NoError(err)
	night := app.TeamRef_builder{Id: team.GetId()}.Build()
	friends, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{Tenant: contoso, Alias: "friends"}.Build())
	x.NoError(err)

	return map[string]func(ctx context.Context, who *app.HolderRef) error{
		"a binding": func(ctx context.Context, who *app.HolderRef) error {
			_, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{Role: reader, Holder: who}.Build())

			return err
		},
		"a place in a site": func(ctx context.Context, who *app.HolderRef) error {
			_, err := b.Ungated.SiteMembership().Add(ctx, app.SiteMembershipAddRequest_builder{Holder: who, Site: seoul}.Build())

			return err
		},
		"a place in a team": func(ctx context.Context, who *app.HolderRef) error {
			_, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{Holder: who, Team: night, Role: reader}.Build())

			return err
		},
		"a place in a group": func(ctx context.Context, who *app.HolderRef) error {
			_, err := b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{
				Holder: who, Group: app.GroupRef_builder{Id: friends.GetId()}.Build(),
			}.Build())

			return err
		},
		"a nomination": func(ctx context.Context, who *app.HolderRef) error {
			_, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
				Tenant: contoso, BorrowerId: pdid.New(pd.HolderDomain).Bytes(), ActsAs: who,
			}.Build())

			return err
		},
	}
}

// TestNothingIsWrittenForSomebodyForgottenAtOnce: a write that names somebody
// reads them, decides, and then writes; and a forgotten person's row stays,
// blank, so the foreign key such a write carries is satisfied by somebody
// forgotten as by anybody. A forget made between the read and the write left
// the row hanging off a blank person: a binding, a place in a site, in a team
// and in a group, and a nomination each answered OK and were there, on
// somebody no pass looks at again, since a pass finds people by the name a
// forgotten one has not got. Right is a refusal that says why, and no row.
//
// So a write is asked once more after it is made, inside its own transaction:
// whether whoever its row names has been forgotten (`forget.Nobody`). The
// kinds here are the ones whose write decides before it opens that
// transaction, so that a forget is made between the two on either store. The
// others are asked the same question -- `TestEveryRowThatNamesAPersonIsAskedAboutThem`
// holds the list of them to the schema -- and on SQLite cannot be arranged:
// a key, an address and a number are read and written in one transaction,
// which no other writer gets between, and an account at a provider and a
// relation read their person again inside theirs.
func TestNothingIsWrittenForSomebodyForgottenAtOnce(t *testing.T) {
	for _, kind := range []string{"a binding", "a place in a site", "a place in a team", "a place in a group", "a nomination"} {
		t.Run(kind, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t)
			into := b.written(t, ctx)[kind]
			x.NotNil(into, "no write of %s is known here", kind)
			who := b.holder(t, ctx, b.Contoso, "erin")

			b.afterTheNextReadOfAPerson(func() {
				_, err := forget.Forget(ctx, b.Ent, who, trail.Policy{}, audit.Contents{}, nil)
				x.NoError(err, "the forget was not made between the read and the write, so this proves nothing")
			})
			err := into(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())

			row, rerr := b.Ent.Holder.Get(ctx, who.Uuid())
			x.NoError(rerr)
			x.Empty(row.Alias, "they were not forgotten, so this proves nothing")

			x.Empty(b.namingTheForgotten(t, ctx), "%s was written for somebody forgotten in the meantime", kind)
			x.Error(err, "and the write answered OK")
			x.ErrorContains(err, "forgotten while it was being written", "what the write is refused for is not said")
		})
	}
}

// TestNobodyForgottenIsBoundByTwoCallersAtOnce is the same rule with nothing
// arranged: forty people, each bound a role and forgotten by name at the same
// moment, by two callers. Whichever comes second finds what the first left --
// the forget takes the binding with the person, or the binding is refused for
// somebody who is gone -- and nobody forgotten is left holding a role.
//
// Many people rather than one, for
// `TestAPersonHasOneAccountAtAProviderWhenTwoAreAddedAtOnce`'s reason: one
// pair racing is a coin toss. And on PostgreSQL it is the only one of these
// that asks what holds there: the forget takes the person's row for itself
// before it reads what hangs off them, which waits for a write that has
// already named them and is waited for by one that is about to.
func TestNobodyForgottenIsBoundByTwoCallersAtOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	reader := roleRef(b.role(t, ctx, "reader", getHolder))

	const people = 40

	for i := range people {
		who := b.holder(t, ctx, b.Contoso, fmt.Sprintf("person-%d", i))

		var (
			wg                sync.WaitGroup
			boundErr, goneErr error
		)
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, boundErr = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
				Role: reader, Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
		}()
		go func() {
			defer wg.Done()
			<-start

			// A binding is asked more before it is written than a forget is,
			// so the forget is begun a little after it: by more each time.
			time.Sleep(time.Duration(i%20) * 200 * time.Microsecond)
			_, goneErr = forget.Forget(ctx, b.Ent, who, trail.Policy{}, audit.Contents{}, nil)
		}()
		close(start)
		wg.Wait()

		x.NoError(goneErr, "person %d was not forgotten (the binding answered %v)", i, boundErr)
	}

	x.Empty(b.namingTheForgotten(t, ctx), "of %d people forgotten while they were being bound a role", people)
}
