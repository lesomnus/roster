package cmd_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/audit"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
)

// TestAPassAtTheBoxSaysWhatItDidAndFailsWhenItCouldNot.
//
// payday's pass logs rather than fails, which is right for a loop: a database
// that blinked is tried again a day later. `roster trail prune` runs the same
// pass and built no logger, so a line it logged went nowhere. With the archive
// unwritable it printed the policy and answered exit 0 (#280): a schedule
// that runs it read a pass nothing had come of as one that had worked.
//
// The pass hands back no result, so the command gives it a logger that
// prints: every line on stderr as it is logged, and a line that carries an
// error is one the pass could not do, which the command fails for. Nothing
// leaves the database that could not be written first, as before.
func TestAPassAtTheBoxSaysWhatItDidAndFailsWhenItCouldNot(t *testing.T) {
	x := require.New(t)

	// A file where the directory should be, so that writing into it fails
	// whoever runs this.
	dir := filepath.Join(t.TempDir(), "archive")
	x.NoError(os.WriteFile(dir, nil, 0o600))

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(was)

	said, err := stderrOf(t, cli.Cmd(&c), "trail", "prune")
	x.Error(err, "answered nil for a pass that wrote nothing; right is an error, and an exit that is not 0")
	x.ErrorContains(err, "the pass")
	x.Contains(said, "trail: the retention window", "what the pass could not do is not said")
	x.Contains(said, "err=", "why it could not is not said")

	now, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(was, now, "rows left the database although nothing had been written")

	// And one that could says what it moved, where it said nothing.
	x.NoError(os.Remove(dir))
	x.NoError(os.Mkdir(dir, 0o700))
	said, err = stderrOf(t, cli.Cmd(&c), "trail", "prune")
	x.NoError(err)
	x.Contains(said, "trail: the retention window")
	x.Contains(said, "moved=", "a pass that moved rows does not say how many")
	x.Contains(said, "trail: a checkpoint")

	now, err = b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Zero(now)
}

// TestForgettingAtTheBoxSaysWhenTheArchivesHalfIsStillOwed.
//
// Forgetting somebody blanks the trail's rows about them in the database, in
// its transaction, and pays the archive's half once that has committed. A
// payment that fails is logged and left owed, and the command, which built no
// logger, printed that they were forgotten and answered exit 0 (#280) -- to
// an operator answering an erasure request with a clock on it, while the
// archive still held what its rows said of the person.
//
// The person is forgotten where the transaction reached, and the command
// still says so. It also says what was logged, and fails for a line that
// carries an error: what is left is owed, and `roster trail owed` counts it.
func TestForgettingAtTheBoxSaysWhenTheArchivesHalfIsStillOwed(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "archive")
	x.NoError(os.WriteFile(dir, nil, 0o600))

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = 90 * 24 * time.Hour
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	who := b.holder(t, ctx, b.Contoso, "leaver")
	x.NoError(b.Ent.Holder.UpdateOneId(who.Uuid()).SetDateErased(time.Now()).Exec(ctx))

	said, err := stderrOf(t, cli.Cmd(&c), "forget", who.String())
	x.Error(err, "answered nil though the archive's half was not paid; right is an error")
	x.Contains(said, "forgotten: ", "what the transaction did is still said")
	x.Contains(said, "forget: the archive's half is owed", "what could not be done is not said")
	x.Contains(said, "err=")

	v, err := b.Ent.Holder.Query().Where(holder.IdEQ(who.Uuid())).Only(ctx)
	x.NoError(err)
	x.Empty(v.Alias, "the person is forgotten where the transaction reached")
	x.Contains(stdoutOf(t, cli.Cmd(&c), "trail", "owed"), "erasure(s) owed")
}

// TestAnActOnTheArchiveSaysWhatItLeftAlone.
//
// A purge by hand destroys what the database's account says the archive
// holds, and a chunk the account does not have is left where it is "and said
// so in the log", in the command's own words. At the box there was no log: the
// purge printed what it destroyed and nothing of what it passed over, and a
// check of the rows that left such a chunk out of what it read said only how
// many rows matched (#280). Each says it now, on stderr, as `serve` logs it.
// Neither fails for it: what is found there is `trail verify`'s to fail for.
func TestAnActOnTheArchiveSaysWhatItLeftAlone(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune"}))

	// A chunk beside contoso's own, under its labels and with bytes nobody
	// wrote: what somebody who can write the archive and nothing else leaves.
	a := flob.NewOsStores(dir)
	cs, err := trail.Chunks(ctx, a)
	x.NoError(err)
	i := slices.IndexFunc(cs, func(v trail.Chunk) bool { return v.Namespace == b.Contoso.String() })
	x.GreaterOrEqual(i, 0, "contoso has no chunk of its own")
	info, err := a.Use(cs[i].Namespace).Stat(ctx, cs[i].Digest)
	x.NoError(err)
	l, err := info.Labels(ctx)
	x.NoError(err)

	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	_, err = z.Write([]byte(`{"id":"nobody wrote this"}` + "\n"))
	x.NoError(err)
	x.NoError(z.Close())
	_, err = a.Use(cs[i].Namespace).Add(ctx, flob.Meta{Labels: l}, &buf)
	x.NoError(err)

	said, err := stderrOf(t, cli.Cmd(&c), "trail", "purge", "--before", time.Now().UTC().Format(time.RFC3339Nano))
	x.NoError(err)
	x.Contains(said, "the manifest does not account for, left alone", "a purge does not say what it passed over")

	said, _ = stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.Contains(said, "the manifest does not account for, left out of what a check of the rows reads",
		"a check of the rows does not say what it left out")
}

// TestAnAcceptanceOfRowsSaysWhoAndWhy.
//
// `trail rows --accept` requires a why, because an acceptance nobody can
// explain later is a finding hidden. It then kept the why nowhere: it was
// handed to a logger no command builds, so it was not printed, and it is not
// on the line, which keeps no reason. Nor did anything say who accepted: the
// line named nobody (#280). `trail accept`, beside it, keeps both.
//
// Who is the operator `--as` names, which is how somebody at the box is
// named everywhere else, and the shell they are at. Each line the acceptance
// writes names that operator as its actor, where a line a command at the box
// writes names nobody; and who, why and the digest are said on stderr and
// logged, where the manual had always said they were.
func TestAnAcceptanceOfRowsSaysWhoAndWhy(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	_, err = entities(t, at(c, "admin"), "tenant", "add", "@newco")
	x.NoError(err)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	operator, err := cmd.ControlHolder(ctx, s.Control, "admin")
	x.NoError(err)
	admin, err := cmd.CustomerOf(ctx, s, "newco", "admin")
	x.NoError(err)

	// A row changed in the database past the servers, which is what an
	// acceptance is of.
	changed := func() string {
		t.Helper()

		x.NoError(s.Ent.Holder.UpdateOneId(admin.Uuid()).SetDateDisabled(time.Now()).Exec(ctx))
		said, err := stderrOf(t, cli.Cmd(&c), "trail", "rows")
		x.ErrorContains(err, "1 finding(s)")

		return digestOf(t, said)
	}
	newest := func() (string, string) {
		t.Helper()

		vs, err := s.Ent.Audit.Query().Where(audit.ObjectId(admin.Uuid())).Order(audit.ByDateCreated()).All(ctx)
		x.NoError(err)
		x.NotEmpty(vs)

		return vs[len(vs)-1].Action, vs[len(vs)-1].ActorId.String()
	}

	of := changed()
	why := "the migration of 2026-10-20 set it"

	// A name that is nobody's is refused, as it is for every command that
	// names who is at the shell, and nothing is accepted.
	err = cli.Cmd(at(c, "nobody")).Run(ctx, []string{"trail", "rows", "--accept", "--of", of, "--why", why})
	x.ErrorContains(err, "--as nobody")
	action, _ := newest()
	x.NotEqual("roster.trail.rows.accept", action)

	said, err := stderrOf(t, cli.Cmd(at(c, "admin")), "trail", "rows", "--accept", "--of", of, "--why", why)
	x.NoError(err)
	x.Contains(said, "by admin", "who accepted is not said")
	x.Contains(said, why, "why is not said")
	x.Contains(said, of, "of what is not said")

	action, actor := newest()
	x.Equal("roster.trail.rows.accept", action)
	x.Equal(operator.Uuid().String(), actor, "the line does not name the operator who accepted")
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}), "a line that names its operator is one the check does not take")

	// Not told who, it says the shell, and the line names nobody, as a line
	// a command at the box writes does.
	x.NoError(s.Ent.Holder.UpdateOneId(admin.Uuid()).ClearDateDisabled().Exec(ctx))
	said, err = stderrOf(t, cli.Cmd(&c), "trail", "rows")
	x.ErrorContains(err, "1 finding(s)")
	said, err = stderrOf(t, cli.Cmd(&c), "trail", "rows", "--accept", "--of", digestOf(t, said), "--why", why)
	x.NoError(err)
	x.Regexp(`by \S+`, said)
	x.Contains(said, why)
	_, actor = newest()
	x.Equal("00000000-0000-0000-0000-000000000000", actor)
}

// TestWhatTheTrailOwesSaysWhetherTheHoldIsStillThere.
//
// What is owed keeps the tenants whose holds kept part of it when it was last
// paid. `roster trail owed` read that as *kept by a legal hold of @tenant,
// until it lifts*, and went on saying so after the hold was lifted (#280): to
// an operator asking why an erasure is still owed, the answer named a hold
// that was no longer there. It asks whether the tenant is held now, and says
// which it is.
func TestWhatTheTrailOwesSaysWhetherTheHoldIsStillThere(t *testing.T) {
	x := require.New(t)
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) { c = *cc })

	// One owed, and kept by contoso's hold, as a payment under a hold leaves
	// it.
	x.NoError(forget.Hold(ctx, b.Ungated, b.Contoso, "case 77"))
	store := pd.TrailStore(b.Ent)
	x.NoError(store.Owe(ctx, []pdid.Id{pdid.New(pd.HolderDomain)}))
	vs, err := store.Owed(ctx, trail.Cursor{}, 10)
	x.NoError(err)
	x.Len(vs, 1)
	x.NoError(store.Kept(ctx, vs[0].Key, []pdid.Id{b.Contoso}))

	held := stdoutOf(t, cli.Cmd(&c), "trail", "owed")
	x.Contains(held, "1 kept by a legal hold of @contoso (")
	x.Contains(held, "until it lifts")

	x.NoError(forget.Lift(ctx, b.Ungated, b.Contoso))
	lifted := stdoutOf(t, cli.Cmd(&c), "trail", "owed")
	x.Contains(lifted, "1 erasure(s) owed", "what is owed is still owed")
	x.NotContains(lifted, "until it lifts", "answered that a hold keeps it; right is that the hold is lifted")
	x.Contains(lifted, "@contoso (")
	x.Contains(lifted, "lifted since")
}
