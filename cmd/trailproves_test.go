package cmd_test

import (
	"os"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/audit"
	"github.com/lesomnus/roster/internal/ent/holder"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// What holds the manual's account of the two checks to the checks (#280).
//
// These are not rules, and nothing here is what ought to be so: each is a
// sentence `docs/operating.md` says of `roster trail rows` and `roster trail
// verify` under *what it proves, and what it cannot*, made in a database by
// hand and asked of both. The page read wider than this for as long as nobody
// had tried. A test that fails here is one of two things: the checks reach
// further than they did, which is the sentence to take out of the page, or
// somebody changed what a line of the trail is bound to.

// TestWhatNeitherCheckOfTheTrailFinds.
//
// A digest binds a row to the identifier, the moment and the action of the
// line that keeps it, and a check reads the newest line about each row and no
// other. So both checks answer that nothing is wrong after a line's actor,
// its tenant or its patch is rewritten, after any older line is rewritten or
// removed, after a line is added in the past, and after a write is undone
// with the line that recorded it -- with a `store:` witness named, which
// hears the archive's checkpoints and nothing of a line still in the
// database.
func TestWhatNeitherCheckOfTheTrailFinds(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = t.TempDir()
		cc.Audit.Retain = 90 * 24 * time.Hour
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		cc.Audit.Checkpoints.Witnesses = []string{"store:" + t.TempDir()}
		cc.Audit.Digest.Key = digestKey
		c = *cc
	})

	kim := b.holder(t, ctx, b.Contoso, "kim")
	ref := app.HolderRef_builder{Id: kim.Bytes()}.Build()
	_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = b.Ungated.Holder().Enable(ctx, app.HolderEnableRequest_builder{Ref: ref}.Build())
	x.NoError(err)

	lines := func() []*ent.Audit {
		t.Helper()

		vs, err := b.Ent.Audit.Query().Where(audit.ObjectId(kim.Uuid())).Order(audit.ByDateCreated(), audit.ById()).All(ctx)
		x.NoError(err)

		return vs
	}
	nothing := func(after string) {
		t.Helper()

		x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "a check of the rows finds something after %s", after)
		x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "verify"}), "a verification finds something after %s", after)
	}

	vs := lines()
	x.Len(vs, 3, "kim was added, disabled and enabled")
	oldest, newest := vs[0], vs[len(vs)-1]
	nothing("nothing at all")

	// Who did it, whose trail it is filed under and read in, and what it set.
	x.NoError(b.Ent.Audit.UpdateOneId(newest.Id).
		SetActorId(b.ContosoUser.Uuid()).
		SetActorTenantId(b.Fabrikam.Uuid()).
		SetTenantId(b.Fabrikam.Uuid()).
		SetPatch([]byte{0}).
		Exec(ctx))
	nothing("the newest line's actor, tenant and patch were rewritten")

	x.NoError(b.Ent.Audit.UpdateOneId(oldest.Id).
		SetAction("/roster.Nothing/Happened").
		SetPatch([]byte{0}).
		SetDigest([]byte{0}).
		Exec(ctx))
	nothing("an older line was rewritten, its digest with it")

	x.NoError(b.Ent.Audit.Create().
		SetId(pdid.New(pd.AuditDomain).Uuid()).
		SetTenantId(newest.TenantId).
		SetActorId(newest.ActorId).
		SetActorTenantId(newest.ActorTenantId).
		SetTraceId(newest.TraceId).
		SetAction(app.HolderService_Erase_FullMethodName).
		SetObjectId(newest.ObjectId).
		SetDomain(newest.Domain).
		SetPatch(newest.Patch).
		SetValue(newest.Value).
		SetFields(newest.Fields).
		SetDigest(newest.Digest).
		SetDigestFields(newest.DigestFields).
		SetDateCreated(newest.DateCreated.AddDate(-1, 0, 0)).
		Exec(ctx))
	nothing("a line was added a year in the past")

	x.NoError(b.Ent.Audit.DeleteOneId(oldest.Id).Exec(ctx))
	nothing("an older line was removed")

	// And the one that is found, so that the ones above are not found for
	// want of looking: what the newest line says was done.
	x.NoError(b.Ent.Audit.UpdateOneId(newest.Id).SetAction("/roster.Nothing/Happened").Exec(ctx))
	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "1 finding(s)")
	x.NoError(b.Ent.Audit.UpdateOneId(newest.Id).SetAction(newest.Action).Exec(ctx))
	nothing("the newest line's action was put back")

	// A write undone with its record: the row as it was, its seal as it was,
	// and the line that said otherwise gone.
	row, err := b.Ent.Holder.Query().Where(holder.IdEQ(kim.Uuid())).Only(ctx)
	x.NoError(err)
	seal, err := b.Ent.RowSeal.Get(ctx, kim.Uuid())
	x.NoError(err)
	had := len(lines())

	_, err = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	nothing("a write through the servers")
	after := lines()
	x.Greater(len(after), had, "disabling somebody wrote no line")

	for _, v := range after[had:] {
		x.NoError(b.Ent.Audit.DeleteOneId(v.Id).Exec(ctx))
	}
	x.NoError(b.Ent.Holder.UpdateOneId(kim.Uuid()).ClearDateDisabled().SetDateUpdated(row.DateUpdated).Exec(ctx))
	x.NoError(b.Ent.RowSeal.UpdateOneId(kim.Uuid()).SetSum(seal.Sum).SetFields(seal.Fields).Exec(ctx))

	now, err := b.Ent.Holder.Query().Where(holder.IdEQ(kim.Uuid())).Only(ctx)
	x.NoError(err)
	x.Nil(now.DateDisabled, "kim is not disabled any more, and nothing says they were")
	nothing("a write was undone with the line that recorded it")
}

// TestNeitherCheckReadsTheControlPlaneOrTheAccessRecord.
//
// `trail rows` reads the data plane's rows against the data plane's trail,
// and the control plane's trail is handed no digest key to begin with. So an
// operator's row changed by hand, with the control plane's whole trail
// removed, is found by nothing. Nor is the access record read: its rows keep
// no digest, and removing them leaves the check as it was. The check prints
// no `not checked:` line for either, since that line is for a kind declared
// off the trail and roster declares none. And a deployment that names no
// archive has no account of one and no checkpoint, so there is nothing for
// `trail verify` to read.
func TestNeitherCheckReadsTheControlPlaneOrTheAccessRecord(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	c.Audit.Digest.Key = digestKey
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	_, err = entities(t, at(c, "admin"), "tenant", "add", "@newco")
	x.NoError(err)
	_, err = entities(t, at(c, "admin"), "holder", "get", "@newco/admin")
	x.NoError(err)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}))

	// The control plane: the operator stopped by hand, and the record of
	// everything done there gone.
	operator, err := cmd.ControlHolder(ctx, s.Control, "admin")
	x.NoError(err)
	x.NoError(s.Control.Ent.Holder.UpdateOneId(operator.Uuid()).SetDateDisabled(time.Now()).Exec(ctx))
	gone, err := s.Control.Ent.Audit.Delete().Exec(ctx)
	x.NoError(err)
	x.NotZero(gone, "the control plane had no trail, so this proves nothing")

	// The access record: who read whose data, gone.
	read, err := s.Ent.Access.Delete().Exec(ctx)
	x.NoError(err)
	x.NotZero(read, "nothing was on the access record, so this proves nothing")

	said, err := stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.NoError(err, "a check of the rows finds what was done to the control plane or to the access record")
	x.NotContains(said, "not checked:", "the check lists what it did not read")

	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "verify"}), "--archive: which directory")
}

// TestAPassThatCouldNotWriteItsArchiveLeavesACheckpointTheStoreNeverKept.
//
// A pass numbers its checkpoint in the database and then writes it to the
// store, so one that runs while the store cannot be written -- here the
// archive's own directory, gone read-only -- takes a number the store never
// keeps. The next verification finds the checkpoint after it naming none
// before it: `unkept`, which the manual described only as a chain somebody
// cut short. It is the same finding, and what tells the two apart is the
// deployment's own record of the pass that failed. `trail accept` clears it,
// and nothing else does.
func TestAPassThatCouldNotWriteItsArchiveLeavesACheckpointTheStoreNeverKept(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a directory nobody may write is one root may, and this needs the archive to refuse a write")
	}
	x := require.New(t)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	x.NoError(os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	said, err := stderrOf(t, cli.Cmd(&c), "trail", "prune")
	x.ErrorContains(err, "the pass", "a pass that could not write its archive answered nil")
	x.Contains(said, "trail: the checkpoint of a pass", "the pass does not say its checkpoint was not kept")

	x.NoError(os.Chmod(dir, 0o700))
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune"}))

	out := captured(t, func() error {
		said, err = stderrOf(t, cli.Cmd(&c), "trail", "verify")

		return nil
	})
	x.ErrorContains(err, "1 finding(s)")
	x.Contains(out, "unkept ")
	x.Contains(out, "names none as the one before it, and the store does not keep 1")

	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune"}))
	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "verify"}), "1 finding(s)", "a pass that works does not clear it")

	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "accept", "--of", digestOf(t, said), "--why", "the pass of this test, while the archive could not be written"}))
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "verify"}))
}
