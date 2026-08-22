// Package trail is what happens to the record of what happened, after long
// enough.
//
// # Why there is anything to decide
//
// `audit.proto` says it about `value`, in the sentence that turned out to be a
// task rather than a caveat: *the trail outlives what it names, so a softly
// erased row's contents live on here. An app with an obligation to destroy data
// has to reckon with the trail, and the answer is a retention policy rather
// than an empty column.* Nothing implemented one, so the answer roster gave was
// **forever**, arrived at by not deciding.
//
// It is also the one table that never stops growing. Every write in the
// deployment is a row here, and unlike a continuation or a link there is no
// expiry to sweep by -- an audit row is not stale, it is old.
//
// # What a policy is, here
//
// Two things a deployment names separately, because they answer to different
// people. **How long a row stays in the database** is an operational question:
// what the console can show, what a query costs, how big the disk is. **How
// long the record exists at all** is the obligation, and it is longer -- often
// years longer -- than anything anybody wants in the hot table.
//
// So the shape is not delete-after-N. It is *leave after N*: rows older than
// the window are written somewhere else and then removed from here, and what
// happens to that somewhere else is a second clock. [Archive] is the first and
// [Purge] is the second.
//
// # And why none of it is an RPC
//
// The layer in front of `AuditService` refuses every write -- "the trail is
// written by what happened, not by anybody asking" -- and that is not an
// oversight to be worked around by adding a retention RPC beside it. The value
// of a trail is exactly that the credential which lets somebody act is not the
// credential that lets them erase the record of having acted. An API key that
// prunes is a stolen key that prunes.
//
// So this runs against ent, and reaches it two ways: `roster trail` at a shell,
// and a sweep inside `serve` for the deployment that would rather not remember
// to run one. Both need the database, which is the boundary being asked for.
package trail

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
)

// Batch is how many rows one pass reads and deletes at a time.
//
// A first run on a deployment that has never pruned is the whole table, and one
// statement over it is a transaction that holds locks for as long as it takes
// and a delete that either finishes or achieves nothing. Batched, an
// interrupted run has still moved everything it wrote.
const Batch = 1000

// Ext is what an archive file is called, after the month it holds.
const Ext = ".jsonl.gz"

// Month is the part of an archive's name that says what is in it.
//
// UTC, so that a deployment does not file the same instant in two months
// depending on where the machine thinks it is.
func Month(at time.Time) string { return at.UTC().Format("2006-01") }

// Named is the file a row of this date belongs in, for one run of [Archive].
//
// **A month and a run**, and the run half is not decoration. The first version
// was the month alone, appended to, on the reasoning that concatenated gzip
// members are a valid stream and so a file need never be rewritten. That is
// true of one writer.
//
// There is not one writer. [Sweep] takes no lock -- neither does the generated
// outbox drain, and its comment says so: *nothing here takes a lock, so two of
// these drain the same rows.* For the drain that is wasted work. Here two
// writers interleave gzip members **inside one member**, because a
// `gzip.Writer` flushes to the file in chunks of its own choosing, and what
// lands is not a gzip stream at all. Two replicas, or an operator running
// `roster trail prune` while the process sweeps, and the month is unreadable.
//
// A run writes its own files, so writers never share one. What it costs is more
// files -- one per month a run reaches, rather than one per month -- and the
// duplicate rows two writers produce, which [Read] already drops because
// [Archive] could always leave those behind by crashing.
//
// The month stays **first** so that [Doomed] can still answer *is all of this
// old enough* from the name, without opening anything.
func Named(at time.Time, run string) string {
	return "audit-" + Month(at) + "." + run + Ext
}

// run is what tells one pass's files from another's.
//
// Random rather than a timestamp or a process id: two replicas starting from
// the same cron minute would collide on the first, and a container that always
// comes up as pid 1 on the second.
func run() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}

// Archive writes every row older than `before` into `dir`, then removes exactly
// the rows it wrote. It answers with how many moved.
//
// # The order, and why it is not a flag
//
// Written, flushed, `fsync`ed, closed -- and only then deleted, by identifier,
// naming the rows that are actually in the file. Not by the same predicate
// again: a second `date_created < before` is a second query, and what it
// matches is whatever is true when it runs rather than what was written. A row
// backdated by a clock that stepped, or written by a replica whose idea of now
// is behind, is a row the second query removes and the file does not have.
//
// The failure that is left is a crash between the sync and the delete, and it
// leaves the rows in **both** places. That is the direction to fail in, and
// [Read] drops the duplicate by identifier so that the next run repeating a
// month costs nothing to read back.
//
// # An empty dir is refused
//
// Deleting without keeping is a thing a deployment may genuinely want, and it
// is not a thing to arrive at by leaving a field blank. See [Collect], which is
// what that deployment calls.
func Archive(ctx context.Context, db *ent.Client, before time.Time, dir string) (int, error) {
	if dir == "" {
		return 0, errors.New("no directory to archive into")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, err
	}

	id, err := run()
	if err != nil {
		return 0, err
	}

	moved := 0
	for {
		vs, err := db.Audit.Query().
			Where(entaudit.DateCreatedLT(before)).
			Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldID)).
			Limit(Batch).
			All(ctx)
		if err != nil {
			return moved, err
		}
		if len(vs) == 0 {
			return moved, nil
		}

		if err := write(dir, id, vs); err != nil {
			return moved, err
		}

		ids := make([]uuid.UUID, len(vs))
		for i, v := range vs {
			ids[i] = v.ID
		}

		n, err := db.Audit.Delete().Where(entaudit.IDIn(ids...)).Exec(ctx)
		if err != nil {
			return moved, err
		}

		moved += n

		if len(vs) < Batch {
			return moved, nil
		}
	}
}

// Count is how many rows are past the window, and changes nothing.
//
// What a dry run answers with. It is a `COUNT` over the one table that never
// stops growing, so it is not free -- and it is the question somebody asks
// once, before the run that is not free either.
func Count(ctx context.Context, db *ent.Client, before time.Time) (int, error) {
	return db.Audit.Query().Where(entaudit.DateCreatedLT(before)).Count(ctx)
}

// Collect removes rows older than `before` and keeps no copy.
//
// Separate from [Archive] rather than the same call with an empty directory,
// because the two are different acts and one of them is irreversible. A
// deployment that means it says so.
func Collect(ctx context.Context, db *ent.Client, before time.Time) (int, error) {
	gone := 0
	for {
		ids, err := db.Audit.Query().
			Where(entaudit.DateCreatedLT(before)).
			Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldID)).
			Limit(Batch).
			IDs(ctx)
		if err != nil {
			return gone, err
		}
		if len(ids) == 0 {
			return gone, nil
		}

		n, err := db.Audit.Delete().Where(entaudit.IDIn(ids...)).Exec(ctx)
		if err != nil {
			return gone, err
		}

		gone += n

		if len(ids) < Batch {
			return gone, nil
		}
	}
}

// write appends one batch to this run's file for whichever month each row
// belongs to.
//
// Appended, and gzip is what makes that free: concatenated members are a valid
// stream and a reader walks them as one. So a file is never rewritten and a run
// that stops half way has still written what it wrote -- and because the name
// carries the run, the writer appending is the only one there is.
func write(dir, id string, vs []*ent.Audit) error {
	byMonth := map[string][]*ent.Audit{}
	for _, v := range vs {
		name := Named(v.DateCreated, id)
		byMonth[name] = append(byMonth[name], v)
	}

	names := make([]string, 0, len(byMonth))
	for name := range byMonth {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := appendTo(filepath.Join(dir, name), byMonth[name]); err != nil {
			return err
		}
	}

	return nil
}

func appendTo(path string, vs []*ent.Audit) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()

	// Built whole and written once. A `gzip.Writer` over the file would flush
	// in chunks of its own choosing, and a chunk is where a second writer gets
	// in -- see [Named]. This is belt beside those braces: the run id already
	// means nobody else has this file open, and a member that reaches the disk
	// in one write cannot be half of one either.
	buf := &bytes.Buffer{}

	z := gzip.NewWriter(buf)
	for _, v := range vs {
		b, err := protojson.Marshal(Row(v))
		if err != nil {
			return err
		}
		if _, err := z.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	if err := z.Close(); err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}

	// The whole point of the ordering. Until this returns, what is in the file
	// is what the operating system intends to write, and the delete below is
	// about to make this copy the only one.
	return f.Sync()
}

// Row is an ent row as the message it was written from.
//
// protojson and not the ent row's own JSON, because what an archive holds has
// to be readable by something that has never heard of this app's storage. The
// field names are the schema's, `patch` and `value` are the bytes they always
// were, and anything that can read an `Audit` off the wire can read one out of
// here.
func Row(v *ent.Audit) *app.Audit {
	b := app.Audit_builder{
		Id:          pdid.Id(v.ID).Bytes(),
		Action:      v.Action,
		TraceId:     v.TraceID,
		Patch:       v.Patch,
		Value:       v.Value,
		DateCreated: timestamppb.New(v.DateCreated),
	}

	if v.TenantID != uuid.Nil {
		b.TenantId = pdid.Id(v.TenantID).Bytes()
	}
	if v.ActorID != uuid.Nil {
		b.ActorId = pdid.Id(v.ActorID).Bytes()
	}
	if v.ObjectID != uuid.Nil {
		b.ObjectId = pdid.Id(v.ObjectID).Bytes()
	}
	if v.ActorTenantID != uuid.Nil {
		b.ActorTenantId = pdid.Id(v.ActorTenantID).Bytes()
	}
	if v.CounterpartTenantID != nil && *v.CounterpartTenantID != uuid.Nil {
		b.CounterpartTenantId = pdid.Id(*v.CounterpartTenantID).Bytes()
	}

	return b.Build()
}

// Read walks archives in the order given and calls `fn` for each row.
//
// **Across the files rather than within one**, which is what the run in a
// name costs: two writers that reached the same month wrote two files, and a
// row in both is one row. It is the same drop that makes [Archive]'s crash
// window cheap -- rows left in the database and in a file are re-archived by
// the next pass -- and now it has a second reason.
//
// It costs an identifier per row in memory for the length of the read. A month
// of one deployment's writes is what that is, and the alternative is answering
// a question about a set with a pass over one of its parts.
func Read(paths []string, fn func(*app.Audit) error) error {
	seen := map[string]bool{}

	for _, path := range paths {
		if err := read(path, seen, fn); err != nil {
			return err
		}
	}

	return nil
}

func read(path string, seen map[string]bool, fn func(*app.Audit) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	z, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer z.Close()

	s := bufio.NewScanner(z)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}

		v := &app.Audit{}
		if err := protojson.Unmarshal(line, v); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		k := string(v.GetId())
		if seen[k] {
			continue
		}
		seen[k] = true

		if err := fn(v); err != nil {
			return err
		}
	}

	return s.Err()
}

// Files is every archive in `dir`, oldest month first.
func Files(dir string) ([]string, error) {
	vs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	out := []string{}
	for _, v := range vs {
		if v.IsDir() || !strings.HasSuffix(v.Name(), Ext) {
			continue
		}

		out = append(out, filepath.Join(dir, v.Name()))
	}
	sort.Strings(out)

	return out, nil
}

// Purge destroys the archives that are entirely older than `before`, and
// answers with what it removed.
//
// **Entirely**, which is what the name is for: a file named for a month holds
// nothing after that month, so one is removable when the month **after** it has
// also passed. A file for January goes when `before` has reached February, and
// not on the 31st.
//
// This is the end of the line and there is nothing after it. It is separate
// from [Archive] because it answers to a different clock and a different
// person: how long the hot table carries a row is an operational choice, and
// how long the record exists at all is the obligation.
func Purge(ctx context.Context, dir string, before time.Time) ([]string, error) {
	vs, err := Doomed(dir, before)
	if err != nil {
		return nil, err
	}

	out := []string{}
	for _, path := range vs {
		if err := os.Remove(path); err != nil {
			return out, err
		}

		out = append(out, path)
	}

	return out, nil
}

// Doomed is what [Purge] would remove, and removes nothing.
//
// Its own function rather than a flag on [Purge], so that the list a dry run
// prints is the list the real one acts on -- two passes that agree today are
// two passes.
func Doomed(dir string, before time.Time) ([]string, error) {
	vs, err := Files(dir)
	if err != nil {
		return nil, err
	}

	cut := before.UTC()

	out := []string{}
	for _, path := range vs {
		at, ok := monthOf(filepath.Base(path))
		if !ok {
			// A file in the directory that this did not write. Left alone: a
			// destructive pass over a directory is not the place to guess.
			continue
		}

		// The month **after** the one the file is named for, because a file
		// named for January holds rows up to the last instant of it. January
		// goes when the cutoff has reached February, and not on the 31st.
		if end := at.AddDate(0, 1, 0); end.After(cut) {
			continue
		}

		out = append(out, path)
	}

	return out, nil
}

// monthOf reads back the half of [Named] that says what is in the file.
//
// The run is not parsed and is not looked at. What a destructive pass needs to
// know is the month, and a name it cannot read a month out of is a file this
// did not write -- see [Doomed], which leaves those alone.
func monthOf(name string) (time.Time, bool) {
	v := strings.TrimSuffix(name, Ext)
	if v == name {
		return time.Time{}, false
	}

	v, ok := strings.CutPrefix(v, "audit-")
	if !ok {
		return time.Time{}, false
	}
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}

	at, err := time.ParseInLocation("2006-01", v, time.UTC)
	if err != nil {
		return time.Time{}, false
	}

	return at, true
}
