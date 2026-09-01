package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// NewCmdTrail is `roster trail`: what happens to the record of what happened,
// after long enough.
//
// # Why it is a command and not an Rpc
//
// The same reason `roster key` is one, and a stronger version of it. The layer
// in front of `AuditService` refuses every write -- *"the trail is written by
// what happened, not by anybody asking"* -- and a retention Rpc beside it would
// be the exception that makes the sentence false. What a trail is worth is that
// the credential which lets somebody act is not the credential that lets them
// erase the record of having acted, and an Api key that prunes is a stolen key
// that prunes.
//
// So this needs the database, which is the boundary that was being asked for.
// `serve` applies the same policy on a clock; see `audit:` in the
// configuration, and `server/trail` for what the two clocks are.
//
// # And what it is not
//
// It is not `roster audit`, which is the generated entity command and reads
// rows through a server. These are the acts no server offers.
func NewCmdTrail(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "trail",
		Brief: "what happens to the record of what happened, after long enough",

		Commands: xli.Commands{
			newCmdTrailPrune(c),
			newCmdTrailRead(c),
			newCmdTrailPurge(c),
			newCmdTrailProfiles(),
		},
	}
}

// newCmdTrailPrune moves rows out of the database, and is the one act here
// that cannot be undone by running it again.
func newCmdTrailPrune(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "prune",
		Brief: "apply the retention policy now, or a window of your own",

		Flags: flg.Flags{
			&flg.String{Name: "older-than", Brief: "how old a row has to be, e.g. 2160h for ninety days"},
			&flg.String{Name: "before", Brief: "an instant instead, RFC 3339"},
			&flg.String{Name: "kind", Brief: "only rows about this kind of thing, e.g. holder; every kind by default"},
			&flg.String{Name: "to", Brief: "the directory to write into; audit.archive by default"},
			&flg.Switch{Name: "discard", Brief: "remove them and keep no copy"},
			&flg.Switch{Name: "dry-run", Brief: "say how many there are and change nothing"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			before, given, err := cutoff(cmd)
			if err != nil {
				return err
			}
			if !given {
				// No window of their own, so the deployment's: one pass of what
				// `audit:` says, per kind, exactly as the sweep does it.
				//
				// This is the default rather than a refusal because it is what
				// somebody putting this in cron means, and because the
				// alternative was worse than inconvenient: a command that only
				// took a cutoff destroyed the kind the configuration said to
				// keep forever.
				return prune(ctx, *c)
			}

			of, err := kindOf(cmd)
			if err != nil {
				return err
			}

			dry, _ := flg.Find[bool](cmd, "dry-run")

			dir, _ := flg.Find[string](cmd, "to")
			if dir == "" {
				dir = c.Audit.Archive
			}

			discard, _ := flg.Find[bool](cmd, "discard")

			// Where the rows go is a question about **destroying** them, so it
			// is asked of a run that will. A dry run that insisted on a
			// destination was refusing to count on the grounds that it had not
			// been told where to put what it was not going to move -- and it
			// made the only safe way to ask *how many* into the one form that
			// needs the dangerous flags filled in first.
			if !dry {
				if dir == "" && !discard {
					// The same refusal `trail.Policy.Valid` makes about the
					// configuration, at the other door. Somebody who has not
					// said where the rows go has not said they may be
					// destroyed.
					return errors.New("--to: nowhere to put what leaves the database; " +
						"name a directory, set audit.archive, or --discard to say the rows are meant to go")
				}
				if dir != "" && discard {
					return errors.New("--discard and --to say two different things about the same rows")
				}
			}

			s, err := Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			store := pd.TrailStore(s.Ent)

			if dry {
				n, err := store.Count(ctx, of, before)
				if err != nil {
					return err
				}

				fmt.Fprintf(os.Stdout, "%d row(s) older than %s\n", n, before.UTC().Format(time.RFC3339))

				return nil
			}

			n := 0
			if discard {
				n, err = trail.Collect(ctx, store, of, before)
			} else {
				n, err = trail.Archive(ctx, store, of, before, dir)
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

			return nil
		}),
	}
}

// newCmdTrailRead is the archive read back, and it opens no database.
//
// Deliberately: the reason to keep the file is that it outlives the deployment
// that wrote it, so a reader that needed the deployment would be answering a
// question nobody has at the moment they have it.
func newCmdTrailRead(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "read",
		Brief: "read an archive back, without a database",

		Args: arg.Args{
			&arg.RestStrings{Name: "FILE", Brief: "the archives to read; --in or audit.archive by default"},
		},

		Flags: flg.Flags{
			&flg.String{Name: "in", Brief: "a directory of archives; audit.archive by default"},
			&flg.String{Name: "object", Brief: "only what happened to this identifier"},
			&flg.String{Name: "actor", Brief: "only what this person did"},
			&flg.String{Name: "tenant", Brief: "only this tenant, on any of the three columns"},
			&flg.String{Name: "action", Brief: "only actions containing this"},
			&flg.String{Name: "since", Brief: "no earlier than, RFC 3339"},
			&flg.String{Name: "until", Brief: "no later than, RFC 3339"},
			&flg.Switch{Name: "json", Brief: "the rows as they are stored, one per line"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			paths, err := archives(cmd, c)
			if err != nil {
				return err
			}

			keep, err := filterOf(cmd)
			if err != nil {
				return err
			}

			asJson, _ := flg.Find[bool](cmd, "json")

			// One call over every file rather than one per file, because the
			// duplicate two writers leave behind is only visible to a reader
			// that has seen both. See [trail.Read].
			return pd.ReadTrail(paths, func(v *app.Audit) error {
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
			})
		}),
	}
}

// newCmdTrailPurge is the end of the line.
//
// By file and not by row, which is what the archive's layout is for: a file is
// named for the month it holds, so one is destroyable when the month after it
// has also passed. Rewriting a file to drop some of its rows would be editing
// an archive, which is the thing this whole package refuses to offer.
func newCmdTrailPurge(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "purge",
		Brief: "destroy the archives that are old enough, and there is nothing after this",

		Flags: flg.Flags{
			&flg.String{Name: "older-than", Brief: "how old the archive has to be, e.g. 61320h for seven years"},
			&flg.String{Name: "before", Brief: "an instant instead, RFC 3339"},
			&flg.String{Name: "kind", Brief: "only archives of this kind of thing; every kind by default"},
			&flg.String{Name: "in", Brief: "the directory to destroy from; audit.archive by default"},
			&flg.Switch{Name: "dry-run", Brief: "say which files and remove nothing"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			before, given, err := cutoff(cmd)
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

			of, err := kindOf(cmd)
			if err != nil {
				return err
			}

			dir, _ := flg.Find[string](cmd, "in")
			if dir == "" {
				dir = c.Audit.Archive
			}
			if dir == "" {
				return errors.New("--in: which directory")
			}

			cut := of.CutFor(before)

			if dry, _ := flg.Find[bool](cmd, "dry-run"); dry {
				vs, err := trail.Doomed(dir, cut)
				if err != nil {
					return err
				}
				for _, v := range vs {
					fmt.Fprintf(os.Stdout, "%s\n", v)
				}

				return nil
			}

			vs, err := trail.Purge(ctx, dir, cut)
			if err != nil {
				return fmt.Errorf("after %d file(s): %w", len(vs), err)
			}
			for _, v := range vs {
				fmt.Fprintf(os.Stdout, "%s\n", v)
			}

			return nil
		}),
	}
}

// kindOf is `--kind`, and every kind when it is not given.
//
// One kind rather than a list, deliberately: a policy is per kind and an
// operator running one of these by hand is answering for one of them. A list
// would be a second way to write a policy, in a place that is not the
// configuration.
func kindOf(cmd *xli.Command) (trail.Kinds, error) {
	v, _ := flg.Find[string](cmd, "kind")
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
func newCmdTrailProfiles() *xli.Command {
	return &xli.Command{
		Name:  "profiles",
		Brief: "the named retention regimes, and where each number comes from",

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
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
// does when it was given no window.
func prune(ctx context.Context, c Config) error {
	p, err := c.Audit.Policy()
	if err != nil {
		return err
	}
	if !p.On() {
		return errors.New("this deployment keeps its trail forever, so there is nothing to apply; " +
			"see `audit:` in the configuration, or name a window with --older-than")
	}

	s, err := Build(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	fmt.Fprintf(os.Stderr, "%s\n", p.String())

	// It logs what it did, per kind, exactly as the sweep does -- which is why
	// nothing is printed here beyond the policy: two accounts of one pass would
	// be two that disagree.
	p.Pass(ctx, pd.TrailStore(s.Ent))

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
func cutoff(cmd *xli.Command) (time.Time, bool, error) {
	older, _ := flg.Find[string](cmd, "older-than")
	before, _ := flg.Find[string](cmd, "before")

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

		return at, true, nil

	default:
		return time.Time{}, false, nil
	}
}

// archives is which files to read.
func archives(cmd *xli.Command, c *Config) ([]string, error) {
	if vs, ok := arg.Get[[]string](cmd, "FILE"); ok && len(vs) > 0 {
		return vs, nil
	}

	dir, _ := flg.Find[string](cmd, "in")
	if dir == "" {
		dir = c.Audit.Archive
	}
	if dir == "" {
		return nil, errors.New("name a file, or --in a directory, or set audit.archive")
	}

	return trail.Files(dir)
}

// filterOf is the flags as one question asked of each row.
func filterOf(cmd *xli.Command) (func(*app.Audit) bool, error) {
	object, err := identifier(cmd, "object")
	if err != nil {
		return nil, err
	}
	actor, err := identifier(cmd, "actor")
	if err != nil {
		return nil, err
	}
	tenant, err := identifier(cmd, "tenant")
	if err != nil {
		return nil, err
	}

	action, _ := flg.Find[string](cmd, "action")

	since, err := instant(cmd, "since")
	if err != nil {
		return nil, err
	}
	until, err := instant(cmd, "until")
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

func identifier(cmd *xli.Command, name string) (pdid.Id, error) {
	v, _ := flg.Find[string](cmd, name)
	if v == "" {
		return pdid.Nil, nil
	}

	k, err := pdid.Parse(v)
	if err != nil {
		return pdid.Nil, fmt.Errorf("--%s: %w", name, err)
	}

	return k, nil
}

func instant(cmd *xli.Command, name string) (time.Time, error) {
	v, _ := flg.Find[string](cmd, name)
	if v == "" {
		return time.Time{}, nil
	}

	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("--%s: %w", name, err)
	}

	return at, nil
}

// named is an identifier as it is printed, and the empty string as a dash.
func named(b []byte) string {
	if len(b) == 0 {
		return "-"
	}

	k, err := pdid.From(b)
	if err != nil {
		return "?"
	}

	return k.String()
}
