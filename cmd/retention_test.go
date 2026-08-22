package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
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
	n, err := trail.Archive(ctx, pd.TrailStore(b.Ent), trail.Kinds{}, time.Now().Add(time.Hour), dir)
	x.NoError(err)
	x.Equal(len(was), n)

	left, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Zero(left, "the rows are still in the database")

	files, err := trail.Files(dir)
	x.NoError(err)
	x.True(strings.HasPrefix(filepath.Base(files[0]), "audit-"+trail.Month(time.Now())+"."),
		"the month has to come first in the name, or nothing can be purged by it: %s", files[0])

	got := map[string]string{}
	x.NoError(pd.ReadTrail(files, func(v *app.Audit) error {
		got[string(v.GetId())] = v.GetAction()

		return nil
	}))

	x.Len(got, len(was), "the file holds fewer rows than the database gave up")
	for _, v := range was {
		x.Equal(v.Action, got[string(pdid.Id(v.ID).Bytes())],
			"a row left the database and is not in the file")
	}
}

// TestTwoRunsOverOneMonthDoNotShareAFile.
//
// The first version of the archive was one file per month, appended to, on the
// reasoning that concatenated gzip members are a valid stream. That is true of
// one writer and there is not one writer: `trail.Sweep` takes no lock -- nor
// does the generated outbox drain, whose comment says *nothing here takes a
// lock, so two of these drain the same rows* -- so two replicas, or an operator
// running `roster trail prune` while the process sweeps, write into one file at
// once. A `gzip.Writer` flushes in chunks of its own choosing, so what
// interleaves is not two members but the inside of one, and the month stops
// being readable at all.
//
// Asserted as the property that prevents it rather than by racing two writers:
// a race that happens to lose is a test that happens to pass. What has to be
// true is that no two runs are ever handed the same file.
func TestTwoRunsOverOneMonthDoNotShareAFile(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	dir := t.TempDir()

	b.holder(t, ctx, b.Acme, "first")
	one, err := trail.Archive(ctx, pd.TrailStore(b.Ent), trail.Kinds{}, time.Now().Add(time.Hour), dir)
	x.NoError(err)
	x.NotZero(one)

	b.holder(t, ctx, b.Acme, "second")
	two, err := trail.Archive(ctx, pd.TrailStore(b.Ent), trail.Kinds{}, time.Now().Add(time.Hour), dir)
	x.NoError(err)
	x.NotZero(two)

	files, err := trail.Files(dir)
	x.NoError(err)
	x.NotEmpty(files)

	// The run is the last part of the name before the extension, and no file
	// may carry both.
	runs := map[string]bool{}
	for _, v := range files {
		name := strings.TrimSuffix(filepath.Base(v), trail.Ext)

		vs := strings.Split(name, ".")
		x.Len(vs, 3, "a name is month.kind.run: %s", name)
		x.Equal("audit-"+trail.Month(time.Now()), vs[0])

		runs[vs[2]] = true
	}
	x.Len(runs, 2, "two runs over the same month were handed the same file")

	// And between them they hold every row, once.
	n := 0
	seen := map[string]bool{}
	x.NoError(pd.ReadTrail(files, func(v *app.Audit) error {
		n++
		x.False(seen[string(v.GetId())], "a row was read twice")
		seen[string(v.GetId())] = true

		return nil
	}))
	x.Equal(one+two, n)
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

	n, err := trail.Archive(ctx, pd.TrailStore(b.Ent), trail.Kinds{}, time.Now().Add(time.Hour), dir)
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
	p := trail.Policy{Keep: trail.Keep{Retain: time.Nanosecond}, Archive: dir, Every: time.Hour}

	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- trail.Sweep(pd.TrailStore(b.Ent), p)(run) }()

	x.Eventually(func() bool {
		n, err := b.Ent.Audit.Query().Count(ctx)

		return err == nil && n == 0
	}, 5*time.Second, 10*time.Millisecond, "the sweep did not apply the window")

	stop()
	x.NoError(<-done)

	files, err := trail.Files(dir)
	x.NoError(err)
	x.NotEmpty(files, "the rows left the database and were not written anywhere")
}

// TestTheWindowIsPerKindOfThing.
//
// The first shape of this was one clock over the table, and it is wrong in a
// way that only shows up in an app with more than one kind of entity in it. A
// deployment's obligations are not uniform: what was done to a **person** is
// under a privacy regime and eventually has to stop existing, and what a
// **machine** did is an operating record whose requirement is the opposite one.
// One clock forces the shorter of the two onto everything, and there is no
// global answer honest for both.
//
// roster is nearly all the first kind, which is why this is asserted with
// `holder` on the keeping side: the point is that the policy can say so, not
// which way round roster would set it.
func TestTheWindowIsPerKindOfThing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "somebody")
	b.identity(t, ctx, who, "github", "gh-somebody")

	holder, ok := pdid.DomainOf("holder")
	x.True(ok, "the schema registered no `holder` domain, so this proves nothing")

	kinds := func() map[pdid.Domain]int {
		vs, err := b.Ent.Audit.Query().All(ctx)
		require.NoError(t, err)

		out := map[pdid.Domain]int{}
		for _, v := range vs {
			out[pdid.Domain(v.Domain)]++
		}

		return out
	}

	was := kinds()
	x.NotZero(was[holder], "no write about a person was recorded")
	x.Greater(len(was), 1, "every row is the same kind, so this proves nothing")

	p, err := config.AuditConfig{
		Retain:  time.Nanosecond,
		Discard: true,
		By:      map[string]config.AuditKeepConfig{"holder": {Profile: "forever"}},
	}.Policy()
	x.NoError(err)

	p.Pass(ctx, pd.TrailStore(b.Ent))

	left := kinds()
	x.Equal(was[holder], left[holder], "a kind the policy keeps forever was swept")
	x.Len(left, 1, "a kind the policy has a window for survived it")
}

// TestAKindThisAppDoesNotHaveIsRefused.
//
// A deployment that meant `holder` and wrote `holders` has configured a
// retention policy for nothing at all, and the rows it thought it was
// protecting fall to the default -- which is the failure this whole thing
// exists to make loud. Refused where the process comes up, like every other
// refusal in `config`.
func TestAKindThisAppDoesNotHaveIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := config.AuditConfig{
		Discard: true,
		Retain:  time.Hour,
		By:      map[string]config.AuditKeepConfig{"holders": {Profile: "forever"}},
	}.Policy()
	x.ErrorContains(err, "not a kind this app has")
	x.ErrorContains(err, "holder", "the refusal does not say what the kinds are")
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
		Audit: config.AuditConfig{Retain: 90 * 24 * time.Hour},
	})
	x.ErrorContains(err, "nowhere to put")

	t.Run("and says so with the setting that means it", func(t *testing.T) {
		x := require.New(t)

		drv, dsn := pdtest.DB(t)

		s, err := cmd.Build(ctx, cmd.Config{
			Db:    config.DbConfig{Driver: drv, Dsn: dsn},
			Watch: config.WatchConfig{Broker: config.BrokerMemory},
			Audit: config.AuditConfig{Retain: 90 * 24 * time.Hour, Discard: true},
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
		p, err := config.AuditConfig{}.Policy()
		x.NoError(err)
		x.False(p.On())
	})
}
