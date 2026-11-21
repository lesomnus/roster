package cmd_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestALockLeavesItsRowAsTheTrailKeepsIt: the write two callers contend on
// changes nothing of the row it is made on.
//
// # What it was
//
// A rule that counts other rows takes a write on a row both callers name
// first (`server/core/only.go`), and that write moved the row's
// `date_updated`, past the recorder on purpose: a lock is nothing anybody
// asked for. But a deployment that keeps digests checks its rows against its
// trail (`roster trail rows`), and a row written past the servers is what
// that check exists to find. So after somebody's identity was erased, which
// takes the person's lock, the check found the person: `differs holder`, for
// a row nobody had touched -- and the same for a tenant after a binding of
// its administration was erased. A site's lock, taken by whatever is put in
// the site, would have made a finding of every site anything was added to
// (#279).
//
// What contends is the write, and not what it writes. So it writes the row as
// it is.
func TestALockLeavesItsRowAsTheTrailKeepsIt(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	rows := func() error { return cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}) }
	x.NoError(rows())

	// The site's lock: a team put in it.
	seoul := b.site(t, ctx, b.Contoso, "seoul")
	b.team(t, ctx, seoul, "ops")
	x.NoError(rows(), "a site something was put in")

	// The person's: one of their two ways in erased.
	b.connected(t, ctx, b.Contoso, "github", "entra")
	who := b.holder(t, ctx, b.Contoso, "person")
	one := b.identity(t, ctx, who, "github", "gh-1")
	b.identity(t, ctx, who, "entra", "en-1")
	_, err := b.Walled.Identity().Erase(b.as(ctx, b.ContosoUser, b.Contoso), app.IdentityRef_builder{Id: one.GetId()}.Build())
	x.NoError(err)
	x.NoError(rows(), "somebody whose identity was erased")

	// The tenant's: one of two bindings of its administration erased.
	l := newLastAdmin(t, b, ctx, "locked")
	second := l.second(t, ctx)
	_, err = b.Walled.Binding().Erase(l.as, b.bindingOf(t, ctx, second))
	x.NoError(err)
	x.NoError(rows(), "a tenant whose administration was narrowed")
}
