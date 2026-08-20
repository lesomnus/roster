package vouch_test

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/vouch"
)

// corpus writes one in the format the well-known list is published in.
func corpus(t *testing.T, secrets ...string) string {
	t.Helper()
	x := require.New(t)

	lines := make([]string, 0, len(secrets))
	for _, v := range secrets {
		sum := sha1.Sum([]byte(v))
		// With a count after it, which the real one carries and this ignores.
		lines = append(lines, strings.ToUpper(hex.EncodeToString(sum[:]))+":12")
	}

	// Sorted, which is what the search needs and what the real one is.
	sort.Strings(lines)

	p := filepath.Join(t.TempDir(), "leaked.txt")
	x.NoError(os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	return p
}

// TestASecretSomebodyHasAlreadyLostIsFound is item 5, and it is a **fact**
// rather than a policy — which is why it is roster's at all.
//
// Length, complexity and rotation are rules a deployment writes and are the
// caller's. *This one is in a corpus of leaks* can only be answered where the
// plaintext is, and roster is the only thing that sees it.
func TestASecretSomebodyHasAlreadyLostIsFound(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// Enough that the binary search does more than one hop.
	known := []string{"password", "hunter2", "123456", "letmein", "qwerty", "dragon", "monkey"}
	at := corpus(t, known...)

	x.NoError(vouch.Sorted(at))

	in, err := vouch.BreachedIn(at)
	x.NoError(err)

	for _, v := range known {
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.True(bad, v)
	}

	for _, v := range []string{"correct horse battery staple", "", "PASSWORD", "hunter3"} {
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.False(bad, v)
	}
}

// TestACorpusThatIsNotSortedIsRefused is the failure that would otherwise be
// silent.
//
// A binary search over an unsorted file answers **no** to things that are in
// it, which is the wrong direction in the one feature whose whole job is to say
// yes — and nothing would say so until somebody looked. So the order is checked
// once at startup rather than trusted.
func TestACorpusThatIsNotSortedIsRefused(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "leaked.txt")
	x.NoError(os.WriteFile(p, []byte("FFFF\nAAAA\n"), 0o600))

	x.Error(vouch.Sorted(p))
}

// TestAnEmptyCorpusIsNotACorpus.
//
// A deployment that named a file has said the answer matters, and one that
// refuses nothing is one that would go on accepting what it said it would not.
func TestAnEmptyCorpusIsNotACorpus(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "leaked.txt")
	x.NoError(os.WriteFile(p, nil, 0o600))

	_, err := vouch.BreachedIn(p)
	x.Error(err)

	_, err = vouch.BreachedIn(filepath.Join(t.TempDir(), "nothing-here"))
	x.Error(err)
}

// TestACorpusLargerThanTheWindowIsStillFound is the half of this that a small
// file cannot exercise.
//
// The search halves until the window is small enough to read, so a corpus that
// fits in one window never halves at all — and every off-by-one in the halving
// would be invisible. This one is a few hundred kilobytes, which is a few
// dozen halvings short of the real corpus and enough for every branch.
func TestACorpusLargerThanTheWindowIsStillFound(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	secrets := make([]string, 0, 4000)
	for i := range 4000 {
		secrets = append(secrets, fmt.Sprintf("leaked-%04d", i))
	}

	at := corpus(t, secrets...)
	x.NoError(vouch.Sorted(at))

	st, err := os.Stat(at)
	x.NoError(err)
	x.Greater(st.Size(), int64(1<<14), "the file fits in one window, so nothing halved")

	in, err := vouch.BreachedIn(at)
	x.NoError(err)

	// Every one of them, so a miss anywhere in the file is a failure rather
	// than a coin toss.
	for _, v := range secrets {
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.True(bad, v)
	}

	for i := range 200 {
		v := fmt.Sprintf("not-leaked-%04d", i)
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.False(bad, v)
	}
}

// TestACorpusThatWasReplacedIsSearchedAsItIsNow.
//
// The size is half of what the search is -- it is the upper bound the halving
// starts from -- and it was measured once, when the deployment started. A
// corpus that was replaced afterwards, which is how these are updated since the
// published one only grows, was then searched through a bound belonging to a
// file that is no longer there.
//
// Which is the failure the whole of `breached.go` is written against: an answer
// of *no* that is wrong, quietly, on the one check whose job is to say yes.
// Nothing errors and nothing logs; a password somebody has already lost is
// simply accepted.
func TestACorpusThatWasReplacedIsSearchedAsItIsNow(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// Small to begin with, so the bound taken at construction is far short of
	// what the file becomes.
	at := corpus(t, "hunter2")

	in, err := vouch.BreachedIn(at)
	x.NoError(err)

	bad, err := in(ctx, []byte("hunter2"))
	x.NoError(err)
	x.True(bad)

	// The update: a corpus large enough that most of it lies past the old
	// bound, written over the same path.
	grown := make([]string, 0, 4096)
	for i := range 4096 {
		grown = append(grown, fmt.Sprintf("leaked-%04d", i))
	}
	grown = append(grown, "hunter2")

	x.NoError(os.Rename(corpus(t, grown...), at))
	x.NoError(vouch.Sorted(at))

	// Everything, including the entries that are only reachable through the
	// new bound. Before the stat moved into the call, the ones past the old
	// size answered *no*.
	for _, v := range []string{"hunter2", "leaked-0000", "leaked-2048", "leaked-4095"} {
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.True(bad, "%s: searched through a bound that belongs to a file that is gone", v)
	}

	// And a corpus that shrank is not read past its end.
	x.NoError(os.Rename(corpus(t, "hunter2"), at))

	bad, err = in(ctx, []byte("leaked-2048"))
	x.NoError(err)
	x.False(bad)
}
