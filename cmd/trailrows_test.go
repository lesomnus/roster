package cmd_test

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/audit"
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
	x.ErrorContains(run("trail", "rows"), "1 row(s) differ")

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

	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "1 are not on it")
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
