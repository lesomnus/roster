package cmd_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/audit"
	"github.com/lesomnus/roster/server/pd"
)

// both runs a command and answers with what it wrote to stdout and to stderr,
// beside its error: `trail prune` says what it counted on one and where it
// went on the other.
func both(t *testing.T, c *cmd.Config, args ...string) (string, string, error) {
	t.Helper()

	var said string
	var err error
	out := captured(t, func() error {
		said, err = stderrOf(t, cli.Cmd(c), args...)

		return nil
	})

	return out, said, err
}

// TestADryRunOfThePolicysPassChangesNothing.
//
// `roster trail prune --dry-run` with no window of its own answered exit 0 and
// had applied the policy: on `audit: {retain, discard: true}` every line past
// the window was destroyed, by the one flag whose brief is *change nothing*.
// The handler went to the policy's pass before it read the flag (#280).
//
// A dry run destroys nothing on either road. With no window it says what the
// pass would take: the rows past each kind's window and where they would go,
// and the chunks of the archive past theirs. Then the pass itself takes them,
// which is what makes the first answer worth reading.
func TestADryRunOfThePolicysPassChangesNothing(t *testing.T) {
	for name, with := range map[string]func(*testing.T, *cmd.Config) string{
		"of a window that keeps no copy": func(t *testing.T, c *cmd.Config) string {
			c.Audit.Retain = time.Nanosecond
			c.Audit.Discard = true

			return "nowhere"
		},
		"of a window with an archive": func(t *testing.T, c *cmd.Config) string {
			c.Audit.Retain = time.Nanosecond
			c.Audit.Archive = t.TempDir()
			c.Audit.Checkpoints.Key = pdtest.Key(t)

			return "the archive"
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			var c cmd.Config
			where := ""
			b, ctx := build(t, func(cc *cmd.Config) {
				where = with(t, cc)
				c = *cc
			})
			b.holder(t, ctx, b.Contoso, "another")

			lines := func() int {
				n, err := b.Ent.Audit.Query().Count(ctx)
				x.NoError(err)

				return n
			}
			was := lines()
			x.NotZero(was, "no trail was written, so this proves nothing")

			out, _, err := both(t, &c, "trail", "prune", "--dry-run")
			x.NoError(err)
			x.Equal(was, lines(), "a dry run took lines out of the database")
			x.Contains(out, fmt.Sprintf("%d row(s) older than ", was), "a dry run does not say how many the pass would take")
			x.Contains(out, "to "+where, "a dry run does not say where they would go")

			if c.Audit.Archive != "" {
				cs, err := trail.Chunks(ctx, flob.NewOsStores(c.Audit.Archive))
				x.NoError(err)
				x.Empty(cs, "a dry run wrote into the archive")

				k, _, err := pd.TrailStore(b.Ent).Latest(ctx)
				x.NoError(err)
				x.Zero(k, "a dry run took a checkpoint")
			}

			x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune"}))
			x.Zero(lines(), "the pass left what its dry run said it would take")
		})
	}
}

// TestAFlagThePolicysPassDoesNotReadIsRefusedByName.
//
// With no `--older-than` and no `--before`, `prune` is one pass of what
// `audit:` says, per kind. It read none of `--kind`, `--to` and `--discard`
// there and said so of none: `prune --kind holder` applied every kind's
// window, and `prune --to /somewhere` wrote into `audit.archive` (#280).
// Somebody who gave a flag meant something by it, so each is refused by its
// name, with what does read it, and nothing is taken.
func TestAFlagThePolicysPassDoesNotReadIsRefusedByName(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Discard = true
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(was)

	for flag, args := range map[string][]string{
		"--kind":    {"--kind", "holder"},
		"--to":      {"--to", t.TempDir()},
		"--discard": {"--discard"},
	} {
		for _, dry := range [][]string{nil, {"--dry-run"}} {
			err := cli.Cmd(&c).Run(ctx, append(append([]string{"trail", "prune"}, args...), dry...))
			x.ErrorContains(err, flag+": ", "answered nil; right is a refusal that names the flag")
			x.ErrorContains(err, "--older-than or --before", "the refusal does not say what reads the flag")

			now, err := b.Ent.Audit.Query().Count(ctx)
			x.NoError(err)
			x.Equal(was, now, "a refused prune %v took lines out of the database", args)
		}
	}
}

// TestADiscardByHandIsTakenBesideAnArchive.
//
// `prune --older-than … --discard` was refused wherever `audit.archive` is
// set, as "--discard and --to say two different things", about a `--to`
// nobody gave: where the rows go fell back to the configured archive and was
// then compared with `--discard` (#280). So the one command the manual gives
// for removing rows by hand under a legal hold could not be run by a
// deployment that keeps an archive.
//
// What is refused is what was said twice, the two flags together. `--discard`
// alone removes the rows and writes none into the archive, whatever the
// configuration names, and leaves what a hold is on, saying how much.
func TestADiscardByHandIsTakenBesideAnArchive(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = 90 * 24 * time.Hour
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")
	b.holder(t, ctx, b.Fabrikam, "held")

	// A hold on one of the two tenants, as `roster hold place` leaves it.
	x.NoError(b.Ent.Tenant.UpdateOneId(b.Fabrikam.Uuid()).SetDateHeld(time.Now()).SetHold("case 77").Exec(ctx))
	held, err := b.Ent.Audit.Query().Where(audit.TenantId(b.Fabrikam.Uuid())).Count(ctx)
	x.NoError(err)
	x.NotZero(held)
	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Greater(was, held, "every line is the held tenant's, so this proves nothing")

	err = cli.Cmd(&c).Run(ctx, []string{"trail", "prune", "--older-than", "1ns", "--discard", "--to", dir})
	x.ErrorContains(err, "--discard and --to", "the two together say two things")
	now, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(was, now)

	_, said, err := both(t, &c, "trail", "prune", "--older-than", "1ns", "--discard")
	x.NoError(err, "answered a refusal about --to, which nobody gave")
	x.Contains(said, fmt.Sprintf("%d row(s) older than ", was-held))
	x.Contains(said, "to nowhere")
	x.Contains(said, fmt.Sprintf("kept by a legal hold: %d row(s)", held), "what a hold kept is not said")

	now, err = b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(held, now, "what is left is what the hold is on")

	cs, err := trail.Chunks(ctx, flob.NewOsStores(dir))
	x.NoError(err)
	x.Empty(cs, "a discard wrote rows into the archive")
}

// TestAWindowThatEndsInTimeToComeIsRefused.
//
// `--older-than 0` is refused, because a window of nothing is everything. The
// same window written as an instant was taken: `--before` with a moment that
// has not come yet named every row there is, and every row written until
// then (#280). It is refused for the same reason, by `prune` and by `purge`,
// which read the two flags through one function.
func TestAWindowThatEndsInTimeToComeIsRefused(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = t.TempDir()
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	was, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	then := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, args := range [][]string{
		{"trail", "prune", "--before", then, "--dry-run"},
		{"trail", "prune", "--before", then},
		{"trail", "purge", "--before", then, "--dry-run"},
		{"trail", "purge", "--before", then},
	} {
		err := cli.Cmd(&c).Run(ctx, args)
		x.ErrorContains(err, "--before: ", "%v answered nil; right is a refusal", args)
		x.ErrorContains(err, "is everything")
	}

	now, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(was, now)

	// An instant that has passed is a window, as it was.
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune", "--before", time.Now().UTC().Format(time.RFC3339Nano), "--dry-run"}))
}

// TestALineByNobodyIsReadAsNobodys.
//
// A line written with no caller framed -- a sweep's, a command's at the box
// -- names nobody: its actor is the zero identifier. `trail read` printed `?`
// for it, which is what it prints for bytes that are no identifier at all, so
// a reader could not tell *nobody* from *damaged* (#280). Nobody is a dash,
// as a line with no actor column is.
func TestALineByNobodyIsReadAsNobodys(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = t.TempDir()
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune"}))

	out := stdoutOf(t, cli.Cmd(&c), "trail", "read")
	x.NotEmpty(out, "the archive holds no line, so this proves nothing")
	for _, line := range strings.Split(out, "\n") {
		cols := strings.Split(line, "\t")
		x.Len(cols, 4, line)
		x.Equal("-", cols[3], "a line by nobody is not read as nobody's: %s", line)
	}
}
