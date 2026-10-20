package cmd_test

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
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
}

// TestRowsAreNotCheckedWithoutAKey: a trail with no key keeps no digest, and
// a check that would find nothing to compare is refused rather than passing.
func TestRowsAreNotCheckedWithoutAKey(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	_, ctx := build(t, func(cc *cmd.Config) { c = *cc })
	x.ErrorContains(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "audit.digest.key")
}
