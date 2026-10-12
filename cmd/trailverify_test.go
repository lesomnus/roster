package cmd_test

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/pd"
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

// TestRowsFromBeforeTheKeyAreAcceptedOnce is what a deployment that signs its
// checkpoints does on the payday that signs the manifest's rows too
// (lesomnus/payday#58, roster#110). A row nobody signed looks the same whether
// it was written before rows were signed or had its signature taken off, so
// with a key every one of them is a finding until an operator who has looked
// runs `trail accept`, once. After that, verify is clean, and what the next
// prune writes is signed as it goes.
func TestRowsFromBeforeTheKeyAreAcceptedOnce(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "before")

	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }

	// Written while the deployment had no key.
	x.NoError(run("trail", "prune"))
	x.NoError(run("trail", "verify"))

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	x.NoError(err)
	c.Audit.Checkpoints.Key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	err = run("trail", "verify")
	x.ErrorContains(err, "finding(s)", "rows nobody signed were taken for signed ones")
	x.NoError(run("trail", "accept", "--why", "written before this deployment signed its rows"))
	x.NoError(run("trail", "verify"))

	// And what is written now is signed as it is written.
	b.holder(t, ctx, b.Contoso, "after")
	x.NoError(run("trail", "prune"))
	x.NoError(run("trail", "verify"))

	t.Run("and a prune into a second archive is refused", func(t *testing.T) {
		x := require.New(t)

		err := run("trail", "prune", "--older-than", "1ns", "--to", t.TempDir())
		x.ErrorContains(err, "--to")
		x.ErrorContains(err, "audit.archive")
		x.NoError(run("trail", "prune", "--older-than", "1ns", "--to", dir), "the archive it already has")
	})
}

// TestADatabaseRestoredFromABackupIsAcceptedOnce is what docs/operating.md says
// to do after the database is restored from a backup older than the latest
// checkpoint (lesomnus/payday#70). `verify` finds it behind the checkpoint
// store, and nothing is checkpointed while it is, as before; but `accept` was
// refused for the same reason, and so nothing ever was again. Now one `trail
// accept` carries it on, numbered past the store's checkpoints, and `verify` is
// clean at once.
func TestADatabaseRestoredFromABackupIsAcceptedOnce(t *testing.T) {
	x := require.New(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	x.NoError(err)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		c = *cc
	})
	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }
	latest := func() int {
		k, _, err := pd.TrailStore(b.Ent).Latest(ctx)
		x.NoError(err)
		return k
	}

	b.holder(t, ctx, b.Contoso, "before")
	x.NoError(run("trail", "prune"))
	x.NoError(run("trail", "verify"))
	then := latest()

	// The backup, of the database's account of the archive.
	backup, err := b.Ent.Archived.Query().All(ctx)
	x.NoError(err)

	b.holder(t, ctx, b.Contoso, "after")
	x.NoError(run("trail", "prune"))
	since := latest()
	x.Greater(since, then)

	// Restored, and the archive and its checkpoints not.
	_, err = b.Ent.Archived.Delete().Exec(ctx)
	x.NoError(err)
	for _, v := range backup {
		x.NoError(b.Ent.Archived.Create().
			SetId(v.Id).
			SetNamespace(v.Namespace).
			SetDigest(v.Digest).
			SetIntent(v.Intent).
			SetLabels(v.Labels).
			SetState(v.State).
			SetSince(v.Since).
			SetGone(v.Gone).
			SetSignature(v.Signature).
			SetSignedBy(v.SignedBy).
			SetDateCreated(v.DateCreated).
			SetDateUpdated(v.DateUpdated).
			Exec(ctx))
	}

	x.ErrorContains(run("trail", "verify"), "finding(s)")
	b.holder(t, ctx, b.Contoso, "meanwhile")
	x.NoError(run("trail", "prune"))
	x.Equal(then, latest(), "a checkpoint was taken over a database behind the store")

	x.NoError(run("trail", "accept", "--why", "the database restored from a backup, and looked at"))
	x.Equal(since+1, latest())
	x.NoError(run("trail", "verify", "--full"))

	// And it goes on from there.
	b.holder(t, ctx, b.Contoso, "and-on")
	x.NoError(run("trail", "prune"))
	x.Greater(latest(), since+1)
	x.NoError(run("trail", "verify", "--full"))
}
