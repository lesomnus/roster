package cmd_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	entemail "github.com/lesomnus/roster/internal/ent/email"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/forget"
)

// TestForgettingSomebodyByNameUnderAHoldSaysWhatAHoldKeeps: a hold does not
// stop a person of the tenant being forgotten by name, and is not meant to
// (`server/forget/hold.go`): their rows are destroyed, and what the hold
// keeps is the trail's lines about them. Five sentences said *nothing of it
// is destroyed*, `roster hold place --help` among them, and the command that
// destroyed somebody's rows under a hold printed a count of trail rows and
// nothing about what a hold is. It says it when it happens: that the tenant is
// held, that their rows are destroyed all the same, and which lines are kept
// until the hold lifts.
func TestForgettingSomebodyByNameUnderAHoldSaysWhatAHoldKeeps(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, getMe)
	ctx := t.Context()
	bob := mustId(t, b.Bob.GetId())

	_, err := b.Server.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: bob.Bytes()}.Build(), Address: "bob@newco.example",
	}.Build())
	x.NoError(err)

	help, err := term(t, cli.NewCmdHold(&b.Local), "place", "--help")
	x.NoError(err)
	x.NotContains(help, "nothing of it is destroyed", "`roster hold place --help` says a hold stops what it does not")

	_, err = stderrOf(t, cli.NewCmdHold(&b.Local), "place", "--why", "Newco v. Somebody, 2026-123", "@newco")
	x.NoError(err)

	out, err := stderrOf(t, cli.NewCmdForget(&b.Local), "@newco/bob")
	x.NoError(err)

	// What happened, which is what the hold does not stop.
	left, err := b.Server.Ent.Email.Query().Where(entemail.HasHolderWith(entholder.IdEQ(bob.Uuid()))).Count(ctx)
	x.NoError(err)
	x.Zero(left, "a forget by name under a hold left their address, so the manual is right and this test is wrong")
	x.NotZero(stillSaying(t, ctx, b.Server.Ent, bob), "the hold kept nothing of the trail, so this proves nothing")

	// And what the command says of it.
	x.Contains(out, "is held", "that their tenant is held is not said: %s", out)
	x.Contains(out, "does not stop", "that a hold does not stop a forget by name is not said: %s", out)
	x.Contains(out, "destroyed", "that their rows are destroyed is not said: %s", out)
	x.Contains(out, "until it is lifted", "which lines are kept, and until when, is not said: %s", out)
	x.Contains(out, "Newco v. Somebody, 2026-123", "what the hold is for is not said where it is passed by: %s", out)
}

// TestAHoldOnADestroyedTenantKeepsItsAliasOnTheClockToo: a destroyed tenant's
// alias is given back when `tenant.alias_hold` is over, and `roster forget
// --reuse` gives it back sooner. Under a hold the second was refused -- it goes
// through `DestroyTenant`, which refuses a tenant an operator holds -- and the
// first was not: `DueAliases` asked no hold, so the clock freed under a hold
// the name the hand was refused. Right is one answer. The clock asks what the
// hand is asked, where the name is given back, so neither frees it; lifted,
// the next pass does.
func TestAHoldOnADestroyedTenantKeepsItsAliasOnTheClockToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.holder(t, ctx, b.Fabrikam, "ivan")
	closes(t, ctx, b.Ungated, b.Fabrikam)
	_, err := forget.DestroyTenant(ctx, b.Ent, b.Fabrikam, trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)
	x.NoError(forget.Hold(ctx, b.Ungated, b.Fabrikam, "a claim that arrived after"))

	alias := func() string {
		t.Helper()
		v, err := b.Ent.Tenant.Get(ctx, b.Fabrikam.Uuid())
		x.NoError(err)

		return v.Alias
	}
	due := func() bool {
		t.Helper()
		vs, err := forget.DueAliases(ctx, b.Ent, time.Now().Add(time.Hour))
		x.NoError(err)
		for _, v := range vs {
			if v == b.Fabrikam {
				return true
			}
		}

		return false
	}

	// By hand, which is refused, as it was.
	_, err = forget.DestroyTenant(ctx, b.Ent, b.Fabrikam, trail.Policy{}, audit.Contents{}, nil)
	x.ErrorIs(err, forget.ErrHeld, "the hand is not refused under a hold, so there is nothing for the clock to agree with")

	// And on the clock.
	x.False(due(), "a held tenant's alias is due to be given back")
	err = forget.FreeAlias(ctx, b.Ungated, b.Fabrikam)
	x.Equal("fabrikam", alias(), "the alias of a tenant an operator holds was given back under the hold")
	x.ErrorIs(err, forget.ErrHeld)

	// Lifted, it is due, and given back.
	x.NoError(forget.Lift(ctx, b.Ungated, b.Fabrikam))
	x.True(due(), "the hold lifted and the alias is not due")
	x.NoError(forget.FreeAlias(ctx, b.Ungated, b.Fabrikam))
	x.Equal(forget.Gone(b.Fabrikam), alias())
}

// TestNobodyIsRestoredIntoATenantThatIsDestroyed: a tenant's destruction
// forgets its people one at a time, and a hold placed while it runs stops it
// with some of them still there, erased or not. `roster restore` of one who
// had been erased answered that they were back, and left them live in a
// tenant stamped destroyed: somebody no door lets in, in a tenant nothing is
// written into, who is forgotten the moment the hold lifts. Right is a
// refusal that says so: what a restore gives back is an account, and there is
// no tenant for it to be one in.
func TestNobodyIsRestoredIntoATenantThatIsDestroyed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	ivan := b.holder(t, ctx, b.Fabrikam, "ivan")
	_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: ivan.Bytes()}.Build())
	x.NoError(err)
	closes(t, ctx, b.Ungated, b.Fabrikam)

	// Stamped destroyed, as the first write of a destruction, and held before
	// it reached anybody.
	x.NoError(b.Ent.Tenant.UpdateOneId(b.Fabrikam.Uuid()).SetDateDestroyed(time.Now()).Exec(bare.TrustAdmission(ctx)))
	x.NoError(forget.Hold(ctx, b.Ungated, b.Fabrikam, "a claim"))

	err = forget.Restore(ctx, b.Ent, ivan, audit.Contents{}, nil)

	row, rerr := b.Ent.Holder.Get(ctx, ivan.Uuid())
	x.NoError(rerr)
	x.NotNil(row.DateErased, "somebody was restored into a tenant that is destroyed")
	x.Error(err)
	x.ErrorContains(err, "destroyed", "why they are not brought back is not said")
}

// TestAHoldPlacedOnAClosedTenantSaysItsGraceRunsOn: a hold stops a tenant
// being destroyed and does not stop its grace: `tenant.restore_within` is
// counted from the close whatever holds the tenant, so one closed and then
// held for longer than the window can be neither restored (the grace ran out)
// nor destroyed (it is held), and the day the hold lifts the next pass
// destroys it. The schema's comment says the grace still runs; the manual
// said *everything carries on from where it waited*, and `roster hold place`
// said the tenant is held and nothing else. It says it where the hold is
// placed, which is the last moment a restore is still an operator's to make.
func TestAHoldPlacedOnAClosedTenantSaysItsGraceRunsOn(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, getMe)
	ctx := t.Context()
	newco := mustId(t, b.Tenant.GetId())

	local := b.Local
	local.Tenant.RestoreWithin = 720 * time.Hour

	t.Run("an open tenant is told nothing of a grace", func(t *testing.T) {
		x := require.New(t)
		out, err := stderrOf(t, cli.NewCmdHold(&local), "place", "--why", "a dispute", "@newco")
		x.NoError(err)
		x.NotContains(out, "restore_within", "%s", out)
		_, err = stderrOf(t, cli.NewCmdHold(&local), "lift", "@newco")
		x.NoError(err)
	})

	closed := closes(t, ctx, b.Server.Ungated, newco)
	until := closed.GetDateClosed().AsTime().Add(local.Tenant.RestoreWithin)

	out, err := stderrOf(t, cli.NewCmdHold(&local), "place", "--why", "Newco v. Somebody, 2026-123", "@newco")
	x.NoError(err)
	x.Contains(out, "tenant.restore_within", "that its grace runs on under the hold is not said: %s", out)
	x.Contains(out, until.UTC().Format(time.RFC3339), "until when it can still be restored is not said: %s", out)
	x.Contains(out, "roster --as <alias> restore", "what to do if it is to come back is not said: %s", out)
	x.Contains(out, "destroy", "what lifting the hold does past the grace is not said: %s", out)

	t.Run("and one whose grace has run out, that it cannot come back", func(t *testing.T) {
		x := require.New(t)
		x.NoError(b.Server.Ent.Tenant.UpdateOneId(newco.Uuid()).SetDateClosed(time.Now().Add(-721 * time.Hour)).Exec(bare.TrustAdmission(ctx)))

		out, err := stderrOf(t, cli.NewCmdHold(&local), "place", "--why", "Newco v. Somebody, 2026-123", "@newco")
		x.NoError(err)
		x.Contains(out, "can no longer be restored", "%s", out)
		x.Contains(out, "destroy", "%s", out)
	})
}

// TestRosterForgetDoesWhatTheLoopsDo: `roster forget` with no name is every
// clock at once, for a deployment that runs the command instead of leaving it
// to `serve`. It was less than the loops in two ways. It stopped at the first
// person or tenant that failed, where each loop logs the failure and carries
// on: one person who could not be forgotten kept every tenant and alias after
// them from being looked at. And it destroyed no consents, which only
// `serve`'s loop did, so a deployment that ran the command kept every consent
// past `consent.keep` for good.
//
// It does what the loops do, and answers at the end with who failed.
func TestRosterForgetDoesWhatTheLoopsDo(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, getMe)
	ctx := t.Context()
	s := b.Server

	local := b.Local
	local.Holder.ForgetAfter = time.Nanosecond
	local.Consent.Keep = time.Nanosecond
	local.Tenant.RestoreWithin = time.Nanosecond

	// Three who left, each with an address and a consent kept past them.
	leavers := map[string]pdid.Id{}
	for i, alias := range []string{"erin", "frank", "grace"} {
		v, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.GetId()}.Build(), Alias: alias}.Build())
		x.NoError(err)
		ref := app.HolderRef_builder{Id: v.GetId()}.Build()
		_, err = s.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: alias + "@newco.example"}.Build())
		x.NoError(err)
		_, err = s.Ungated.Consent().Add(ctx, app.ConsentAddRequest_builder{
			Holder: ref, App: b.Alice.GetId(), Document: "terms", Version: "1", Act: app.ConsentAct_CONSENT_ACT_AGREE, Seq: int64(i + 1),
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Holder().Erase(ctx, ref)
		x.NoError(err)
		leavers[alias] = mustId(t, v.GetId())
	}
	consents := func() int {
		t.Helper()
		n, err := s.Ent.Consent.Query().Count(ctx)
		x.NoError(err)

		return n
	}
	x.Equal(3, consents())

	// One of them cannot be forgotten: something this deployment's schema does
	// not know of is keyed to their address, so the database refuses to remove
	// it. On either store, and in whichever process asks.
	_, err := s.Db.ExecContext(ctx, `CREATE TABLE "zz_kept" ("email_id" uuid REFERENCES "email" ("id"))`)
	x.NoError(err)
	_, err = s.Db.ExecContext(ctx, `INSERT INTO "zz_kept" ("email_id") SELECT "id" FROM "email" WHERE "address" = 'frank@newco.example'`)
	x.NoError(err)

	// And a second customer, closed for longer than its grace.
	other, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "oldco"}.Build())
	x.NoError(err)
	oldco := mustId(t, other.GetId())
	closes(t, ctx, s.Ungated, oldco)

	out, err := stderrOf(t, cli.NewCmdForget(&local))

	forgotten := func(alias string) bool {
		t.Helper()
		row, err := s.Ent.Holder.Get(ctx, leavers[alias].Uuid())
		x.NoError(err)

		return row.Alias == ""
	}
	x.False(forgotten("frank"), "the one who cannot be forgotten was, so this proves nothing: %s", out)
	x.True(forgotten("erin"), "%s", out)
	x.True(forgotten("grace"), "somebody after the one who failed was not forgotten: %s", out)

	gone, gerr := s.Ent.Tenant.Get(ctx, oldco.Uuid())
	x.NoError(gerr)
	x.NotNil(gone.DateDestroyed, "a person failed, and a closed tenant whose grace had run out was not looked at: %s", out)

	x.Zero(consents(), "`roster forget` destroyed no consent kept past consent.keep: %s", out)

	// At the end, who failed: in what it prints and in what it answers.
	x.Error(err, "somebody could not be forgotten and the command answered OK")
	x.ErrorContains(err, leavers["frank"].String(), "who failed is not said")
	x.Contains(out, fmt.Sprintf("%s was not forgotten", leavers["frank"]), "%s", out)
}
