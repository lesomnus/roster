package trail_test

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/trail"
)

// TestAnArchiveReadsBackOnceWhateverIsInIt.
//
// [trail.Archive] writes, syncs and only then deletes, so the one failure it
// can leave behind is rows in **both** places -- which is the direction to fail
// in, and which makes the next run write the same month again. This is the
// other half of that arrangement: a row read twice is a row read once.
//
// Written against a file rather than through a database, because what is being
// asserted is a property of the format and the reader. The way to arrange the
// duplicate through the database is to arrange the crash.
func TestAnArchiveReadsBackOnceWhateverIsInIt(t *testing.T) {
	x := require.New(t)

	at := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	v := app.Audit_builder{
		Id:          pdid.New(3).Bytes(),
		Action:      "/roster.HolderService/Add",
		DateCreated: timestamppb.New(at),
	}.Build()

	b, err := protojson.Marshal(v)
	x.NoError(err)

	dir := t.TempDir()
	path := filepath.Join(dir, trail.Named(at))

	// Twice, and as two gzip members rather than two lines of one -- which is
	// what appending actually produces, and is the shape that would go unnoticed
	// if the reader stopped at the first member.
	f, err := os.Create(path)
	x.NoError(err)
	for range 2 {
		buf := &bytes.Buffer{}
		z := gzip.NewWriter(buf)
		_, err := z.Write(append(b, '\n'))
		x.NoError(err)
		x.NoError(z.Close())

		_, err = f.Write(buf.Bytes())
		x.NoError(err)
	}
	x.NoError(f.Close())

	got := []*app.Audit{}
	x.NoError(trail.Read(path, func(v *app.Audit) error {
		got = append(got, v)

		return nil
	}))

	x.Len(got, 1, "the same row was read twice")
	x.Equal(v.GetAction(), got[0].GetAction())
	x.Equal(at, got[0].GetDateCreated().AsTime())
}

// TestAnArchiveIsDestroyedByTheMonthAfterIt.
//
// The archive is a file per month and [trail.Purge] decides from the name, so
// the arithmetic is the whole of the rule and it is the kind that is wrong by
// one. A file named for January holds rows up to the last instant of January,
// so it is destroyable once the cutoff has reached February -- and not on the
// 31st, when most of it is still inside the window.
func TestAnArchiveIsDestroyedByTheMonthAfterIt(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	for _, v := range []string{"2024-11", "2024-12", "2025-01"} {
		x.NoError(os.WriteFile(filepath.Join(dir, "audit-"+v+trail.Ext), nil, 0o600))
	}

	// Something else in the directory, which is not this to destroy.
	x.NoError(os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600))

	t.Run("the month it is in stays", func(t *testing.T) {
		x := require.New(t)

		vs, err := trail.Doomed(dir, time.Date(2025, 1, 31, 23, 59, 59, 0, time.UTC))
		x.NoError(err)
		x.Len(vs, 2)
		x.Contains(vs[0], "2024-11")
		x.Contains(vs[1], "2024-12")
	})

	t.Run("and goes the instant it is over", func(t *testing.T) {
		x := require.New(t)

		vs, err := trail.Doomed(dir, time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC))
		x.NoError(err)
		x.Len(vs, 3)
	})

	t.Run("and what this did not write is left alone", func(t *testing.T) {
		x := require.New(t)

		vs, err := trail.Purge(t.Context(), dir, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
		x.NoError(err)
		x.Len(vs, 3)

		_, err = os.Stat(filepath.Join(dir, "notes.txt"))
		x.NoError(err, "a destructive pass guessed about a file it did not write")
	})
}

// TestAPolicyThatDestroysWithoutKeepingHasToSaySo.
//
// The failure this is about does not look like a failure. A deployment that
// writes `audit.retain: 2160h` and leaves `audit.archive` empty has configured
// something that works: the sweep runs, the table stops growing, and every
// graph an operator looks at improves. What it is doing is destroying the
// evidence, and the day that is discovered is the day somebody asks for it.
//
// So the empty field is refused rather than obeyed, and `discard` is the way to
// mean it. Read at startup -- see `cmd.Build` -- because a refusal a day later
// is a refusal after the first pass.
func TestAPolicyThatDestroysWithoutKeepingHasToSaySo(t *testing.T) {
	day := 24 * time.Hour

	for _, tc := range []struct {
		name string
		p    trail.Policy
		err  string
	}{
		{
			name: "a window and nowhere to put what leaves it",
			p:    trail.Policy{Retain: 90 * day},
			err:  "nowhere to put",
		},
		{
			name: "an archive clock and no archive",
			p:    trail.Policy{Destroy: 7 * 365 * day},
			err:  "names none",
		},
		{
			name: "destroyed before it is written",
			p:    trail.Policy{Retain: 90 * day, Archive: "/tmp/x", Destroy: 30 * day},
			err:  "shorter than",
		},
		{
			name: "a deployment that means it",
			p:    trail.Policy{Retain: 90 * day, Discard: true},
		},
		{
			name: "the two clocks the right way round",
			p:    trail.Policy{Retain: 90 * day, Archive: "/tmp/x", Destroy: 7 * 365 * day},
		},
		{
			name: "and nothing at all, which is forever",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			err := tc.p.Valid()
			if tc.err == "" {
				x.NoError(err)

				return
			}

			x.ErrorContains(err, tc.err)
		})
	}

	t.Run("and nothing at all does nothing at all", func(t *testing.T) {
		x := require.New(t)

		x.False(trail.Policy{}.On())
		x.True(trail.Policy{Retain: day, Discard: true}.On())
	})
}
