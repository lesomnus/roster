package cmd_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	payaudit "github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/pd"
)

// digestKey is an `audit.digest.key` given the way the environment gives one.
var digestKey = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

// started is what `cmd.Build` answers a deployment that says `a` under
// `audit:` and nothing else of note.
func started(t *testing.T, a config.AuditConfig) error {
	t.Helper()

	drv, dsn := pdtest.DB(t)
	s, err := cmd.Build(t.Context(), cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Audit: a,
	})
	if s != nil {
		s.Close()
	}

	return err
}

// TestAnAuditBlockThatCannotBeMeantIsRefusedAtStart.
//
// Each of these started, and each did something other than what it says, with
// nothing anywhere to say so (#280) -- the shape `control:` with no database
// had (#334) and a file that said `allow_general_writes` (#351). What reads
// the block is payday's policy, and the causes are there; a deployment's
// file is roster's to refuse, before anything is opened, in a sentence that
// names the key and says what to write instead.
//
// Two sorts. **Nothing reads it**: the setting is taken and changes nothing,
// so the file believes something the deployment does not do. **Two things
// that cannot both be meant**: each is read, and what comes of the pair is
// what neither says.
func TestAnAuditBlockThatCannotBeMeantIsRefusedAtStart(t *testing.T) {
	const day = 24 * time.Hour

	// A block that keeps an archive, with the key one is refused without.
	archived := func(a config.AuditConfig) config.AuditConfig {
		a.Archive = t.TempDir()
		a.Checkpoints.Key = pdtest.Key(t)

		return a
	}
	by := func(kind string, v config.AuditKeepConfig) map[string]config.AuditKeepConfig {
		return map[string]config.AuditKeepConfig{kind: v}
	}
	inside := func() config.AuditConfig {
		a := archived(config.AuditConfig{Retain: 90 * day})
		a.Checkpoints.Witnesses = []string{"store:" + filepath.Join(a.Archive, "witness")}

		return a
	}

	for name, v := range map[string]struct {
		block config.AuditConfig
		says  []string
	}{
		// Two things that cannot both be meant.
		"a pass that discards beside a digest key, which it carries nothing forward for": {
			config.AuditConfig{Retain: 90 * day, Discard: true, Digest: config.AuditDigestConfig{Key: digestKey}},
			[]string{"audit.discard with audit.digest.key: ", "not-on-trail", "audit.archive", "or take audit.digest.key out"},
		},
		"and one kind's discard beside it": {
			archived(config.AuditConfig{
				Retain: 90 * day,
				By:     by("holder", config.AuditKeepConfig{Retain: 30 * day, Discard: true}),
				Digest: config.AuditDigestConfig{Key: digestKey},
			}),
			[]string{"audit.by.holder.discard with audit.digest.key: ", "audit.by.holder.destroy"},
		},
		"destroy with no retain, which keeps a row in the database for ever": {
			archived(config.AuditConfig{Destroy: 365 * day}),
			[]string{"audit.destroy: ", "cannot both be meant", "write audit.retain"},
		},
		"and one kind's destroy, which does not take the deployment's retain": {
			archived(config.AuditConfig{Retain: 90 * day, By: by("holder", config.AuditKeepConfig{Destroy: 365 * day})}),
			[]string{"audit.by.holder.destroy: ", "write audit.by.holder.retain"},
		},
		"a store witness inside the archive it is to be out of reach of": {
			inside(),
			[]string{"audit.checkpoints.witnesses: store:", "inside audit.archive", "cannot both be meant", "a directory of its own"},
		},

		// Nothing reads it.
		"discard with no retain": {
			config.AuditConfig{Discard: true},
			[]string{"audit.discard: ", "nothing reads", "write audit.retain"},
		},
		"and one kind's discard, which does not take the deployment's retain": {
			config.AuditConfig{Retain: 90 * day, Discard: true, By: by("holder", config.AuditKeepConfig{Discard: true})},
			[]string{"audit.by.holder.discard: ", "write audit.by.holder.retain"},
		},
		"a kind's block that says nothing, which payday reads as for ever": {
			config.AuditConfig{Retain: 90 * day, Discard: true, By: by("holder", config.AuditKeepConfig{})},
			[]string{"audit.by.holder: the block says nothing", "profile: forever", "take the block out"},
		},
		"every with no window": {
			config.AuditConfig{Every: time.Hour},
			[]string{"audit.every: ", "nothing reads", "write a window"},
		},
		"hear_within with no witness": {
			func() config.AuditConfig {
				a := archived(config.AuditConfig{Retain: 90 * day})
				a.Checkpoints.HearWithin = time.Hour

				return a
			}(),
			[]string{"audit.checkpoints.hear_within: ", "nothing reads", "name a witness"},
		},
		"a retain that is less than no time": {
			config.AuditConfig{Retain: -time.Hour},
			[]string{"audit.retain: -1h0m0s", "for ever", "or take it out"},
		},
		"a destroy that is": {
			archived(config.AuditConfig{Retain: 90 * day, Destroy: -time.Hour}),
			[]string{"audit.destroy: -1h0m0s", "or take it out"},
		},
		"an every that is": {
			config.AuditConfig{Retain: 90 * day, Discard: true, Every: -time.Hour},
			[]string{"audit.every: -1h0m0s", "a day", "or take it out"},
		},
		"a kind's retain that is": {
			config.AuditConfig{Retain: 90 * day, Discard: true, By: by("holder", config.AuditKeepConfig{Retain: -time.Hour, Discard: true})},
			[]string{"audit.by.holder.retain: -1h0m0s"},
		},
		"a floor that is": {
			config.AuditConfig{Min: config.AuditMinConfig{Retain: -time.Hour}},
			[]string{"audit.min.retain: -1h0m0s", "no floor"},
		},
		"a hear_within that is": {
			func() config.AuditConfig {
				a := archived(config.AuditConfig{Retain: 90 * day})
				a.Checkpoints.Witnesses = []string{"log:"}
				a.Checkpoints.HearWithin = -time.Hour

				return a
			}(),
			[]string{"audit.checkpoints.hear_within: -1h0m0s", "twice audit.every"},
		},
		"a window for the trail's own table": {
			config.AuditConfig{By: by("audit", config.AuditKeepConfig{Profile: "forever"})},
			[]string{"audit.by.audit: no line of the trail is about", "take the block out"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			err := started(t, v.block)
			x.Error(err, "the deployment started; right is a refusal that names the key")
			for _, say := range v.says {
				x.ErrorContains(err, say)
			}
		})
	}
}

// TestWhatOfAnAuditBlockIsReadAfterAllStillStarts.
//
// The other half of the reading (#280): what looked as if nothing read it,
// and is read. Each is left, and held here so that a refusal added later does
// not take it.
func TestWhatOfAnAuditBlockIsReadAfterAllStillStarts(t *testing.T) {
	const day = 24 * time.Hour

	for name, block := range map[string]config.AuditConfig{
		// `prune --to`, and `--in` of `purge`, `verify` and `accept`, name the
		// archive of a run for a deployment that names none, and such an
		// archive is signed with this key and checkpointed where these say.
		"a checkpoints key with no archive": {Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t)}},
		"a checkpoints directory with none": {Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t), Dir: t.TempDir()}},

		// A floor is what the deployment's own windows are held to where it
		// comes up, whoever answers for a tenant: one below it is refused
		// (below), so one above it is said for a reason.
		"a floor under the window": {Retain: 365 * day, Discard: true, Min: config.AuditMinConfig{Retain: 90 * day}},
		"a floor and no window":    {Min: config.AuditMinConfig{Retain: 90 * day}},

		// The access record keeps no digest, so there is nothing a discard
		// of it fails to carry forward.
		"the access record discarded beside a digest key": {
			Retain: 90 * day, Archive: t.TempDir(), Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t)},
			By:     map[string]config.AuditKeepConfig{"access": {Retain: 30 * day, Discard: true}},
			Digest: config.AuditDigestConfig{Key: digestKey},
		},

		// With no window, `every` is still how long a witness may go without
		// hearing the newest checkpoint of an archive kept by hand, twice
		// over, where `hear_within` does not say.
		"every with no window and a witness to hear": {
			Every: time.Hour, Archive: t.TempDir(),
			Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t), Witnesses: []string{"log:"}},
		},

		// And a kind's content, alone: see the test after this.
		"a kind's content and no window": {
			Retain: 90 * day, Discard: true,
			By: map[string]config.AuditKeepConfig{"holder": {Content: "fields"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, started(t, block))
		})
	}

	t.Run("and a window under its floor is refused, as it was", func(t *testing.T) {
		err := started(t, config.AuditConfig{Retain: 90 * day, Discard: true, Min: config.AuditMinConfig{Retain: 365 * day}})
		require.ErrorContains(t, err, "the floor is")
	})
}

// TestWhatAKindsBlockMaySayFollowsFromWhatTheTrailHoldsOfIt.
//
// Two of the refusals are about kinds, and neither is a list somebody keeps
// in step by hand. The schema registers the trail's own tables as kinds --
// the trail, the outbox, the archive's account, the erasures owed, a row's
// seal -- and a window for one of those is read by nothing, because no line
// of the trail is about a row of them. And a kind's discard is refused beside
// a digest key only where its lines keep a digest, which the access record's
// do not.
//
// What says which is which is the generated recorder: `pd.Record` answers
// *no such row* of a kind the trail holds lines about, and passes over one it
// does not.
func TestWhatAKindsBlockMaySayFollowsFromWhatTheTrailHoldsOfIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const day = 24 * time.Hour
	names := pdid.Domains()
	x.Greater(len(names), 20, "the schema registered too few kinds to mean anything")

	refused := []string{}
	for _, d := range slices.Sorted(maps.Keys(names)) {
		name := names[d]

		_, err := pd.Record(ctx, b.Ent, payaudit.Contents{}, "roster.test", []pdid.Id{pdid.New(d)})
		lines := err != nil

		window := cmd.AuditMeant(config.AuditConfig{By: map[string]config.AuditKeepConfig{name: {Profile: "forever"}}})
		discard := cmd.AuditMeant(config.AuditConfig{
			By:     map[string]config.AuditKeepConfig{name: {Retain: 30 * day, Discard: true}},
			Digest: config.AuditDigestConfig{Key: digestKey},
		})

		switch {
		case lines:
			x.NoError(window, "%s: the trail holds lines about it, and a window for it was refused", name)
			x.ErrorContains(discard, "audit.by."+name+".discard with audit.digest.key", "%s: its lines keep a digest, and its discard beside a key was taken", name)
		case name == "access":
			// The access record: no line of the trail, and rows of its own
			// that the passes keep on a window of their own.
			x.NoError(window)
			x.NoError(discard)
		default:
			x.ErrorContains(window, "audit.by."+name+": no line of the trail is about", "%s: nothing is written about it, and a window for it was taken", name)
			refused = append(refused, name)
		}
	}

	x.Equal([]string{"archived", "audit", "outbox", "owed", "row-seal"}, slices.Sorted(slices.Values(refused)),
		"the trail's own tables are not the ones this was written against")
}

// TestAKindsContentAloneKeepsTheDeploymentsWindow.
//
// `audit.by.<kind>.content` says how much of a write to that kind a line
// keeps, which payday's own comment calls best said by kind. A block that
// said that and no window was read as a window of *for ever*: on `audit:
// {retain: 2s, archive, by: {holder: {content: fields}}}` the policy said
// `holder: forever`, and a pass moved everything but the lines about people
// (#280). A block that says a floor beside its content already kept the
// deployment's window, so the same block with one line taken out kept another.
//
// It means what it says: that kind's content, and the window everything else
// has. roster reads the policy with such a kind left to the deployment's
// (`cmd.AuditPolicy`), which is how the control plane's trail has always been
// handed its contents.
func TestAKindsContentAloneKeepsTheDeploymentsWindow(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = t.TempDir()
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		cc.Audit.By = map[string]config.AuditKeepConfig{"holder": {Content: "fields"}}
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")

	// The content is the kind's: a line about a person keeps which fields
	// were written and not what they were set to.
	vs, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	people := 0
	for _, v := range vs {
		if pdid.Domain(v.Domain) != pd.HolderDomain {
			continue
		}
		people++
		x.Empty(v.Patch, "a line about a person keeps what was written, at `content: fields`")
		x.NotEmpty(v.Fields)
	}
	x.NotZero(people, "no line is about a person, so this proves nothing")

	p, err := cmd.AuditPolicy(c.Audit)
	x.NoError(err)
	x.Equal(p.Keep, p.For(pd.HolderDomain), "a kind that says only its content has a window of its own")
	x.NotContains(p.String(), "holder: forever")

	// And the window is the deployment's: the pass moves the lines about
	// people with the rest.
	said, err := stderrOf(t, cli.Cmd(&c), "trail", "prune")
	x.NoError(err)
	x.NotContains(said, "holder: forever")

	left, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Zero(left, "the lines about people stayed where the rest left")
}

// auditOf reads a file the way the binary reads one, over what `c` already
// says, and answers the configuration that comes of it.
func auditOf(t *testing.T, c cmd.Config, file string) cmd.Config {
	t.Helper()

	k := cli.Cmd(&c)
	k.Writer = io.Discard
	require.NoError(t, k.Run(t.Context(), []string{"--config", file, "config"}), "%s was not read", file)

	return c
}

// starts holds one `audit:` block to both halves of what a start asks of it:
// roster's refusals, of the block as it is written, and then payday's, with a
// directory and a key that are there in place of each one it names.
func starts(t *testing.T, where string, a config.AuditConfig) {
	t.Helper()
	x := require.New(t)

	x.NoError(cmd.AuditMeant(a), "%s: refused as it is written", where)

	if a.Archive != "" {
		a.Archive = t.TempDir()
	}
	if a.Checkpoints.Key != "" {
		a.Checkpoints.Key = pdtest.Key(t)
	}
	if a.Checkpoints.Dir != "" {
		a.Checkpoints.Dir = t.TempDir()
	}
	for i, w := range a.Checkpoints.Witnesses {
		if strings.HasPrefix(w, "store:") {
			a.Checkpoints.Witnesses[i] = "store:" + t.TempDir()
		}
	}
	if a.Digest.Key != "" {
		a.Digest.Key = digestKey
	}

	_, err := cmd.AuditPolicy(a)
	x.NoError(err, "%s: refused once its directories and its key are there", where)
}

// TestEveryAuditBlockThePagesShowStarts.
//
// What is refused at start is refused of a file somebody wrote from these
// pages, so every `audit:` block they show is held to the check, and so is
// every file and environment this repository ships that a deployment starts
// from. A block that names no archive is a part, shown beside the whole it
// goes into -- the manual's first -- and is read laid over that one, which is
// how the page reads it.
func TestEveryAuditBlockThePagesShowStarts(t *testing.T) {
	x := require.New(t)
	root := filepath.Join("..")

	// The blocks the pages show: every `yaml` fence that says `audit:`.
	type shown struct {
		where string
		file  string
	}
	blocks := []shown{}
	pages, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	x.NoError(err)
	more, err := filepath.Glob(filepath.Join(root, "docs", "*", "*.md"))
	x.NoError(err)
	for _, page := range append(pages, more...) {
		src, err := os.ReadFile(page)
		x.NoError(err)

		for _, m := range regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatchIndex(string(src), -1) {
			body := string(src[m[2]:m[3]])
			if !regexp.MustCompile(`(?m)^audit:`).MatchString(body) {
				continue
			}

			line := 1 + strings.Count(string(src[:m[2]]), "\n")
			file := filepath.Join(t.TempDir(), "roster.yaml")
			x.NoError(os.WriteFile(file, []byte(body), 0o600))
			blocks = append(blocks, shown{where: page + ":" + strconv.Itoa(line), file: file})
		}
	}
	x.GreaterOrEqual(len(blocks), 3, "the pages show fewer `audit:` blocks than the manual has")

	// And the one `roster.yaml` shows in a comment, as somebody takes the
	// comment marks off it.
	src, err := os.ReadFile(filepath.Join(root, "roster.yaml"))
	x.NoError(err)
	if m := regexp.MustCompile(`(?m)^#   audit:\n(?:#     .*\n)+`).Find(src); m != nil {
		file := filepath.Join(t.TempDir(), "roster.yaml")
		x.NoError(os.WriteFile(file, regexp.MustCompile(`(?m)^#   `).ReplaceAll(m, nil), 0o600))
		blocks = append(blocks, shown{where: "roster.yaml, in its comment", file: file})
	} else {
		x.Fail("roster.yaml shows no `audit:` block in its comment any more; take this out, or find it again")
	}

	var whole *cmd.Config
	for _, v := range blocks {
		c := auditOf(t, cmd.Config{}, v.file)
		if c.Audit.Archive == "" {
			x.NotNil(whole, "%s: a part, and no whole block before it to be laid over", v.where)
			x.NoError(cmd.AuditMeant(c.Audit), "%s: refused as it is written", v.where)
			c = auditOf(t, *whole, v.file)
		} else if whole == nil {
			whole = &c
		}

		starts(t, v.where, c.Audit)
	}

	// The files a deployment starts from, read whole.
	for _, f := range []string{"roster.yaml", filepath.Join("deploy", "config.yaml")} {
		starts(t, f, auditOf(t, cmd.Config{}, filepath.Join(root, f)).Audit)
	}

	// And the two that say it through the environment: the rig's, and the
	// component a deployment turns on.
	empty := filepath.Join(t.TempDir(), "roster.yaml")
	x.NoError(os.WriteFile(empty, nil, 0o600))
	for _, f := range []string{"compose.yaml", filepath.Join("deploy", "trail", "roster.yaml")} {
		t.Run(f, func(t *testing.T) {
			x := require.New(t)

			src, err := os.ReadFile(filepath.Join(root, f))
			x.NoError(err)

			said := 0
			for _, m := range regexp.MustCompile(`(ROSTER_AUDIT_[A-Z_]+)(?:: |, value: )(.+?)(?: \})?\n`).FindAllStringSubmatch(string(src), -1) {
				t.Setenv(m[1], strings.Trim(strings.TrimSpace(m[2]), `'"`))
				said++
			}
			x.NotZero(said, "%s says nothing of `audit:` any more", f)

			c := auditOf(t, cmd.Config{}, empty)
			x.NotEmpty(c.Audit.Archive, "%s: what it says of `audit:` was not read", f)
			starts(t, f, c.Audit)
		})
	}
}
