package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
)

// stillSaying is how many of the trail's lines about these rows still say
// something: a `patch` or a `value`, which is what a forget takes from them.
func stillSaying(t *testing.T, ctx context.Context, db *ent.Client, about ...pdid.Id) int {
	t.Helper()

	n := 0
	for _, k := range about {
		vs, err := db.Audit.Query().Where(entaudit.ObjectIdEQ(k.Uuid())).All(ctx)
		require.NoError(t, err)
		for _, v := range vs {
			if len(v.Patch) > 0 || len(v.Value) > 0 {
				n++
			}
		}
	}

	return n
}

// keyOf is a key of somebody's with a name and a description on it, which is
// what the trail's line about one says, and its identifier.
func keyOf(t *testing.T, ctx context.Context, s app.Server, who pdid.Id, alias, desc, secret string) pdid.Id {
	t.Helper()

	v, err := s.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:  alias, Desc: desc, Methods: []string{getMe}, Secret: []byte(secret),
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// TestWhatTheTrailOwesIsPaidWhereItIsKeptForEver: somebody forgotten under a
// hold keeps the trail's lines about them until the hold lifts, and the
// erasure is owed until then. What pays a debt afterwards was the trail's
// pass alone, and `serve` spins that only where the policy names a retention
// window. Keeping the trail for ever is the default, so in a default
// deployment nothing ever paid: with the hold lifted, the lines about the key
// that went with the person went on saying its name and whose laptop it was,
// for good, and the manual said nobody runs anything again. Right is that
// they say nothing once a loop of the deployment has run.
//
// So a deployment whose trail has no pass spins one that pays, and nothing
// else of a pass: the trail of everybody else is as it was, since for ever is
// still what that deployment keeps it for.
func TestWhatTheTrailOwesIsPaidWhereItIsKeptForEver(t *testing.T) {
	x := require.New(t)

	// No `audit:` block at all, which is the trail kept for ever.
	var cfg cmd.Config
	b, ctx := build(t, func(c *cmd.Config) { cfg = *c })

	erin := b.holder(t, ctx, b.Contoso, "erin")
	b.addressOf(t, ctx, erin, "erin@contoso.example")
	hers := keyOf(t, ctx, b.Ungated, erin, "erins-laptop", "Erin Example's laptop", "0123456789abcdef0123456789abcdef")
	theirs := keyOf(t, ctx, b.Ungated, b.ContosoUser, "somebodys-laptop", "somebody else's", "abcdef0123456789abcdef0123456789")

	p, err := cmd.TrailPolicy(ctx, cfg)
	x.NoError(err)
	x.False(p.On(), "this deployment names a retention window, so it proves nothing about one that names none")
	p = forget.Held(p, b.Ent)
	cs, err := cfg.Audit.Contents()
	x.NoError(err)

	owed := func() int {
		t.Helper()
		v, err := p.Owing(ctx, pd.TrailStore(b.Ent))
		x.NoError(err)

		return v.Objects
	}

	// Held, forgotten by name, and the hold lifted.
	x.NoError(forget.Hold(ctx, b.Ungated, b.Contoso, "Contoso v. Fabrikam, 2026-123"))
	res, err := forget.Forget(ctx, b.Ent, erin, p, cs, b.Queue)
	x.NoError(err)
	x.True(res.Held.Any(), "the hold kept nothing of the trail, so nothing is owed and this proves nothing")
	x.NotZero(stillSaying(t, ctx, b.Ent, hers), "the trail says nothing of their key under the hold, so this proves nothing")
	x.NotZero(owed())
	x.NoError(forget.Lift(ctx, b.Ungated, b.Contoso))

	lines, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	others := stillSaying(t, ctx, b.Ent, theirs)
	x.NotZero(others, "the trail says nothing of anybody else's key, so nothing is proved about what is kept")

	// What this deployment spins that pays: the trail's pass where there is
	// one, and where there is none, the loop that stands in for it.
	var pays spin.Spinner
	for _, v := range b.Spin {
		if n, ok := v.(spin.Named); ok && (n.SpinName() == "roster.trail" || n.SpinName() == "roster.owed") {
			pays, _ = v.(spin.Spinner)
		}
	}
	x.NotNil(pays, "nothing this deployment spins pays what the trail owes: it spins %v, and a hold that lifts is followed by nothing", loops(b.Spin))

	// One pass runs at once; it is waited for by what it does, and then the
	// loop is stopped.
	run, stop := context.WithCancel(ctx)
	over := make(chan struct{})
	go func() { defer close(over); _ = pays.Spin(run) }()
	x.Eventually(func() bool { return owed() == 0 }, 30*time.Second, 10*time.Millisecond,
		"the hold lifted and what was owed under it was not paid")
	stop()
	<-over

	x.Zero(stillSaying(t, ctx, b.Ent, erin, hers), "the trail still says what it said of somebody forgotten, and of the key that went with them")

	// And nothing else went: the trail is kept for ever here.
	after, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(lines, after, "paying what was owed took lines of the trail")
	x.Equal(others, stillSaying(t, ctx, b.Ent, theirs), "paying what was owed blanked somebody else's lines")
}

// TestRosterForgetPaysWhatTheTrailOwes: `roster forget` with no name is what
// an operator runs for everything a clock is done waiting for, and it did
// nothing about an erasure owed: after a hold lifted, it printed that nobody
// was due and left the lines as they were, `roster trail prune` refused for
// want of a window, and `roster forget <identifier>` again paid the person's
// own line and not their key's. It pays what is owed, and says what it paid;
// `--dry-run` says what is owed and pays nothing.
func TestRosterForgetPaysWhatTheTrailOwes(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, getMe)
	ctx := t.Context()
	bob := mustId(t, b.Bob.GetId())

	his := keyOf(t, ctx, b.Server.Ungated, bob, "bobs-laptop", "Bob Example's laptop", "0123456789abcdef0123456789abcdef")

	owed := func() int {
		t.Helper()
		v, err := forget.Held(trail.Policy{}, b.Server.Ent).Owing(ctx, pd.TrailStore(b.Server.Ent))
		x.NoError(err)

		return v.Objects
	}

	_, err := stderrOf(t, cli.NewCmdHold(&b.Local), "place", "--why", "Newco v. Somebody, 2026-123", "@newco")
	x.NoError(err)
	_, err = stderrOf(t, cli.NewCmdForget(&b.Local), "@newco/bob")
	x.NoError(err)
	x.NotZero(owed(), "nothing is owed under the hold, so this proves nothing")
	x.NotZero(stillSaying(t, ctx, b.Server.Ent, his))
	_, err = stderrOf(t, cli.NewCmdHold(&b.Local), "lift", "@newco")
	x.NoError(err)

	dry, err := stderrOf(t, cli.NewCmdForget(&b.Local), "--dry-run")
	x.NoError(err)
	x.NotZero(owed(), "--dry-run paid")

	out, err := stderrOf(t, cli.NewCmdForget(&b.Local))
	x.NoError(err)
	x.Zero(owed(), "`roster forget` left what the trail owes: %s", out)
	x.Zero(stillSaying(t, ctx, b.Server.Ent, bob, his), "the trail still says what it said of somebody forgotten, and of the key that went with them")

	x.Contains(dry, "owed", "what is owed is not said before it is paid: %s", dry)
	x.Contains(out, "owed", "what was paid is not said: %s", out)
}
