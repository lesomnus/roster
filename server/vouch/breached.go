package vouch

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Whether a secret is one somebody has already lost.
//
// # Why roster and not the caller
//
// Because roster is the only thing that sees the plaintext. Everything else a
// deployment might want to say about a password -- how long, which characters,
// how recently changed -- is **policy** and stays with whoever collects it;
// *this one is in a corpus of leaks* is a **fact**, and the only place it can be
// checked is where the secret is.
//
// # A file, and not a service
//
// The obvious source is an API, and the deployment this app is most careful
// about has no network at all. So the corpus is a file a deployment puts on the
// box, in the format the well-known one is published in -- SHA-1, uppercase
// hex, one per line, sorted -- and the lookup halves the file until a window is
// small enough to read. No index to build, nothing loaded into memory, and
// `sort -u` is enough to make one.
//
// SHA-1 because that is what the corpus is published as, and it is the one
// place in this app where the choice of hash is somebody else's. It is not
// protecting anything: what is being asked is whether a value appears in a
// public list.
//
// # It is a refusal and not a warning
//
// A deployment that turns this on has said the answer matters, and a check
// whose result is advice is a check nobody acts on. What it costs is somebody
// being told to pick again, which is a sentence rather than a lockout.

// Breached answers whether a secret is one somebody has already lost.
//
// Given rather than built, so that a deployment with a corpus somewhere this
// package cannot reach -- a service, a shared mount, a bloom filter somebody
// already has -- writes a function instead of a file.
type Breached func(ctx context.Context, secret []byte) (bool, error)

// BreachedIn is [Breached] over a file of SHA-1 hashes.
//
// The format the well-known corpus is published in: uppercase hex, one per
// line, sorted, optionally followed by a count this ignores. A file that is not
// sorted answers *no* to things that are in it, which is the direction that
// fails quietly -- so the order is checked at startup rather than trusted, which
// is what [Sorted] is for.
func BreachedIn(path string) (Breached, error) {
	// Opened once here to refuse a deployment that named a corpus it has not
	// got, or an empty one -- both at startup, where a mistake is a server that
	// does not come up rather than a rule that quietly stops applying.
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("vouch: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("vouch: %w", err)
	}
	if st.Size() == 0 {
		return nil, fmt.Errorf("vouch: %s: an empty corpus refuses nothing, which is not what turning this on meant", path)
	}

	return func(ctx context.Context, secret []byte) (bool, error) {
		f, err := os.Open(path)
		if err != nil {
			return false, err
		}
		defer f.Close()

		// **This** file's size, and not the one measured at startup.
		//
		// The size is half of what the search is: it is the upper bound the
		// halving starts from. Held from boot, a corpus that was replaced --
		// which is how these are updated, since the published one grows -- is
		// searched through a bound belonging to a file that is no longer there.
		// A smaller number hides everything past it; a larger one sends the
		// first `ReadAt` past the end.
		//
		// Both are the failure this whole file is written against: an answer of
		// *no* that is wrong, quietly, on the one check whose job is to say yes.
		//
		// The cost is a stat per call on the password-**set** path, which is
		// somebody typing a new password. The read that follows is already two
		// or three seeks over a file measured in gigabytes.
		st, err := f.Stat()
		if err != nil {
			return false, err
		}

		sum := sha1.Sum(secret)

		return search(f, st.Size(), strings.ToUpper(hex.EncodeToString(sum[:])))
	}, nil
}

// search narrows to a window and then reads it.
//
// # Why it is not a pure binary search
//
// One over byte offsets has to reason about a seek that lands in the middle of
// a line, and every version of that reasoning is a place to be off by one line
// -- in a function whose whole job is to say **yes** and whose failure mode is
// to say no quietly. So it halves until the window is small enough to read, and
// then reads it.
//
// The cost is one extra read of at most [window] bytes, against a file that may
// be tens of gigabytes. What it buys is that the part which is easy to get
// wrong is a loop over lines rather than arithmetic over offsets.
func search(r io.ReaderAt, size int64, want string) (bool, error) {
	lo, hi := int64(0), size

	for hi-lo > window {
		mid := (lo + hi) / 2

		at, line, err := lineAt(r, mid, hi)
		if err != nil {
			return false, err
		}
		if line == "" {
			// No line start in the upper half, so everything left is below.
			hi = mid
			continue
		}

		if strings.ToUpper(prefixOf(line)) <= want {
			lo = at
		} else {
			hi = at
		}
	}

	return scan(r, lo, hi, want)
}

// window is how much is read rather than halved.
//
// A hash line is forty characters and a little, so this is a few hundred lines
// -- small enough to read in one go and large enough that the halving stops
// well before the interesting arithmetic starts.
const window = 1 << 14

// scan reads a window and looks for the line.
//
// `lo` is always a line start: zero, or a value [search] took from [lineAt],
// which answers where a line begins. Moving it forward here would skip that
// line -- and that line is the candidate, since it is the one the last
// comparison said the answer is at or after.
func scan(r io.ReaderAt, lo, hi int64, want string) (bool, error) {
	n := hi - lo
	if n <= 0 {
		return false, nil
	}

	// A line may run past `hi`, so read a little further and let the compare
	// decide -- a partial last line simply does not match.
	b := make([]byte, min(n+window, 1<<20))
	if _, err := r.ReadAt(b, lo); err != nil && err != io.EOF {
		return false, err
	}

	for _, line := range strings.Split(string(b), "\n") {
		if strings.ToUpper(prefixOf(strings.TrimRight(line, "\r"))) == want {
			return true, nil
		}
	}

	return false, nil
}

// lineAt is the line that starts at or after `at`, and where it starts.
func lineAt(r io.ReaderAt, at, size int64) (int64, string, error) {
	const page = 4096

	n := min(page, size-at)
	if n <= 0 {
		return 0, "", nil
	}

	b := make([]byte, n)
	if _, err := r.ReadAt(b, at); err != nil && err != io.EOF {
		return 0, "", err
	}

	// Forward to a boundary unless this is the first byte of the file, which is
	// a boundary already.
	i := 0
	if at > 0 {
		i = strings.IndexByte(string(b), '\n')
		if i < 0 {
			return 0, "", nil
		}

		i++
	}

	rest := string(b[i:])
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}

	return at + int64(i), strings.TrimRight(rest, "\r"), nil
}

// prefixOf is the hash, without whatever the corpus writes after it.
func prefixOf(line string) string {
	if i := strings.IndexAny(line, ":\t "); i >= 0 {
		return line[:i]
	}

	return line
}

// Sorted answers whether a corpus is in the order the search needs.
//
// Read once at startup rather than trusted, because a file that is not sorted
// answers *no* to things that are in it -- which is the direction that fails
// quietly, in the one feature whose whole job is to say yes.
func Sorted(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("vouch: %w", err)
	}
	defer f.Close()

	was := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		v := strings.ToUpper(prefixOf(strings.TrimRight(sc.Text(), "\r")))
		if v == "" {
			continue
		}
		if !sort.StringsAreSorted([]string{was, v}) {
			return fmt.Errorf("vouch: %s: not sorted at %q, so a search over it would answer no to things that are in it", path, v)
		}

		was = v
	}

	return sc.Err()
}
