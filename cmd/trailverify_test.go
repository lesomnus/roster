package cmd_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/pd"
)

// stderrOf runs a command and answers with what it wrote to stderr, beside
// its error: where `trail verify` and `trail rows` say the digest of what they
// found, apart from the findings themselves.
func stderrOf(t *testing.T, k *xli.Command, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		read <- b
	}()

	was := os.Stderr
	os.Stderr = w
	err = k.Run(t.Context(), args)
	os.Stderr = was
	require.NoError(t, w.Close())

	return string(<-read), err
}

// digestOf is the digest a check printed of what it found.
func digestOf(t *testing.T, out string) string {
	t.Helper()

	m := regexp.MustCompile(`(?m)^digest (sha256:[0-9a-f]+)$`).FindStringSubmatch(out)
	require.NotNil(t, m, "no digest in what it printed:\n%s", out)

	return m[1]
}

// nothingLookedAt is a digest no check printed.
var nothingLookedAt = "sha256:" + strings.Repeat("0", 64)

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
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
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
	out, err := stderrOf(t, cli.Cmd(&c), "trail", "verify")
	x.ErrorContains(err, "1 finding(s)")
	of := digestOf(t, out)
	x.ErrorContains(err, "--of "+of, "verify does not say what an acceptance of it names")

	x.ErrorContains(run("trail", "accept", "--why", "looked at"), "--of")
	x.ErrorContains(run("trail", "accept", "--of", of), "--why")

	// Of findings other than these, it is refused, and says to verify again.
	err = run("trail", "accept", "--of", nothingLookedAt, "--why", "not what was looked at")
	x.ErrorIs(err, trail.ErrUnreviewed)
	x.ErrorContains(err, "roster trail verify")

	x.NoError(run("trail", "accept", "--of", of, "--by", "the test", "--why", "put there by this test, and looked at"))
	out, err = stderrOf(t, cli.Cmd(&c), "trail", "verify")
	x.NoError(err)
	x.Contains(out, "by the test: put there by this test, and looked at (unaccounted 1)", "verify does not say what was accepted")

	// And one more, which a purge of everything leaves where it is.
	plant()
	x.NoError(run("trail", "purge", "--older-than", "1ns"))
	left, err := trail.Chunks(ctx, a)
	x.NoError(err)
	x.Len(left, 1, "a purge destroyed a chunk the database does not account for, or left one it does")
	err = run("trail", "verify")
	x.ErrorContains(err, "1 finding(s)", "the chunk nobody wrote is still the one finding")
}

// TestAFullCheckIsAcceptedAsAFullOne is `trail accept --full`. Only `verify
// --full` reads a chunk's bytes, which is the one way a chunk whose bytes are
// not its digest is found (`altered`), so a full check's digest is not what a
// light one finds: an acceptance verifies again the way the check it names did,
// and one of a full check made without `--full` is of findings it never looked
// at (payday#115).
func TestAFullCheckIsAcceptedAsAFullOne(t *testing.T) {
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

	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }
	x.NoError(run("trail", "prune"))

	// A chunk's bytes written again beneath its name, as somebody who can
	// write the archive's disk would.
	cs, err := trail.Chunks(ctx, flob.NewOsStores(dir))
	x.NoError(err)
	x.NotEmpty(cs, "the pass archived nothing, so this proves nothing")
	d := cs[0].Digest
	enc := d.Encoded()
	path := filepath.Join(dir, "share", d.Algorithm().String(), enc[0:2], enc[2:4], enc[4:])
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	_, err = z.Write([]byte(`{"id":"nobody wrote this"}` + "\n"))
	x.NoError(err)
	x.NoError(z.Close())
	x.NoError(os.Chmod(path, 0o600))
	x.NoError(os.WriteFile(path, buf.Bytes(), 0o600))

	x.NoError(run("trail", "verify"), "a light check read a chunk's bytes")
	out, err := stderrOf(t, cli.Cmd(&c), "trail", "verify", "--full")
	x.ErrorContains(err, "1 finding(s)")
	x.ErrorContains(err, "--full --why", "verify --full does not say its acceptance is a full one")
	of := digestOf(t, out)

	x.ErrorIs(run("trail", "accept", "--of", of, "--why", "looked at"), trail.ErrUnreviewed,
		"a full check was accepted by a light one")
	x.NoError(run("trail", "accept", "--of", of, "--full", "--why", "the chunk's bytes, looked at"))

	out, err = stderrOf(t, cli.Cmd(&c), "trail", "verify")
	x.NoError(err)
	x.Contains(out, ": the chunk's bytes, looked at (altered 1)", "verify does not say what was accepted")
}

// TestRowsNobodySignedAreAcceptedOnce is what a deployment does whose
// archive's account has rows nobody signed: written by a build from before rows
// were signed (lesomnus/payday#58, roster#110), or by one that kept the archive
// without a key at all (payday#115) -- which is not a state this build can
// make, since it keeps no archive without one. A row nobody signed looks the
// same whether it was written so or had its signature taken off, which is how
// this makes one, and each is a finding until an operator who has looked runs
// `trail accept`, once. After that, verify is clean, and what the next prune
// writes is signed as it goes.
func TestRowsNobodySignedAreAcceptedOnce(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "before")

	run := func(args ...string) error { return cli.Cmd(&c).Run(ctx, args) }

	x.NoError(run("trail", "prune"))
	x.NoError(run("trail", "verify"))

	// The rows as a build that did not sign them left them.
	n, err := b.Ent.Archived.Update().SetSignature([]byte{}).SetSignedBy([]byte{}).Save(ctx)
	x.NoError(err)
	x.NotZero(n, "the account has no rows, so this proves nothing")

	out, err := stderrOf(t, cli.Cmd(&c), "trail", "verify")
	x.ErrorContains(err, "finding(s)", "rows nobody signed were taken for signed ones")
	x.NoError(run("trail", "accept", "--of", digestOf(t, out), "--why", "written before this deployment signed its rows"))
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

	t.Run("and so are an acceptance and a purge of one", func(t *testing.T) {
		x := require.New(t)

		// Read against an archive with nothing in it, an acceptance would take
		// every chunk of this one for missing.
		other := t.TempDir()
		for _, args := range [][]string{
			{"trail", "accept", "--in", other, "--of", nothingLookedAt, "--why", "a second archive"},
			{"trail", "purge", "--older-than", "1ns", "--in", other},
		} {
			err := run(args...)
			x.ErrorContains(err, "--in", args)
			x.ErrorContains(err, "audit.archive", args)
		}
		out, err := stderrOf(t, cli.Cmd(&c), "trail", "verify")
		x.NoError(err, "a refused act changed the account")

		x.NoError(run("trail", "accept", "--in", dir, "--of", digestOf(t, out), "--why", "the archive it already has"))
		x.NoError(run("trail", "purge", "--older-than", "1ns", "--in", dir, "--dry-run"))
		x.NoError(run("trail", "verify"))
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

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
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

	out, err := stderrOf(t, cli.Cmd(&c), "trail", "verify")
	x.ErrorContains(err, "--restored", "verify does not say a restore names its checkpoint")
	of := digestOf(t, out)

	// A restore is accepted as one, naming the checkpoint its backup holds.
	err = run("trail", "accept", "--of", of, "--why", "the database restored from a backup")
	x.ErrorIs(err, trail.ErrRestored)
	x.ErrorContains(err, "roster trail verify")
	x.ErrorIs(run("trail", "accept", "--of", of, "--restored", strconv.Itoa(then+1), "--why", "the wrong backup"), trail.ErrRestored)

	x.NoError(run("trail", "accept", "--of", of, "--restored", strconv.Itoa(then), "--why", "the database restored from a backup, and looked at"))
	x.Equal(since+1, latest())
	x.NoError(run("trail", "verify", "--full"))

	// And it goes on from there.
	b.holder(t, ctx, b.Contoso, "and-on")
	x.NoError(run("trail", "prune"))
	x.Greater(latest(), since+1)
	x.NoError(run("trail", "verify", "--full"))
}

// TestAnArchiveIsKeptWithAKey: the account of an archive is signed, and there
// is no key everybody knows to sign it with (payday#115). So a deployment that
// names an archive and no `audit.checkpoints.key` is refused where it comes up,
// and so is a trail command handed an archive of its own -- with the setting
// that means it, and how to make one -- rather than writing an account the
// first verification finds nothing signed.
func TestAnArchiveIsKeptWithAKey(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	_, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Audit: config.AuditConfig{Archive: t.TempDir(), Retain: 90 * 24 * time.Hour},
	})
	x.ErrorIs(err, trail.ErrNoKey)
	x.ErrorContains(err, "audit.checkpoints.key")

	t.Run("and a trail command handed one", func(t *testing.T) {
		x := require.New(t)

		var c cmd.Config
		b, ctx := build(t, func(cc *cmd.Config) { c = *cc })
		b.holder(t, ctx, b.Contoso, "another")
		was, err := b.Ent.Audit.Query().Count(ctx)
		x.NoError(err)

		err = cli.Cmd(&c).Run(ctx, []string{"trail", "prune", "--older-than", "1ns", "--to", t.TempDir()})
		x.ErrorContains(err, "audit.checkpoints.key")

		now, err := b.Ent.Audit.Query().Count(ctx)
		x.NoError(err)
		x.Equal(was, now, "a prune refused for its key took rows out of the database")
	})
}

// TestAVerificationSaysWhatEachWitnessHeard: a deployment that names
// witnesses has every checkpoint heard outside the database and the archive
// (payday#115) -- `log:` and `store:`, which this binary links -- and `trail
// verify` says what each that answers heard last, beside the digest: nothing
// yet before the first checkpoint, and then the newest checkpoint's number and
// what the store keeps it under. A witness that only tells answers nothing,
// and is not among them.
func TestAVerificationSaysWhatEachWitnessHeard(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = t.TempDir()
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		cc.Audit.Checkpoints.Witnesses = []string{"log:", "store:" + t.TempDir()}
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	out, err := stderrOf(t, cli.Cmd(&c), "trail", "verify")
	x.NoError(err)
	x.Contains(out, "heard by "+c.Audit.Checkpoints.Witnesses[1]+": nothing yet\n")

	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune"}))
	out, err = stderrOf(t, cli.Cmd(&c), "trail", "verify")
	x.NoError(err)

	m := regexp.MustCompile(`(?m)^heard by (store:\S+): checkpoint ([0-9]+), (sha256:[0-9a-f]{64}), at \S+$`).FindStringSubmatch(out)
	x.NotNil(m, "verify does not say what the store witness heard:\n%s", out)
	x.Equal(c.Audit.Checkpoints.Witnesses[1], m[1])
	x.Contains(out, "checkpoint "+m[2]+" compared", "the witness heard another checkpoint than the newest")
	x.NotContains(out, "heard by log:", "a witness that only tells answered")
}
