package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/trail"
)

// TestWhatLeavesTheDatabaseIsInTheFileBeforeItLeaves.
//
// `audit.proto` asks for this in as many words -- *an app with an obligation to
// destroy data has to reckon with the trail, and the answer is a retention
// policy rather than an empty column* -- and until there was one, the answer
// roster gave was forever, arrived at by not deciding.
//
// What the pair has to be is one act. Two commands, or one command with the
// export optional, is a deployment that exports and forgets to delete, or
// deletes without having exported -- and only one of those two is noticed.
func TestWhatLeavesTheDatabaseIsInTheFileBeforeItLeaves(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Some writes, which is what makes trail rows: nothing writes one by hand.
	who := b.holder(t, ctx, b.Acme, "leaver")
	b.identity(t, ctx, who, "github", "gh-leaver")

	was, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(was, "no trail was written, so this proves nothing")

	dir := t.TempDir()

	// Everything, which is what a cutoff in the future means.
	n, err := trail.Archive(ctx, b.Ent, time.Now().Add(time.Hour), dir)
	x.NoError(err)
	x.Equal(len(was), n)

	left, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Zero(left, "the rows are still in the database")

	files, err := trail.Files(dir)
	x.NoError(err)
	x.Len(files, 1, "one month of writes should be one file")
	x.Equal(trail.Named(time.Now()), filepath.Base(files[0]))

	got := map[string]string{}
	x.NoError(trail.Read(files[0], func(v *app.Audit) error {
		got[string(v.GetId())] = v.GetAction()

		return nil
	}))

	x.Len(got, len(was), "the file holds fewer rows than the database gave up")
	for _, v := range was {
		x.Equal(v.Action, got[string(trail.Row(v).GetId())],
			"a row left the database and is not in the file")
	}
}

// TestNothingLeavesTheDatabaseThatCouldNotBeWritten.
//
// The ordering is the whole of the guarantee and it is invisible when it works:
// written, flushed, synced, closed, and only then deleted. A directory that
// cannot be written to is the cheapest way to ask whether the order is really
// that way round, and a run that answers by having deleted the rows anyway is
// one that would have answered the same way on a full disk.
func TestNothingLeavesTheDatabaseThatCouldNotBeWritten(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.holder(t, ctx, b.Acme, "leaver")

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(was)

	// A file where the directory should be, so that creating it fails.
	dir := filepath.Join(t.TempDir(), "archive")
	x.NoError(os.WriteFile(dir, nil, 0o600))

	n, err := trail.Archive(ctx, b.Ent, time.Now().Add(time.Hour), dir)
	x.Error(err)
	x.Zero(n)

	left, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(was, left, "rows were removed although nothing had been written")
}

// TestThePolicyIsAppliedByTheProcessAndNotOnlyByAnOperator.
//
// A retention policy that runs when somebody remembers to run it is not a
// policy. `roster trail prune` is the door for the deployment that would rather
// drive it from cron, and `serve` is the one that needs nothing driving it --
// and this is the half that is easy to leave unwired, because a missing sweep
// looks exactly like a deployment nobody has pruned yet.
//
// One pass, arranged by letting the loop run and then stopping it: `spin.Every`
// does the work before it waits, so what this asserts is the first pass rather
// than a race with a timer.
func TestThePolicyIsAppliedByTheProcessAndNotOnlyByAnOperator(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.holder(t, ctx, b.Acme, "leaver")

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(was)

	dir := t.TempDir()

	// A window of nothing, so that everything already written is past it.
	p := trail.Policy{Retain: time.Nanosecond, Archive: dir, Every: time.Hour}

	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- trail.Sweep(b.Ent, p)(run) }()

	x.Eventually(func() bool {
		n, err := b.Ent.Audit.Query().Count(ctx)

		return err == nil && n == 0
	}, 5*time.Second, 10*time.Millisecond, "the sweep did not apply the window")

	stop()
	x.NoError(<-done)

	files, err := trail.Files(dir)
	x.NoError(err)
	x.Len(files, 1, "the rows left the database and were not written anywhere")
}

// TestADeploymentIsNotAllowedToDestroyEvidenceByLeavingAFieldBlank.
//
// The configuration mistake this refuses does not look like one. `audit.retain`
// with no `audit.archive` runs, the table stops growing, and every graph an
// operator watches improves -- and what it is doing is destroying the trail.
// The day it is discovered is the day somebody asks for a record.
//
// So it is refused where the process comes up rather than where the sweep runs,
// which is the difference between somebody watching and a day later.
func TestADeploymentIsNotAllowedToDestroyEvidenceByLeavingAFieldBlank(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)

	_, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Audit: cmd.AuditConfig{Retain: 90 * 24 * time.Hour},
	})
	x.ErrorContains(err, "nowhere to put")

	t.Run("and says so with the setting that means it", func(t *testing.T) {
		x := require.New(t)

		drv, dsn := pdtest.DB(t)

		s, err := cmd.Build(ctx, cmd.Config{
			Db:    config.DbConfig{Driver: drv, Dsn: dsn},
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
			Audit: cmd.AuditConfig{Retain: 90 * 24 * time.Hour, Discard: true},
		})
		x.NoError(err)
		x.NoError(s.Close())
	})

	t.Run("and a deployment that says nothing keeps everything", func(t *testing.T) {
		x := require.New(t)

		drv, dsn := pdtest.DB(t)

		s, err := cmd.Build(ctx, cmd.Config{
			Db:    config.DbConfig{Driver: drv, Dsn: dsn},
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
		})
		x.NoError(err)
		defer s.Close()

		// No sweep for a trail nobody has set a window on, which is what makes
		// the default forever rather than a number this version happened to
		// pick.
		x.False(cmd.AuditConfig{}.Policy().On())
	})
}
