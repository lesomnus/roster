package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"log/slog"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
)

// NewCmdTrail is `roster trail`: what happens to the record of what happened,
// after long enough.
//
// # Why it is a command and not an RPC
//
// The same reason `roster key` is one, and a stronger version of it. The layer
// in front of `AuditService` refuses every write -- *"the trail is written by
// what happened, not by anybody asking"* -- and a retention RPC beside it would
// be the exception that makes the sentence false. What a trail is worth is that
// the credential which lets somebody act is not the credential that lets them
// erase the record of having acted, and an API key that prunes is a stolen key
// that prunes.
//
// So this needs the database, which is the boundary that was being asked for.
// `serve` applies the same policy on a clock; see `audit:` in the
// configuration, and payday's `trail` package for what the two clocks are.
//
// # And what it is not
//
// It is not `roster audit`, which is the generated entity command and reads
// rows through a server. These are the acts no server offers.
func NewCmdTrail(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "trail",
		Brief: "what happens to the record of what happened, after long enough",

		Commands: xli.Commands{
			newCmdTrailPrune(c),
			newCmdTrailRead(l, c),
			newCmdTrailPurge(c),
			newCmdTrailVerify(l, c),
			newCmdTrailRows(c),
			newCmdTrailAccept(c),
			newCmdTrailOwed(c),
			newCmdTrailProfiles(),
		},

		Handler: xli.RequireSubcommand(),
	}
}

// newCmdTrailPrune moves rows out of the database, and is the one act here
// that cannot be undone by running it again.
func newCmdTrailPrune(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "prune",
		Brief: "apply the retention policy now, or a window of your own",

		Flags: flg.Flags{
			&flg.String{Name: "older-than", Brief: "how old a row has to be, e.g. 2160h for ninety days"},
			&flg.String{Name: "before", Brief: "an instant instead, RFC 3339"},
			&flg.String{Name: "kind", Brief: "with a window of your own, only rows about this kind of thing, e.g. holder; every kind by default"},
			&flg.String{Name: "to", Brief: "with a window of your own, the directory to write into; audit.archive by default"},
			&flg.Switch{Name: "discard", Brief: "with a window of your own, remove them and keep no copy"},
			&flg.Switch{Name: "dry-run", Brief: "say how many there are and where they would go, and change nothing"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			before, given, err := cutoff(cl)
			if err != nil {
				return err
			}

			// Every flag is read before either road is taken. `--dry-run` was
			// read on one of them only, so with no window it was passed over
			// and the policy applied (#280): the flag whose whole meaning is
			// *change nothing* destroyed what was past the window.
			dry, _ := flg.Find[bool](cl, "dry-run")
			kind, _ := flg.Find[string](cl, "kind")
			to, _ := flg.Find[string](cl, "to")
			discard, _ := flg.Find[bool](cl, "discard")

			if !given {
				// No window of their own, so the deployment's: one pass of what
				// `audit:` says, per kind, exactly as the sweep does it.
				//
				// This is the default rather than a refusal because it is what
				// somebody putting this in cron means, and because the
				// alternative was worse than inconvenient: a command that only
				// took a cutoff destroyed the kind the configuration said to
				// keep forever.
				//
				// Which kinds, where to and whether a copy is kept are the
				// configuration's on this road, so a flag that says one of them
				// is refused by its name. Passed over, `--kind holder` applied
				// every kind's window and `--to` wrote somewhere else than it
				// named, and somebody who gave a flag meant something by it.
				for _, f := range []struct {
					name  string
					given bool
				}{
					{"--kind", kind != ""},
					{"--to", to != ""},
					{"--discard", discard},
				} {
					if f.given {
						return fmt.Errorf("%s: only a window of your own reads it, and --older-than or --before names none. "+
							"With no window this is one pass of what `audit:` says, of every kind, to where it says; "+
							"name a window, or leave %s out", f.name, f.name)
					}
				}

				return prune(ctx, *c, dry)
			}

			of, err := kindOf(cl)
			if err != nil {
				return err
			}

			// What was said twice: a directory to write the rows into, and that
			// no copy is kept. Compared before the configured archive is read
			// for a `--to` nobody gave -- compared after, `--discard` was
			// refused in a sentence about `--to` wherever `audit.archive` is
			// set (#280), which is every deployment the manual describes a
			// discard under a hold for. A dry run is refused too: what it
			// counts is what the same line without `--dry-run` would take.
			if to != "" && discard {
				return errors.New("--discard and --to say two different things about the same rows: " +
					"one keeps no copy and the other says where the copy goes; give one")
			}

			// Read here and not bound to `audit.archive` as `--in` is: bound,
			// it would also move the archive of the policy a run with no window
			// applies -- the sweep's, exactly -- and of the one `cmd.Build`
			// checks.
			dir := to
			if dir == "" {
				dir = c.Audit.Archive
			} else if c.Audit.Archive != "" && !sameDir(dir, c.Audit.Archive) {
				// The database keeps one account of one archive, and every
				// check reads that one (`trail verify`, every pass). Rows
				// written into a second are rows the first one is found to be
				// missing.
				return fmt.Errorf("--to: %s is not audit.archive (%s), and the database keeps an account of one archive; "+
					"leave --to out, or name that one", dir, c.Audit.Archive)
			}

			// Where the rows go is a question about **destroying** them, so it
			// is asked of a run that will. A dry run that insisted on a
			// destination was refusing to count on the grounds that it had not
			// been told where to put what it was not going to move -- and it
			// made the only safe way to ask *how many* into the one form that
			// needs the dangerous flags filled in first.
			if !dry && dir == "" && !discard {
				// The same refusal `trail.Policy.Valid` makes about the
				// configuration, at the other door. Somebody who has not
				// said where the rows go has not said they may be
				// destroyed.
				return errors.New("--to: nowhere to put what leaves the database; " +
					"name a directory, set audit.archive, or --discard to say the rows are meant to go")
			}

			s, err := built(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// What the act logs is said, on stderr: the checkpoint it ends in,
			// a witness that did not hear it. See [aloud].
			ctx, _ = aloud(ctx)

			store := pd.TrailStore(s.Ent)

			if dry {
				n, err := store.Count(ctx, trail.Scope{Kinds: of}, before)
				if err != nil {
					return err
				}

				// And where they would go, when the line says: the flags a
				// dry run is given are the ones the run after it is given.
				where := ""
				switch {
				case discard:
					where = ", to nowhere but for what a legal hold is on"
				case dir != "":
					where = ", to " + dir
				}
				fmt.Fprintf(os.Stdout, "%d row(s) older than %s%s\n", n, before.UTC().Format(time.RFC3339), where)

				return nil
			}

			// The deployment's policy even for a window of the operator's own:
			// it is what knows whose trail a legal hold is on, and a removal by
			// hand is not an exception to one -- nor to an operator's hold on a
			// tenant, which roster answers it with.
			//
			// And its archive, which is where it writes: `--to` is that
			// archive, or the one for this run where the configuration names
			// none. A discard beside a configured archive keeps the policy's
			// own, where its receipt and its checkpoint go, and writes no row
			// into it.
			ac := c.Audit
			if !discard {
				ac.Archive = dir
			}
			p, err := cmd.AuditPolicy(ac)
			if err != nil {
				return err
			}
			p = forget.Held(p, s.Ent)

			n := 0
			held := trail.Held{}
			if discard {
				n, held, err = p.Collect(ctx, store, of, before)
			} else {
				// Through the policy (payday#59): sealed with its keys, the
				// manifest's rows signed with its key, and a checkpoint at the
				// end, as a pass writes.
				n, err = trail.Archive(ctx, store, p, of, before)
			}
			if err != nil {
				// With the count, because a run that moved most of the table
				// and then failed has still moved it -- and the operator's next
				// question is whether to start over.
				return fmt.Errorf("after %d row(s): %w", n, err)
			}

			where := dir
			if discard {
				where = "nowhere"
			}

			fmt.Fprintf(os.Stderr, "%d row(s) older than %s, to %s\n",
				n, before.UTC().Format(time.RFC3339), where)
			kept(held)

			return nil
		}),
	}
}

// newCmdTrailRead is the archive read back, and it opens no database.
//
// Deliberately: the reason to keep the archive is that it outlives the
// deployment that wrote it, so a reader that needed the deployment would be
// answering a question nobody has at the moment they have it. Which is also
// why it checks nothing: what says the archive is what was written is the
// database's account of it, and `trail verify` is the reader that has both.
func newCmdTrailRead(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "read",
		Brief: "read an archive back, without a database",

		Args: arg.Args{
			&arg.RestStrings{Name: "FILE", Brief: "files to read -- a chunk taken out of the archive; the archive at --in or audit.archive by default"},
		},

		Flags: flg.Flags{
			cfg.Bind(l, &c.Audit.Archive, &flg.String{Name: "in", Brief: "a directory of archives; audit.archive by default"}),
			&flg.String{Name: "object", Brief: "only what happened to this identifier"},
			&flg.String{Name: "actor", Brief: "only what this person did"},
			&flg.String{Name: "tenant", Brief: "only this tenant, on any of the three columns"},
			&flg.String{Name: "action", Brief: "only actions containing this"},
			&flg.String{Name: "since", Brief: "no earlier than, RFC 3339"},
			&flg.String{Name: "until", Brief: "no later than, RFC 3339"},
			&flg.Switch{Name: "json", Brief: "the rows as they are stored, one per line"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			paths, err := archives(cl, c)
			if err != nil {
				return err
			}
			if len(paths) == 0 && c.Audit.Archive == "" {
				return errors.New("name a file, or --in a directory, or set audit.archive")
			}

			keep, err := filterOf(cl)
			if err != nil {
				return err
			}

			asJson, _ := flg.Find[bool](cl, "json")

			show := func(v *app.Audit) error {
				if !keep(v) {
					return nil
				}
				if asJson {
					b, err := protojson.Marshal(v)
					if err != nil {
						return err
					}

					fmt.Fprintf(os.Stdout, "%s\n", b)

					return nil
				}

				fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n",
					v.GetDateCreated().AsTime().UTC().Format(time.RFC3339),
					v.GetAction(),
					named(v.GetObjectId()),
					named(v.GetActorId()))

				return nil
			}

			// Files named on the command line are files: a chunk somebody took
			// out of the archive.
			if len(paths) > 0 {
				return trail.ReadFiles(paths, pd.TrailOf(show))
			}

			// One call over the whole archive rather than one per chunk,
			// because the duplicate two writers leave behind is only visible
			// to a reader that has seen both. See [trail.Read].
			return trail.Read(ctx, flob.NewOsStores(c.Audit.Archive), pd.TrailOf(show))
		}),
	}
}

// newCmdTrailPurge is the end of the line.
//
// By chunk and not by row, which is what the archive's layout is for: a chunk
// says when the newest row in it was written, so one is destroyable when that
// row is past the cutoff. Rewriting a chunk to drop some of its rows would be
// editing an archive, which only a legal hold on part of a file from before is
// a reason for -- and that is payday's to do, not a flag here.
func newCmdTrailPurge(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "purge",
		Brief: "destroy the archives that are old enough, and there is nothing after this",

		Flags: flg.Flags{
			&flg.String{Name: "older-than", Brief: "how old the archive has to be, e.g. 61320h for seven years"},
			&flg.String{Name: "before", Brief: "an instant instead, RFC 3339"},
			&flg.String{Name: "kind", Brief: "only chunks of this kind of thing; every kind by default"},
			&flg.String{Name: "in", Brief: "the directory to destroy from, which is audit.archive when that is set"},
			&flg.Switch{Name: "dry-run", Brief: "say which chunks and remove nothing"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			before, given, err := cutoff(cl)
			if err != nil {
				return err
			}
			if !given {
				// The archive half of the policy is applied by the same pass
				// `prune` runs, so there is nothing for a bare `purge` to mean
				// that is not already covered -- and guessing would be guessing
				// about the one act with nothing after it.
				return errors.New("--older-than or --before: how old is old enough; " +
					"`trail prune` applies the deployment's own policy, this destroys archives by hand")
			}

			of, err := kindOf(cl)
			if err != nil {
				return err
			}

			c, err := inArchive(cl, c)
			if err != nil {
				return err
			}
			if c.Audit.Archive == "" {
				return errors.New("--in: which directory")
			}

			// The deployment's policy, for its archive and for its holds: a
			// purge by hand leaves what a hold is on, and says how much. And
			// in the trail's turn, which a pass of the running deployment
			// takes too.
			p, err := cmd.TrailPolicy(ctx, *c)
			if err != nil {
				return err
			}

			// And the database, which keeps the account of the archive a
			// purge decides by: a chunk it does not account for is left
			// alone, and said so in the log -- it is what `trail verify` is
			// for. The dry run asks it too, so that it is the same list.
			s, err := built(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			p = forget.Held(p, s.Ent)

			// The log that says so, which is stderr here ([aloud]): with no
			// logger a purge said what it destroyed and nothing of what it
			// passed over.
			ctx, _ = aloud(ctx)

			store := pd.TrailStore(s.Ent)
			cut := of.CutFor(before)

			if dry, _ := flg.Find[bool](cl, "dry-run"); dry {
				vs, held, err := p.Doomed(ctx, store, cut)
				if err != nil {
					return err
				}
				for _, v := range vs {
					fmt.Fprintf(os.Stdout, "%s\n", v)
				}
				kept(held)

				return nil
			}

			vs, held, err := p.Purge(ctx, store, cut)
			if err != nil {
				return fmt.Errorf("after %d chunk(s): %w", len(vs), err)
			}
			for _, v := range vs {
				fmt.Fprintf(os.Stdout, "%s\n", v)
			}
			kept(held)

			return nil
		}),
	}
}

// newCmdTrailVerify compares the archive with the account the database keeps
// of it, and changes nothing.
//
// It is what every pass does lightly at its start -- the archive's chunks by
// name and labels against the manifest, and the latest checkpoint against the
// rows it was taken over -- and `--full` also reads every chunk whole and
// checks its bytes. It fails when it found something, so it is the one to put
// on a schedule. Unlike `read` it needs the database: the account is in it,
// and the account is what is being compared.
//
// It prints the digest of what it found, which is what `accept --of` takes
// back, and the latest acceptances the checkpoints keep (payday#115).
func newCmdTrailVerify(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "verify",
		Brief: "compare the archive with the account the database keeps of it, and change nothing",

		Flags: flg.Flags{
			cfg.Bind(l, &c.Audit.Archive, &flg.String{Name: "in", Brief: "the archive's directory; audit.archive by default"}),
			&flg.Switch{Name: "full", Brief: "also read every chunk whole and check its bytes"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			s, p, err := archived(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			// A witness that does not answer is said in the log and is in
			// none of what is printed below; the log is stderr here ([aloud]).
			ctx, _ = aloud(ctx)

			full, _ := flg.Find[bool](cl, "full")
			v, err := p.Verify(ctx, pd.TrailStore(s.Ent), full)
			if err != nil {
				return err
			}

			read := fmt.Sprintf("%d row(s) in the manifest, %d blob(s) in the archive", v.Rows, v.Blobs)
			if full {
				read += fmt.Sprintf(", %d read whole", v.Hashed)
			}
			if v.Checkpoint > 0 {
				read += fmt.Sprintf("; checkpoint %d compared", v.Checkpoint)
			}
			fmt.Fprintf(os.Stderr, "%s\n", read)

			// What an acceptance of these findings names, and what was
			// accepted before: who, why, and of what.
			fmt.Fprintf(os.Stderr, "digest %s\n", v.Digest)
			for _, a := range v.Accepted {
				fmt.Fprintf(os.Stderr, "accepted %s\n", acceptance(a))
			}
			// And what each witness that answers heard last, which is what
			// the chain was just held to (payday#115): beside the findings,
			// so that a clean run still says how far outside the database
			// the newest checkpoint is known.
			for _, h := range heard(v.Heard) {
				fmt.Fprintf(os.Stderr, "%s\n", h)
			}

			for _, f := range v.Findings {
				fmt.Fprintf(os.Stdout, "%s\n", f)
			}
			if !v.Ok() {
				how := "--of " + string(v.Digest)
				if full {
					how += " --full"
				}
				if slices.ContainsFunc(v.Findings, func(f trail.Finding) bool { return f.Kind == "behind" }) {
					how += " --restored <the checkpoint the backup holds>"
				}

				return fmt.Errorf("%d finding(s): look at each, and `roster trail accept %s --why ...` once the archive is right", len(v.Findings), how)
			}

			return nil
		}),
	}
}

// acceptance is one acceptance as a verification lists it: where it was kept,
// who said it and why, and what it was of.
func acceptance(a trail.Accepted) string {
	kinds := make([]string, 0, len(a.Kinds))
	for _, k := range slices.Sorted(maps.Keys(a.Kinds)) {
		kinds = append(kinds, fmt.Sprintf("%s %d", k, a.Kinds[k]))
	}
	if len(kinds) == 0 {
		kinds = append(kinds, "nothing")
	}

	twice := 0
	for _, ts := range a.Twice {
		twice += len(ts)
	}
	if twice > 0 {
		kinds = append(kinds, fmt.Sprintf("heard twice %d", twice))
	}

	v := fmt.Sprintf("at checkpoint %d, %s, by %s: %s (%s)", a.Checkpoint, a.At.UTC().Format(time.RFC3339), a.By, a.Why, strings.Join(kinds, ", "))
	if a.Restored != nil {
		v += fmt.Sprintf(", restoring checkpoint %d", *a.Restored)
	}

	return v
}

// heard is what each witness that answered heard last, as a verification
// prints it, one line each in the order of their names: the checkpoint's
// number and digest, when the witness heard it by its own clock, and how many
// numbers it heard as two statements -- each of which is also a `rewritten`
// finding until an acceptance takes it in.
//
// A witness that only tells (`log:`) answers nothing and is not here -- what
// it heard is the monitoring's to say -- and neither is one that did not
// answer this time. `unheard` is the finding when nothing that answers heard
// the newest.
func heard(hs map[string]trail.Heard) []string {
	out := make([]string, 0, len(hs))
	for _, name := range slices.Sorted(maps.Keys(hs)) {
		h := hs[name]
		// One that answered and has heard nothing yet -- named before the
		// deployment's first checkpoint, or since the last -- answers the
		// zero statement, numbered nothing, which the chain is not held to.
		if h.K == 0 {
			out = append(out, fmt.Sprintf("heard by %s: nothing yet", name))
			continue
		}

		v := fmt.Sprintf("heard by %s: checkpoint %d, %s, at %s", name, h.K, h.Digest, h.At.UTC().Format(time.RFC3339))
		if n := len(h.Twice); n > 0 {
			v += fmt.Sprintf("; %d number(s) heard as two statements", n)
		}
		out = append(out, v)
	}

	return out
}

// newCmdTrailRows checks the rows against the trail: each row of every kind
// on it, digested again and compared with the digest the newest line of the
// trail about it keeps (`audit.digest.key`, payday#102) -- in the database, or
// in the archive once it has left (payday#108) -- and every thing the trail is
// about that is no row (payday#115). A row somebody changed in the database
// past the servers is one that differs, a row somebody added there is one
// nothing on the trail is about, and a row somebody removed is missing. A line
// whose digest is not its own, a line with none, and one under a key this was
// not given say nothing about the row beside them, and are findings too: each
// is what whoever can write the database can make of a line.
//
// `verify` is the archive against the database's account of it; this is the
// database's rows against what the trail says the last write left of them. It
// changes nothing, and fails on any finding, so a schedule can run it.
//
// `--accept` is an operator who has looked at each and decided the rows are
// right -- a migration rewrote them, they were written before the trail could
// say anything about them -- and it puts what this found on the trail as that
// act, one line each: `pd.Record` of the rows there are, `pd.RecordErased` of
// the ones that are gone. It is of what was looked at (payday#115): `--of` is
// the digest the check printed (`audit.Rows.Digest`), and an acceptance is
// refused when this check found anything else, so a row changed after the
// operator looked is not recorded beside the ones they did. A forged line and a
// key not given are not accepted here: one is a line to look at, and the other
// a key to give.
//
// # Who accepted, and why
//
// Who is the operator `--as` names, as for every command at the box that
// names somebody, and the shell they are at; with no `--as`, the shell alone.
// There is no `--by` as `trail accept` has, since `--as` already says it, and
// says it of an operator the control plane has rather than of a word.
//
// Each line the acceptance writes names that operator as its actor. A line a
// command at the box writes names nobody, because the box has no caller to
// frame; here there is somebody, who looked and decided, and an acceptance
// that names nobody is the one a finding hides behind. Framed for
// `pd.Record` alone, which writes the trail and asks no rule of anybody, and
// with no tenant, as `resources apply` frames its provisioner: the line is
// filed where it was, and only says who.
//
// The why is not on the line: `pd.Record` keeps no reason, and a line of the
// trail has no column for one (payday#115). It is logged with the digest and
// who, and the log at the box is stderr ([aloud]) -- where for as long as no
// command built a logger it was said to be logged and went nowhere (#280). So
// what keeps the why is whatever keeps what the command printed.
//
// # What is personal, which the seal covers
//
// The digest covers every field but the ones declared personal, which a
// line of the trail -- copied to the archive and kept for years -- must not
// keep a MAC of. Those are covered by the row's seal, kept beside the row in
// the database alone (payday#115), and a row whose newest write sealed
// nothing -- written before its kind declared the field personal -- is
// counted as from before seals: no finding, and nothing vouches for its
// personal fields. `--accept` puts those on the trail too, which seals them,
// and does so when they are all the check found (docs/operating.md).
func newCmdTrailRows(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "rows",
		Brief: "check the live rows against the digests the trail keeps of them, and change nothing",

		Flags: flg.Flags{
			&flg.Switch{Name: "accept", Brief: "put what the check found on the trail as accepted, once somebody has looked"},
			&flg.String{Name: "of", Brief: "with --accept, the digest a check printed of what was looked at"},
			&flg.String{Name: "why", Brief: "what was looked at and decided, with --accept; it is said on stderr with who accepts, and is not on the trail"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			accept, _ := flg.Find[bool](cl, "accept")
			of, _ := flg.Find[string](cl, "of")
			why, _ := flg.Find[string](cl, "why")
			if accept && strings.TrimSpace(of) == "" {
				return errors.New("--of: the digest `roster trail rows` printed of what was looked at")
			}
			if accept && strings.TrimSpace(why) == "" {
				return errors.New("--why: what was looked at and decided")
			}

			cs, err := c.Audit.Contents()
			if err != nil {
				return fmt.Errorf("audit: %w", err)
			}
			if cs.Digest == nil {
				return errors.New("audit.digest.key names no key, so the trail keeps no digest to check a row against")
			}

			p, err := cmd.AuditPolicy(c.Audit)
			if err != nil {
				return fmt.Errorf("audit: %w", err)
			}

			s, err := built(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// Who accepts, before anything is read: a name that is nobody's
			// is refused as it is everywhere.
			by := cmd.Operator{}
			if accept {
				if by, err = accepting(ctx, c, s); err != nil {
					return err
				}
			}

			// What the check logs is said, on stderr ([aloud]): the chunks of
			// the archive it left out of what it read, which a check that
			// answers *all match* has still not read.
			ctx, _ = aloud(ctx)

			v, err := pd.Digests(ctx, s.Ent, cs.Digest, p)
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "%d row(s) read: %d match, %d differ, %d forged, %d with no digest, %d not on the trail, %d under a key not given; %d missing; "+
				"%d from before digests began, %d under a key let go; %d forgotten; %d checked against the archive\n",
				v.Read, v.Matches, len(v.Differs), len(v.Forged), len(v.NoDigest), len(v.NotOnTrail), len(v.KeyNotHeld), len(v.Missing),
				v.BeforeDigests, v.Retired, v.Forgotten, v.Archived)

			// What an acceptance of these findings names.
			digest := v.Digest()
			fmt.Fprintf(os.Stderr, "digest %s\n", digest)

			// The rows whose newest write sealed nothing, which a check
			// cannot vouch for the personal fields of (payday#115): no
			// finding, since nothing can be said of what they were, and
			// counted every run rather than listed. The acceptance that seals
			// them is the one below, of this digest, which names them.
			if n := len(v.Unsealed); n > 0 {
				fmt.Fprintf(os.Stderr, "%d row(s) from before seals: what is personal in them is vouched for by nothing until "+
					"`roster trail rows --accept --of %s --why ...` seals them\n", n, digest)
			}

			// Each finding a line, by what it is, then its kind and the row:
			// what a schedule greps and an operator looks up.
			for _, f := range []struct {
				what string
				ids  []pdid.Id
			}{
				{"differs", v.Differs},
				{"forged", v.Forged},
				{"no-digest", v.NoDigest},
				{"not-on-trail", v.NotOnTrail},
				{"key-not-held", v.KeyNotHeld},
				{"missing", v.Missing},
			} {
				for _, id := range f.ids {
					fmt.Fprintf(os.Stdout, "%s %s %s\n", f.what, id.Domain(), id)
				}
			}
			// And the keys those were made under, for `audit.digest.previous`
			// or, once somebody has decided they are gone, `retired`.
			for _, k := range v.Unheld {
				fmt.Fprintf(os.Stdout, "unheld-key %s\n", k)
			}
			// What was not looked at, every time, so that a check that found
			// nothing says what it did not read.
			for _, k := range v.Skipped {
				fmt.Fprintf(os.Stderr, "not checked: %s, kept off the trail: %s\n", k.Kind, k.Why)
			}
			// Nothing found and nothing to seal is nothing to accept. Rows
			// from before seals are no finding, so a check that found only
			// them passes -- and is still something `--accept` puts on the
			// trail, which is how they are sealed.
			if v.Ok() && (!accept || len(v.Unsealed) == 0) {
				return nil
			}

			findings := len(v.Differs) + len(v.Forged) + len(v.NoDigest) + len(v.NotOnTrail) + len(v.KeyNotHeld) + len(v.Missing)
			if !accept {
				return fmt.Errorf("%d finding(s): look at each, and `roster trail rows --accept --of %s --why ...` once the rows are right", findings, digest)
			}

			// What was looked at, and nothing found since: an acceptance of
			// rows somebody changed after the check they read is of rows
			// nobody looked at (payday#115).
			if strings.TrimSpace(of) != digest {
				return fmt.Errorf("--of: the check looked at was %s, and this one found %s: "+
					"run `roster trail rows` again, and look at what it finds", strings.TrimSpace(of), digest)
			}
			if n := len(v.Forged) + len(v.KeyNotHeld); n > 0 {
				return fmt.Errorf("%d line(s) whose digest is not their own or under a key not given: those are not accepted here -- "+
					"look at the line, or give the key in audit.digest.previous or retired", n)
			}

			// As the operator who accepts, where one was named: each line says
			// who. See this command's comment.
			as := ctx
			if !by.Id.IsZero() {
				as = frame.Into(ctx, frame.New(by.Id, pdid.Nil, frame.Whole()))
			}

			there, err := pd.Record(as, s.Ent, cs, acceptedRows, slices.Concat(v.Differs, v.NoDigest, v.NotOnTrail))
			if err != nil {
				return fmt.Errorf("accept: %w", err)
			}
			gone, err := pd.RecordErased(as, s.Ent, cs, acceptedRows, v.Missing)
			if err != nil {
				return fmt.Errorf("accept: %w", err)
			}
			// And the rows from before seals, which `pd.Record` seals as it
			// records them: what the operator looked at was the count and the
			// digest that names each, and what they are now is what the seal
			// vouches for from here on.
			sealed, err := pd.Record(as, s.Ent, cs, acceptedRows, v.Unsealed)
			if err != nil {
				return fmt.Errorf("accept: %w", err)
			}

			who := accepter(by)
			log.From(ctx).InfoContext(ctx, "trail: rows accepted as they are", slog.String("by", who), slog.String("of", digest), slog.String("why", why),
				slog.Int("rows", there), slog.Int("gone", gone), slog.Int("sealed", sealed))
			fmt.Fprintf(os.Stderr, "accepted %d row(s) and %d gone, and sealed %d from before seals, by %s; the trail says they are what they are now\n", there, gone, sealed, who)

			return nil
		}),
	}
}

// acceptedRows is the act `roster trail rows --accept` is on the trail as.
const acceptedRows = "roster.trail.rows.accept"

// accepting is who accepts what a check of the rows found: the operator
// `--as` names, checked against the control plane as [atTheBox] checks one,
// and the shell they are at. With no `--as` it is the shell alone, and with
// neither it is refused, as `trail accept` refuses an acceptance by nobody.
func accepting(ctx context.Context, c *cmd.Config, s *cmd.Server) (cmd.Operator, error) {
	if strings.TrimSpace(c.Client.As) != "" {
		return atTheBox(ctx, c, s)
	}

	by := cmd.Operator{Where: thisShell()}
	if by.Where == "" {
		return by, errors.New("--as: who accepts, since this shell does not say")
	}

	return by, nil
}

// accepter is who accepted, as it is said: the operator and the shell, or the
// shell alone.
func accepter(by cmd.Operator) string {
	switch {
	case by.Alias == "":
		return by.Where
	case by.Where == "":
		return by.Alias
	default:
		return by.Alias + " (" + by.Where + ")"
	}
}

// newCmdTrailAccept is an operator who has looked at what `verify` found and
// decided the archive is right -- a pass a crash stopped, a chunk restored from
// a backup -- making the account say what the archive holds.
//
// # Of what was looked at
//
// `--of` is the digest `verify` printed, and the acceptance is of those
// findings and no others: verified again -- `--full` when the one looked at
// was -- and refused when it finds anything else (payday#115). A database
// restored from a backup is accepted as the restore it is, `--restored`
// naming the checkpoint the backup holds. Who accepts and why are kept, in the
// checkpoint it ends in and in a receipt: the operator `--as` names and the
// shell they are at, as `trail rows --accept` keeps them; where `--as` names
// nobody, `--by`, which is the operator's user and host when it is not given;
// and `--why`, which is required, because an acceptance nobody can explain
// later is a finding hidden.
//
// `--as` was read by nothing here, so an operator who named themselves was
// kept as the shell's user (#284). Given beside `--by`, the two would be two
// answers to one question, and it is refused before anything is opened.
func newCmdTrailAccept(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "accept",
		Brief: "make the account of the archive say what it holds, once somebody has looked",

		Flags: flg.Flags{
			&flg.String{Name: "in", Brief: "the archive's directory, which is audit.archive when that is set"},
			&flg.String{Name: "of", Brief: "the digest `roster trail verify` printed of the findings looked at"},
			&flg.String{Name: "by", Brief: "who accepts, where --as names nobody; the shell's user and host by default"},
			&flg.String{Name: "why", Brief: "what was looked at and decided; it is kept"},
			&flg.Switch{Name: "full", Brief: "the verification looked at was --full"},
			&flg.Int{Name: "restored", Brief: "for a database restored from a backup, the checkpoint the backup holds (0 for one from before the first)"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			of, _ := flg.Find[string](cl, "of")
			if strings.TrimSpace(of) == "" {
				return errors.New("--of: the digest `roster trail verify` printed of the findings looked at")
			}
			why, _ := flg.Find[string](cl, "why")
			if strings.TrimSpace(why) == "" {
				return errors.New("--why: what was looked at and decided")
			}
			by, _ := flg.Find[string](cl, "by")
			named := strings.TrimSpace(c.Client.As) != ""
			if named && strings.TrimSpace(by) != "" {
				return errors.New("--by: --as says who accepts; drop one of them")
			}
			if !named && strings.TrimSpace(by) == "" {
				by = thisShell()
			}
			if !named && strings.TrimSpace(by) == "" {
				return errors.New("--by: who accepts, since this shell does not say")
			}

			a := trail.Acceptance{Of: flob.Digest(strings.TrimSpace(of)), By: by, Why: why}
			a.Full, _ = flg.Find[bool](cl, "full")
			if k, ok := flg.Find[int](cl, "restored"); ok {
				a.Restored = &k
			}

			c, err := inArchive(cl, c)
			if err != nil {
				return err
			}
			s, p, err := archived(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			if named {
				who, err := accepting(ctx, c, s)
				if err != nil {
					return err
				}
				by = accepter(who)
				a.By = by
			}

			// What the acceptance logs is said, on stderr ([aloud]).
			ctx, _ = aloud(ctx)

			v, err := p.Accept(ctx, pd.TrailStore(s.Ent), a)
			for _, f := range v.Findings {
				fmt.Fprintf(os.Stdout, "%s\n", f)
			}
			if errors.Is(err, trail.ErrUnreviewed) || errors.Is(err, trail.ErrRestored) {
				again := "roster trail verify"
				if a.Full {
					again += " --full"
				}

				return fmt.Errorf("%w\n\nrun `%s` again, look at what it finds, and accept its digest", err, again)
			}
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "accepted %d finding(s), by %s\n", len(v.Findings), by)

			return nil
		}),
	}
}

// thisShell is the user and the host of the shell a command is run from, and
// empty when neither can be told: who accepts when `--by` does not say, and
// where a call made at the box came from, beside the operator `--as` names.
func thisShell() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	host, _ := os.Hostname()

	switch {
	case name != "" && host != "":
		return name + "@" + host
	case name != "":
		return name
	default:
		return host
	}
}

// archived is the deployment's policy and the server whose database keeps the
// account of its archive, refused when there is no archive to account for.
func archived(ctx context.Context, c *cmd.Config) (*cmd.Server, trail.Policy, error) {
	if c.Audit.Archive == "" {
		return nil, trail.Policy{}, errors.New("--in: which directory; or set audit.archive")
	}

	p, err := cmd.AuditPolicy(c.Audit)
	if err != nil {
		return nil, trail.Policy{}, err
	}

	s, err := built(ctx, *c)
	if err != nil {
		return nil, trail.Policy{}, err
	}

	return s, forget.Held(p, s.Ent), nil
}

// kindOf is `--kind`, and every kind when it is not given.
//
// One kind rather than a list, deliberately: a policy is per kind and an
// operator running one of these by hand is answering for one of them. A list
// would be a second way to write a policy, in a place that is not the
// configuration.
func kindOf(cl *xli.Command) (trail.Kinds, error) {
	v, _ := flg.Find[string](cl, "kind")
	if v == "" {
		return trail.Kinds{}, nil
	}

	d, err := trail.DomainOf(v)
	if err != nil {
		return trail.Kinds{}, fmt.Errorf("--kind: %w", err)
	}

	return trail.Only(d), nil
}

// newCmdTrailProfiles prints the table, because a number in a configuration
// file that nobody can trace is a number nobody will change.
// newCmdTrailOwed is what is owed of the trail's erasures and not paid yet
// (payday#115, `trail.Policy.Owing`). Somebody forgotten has the trail's rows
// about them blanked in the transaction that forgets them, and the archive's
// half and the rows' seals owed there; `forget` pays them once it has
// committed, and what it could not pay is the next pass's. So what stays owed
// for long is one a legal hold keeps part of, which waits for the hold to lift
// and is said by tenant, or one that cannot be done -- an archive that cannot
// be written, a chunk whose bytes are not its digest (`roster trail verify`).
// It reads the database and changes nothing.
//
// What an erasure owed keeps is whose holds kept part of it **when it was last
// paid**, and nothing writes it again until the next payment. So a tenant it
// names is asked whether it is held now: read as it stood, this went on saying
// *until it lifts* of a hold that had been lifted (#280).
func newCmdTrailOwed(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "owed",
		Brief: "what is owed of the trail's erasures and not paid yet: how many, since when, and what a legal hold keeps",

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			s, err := built(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// A question of the store's, which no part of the policy answers.
			o, err := trail.Policy{}.Owing(ctx, pd.TrailStore(s.Ent))
			if err != nil {
				return err
			}
			if o.Objects == 0 {
				fmt.Fprintf(os.Stdout, "nothing of the trail's erasures is owed\n")

				return nil
			}

			fmt.Fprintf(os.Stdout, "%d erasure(s) owed, the oldest since %s\n", o.Objects, o.Oldest.UTC().Format(time.RFC3339))
			held := forget.Holds(s.Ent)
			for _, id := range slices.SortedFunc(maps.Keys(o.Held), func(a, b pdid.Id) int { return strings.Compare(a.String(), b.String()) }) {
				name := id.String()
				if t, err := s.Ent.Tenant.Get(ctx, id.Uuid()); err == nil && t.Alias != "" {
					name = "@" + t.Alias + " (" + name + ")"
				}

				// Asked of what a pass asks, so that the two agree on what a
				// hold is.
				t, err := held(ctx, id)
				if err != nil {
					return err
				}
				if t.Hold != nil {
					fmt.Fprintf(os.Stdout, "  %d kept by a legal hold of %s, until it lifts\n", o.Held[id], name)

					continue
				}
				fmt.Fprintf(os.Stdout, "  %d kept by a legal hold of %s when last paid, which has been lifted since: owed until the next payment\n", o.Held[id], name)
			}

			return nil
		}),
	}
}

func newCmdTrailProfiles() *xli.Command {
	return &xli.Command{
		Name:  "profiles",
		Brief: "the named retention regimes, and where each number comes from",

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			names := make([]string, 0, len(trail.Profiles))
			for k := range trail.Profiles {
				names = append(names, k)
			}
			sort.Strings(names)

			for _, name := range names {
				v := trail.Profiles[name]
				fmt.Fprintf(os.Stdout, "%-16s retain=%-8s destroy=%-8s %s\n",
					name, dur(v.Retain), dur(v.Destroy), v.Why)
			}

			fmt.Fprintln(os.Stderr,
				"\nA starting point and not a guarantee: what a deployment must keep depends "+
					"on what it processes and for whom.")

			return nil
		}),
	}
}

func dur(v time.Duration) string {
	if v == 0 {
		return "forever"
	}

	return v.String()
}

// prune is one pass of the deployment's own policy, which is what this command
// does when it was given no window -- or, dry, what that pass would take.
func prune(ctx context.Context, c cmd.Config, dry bool) error {
	p, err := cmd.TrailPolicy(ctx, c)
	if err != nil {
		return err
	}
	if !p.On() {
		return errors.New("this deployment keeps its trail forever, so there is nothing to apply; " +
			"see `audit:` in the configuration, or name a window with --older-than")
	}

	s, err := built(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	// Asked whether there is a policy before roster's holds are added, which
	// answer per tenant and so read as one; see `cmd.Build`.
	p = forget.Held(p, s.Ent)
	fmt.Fprintf(os.Stderr, "%s\n", p.String())

	// It logs what it did, per kind, exactly as the sweep does -- which is why
	// nothing is printed here beyond the policy: two accounts of one pass would
	// be two that disagree.
	//
	// And the log is said, which it was not: no command builds a logger, so
	// the one account of the pass went nowhere, and a pass that could not
	// write its archive printed the policy and answered exit 0 (#280). The
	// pass hands back no result -- it is written for a loop, which tries again
	// a day later -- so what it could not do is read off what it logged: a
	// line that carries an error. See [aloud].
	ctx, told := aloud(ctx)

	if dry {
		return wouldPrune(ctx, p, pd.TrailStore(s.Ent))
	}

	p.Pass(ctx, pd.TrailStore(s.Ent))

	return told.failed("the pass")
}

// wouldPrune says what one pass of the policy would take, and takes nothing:
// the rows past each kind's window and where they would go, the chunks of the
// archive past theirs, and the erasures owed it would pay.
//
// payday has no dry pass, so this asks the questions a pass decides by, of the
// same store: how many rows of a kind are older than its window
// (`Store.Count`, which a dry run by hand asks too), and which chunks a purge
// at each kind's own end would destroy (`Policy.Doomed`, which destroys
// nothing and reads the account the pass decides by). Every tenant keeps the
// deployment's windows here -- roster's tenants answer holds and nothing else
// (`forget.Holds`) -- so the counts by kind are the pass's, but for what a
// hold is on: a held row a window would discard is moved to the archive, or
// left where there is none.
func wouldPrune(ctx context.Context, p trail.Policy, store trail.Store) error {
	now := time.Now()
	names := pdid.Domains()

	rows := func(kind string, of trail.Kinds, k trail.Keep) error {
		if k.Retain <= 0 {
			// Kept in the database for good.
			return nil
		}

		before := now.Add(-k.Retain)
		n, err := store.Count(ctx, trail.Scope{Kinds: of}, before)
		if err != nil {
			return err
		}

		where := "the archive"
		if k.Discard || p.Archive == nil {
			where = "nowhere but for what a legal hold is on"
		}
		fmt.Fprintf(os.Stdout, "%s: %d row(s) older than %s would leave the database, to %s\n",
			kind, n, before.UTC().Format(time.RFC3339), where)

		return nil
	}

	named := p.Named()
	for _, d := range named {
		if err := rows(names[d], trail.Only(d), p.For(d)); err != nil {
			return err
		}
	}
	if err := rows("anything else", trail.Except(named...), p.Keep); err != nil {
		return err
	}

	if p.Archive != nil {
		// A chunk goes when its kind's whole window has run out: `destroy`,
		// or for a kind that keeps no copy, `retain`, which is how long what
		// a hold sent to the archive lasts there once the hold is lifted.
		end := func(kind string) (time.Time, bool) {
			k := p.Keep
			if d, err := trail.DomainOf(kind); err == nil {
				k = p.For(d)
			}

			switch {
			case k.Destroy > 0:
				return now.Add(-k.Destroy), true
			case k.Discard && k.Retain > 0:
				return now.Add(-k.Retain), true
			}

			return time.Time{}, false
		}

		vs, held, err := p.Doomed(ctx, store, end)
		if err != nil {
			return err
		}
		for _, v := range vs {
			fmt.Fprintf(os.Stdout, "%s would be destroyed\n", v)
		}
		kept(held)
	}

	// A question of the store's, as `trail owed` asks it.
	o, err := trail.Policy{}.Owing(ctx, store)
	if err != nil {
		return err
	}
	if o.Objects > 0 {
		fmt.Fprintf(os.Stdout, "%d erasure(s) owed would be paid, but for what a legal hold keeps\n", o.Objects)
	}

	fmt.Fprintf(os.Stderr, "a dry run: nothing was changed\n")

	return nil
}

// cutoff is `--older-than` or `--before`, and answers whether either was given.
//
// Both is refused, because they are two ways of naming one instant and a
// command given two has not been told which -- the same refusal `vouch.refOf`
// makes about a person named twice, for the same reason.
//
// Neither is **not** refused, and used to be. A window is what an operator
// names when they mean something other than the policy; when they name nothing
// they mean the policy, and the version that insisted on a cutoff made
// `--older-than 1ns` the obvious thing to type -- which destroys the kind the
// configuration says to keep forever.
//
// A window of nothing is refused in both spellings: `--older-than 0`, and a
// `--before` that has not come yet, which was taken (#280).
func cutoff(cl *xli.Command) (time.Time, bool, error) {
	older, _ := flg.Find[string](cl, "older-than")
	before, _ := flg.Find[string](cl, "before")

	switch {
	case older != "" && before != "":
		return time.Time{}, false, errors.New("--older-than and --before name the same instant two ways; give one")

	case older != "":
		d, err := time.ParseDuration(older)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("--older-than: %w", err)
		}
		if d <= 0 {
			return time.Time{}, false, errors.New("--older-than: a window of nothing is everything")
		}

		return time.Now().Add(-d), true, nil

	case before != "":
		at, err := time.Parse(time.RFC3339, before)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("--before: %w", err)
		}
		if at.After(time.Now()) {
			// The same window `--older-than 0` is, and less: every row there
			// is, and every row written between now and then.
			return time.Time{}, false, fmt.Errorf("--before: %s is still to come, and a window that ends after now is everything", before)
		}

		return at, true, nil

	default:
		return time.Time{}, false, nil
	}
}

// archives is the files named on the command line, and none when the archive
// itself is what is read.
func archives(cl *xli.Command, c *cmd.Config) ([]string, error) {
	if vs, ok := arg.Get[[]string](cl, "FILE"); ok && len(vs) > 0 {
		return vs, nil
	}

	return nil, nil
}

// kept says what a legal hold left as it was, which is not a failure and is
// not nothing either.
func kept(h trail.Held) {
	if !h.Any() {
		return
	}

	fmt.Fprintf(os.Stderr, "kept by a legal hold: %d row(s) and %d chunk(s), of %v\n",
		h.Rows, h.Chunks, append(h.By, h.Unanswered...))
}

// filterOf is the flags as one question asked of each row.
func filterOf(cl *xli.Command) (func(*app.Audit) bool, error) {
	object, err := identifier(cl, "object")
	if err != nil {
		return nil, err
	}
	actor, err := identifier(cl, "actor")
	if err != nil {
		return nil, err
	}
	tenant, err := identifier(cl, "tenant")
	if err != nil {
		return nil, err
	}

	action, _ := flg.Find[string](cl, "action")

	since, err := instant(cl, "since")
	if err != nil {
		return nil, err
	}
	until, err := instant(cl, "until")
	if err != nil {
		return nil, err
	}

	return func(v *app.Audit) bool {
		if object != pdid.Nil && !is(object, v.GetObjectId()) {
			return false
		}
		if actor != pdid.Nil && !is(actor, v.GetActorId()) {
			return false
		}
		if tenant != pdid.Nil {
			// Any of the three, which is what the wall reads: a row is one
			// tenant's business if its object was theirs, if its actor was, or
			// if they were the other party to it.
			if !is(tenant, v.GetTenantId()) &&
				!is(tenant, v.GetActorTenantId()) &&
				!is(tenant, v.GetCounterpartTenantId()) {
				return false
			}
		}
		if action != "" && !strings.Contains(v.GetAction(), action) {
			return false
		}

		at := v.GetDateCreated().AsTime()
		if !since.IsZero() && at.Before(since) {
			return false
		}
		if !until.IsZero() && at.After(until) {
			return false
		}

		return true
	}, nil
}

// is answers whether a column holds this identifier.
func is(k pdid.Id, b []byte) bool {
	v, err := pdid.From(b)
	if err != nil {
		return false
	}

	return v == k
}

func identifier(cl *xli.Command, name string) (pdid.Id, error) {
	v, _ := flg.Find[string](cl, name)
	if v == "" {
		return pdid.Nil, nil
	}

	k, err := pdid.Parse(v)
	if err != nil {
		return pdid.Nil, fmt.Errorf("--%s: %w", name, err)
	}

	return k, nil
}

func instant(cl *xli.Command, name string) (time.Time, error) {
	v, _ := flg.Find[string](cl, name)
	if v == "" {
		return time.Time{}, nil
	}

	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("--%s: %w", name, err)
	}

	return at, nil
}

// named is an identifier as it is printed, and nobody as a dash: a column
// with nothing in it, or the zero identifier, which is how the trail writes
// an actor nobody was framed as. Only bytes that are no identifier at all are
// a question mark, so that *nobody* is not read as *damaged*.
func named(b []byte) string {
	if len(b) == 0 || (len(b) == len(pdid.Nil) && pdid.Id(b).IsZero()) {
		return "-"
	}

	k, err := pdid.From(b)
	if err != nil {
		return "?"
	}

	return k.String()
}

// inArchive is the configuration an act on the archive is taken with: `--in`
// names the archive `audit.archive` already is, or the archive of this run for
// a deployment that names none, and nothing else.
//
// The database keeps an account of one archive, and every check reads that
// one (`trail verify`, every pass). Read in place of a second, an acceptance
// would write the account from what the second holds -- every chunk of the
// first taken for missing, and signed as erased -- and a purge would decide
// by an account of chunks it does not hold. So `--in` is read here, the way
// `prune --to` is, and not bound to `audit.archive` as `read --in` is.
func inArchive(cl *xli.Command, c *cmd.Config) (*cmd.Config, error) {
	dir, _ := flg.Find[string](cl, "in")
	switch {
	case dir == "":
		return c, nil
	case c.Audit.Archive == "":
		v := *c
		v.Audit.Archive = dir
		return &v, nil
	case !sameDir(dir, c.Audit.Archive):
		return nil, fmt.Errorf("--in: %s is not audit.archive (%s), and the database keeps an account of one archive; "+
			"leave --in out, or name that one", dir, c.Audit.Archive)
	}

	return c, nil
}

// sameDir answers whether two names are of one directory, as far as their
// paths say.
func sameDir(a, b string) bool {
	x, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	y, err := filepath.Abs(b)
	if err != nil {
		return false
	}

	return filepath.Clean(x) == filepath.Clean(y)
}
