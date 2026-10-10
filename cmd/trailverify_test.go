package cmd_test

import (
	"bytes"
	"compress/gzip"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// TestTheArchiveIsCheckedAgainstTheDatabasesAccountOfIt is `roster trail
// verify`, `accept` and `purge` at a shell.
//
// What the trail moves out of the database is written into the archive and
// into the database's account of it at once, so a verification after a pass
// finds nothing. A chunk put beside them by somebody who can write the archive
// and nothing else is found, and a purge by hand leaves it alone -- the
// account decides what goes. Once somebody has looked, `accept` takes it in.
func TestTheArchiveIsCheckedAgainstTheDatabasesAccountOfIt(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }

	x.NoError(run("trail", "prune"))
	x.NoError(run("trail", "verify", "--full"))

	// A chunk of contoso's, under the labels of one that is there and with
	// bytes nobody wrote.
	a := flob.NewOsStores(dir)
	cs, err := trail.Chunks(ctx, a)
	x.NoError(err)
	i := slices.IndexFunc(cs, func(v trail.Chunk) bool { return v.Namespace == b.Contoso.String() })
	x.GreaterOrEqual(i, 0, "contoso has no chunk of its own")
	plant := func() {
		info, err := a.Use(cs[i].Namespace).Stat(ctx, cs[i].Digest)
		x.NoError(err)
		l, err := info.Labels(ctx)
		x.NoError(err)

		var buf bytes.Buffer
		z := gzip.NewWriter(&buf)
		_, err = z.Write([]byte(`{"id":"` + time.Now().Format(time.RFC3339Nano) + `"}` + "\n"))
		x.NoError(err)
		x.NoError(z.Close())
		_, err = a.Use(cs[i].Namespace).Add(ctx, flob.Meta{Labels: l}, &buf)
		x.NoError(err)
	}

	plant()
	err = run("trail", "verify")
	x.ErrorContains(err, "1 finding(s)")
	x.ErrorContains(run("trail", "accept"), "--why")
	x.NoError(run("trail", "accept", "--why", "put there by this test, and looked at"))
	x.NoError(run("trail", "verify"))

	// And one more, which a purge of everything leaves where it is.
	plant()
	x.NoError(run("trail", "purge", "--older-than", "1ns"))
	left, err := trail.Chunks(ctx, a)
	x.NoError(err)
	x.Len(left, 1, "a purge destroyed a chunk the database does not account for, or left one it does")
	err = run("trail", "verify")
	x.ErrorContains(err, "1 finding(s)", "the chunk nobody wrote is still the one finding")
}
