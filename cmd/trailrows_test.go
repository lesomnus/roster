package cmd_test

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	payaudit "github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/audit"
	"github.com/lesomnus/roster/internal/ent/email"
	"github.com/lesomnus/roster/internal/ent/holder"
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
	x.ErrorContains(run("trail", "rows"), "1 finding(s)")

	// And somebody who has looked accepts it as it is, which is an act on the
	// trail -- and needs saying why (payday#108).
	x.ErrorContains(run("trail", "rows", "--accept"), "--why")
	x.NoError(run("trail", "rows", "--accept", "--why", "the migration of 2026-10-20 set it"))
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
	_, err := b.Ent.Email.Delete().Where(email.HasHolderWith(holder.IdEQ(h.Uuid()))).Exec(ctx)
	x.NoError(err)
	x.ErrorContains(run("trail", "rows"), "finding(s)")

	x.NoError(run("trail", "rows", "--accept", "--why", "the address was removed by hand on 2026-10-22"))
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
	// as the act that did it (payday#115).
	_, err = forget.Forget(ctx, b.Ent, h, trail.Policy{}, cs)
	x.NoError(err)
	x.NoError(run("trail", "rows"))

	x.NoError(b.Ent.Tenant.UpdateOneId(b.Contoso.Uuid()).SetDateClosed(time.Now()).Exec(ctx))
	_, err = forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, cs)
	x.NoError(err)

	vs, err := b.Ent.Audit.Query().Where(audit.ObjectId(b.Contoso.Uuid())).Order(audit.ByDateCreated()).All(ctx)
	x.NoError(err)
	x.Equal(forget.DestroyedTenant, vs[len(vs)-1].Action)
	x.NoError(run("trail", "rows"))
}

// TestRowsAreNotCheckedWithoutAKey: a trail with no key keeps no digest, and
// a check that would find nothing to compare is refused rather than passing.
func TestRowsAreNotCheckedWithoutAKey(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	_, ctx := build(t, func(cc *cmd.Config) { c = *cc })
	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "audit.digest.key")
}
