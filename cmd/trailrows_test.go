package cmd_test

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	payaudit "github.com/lesomnus/payday/audit"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/audit"
	"github.com/lesomnus/roster/internal/ent/email"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/rowseal"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/forget"
)

// TestTheLiveRowsAreCheckedAgainstTheTrail is `roster trail rows`: with a
// digest key, every row a server wrote matches what the trail keeps of it, and
// one changed in the database itself, past the servers, does not.
func TestTheLiveRowsAreCheckedAgainstTheTrail(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	h := b.holder(t, ctx, b.Contoso, "another")

	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }
	x.NoError(run("trail", "rows"))

	x.NoError(b.Ent.Holder.UpdateOneId(h.Uuid()).SetDateDisabled(time.Now()).Exec(ctx))
	out, err := stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.ErrorContains(err, "1 finding(s)")
	of := digestOf(t, out)
	x.ErrorContains(err, "--of "+of, "a check does not say what an acceptance of it names")

	// And somebody who has looked accepts it as it is, which is an act on the
	// trail -- of what they looked at (payday#115), and saying why
	// (payday#108).
	x.ErrorContains(run("trail", "rows", "--accept", "--why", "looked at"), "--of: the digest")
	x.ErrorContains(run("trail", "rows", "--accept", "--of", of), "--why")
	x.ErrorContains(run("trail", "rows", "--accept", "--of", nothingLookedAt, "--why", "not what was looked at"), "`roster trail rows` again")
	x.NoError(run("trail", "rows", "--accept", "--of", of, "--why", "the migration of 2026-10-20 set it"))
	x.NoError(run("trail", "rows"))

	vs, err := b.Ent.Audit.Query().Where(audit.ObjectId(h.Uuid())).Order(audit.ByDateCreated()).All(ctx)
	x.NoError(err)
	x.Equal("roster.trail.rows.accept", vs[len(vs)-1].Action)
}

// TestARowNothingOnTheTrailIsAboutFailsTheCheck: what a row somebody added in
// the database past the servers looks like, which is a finding as a row that
// differs is (payday#108).
func TestARowNothingOnTheTrailIsAboutFailsTheCheck(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	h := b.holder(t, ctx, b.Contoso, "another")
	_, err := b.Ent.Audit.Delete().Where(audit.ObjectId(h.Uuid())).Exec(ctx)
	x.NoError(err)

	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "1 finding(s)")
}

// TestARowRemovedPastTheServersFailsTheCheck: an address deleted in the
// database is a thing the trail says is a row and is not -- what a row
// somebody removed past the servers looks like (payday#115) -- and accepted, it
// is on the trail as gone.
func TestARowRemovedPastTheServersFailsTheCheck(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }

	h := b.holder(t, ctx, b.Contoso, "another")
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: h.Bytes()}.Build(),
		Address: "another@contoso.example",
	}.Build())
	x.NoError(err)
	x.NoError(run("trail", "rows"))

	n, err := b.Ent.Email.Delete().Where(email.HasHolderWith(holder.IdEQ(h.Uuid()))).Exec(ctx)
	x.NoError(err)
	x.Equal(1, n)
	out, err := stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.ErrorContains(err, "1 finding(s)")

	x.NoError(run("trail", "rows", "--accept", "--of", digestOf(t, out), "--why", "the address was removed by hand on 2026-10-22"))
	x.NoError(run("trail", "rows"))
}

// TestACheckWithAnotherKeyFindsEveryRow: a check handed a key other than the
// one the trail was written under does not pass (payday#115) -- it finds every
// row under a key it was not given -- and told that key was let go, it counts
// them instead.
func TestACheckWithAnotherKeyFindsEveryRow(t *testing.T) {
	x := require.New(t)

	server := bytes.Repeat([]byte{7}, 32)
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(server)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	other := c
	other.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	x.ErrorContains(cli.Cmd(&other).Run(ctx, []string{"trail", "rows"}), "finding(s)")

	other.Audit.Digest.Retired = []string{payaudit.FingerprintOf(server).String()}
	x.NoError(cli.Cmd(&other).Run(ctx, []string{"trail", "rows"}))
}

// TestWhatTheDeploymentWritesPastItsServersIsOnTheTrailAsItsAct: a tenant
// destroyed and a person brought back are written through ent, and each is
// recorded as the act it is -- so a check of the rows finds nothing after
// either (payday#108).
func TestWhatTheDeploymentWritesPastItsServersIsOnTheTrailAsItsAct(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	cs, err := c.Audit.Contents()
	x.NoError(err)
	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }

	h := b.holder(t, ctx, b.Contoso, "another")
	x.NoError(b.Ent.Holder.UpdateOneId(h.Uuid()).SetDateErased(time.Now()).Exec(ctx))
	x.NoError(forget.Restore(ctx, b.Ent, h, cs))
	x.NoError(run("trail", "rows"))

	// Forgotten: their rows removed and the holder blanked, each on the trail
	// as the act that did it (payday#115) -- under the deployment's own
	// policy, as `roster forget` and the sweep hand it, which carries the
	// digest key the forgetting leaves a seal that says so under. The check
	// counts the holder as forgotten, and vouches for what its digest covers.
	p, err := c.Audit.Policy()
	x.NoError(err)
	_, err = forget.Forget(ctx, b.Ent, h, p, cs)
	x.NoError(err)
	out, err := stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.NoError(err)
	x.Contains(out, "; 1 forgotten;")

	x.NoError(b.Ent.Tenant.UpdateOneId(b.Contoso.Uuid()).SetDateClosed(time.Now()).Exec(ctx))
	_, err = forget.DestroyTenant(ctx, b.Ent, b.Contoso, p, cs)
	x.NoError(err)

	vs, err := b.Ent.Audit.Query().Where(audit.ObjectId(b.Contoso.Uuid())).Order(audit.ByDateCreated()).All(ctx)
	x.NoError(err)
	x.Equal(forget.DestroyedTenant, vs[len(vs)-1].Action)
	x.NoError(run("trail", "rows"))
}

// TestRowsFromBeforeSealsAreSealedByAnAcceptance is the one acceptance an
// operator runs after the upgrade that brought seals (payday#115). A holder
// whose newest write sealed nothing -- made here by writing its newest line's
// digest as a build before seals would have, and taking its seal off -- is
// counted as from before seals, and is no finding. `--accept` of that check
// seals it, though the check found nothing, and the next one counts none.
func TestRowsFromBeforeSealsAreSealedByAnAcceptance(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	cs, err := c.Audit.Contents()
	x.NoError(err)
	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }

	h := b.holder(t, ctx, b.Contoso, "another")
	newest, err := b.Ent.Audit.Query().Where(audit.ObjectId(h.Uuid())).Order(audit.ByDateCreated(entsql.OrderDesc())).First(ctx)
	x.NoError(err)
	q := b.Ent.Holder.Query().Where(holder.IdEQ(h.Uuid()))
	bare.HolderSelectInit(q, nil)
	row, err := q.Only(ctx)
	x.NoError(err)
	sum, err := cs.Digest.Of(h, row.Proto(), newest.DigestFields, payaudit.Line{Id: newest.Id, Created: newest.DateCreated, Action: newest.Action})
	x.NoError(err)
	x.False(payaudit.Sealing(sum))
	x.NoError(b.Ent.Audit.UpdateOneId(newest.Id).SetDigest(sum).Exec(ctx))
	x.NoError(b.Ent.RowSeal.DeleteOneId(h.Uuid()).Exec(ctx))

	sealed := func() bool {
		ok, err := b.Ent.RowSeal.Query().Where(rowseal.IdEQ(h.Uuid())).Exist(ctx)
		x.NoError(err)
		return ok
	}

	out, err := stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.NoError(err, "a row from before seals was taken for a finding")
	x.Contains(out, "1 row(s) from before seals")
	of := digestOf(t, out)

	x.ErrorContains(run("trail", "rows", "--accept", "--of", nothingLookedAt, "--why", "not what was looked at"), "`roster trail rows` again")
	x.False(sealed(), "an acceptance of another check sealed it")

	out, err = stderrOf(t, cli.Cmd(&c), "trail", "rows", "--accept", "--of", of, "--why", "written before seals")
	x.NoError(err)
	x.Contains(out, "sealed 1 from before seals")
	x.True(sealed(), "the acceptance sealed nothing")

	out, err = stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.NoError(err)
	x.NotContains(out, "from before seals")

	vs, err := b.Ent.Audit.Query().Where(audit.ObjectId(h.Uuid())).Order(audit.ByDateCreated()).All(ctx)
	x.NoError(err)
	x.Equal("roster.trail.rows.accept", vs[len(vs)-1].Action)
}

// TestRowsAreNotCheckedWithoutAKey: a trail with no key keeps no digest, and
// a check that would find nothing to compare is refused rather than passing.
func TestRowsAreNotCheckedWithoutAKey(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	_, ctx := build(t, func(cc *cmd.Config) { c = *cc })
	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "audit.digest.key")
}
